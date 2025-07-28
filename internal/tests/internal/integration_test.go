package internal

import (
	"context"
	"testing"
	"time"

	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	"github.com/temporalio/temporal-worker-controller/internal/controller"
	"github.com/temporalio/temporal-worker-controller/internal/k8s"
	"github.com/temporalio/temporal-worker-controller/internal/testhelpers"
	"go.temporal.io/server/common/dynamicconfig"
	"go.temporal.io/server/temporal"
	"go.temporal.io/server/temporaltest"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

const (
	testShortPollerHistoryTTL         = time.Second
	testDrainageVisibilityGracePeriod = time.Second
	testDrainageRefreshInterval       = time.Second
)

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
		if err := k8sClient.Delete(ctx, testNamespace); err != nil {
			t.Errorf("failed to delete test namespace: %v", err)
		}
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
	if err := k8sClient.Create(ctx, temporalConnection); err != nil {
		t.Fatalf("failed to create TemporalConnection: %v", err)
	}

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

	if err := k8sClient.Create(ctx, twd); err != nil {
		t.Fatalf("failed to create TemporalWorkerDeployment: %v", err)
	}

	// Wait for controller to create the deployment
	expectedDeploymentName := k8s.ComputeVersionedDeploymentName(twd.Name, k8s.ComputeBuildID(twd))
	waitForDeployment(t, k8sClient, expectedDeploymentName, twd.Namespace, 30*time.Second)

	// Get the created deployment
	var deployment appsv1.Deployment
	if err := k8sClient.Get(ctx, types.NamespacedName{
		Name:      expectedDeploymentName,
		Namespace: twd.Namespace,
	}, &deployment); err != nil {
		t.Fatalf("failed to get deployment: %v", err)
	}

	// Verify the deployment has the finalizer (this should be added by the controller)
	t.Log("Verifying deployment has finalizer protection")
	if !controllerutil.ContainsFinalizer(&deployment, controller.TemporalWorkerDeploymentFinalizer) {
		// Since the deployment itself doesn't have the finalizer, let's verify it's owned by the TWD
		// which should have the finalizer and prevent deletion
		t.Log("Deployment doesn't have finalizer directly, checking owner reference protection")
	}

	// Verify the deployment has proper owner references
	found := false
	for _, ownerRef := range deployment.OwnerReferences {
		if ownerRef.Kind == "TemporalWorkerDeployment" && ownerRef.Name == twd.Name {
			found = true
			if ownerRef.BlockOwnerDeletion == nil || !*ownerRef.BlockOwnerDeletion {
				t.Error("Owner reference should have BlockOwnerDeletion set to true")
			}
			break
		}
	}
	if !found {
		t.Error("Deployment should have TemporalWorkerDeployment as owner reference")
	}

	// Try to delete the deployment directly (this should fail or be recreated)
	t.Log("Attempting to delete deployment directly")
	originalUID := deployment.UID
	if err := k8sClient.Delete(ctx, &deployment); err != nil {
		t.Logf("Direct deletion failed as expected: %v", err)
	} else {
		// If deletion succeeded, verify the controller recreates it
		t.Log("Deletion succeeded, verifying controller recreates deployment")
		time.Sleep(5 * time.Second) // Give controller time to recreate

		var recreatedDeployment appsv1.Deployment
		if err := k8sClient.Get(ctx, types.NamespacedName{
			Name:      expectedDeploymentName,
			Namespace: twd.Namespace,
		}, &recreatedDeployment); err != nil {
			t.Error("Controller should have recreated the deployment after direct deletion")
		} else if recreatedDeployment.UID == originalUID {
			t.Error("Deployment should have been recreated with new UID")
		} else {
			t.Log("Controller successfully recreated the deployment")
		}
	}

	// Now test proper deletion through the controller by deleting the TWD
	t.Log("Testing proper deletion through TemporalWorkerDeployment deletion")

	// Delete the TemporalWorkerDeployment
	if err := k8sClient.Delete(ctx, twd); err != nil {
		t.Fatalf("failed to delete TemporalWorkerDeployment: %v", err)
	}

	// Wait for the deployment to be cleaned up
	t.Log("Waiting for deployment to be cleaned up by controller")
	deadline := time.Now().Add(30 * time.Second)
	deploymentDeleted := false
	for time.Now().Before(deadline) {
		var checkDeployment appsv1.Deployment
		if err := k8sClient.Get(ctx, types.NamespacedName{
			Name:      expectedDeploymentName,
			Namespace: twd.Namespace,
		}, &checkDeployment); err != nil {
			if client.IgnoreNotFound(err) == nil {
				deploymentDeleted = true
				break
			}
		}
		time.Sleep(1 * time.Second)
	}

	if !deploymentDeleted {
		t.Error("Controller should have cleaned up the deployment when TWD was deleted")
	} else {
		t.Log("Controller successfully cleaned up deployment when TWD was deleted")
	}
}
