package internal

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	"github.com/temporalio/temporal-worker-controller/internal/k8s"
	"github.com/temporalio/temporal-worker-controller/internal/testhelpers"
	"go.temporal.io/server/common/dynamicconfig"
	"go.temporal.io/server/temporal"
	"go.temporal.io/server/temporaltest"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

const (
	testShortPollerHistoryTTL         = time.Second
	testDrainageVisibilityGracePeriod = time.Second
	testDrainageRefreshInterval       = time.Second
)

// waitForCondition polls a condition function until it returns true or timeout is reached
func waitForCondition(condition func() bool, timeout, interval time.Duration) bool {
	deadline := time.After(timeout)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-deadline:
			return false
		case <-ticker.C:
			if condition() {
				return true
			}
		}
	}
}

type testEnv struct {
	k8sClient  client.Client
	mgr        manager.Manager
	ts         *temporaltest.TestServer
	connection *temporaliov1alpha1.TemporalConnection
	replicas   map[string]int32
	images     map[string]string
}

// TestIntegration runs integration tests for the Temporal Worker Controller
func TestIntegration(t *testing.T) {
	// Set up test environment
	cfg, k8sClient, mgr, _, cleanup := setupTestEnvironment(t)
	defer cleanup()

	// Create test namespace
	testNamespace := createTestNamespace(t, k8sClient)
	defer cleanupTestNamespace(t, cfg, k8sClient, testNamespace)

	// Create test Temporal server and client
	dc := dynamicconfig.NewMemoryClient()
	// make versions drain faster
	dc.OverrideValue("matching.wv.VersionDrainageStatusVisibilityGracePeriod", testDrainageVisibilityGracePeriod)
	dc.OverrideValue("matching.wv.VersionDrainageStatusRefreshInterval", testDrainageRefreshInterval)
	ts := temporaltest.NewServer(
		temporaltest.WithT(t),
		temporaltest.WithBaseServerOptions(temporal.WithDynamicConfigClient(dc)),
	)

	tests := map[string]*testhelpers.TestCaseBuilder{
		"manual-rollout-expect-no-change": testhelpers.NewTestCase().
			WithInput(
				testhelpers.NewTemporalWorkerDeploymentBuilder().
					WithManualStrategy().
					WithTargetTemplate("v1"),
			).
			WithWaitTime(5 * time.Second). // wait before checking to confirm no change
			WithExpectedStatus(
				testhelpers.NewStatusBuilder().
					WithTargetVersion("v1", temporaliov1alpha1.VersionStatusInactive, -1, true, false),
			),
		"all-at-once-rollout-2-replicas": testhelpers.NewTestCase().
			WithInput(
				testhelpers.NewTemporalWorkerDeploymentBuilder().
					WithAllAtOnceStrategy().
					WithReplicas(2).
					WithTargetTemplate("v1"),
			).
			WithExpectedStatus(
				testhelpers.NewStatusBuilder().
					WithTargetVersion("v1", temporaliov1alpha1.VersionStatusCurrent, -1, true, false).
					WithCurrentVersion("v1", true, false),
			),
		"progressive-rollout-no-unversioned-pollers-expect-all-at-once": testhelpers.NewTestCase().
			WithInput(
				testhelpers.NewTemporalWorkerDeploymentBuilder().
					WithProgressiveStrategy(testhelpers.ProgressiveStep(5, time.Hour)).
					WithTargetTemplate("v1"),
			).
			WithExpectedStatus(
				testhelpers.NewStatusBuilder().
					WithTargetVersion("v1", temporaliov1alpha1.VersionStatusCurrent, -1, true, false).
					WithCurrentVersion("v1", true, false),
			),
		//// TODO(carlydf): this won't work until the controller detects unversioned pollers
		// "progressive-rollout-yes-unversioned-pollers-expect-first-step": testhelpers.NewTestCase().
		//	WithInput(
		//		testhelpers.NewTemporalWorkerDeploymentBuilder().
		//			WithProgressiveStrategy(testhelpers.ProgressiveStep(5, time.Hour)).
		//			WithTargetTemplate("v1"),
		//	).
		//	WithSetupFunction(setupUnversionedPoller).
		//	WithExpectedStatus(
		//		testhelpers.NewStatusBuilder().
		//			WithTargetVersion("v1", temporaliov1alpha1.VersionStatusRamping, 5, true, false),
		//	),
		"nth-progressive-rollout-expect-first-step": testhelpers.NewTestCase().
			WithInput(
				testhelpers.NewTemporalWorkerDeploymentBuilder().
					WithProgressiveStrategy(testhelpers.ProgressiveStep(5, time.Hour)).
					WithTargetTemplate("v1").
					WithStatus(
						testhelpers.NewStatusBuilder().
							WithTargetVersion("v0", temporaliov1alpha1.VersionStatusCurrent, -1, true, true).
							WithCurrentVersion("v0", true, true),
					),
			).
			WithExistingDeployments(
				testhelpers.NewDeploymentInfo("v0", 1),
			).
			WithExpectedStatus(
				testhelpers.NewStatusBuilder().
					WithTargetVersion("v1", temporaliov1alpha1.VersionStatusRamping, 5, true, false),
			),
		"nth-progressive-rollout-with-success-gate": testhelpers.NewTestCase().
			WithInput(
				testhelpers.NewTemporalWorkerDeploymentBuilder().
					WithProgressiveStrategy(testhelpers.ProgressiveStep(5, time.Hour)).
					WithGate(true).
					WithTargetTemplate("v1").
					WithStatus(
						testhelpers.NewStatusBuilder().
							WithTargetVersion("v0", temporaliov1alpha1.VersionStatusCurrent, -1, true, true).
							WithCurrentVersion("v0", true, true),
					),
			).
			WithExistingDeployments(
				testhelpers.NewDeploymentInfo("v0", 1),
			).
			WithExpectedStatus(
				testhelpers.NewStatusBuilder().
					WithTargetVersion("v1", temporaliov1alpha1.VersionStatusRamping, 5, true, false),
			),
		"nth-progressive-rollout-with-failed-gate": testhelpers.NewTestCase().
			WithInput(
				testhelpers.NewTemporalWorkerDeploymentBuilder().
					WithProgressiveStrategy(testhelpers.ProgressiveStep(5, time.Hour)).
					WithGate(false).
					WithTargetTemplate("v1").
					WithStatus(
						testhelpers.NewStatusBuilder().
							WithTargetVersion("v0", temporaliov1alpha1.VersionStatusCurrent, -1, true, true).
							WithCurrentVersion("v0", true, true),
					),
			).
			WithExistingDeployments(
				testhelpers.NewDeploymentInfo("v0", 1),
			).
			WithWaitTime(5 * time.Second).
			WithExpectedStatus(
				testhelpers.NewStatusBuilder().
					WithTargetVersion("v1", temporaliov1alpha1.VersionStatusInactive, -1, true, false).
					WithCurrentVersion("v0", true, true),
			),
		"failed-gate-is-not-scaled-down-while-target": testhelpers.NewTestCase().
			WithInput(
				testhelpers.NewTemporalWorkerDeploymentBuilder().
					WithAllAtOnceStrategy().
					WithGate(false).
					WithTargetTemplate("v1"),
			).
			WithWaitTime(5 * time.Second).
			WithExpectedStatus(
				testhelpers.NewStatusBuilder().
					WithTargetVersion("v1", temporaliov1alpha1.VersionStatusInactive, -1, true, false),
			),
		"failed-gate-is-scaled-down-when-deprecated": testhelpers.NewTestCase().
			WithInput(
				testhelpers.NewTemporalWorkerDeploymentBuilder().
					WithAllAtOnceStrategy().
					WithTargetTemplate("v2").
					WithStatus(
						testhelpers.NewStatusBuilder().
							WithTargetVersion("v1", temporaliov1alpha1.VersionStatusInactive, -1, true, true).
							WithDeprecatedVersions(
								testhelpers.NewDeprecatedVersionInfo("v0", temporaliov1alpha1.VersionStatusDrained, true, true, true),
							),
					),
			).
			WithExistingDeployments(
				testhelpers.NewDeploymentInfo("v0", 1),
				testhelpers.NewDeploymentInfo("v1", 1),
			).
			WithWaitTime(5*time.Second).
			WithExpectedStatus(
				testhelpers.NewStatusBuilder().
					WithTargetVersion("v2", temporaliov1alpha1.VersionStatusCurrent, -1, true, false).
					WithCurrentVersion("v2", true, false).
					WithDeprecatedVersions(
						testhelpers.NewDeprecatedVersionInfo("v0", temporaliov1alpha1.VersionStatusDrained, true, false, true),
						testhelpers.NewDeprecatedVersionInfo("v1", temporaliov1alpha1.VersionStatusInactive, true, false, true),
					),
			).
			WithExpectedDeployments( // note: right now this is only checked for deprecated versions, TODO(carlydf) add for non-deprecated too
				testhelpers.NewDeploymentInfo("v0", 1),
				testhelpers.NewDeploymentInfo("v1", 0),
				testhelpers.NewDeploymentInfo("v2", 1),
			),
	}
	// TODO(carlydf): Add additional test case where multiple ramping steps are done

	for testName, tc := range tests {
		t.Run(testName, func(t *testing.T) {
			ctx := context.Background()
			testTemporalWorkerDeploymentCreation(ctx, t, k8sClient, mgr, ts, tc.BuildWithValues(testName, testNamespace.Name, ts.GetDefaultNamespace()))
		})
	}

	// Create short TTL test Temporal server and client
	dcShortTTL := dynamicconfig.NewMemoryClient()
	// make versions eligible for deletion faster
	dcShortTTL.OverrideValue("matching.PollerHistoryTTL", testShortPollerHistoryTTL) // default is 5 minutes
	// make versions drain faster
	dcShortTTL.OverrideValue("matching.wv.VersionDrainageStatusVisibilityGracePeriod", testDrainageVisibilityGracePeriod)
	dcShortTTL.OverrideValue("matching.wv.VersionDrainageStatusRefreshInterval", testDrainageRefreshInterval)
	tsShortTTL := temporaltest.NewServer(
		temporaltest.WithT(t),
		temporaltest.WithBaseServerOptions(temporal.WithDynamicConfigClient(dcShortTTL)),
	)
	testsShortPollerTTL := map[string]*testhelpers.TestCaseBuilder{
		// Note: Add tests that require pollers to expire quickly here
	}

	for testName, tc := range testsShortPollerTTL {
		t.Run(testName, func(t *testing.T) {
			ctx := context.Background()
			testTemporalWorkerDeploymentCreation(ctx, t, k8sClient, mgr, tsShortTTL, tc.BuildWithValues(testName, testNamespace.Name, tsShortTTL.GetDefaultNamespace()))
		})
	}

	// Add test for deployment deletion protection
	t.Run("deployment-deletion-protection", func(t *testing.T) {
		testDeploymentDeletionProtection(t, k8sClient, ts)
	})

}

// testTemporalWorkerDeploymentCreation tests the creation of a TemporalWorkerDeployment and waits for the expected status
func testTemporalWorkerDeploymentCreation(
	ctx context.Context,
	t *testing.T,
	k8sClient client.Client,
	mgr manager.Manager,
	ts *temporaltest.TestServer,
	tc testhelpers.TestCase,
) {
	twd := tc.GetTWD()
	expectedStatus := tc.GetExpectedStatus()

	t.Log("Creating a TemporalConnection")
	temporalConnection := &temporaliov1alpha1.TemporalConnection{
		ObjectMeta: metav1.ObjectMeta{
			Name:      twd.Spec.WorkerOptions.TemporalConnection,
			Namespace: twd.Namespace,
		},
		Spec: temporaliov1alpha1.TemporalConnectionSpec{
			HostPort: ts.GetFrontendHostPort(),
		},
	}
	if err := k8sClient.Create(ctx, temporalConnection); err != nil {
		t.Fatalf("failed to create TemporalConnection: %v", err)
	}

	env := testhelpers.TestEnv{
		K8sClient:                  k8sClient,
		Mgr:                        mgr,
		Ts:                         ts,
		Connection:                 temporalConnection,
		ExistingDeploymentReplicas: tc.GetExistingDeploymentReplicas(),
		ExistingDeploymentImages:   tc.GetExistingDeploymentImages(),
		ExpectedDeploymentReplicas: tc.GetExpectedDeploymentReplicas(),
	}

	makePreliminaryStatusTrue(ctx, t, env, twd)

	// verify that temporal state matches the preliminary status, to confirm that makePreliminaryStatusTrue worked
	verifyTemporalStateMatchesStatusEventually(t, ctx, ts, twd, twd.Status, 30*time.Second, 5*time.Second)

	t.Log("Creating a TemporalWorkerDeployment")
	if err := k8sClient.Create(ctx, twd); err != nil {
		t.Fatalf("failed to create TemporalWorkerDeployment: %v", err)
	}

	t.Log("Waiting for the controller to reconcile")
	expectedDeploymentName := k8s.ComputeVersionedDeploymentName(twd.Name, k8s.ComputeBuildID(twd))
	waitForDeployment(t, k8sClient, expectedDeploymentName, twd.Namespace, 30*time.Second)
	workerStopFuncs := applyDeployment(t, ctx, k8sClient, expectedDeploymentName, twd.Namespace)
	defer handleStopFuncs(workerStopFuncs)

	if wait := tc.GetWaitTime(); wait != nil {
		time.Sleep(*wait)
	}
	verifyTemporalWorkerDeploymentStatusEventually(t, ctx, env, twd.Name, twd.Namespace, expectedStatus, 30*time.Second, 5*time.Second)
	verifyTemporalStateMatchesStatusEventually(t, ctx, ts, twd, *expectedStatus, 30*time.Second, 5*time.Second)
}

// testDeploymentDeletionProtection verifies that deployment resources can only be deleted by the controller
func testDeploymentDeletionProtection(t *testing.T, k8sClient client.Client, ts *temporaltest.TestServer) {
	ctx := context.Background()

	// Create test namespace
	testNamespace := createTestNamespace(t, k8sClient)
	defer func() {
		err := k8sClient.Delete(ctx, testNamespace)
		assert.NoError(t, err, "failed to delete test namespace")
	}()

	// Create TemporalConnection
	temporalConnection := &temporaliov1alpha1.TemporalConnection{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-connection",
			Namespace: testNamespace.Name,
		},
		Spec: temporaliov1alpha1.TemporalConnectionSpec{
			HostPort: ts.GetFrontendHostPort(),
		},
	}
	err := k8sClient.Create(ctx, temporalConnection)
	require.NoError(t, err, "failed to create TemporalConnection")

	// Create TemporalWorkerDeployment
	twd := &temporaliov1alpha1.TemporalWorkerDeployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-worker",
			Namespace: testNamespace.Name,
		},
		Spec: temporaliov1alpha1.TemporalWorkerDeploymentSpec{
			Replicas: func() *int32 { r := int32(1); return &r }(),
			Template: testhelpers.MakeHelloWorldPodSpec("test-image:v1"),
			RolloutStrategy: temporaliov1alpha1.RolloutStrategy{
				Strategy: temporaliov1alpha1.UpdateAllAtOnce,
			},
			WorkerOptions: temporaliov1alpha1.WorkerOptions{
				TemporalConnection: "test-connection",
				TemporalNamespace:  ts.GetDefaultNamespace(),
			},
		},
	}

	err = k8sClient.Create(ctx, twd)
	require.NoError(t, err, "failed to create TemporalWorkerDeployment")

	// Wait for controller to create the deployment
	expectedDeploymentName := k8s.ComputeVersionedDeploymentName(twd.Name, k8s.ComputeBuildID(twd))
	waitForDeployment(t, k8sClient, expectedDeploymentName, twd.Namespace, 30*time.Second)

	// Get the created deployment
	var deployment appsv1.Deployment
	err = k8sClient.Get(ctx, types.NamespacedName{
		Name:      expectedDeploymentName,
		Namespace: twd.Namespace,
	}, &deployment)
	require.NoError(t, err, "failed to get deployment")

	// Verify the deployment has proper owner references
	var ownerRefFound bool
	var blockOwnerDeletion *bool
	for _, ownerRef := range deployment.OwnerReferences {
		if ownerRef.Kind == "TemporalWorkerDeployment" && ownerRef.Name == twd.Name {
			ownerRefFound = true
			blockOwnerDeletion = ownerRef.BlockOwnerDeletion
			break
		}
	}
	assert.True(t, ownerRefFound, "Deployment should have TemporalWorkerDeployment as owner reference")
	assert.NotNil(t, blockOwnerDeletion, "Owner reference should have BlockOwnerDeletion field set")
	if blockOwnerDeletion != nil {
		assert.True(t, *blockOwnerDeletion, "Owner reference should have BlockOwnerDeletion set to true")
	}

	// Try to delete the deployment directly (this should fail or be recreated)
	originalUID := deployment.UID
	err = k8sClient.Delete(ctx, &deployment)
	if err != nil {
		// Direct deletion failed as expected due to owner reference protection
		t.Logf("Direct deletion failed as expected: %v", err)
	} else {
		// If deletion succeeded, verify the controller recreates it with proper polling
		eventuallyRecreated := func() bool {
			var recreatedDeployment appsv1.Deployment
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      expectedDeploymentName,
				Namespace: twd.Namespace,
			}, &recreatedDeployment)

			if err != nil {
				return false // Deployment not found yet
			}

			// Check if it's a new deployment (different UID)
			return recreatedDeployment.UID != originalUID
		}

		if !waitForCondition(eventuallyRecreated, 30*time.Second, 1*time.Second) {
			assert.Fail(t, "Controller should have recreated the deployment after direct deletion within 30 seconds")
		}
	}

	// Now test proper deletion through the controller by deleting the TWD
	// Delete the TemporalWorkerDeployment
	err = k8sClient.Delete(ctx, twd)
	require.NoError(t, err, "failed to delete TemporalWorkerDeployment")

	// Wait for the deployment to be cleaned up
	eventuallyDeleted := func() bool {
		var checkDeployment appsv1.Deployment
		err := k8sClient.Get(ctx, types.NamespacedName{
			Name:      expectedDeploymentName,
			Namespace: twd.Namespace,
		}, &checkDeployment)
		return client.IgnoreNotFound(err) == nil // Returns true if deployment is not found (deleted)
	}

	deploymentDeleted := waitForCondition(eventuallyDeleted, 30*time.Second, 1*time.Second)
	assert.True(t, deploymentDeleted, "Controller should have cleaned up the deployment when TWD was deleted")
}
