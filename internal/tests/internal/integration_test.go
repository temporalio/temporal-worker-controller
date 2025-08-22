package internal

import (
	"context"
	"testing"
	"time"

	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	"github.com/temporalio/temporal-worker-controller/internal/k8s"
	"github.com/temporalio/temporal-worker-controller/internal/testhelpers"
	"go.temporal.io/server/common/dynamicconfig"
	"go.temporal.io/server/temporal"
	"go.temporal.io/server/temporaltest"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
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
				testhelpers.NewExistingDeploymentInfo("v0", 1),
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
				testhelpers.NewExistingDeploymentInfo("v0", 1),
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
				testhelpers.NewExistingDeploymentInfo("v0", 1),
			).
			WithWaitTime(5 * time.Second).
			WithExpectedStatus(
				testhelpers.NewStatusBuilder().
					WithTargetVersion("v1", temporaliov1alpha1.VersionStatusInactive, -1, true, false).
					WithCurrentVersion("v0", true, true),
			),
	}

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
			testTemporalWorkerDeploymentCreation(ctx, t, k8sClient, mgr, tsShortTTL, tc.BuildWithValues(testName, testNamespace.Name, ts.GetDefaultNamespace()))
		})
	}

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
	}

	makePreliminaryStatusTrue(ctx, t, env, twd)
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
	verifyTemporalWorkerDeploymentStatusEventually(t, ctx, k8sClient, twd.Name, twd.Namespace, expectedStatus, 30*time.Second, 5*time.Second)
	verifyTemporalStateMatchesStatusEventually(t, ctx, ts, twd, *expectedStatus, 30*time.Second, 5*time.Second)
}
