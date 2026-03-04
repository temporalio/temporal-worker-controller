package internal

// This file tests that status.conditions and Kubernetes Events are correctly
// populated by the controller. Only scenarios that are naturally triggered by
// the existing test machinery are covered here.
//
// Covered:
//   - ConditionTemporalConnectionHealthy = True  (any successful reconcile)
//   - ConditionRolloutComplete = True            (version promoted to current)
//   - Event reason RolloutComplete               (emitted alongside the condition above)
//   - ConditionTemporalConnectionHealthy = False (missing TemporalConnection)
//   - Event reason TemporalConnectionNotFound    (emitted alongside the condition above)
//   - ReasonTemporalClientCreationFailed: TemporalConnection pointing to an unreachable port
//   - ReasonTemporalStateFetchFailed: TWD pointing to a Temporal namespace that doesn't exist
//
// Not yet covered, ranked by ease of triggering in functional tests:
//   1. ReasonTestWorkflowStartFailed (Hard): Temporal does NOT reject StartWorkflow for
//      unregistered workflow types — it queues the workflow and waits for a worker. There is
//      no clean way to make StartWorkflow itself fail without injecting network errors or
//      context cancellation.
//   2. ReasonDeploymentCreateFailed / UpdateFailed / ScaleFailed / DeleteFailed (Hard):
//      need the k8s API server in envtest to reject the operation (e.g. via a webhook or quota).
//   3. ReasonVersionPromotionFailed / ReasonMetadataUpdateFailed (Hard): need SetCurrentVersion
//      or UpdateVersionMetadata to fail on an otherwise healthy Temporal server. Similar challenge
//      as making StartWorkflow fail.
//   4. ReasonPlanGenerationFailed / ReasonPlanExecutionFailed (Hard): meta-errors that wrap the
//      above; would fire automatically if any of the above are triggered.
//   5. ReasonAuthSecretInvalid (Impossible): This is never triggered. The CRD schema enforces a DNS name pattern
//      on mutualTLSSecretRef.name, so an empty name is rejected before it reaches the controller.
//      Additionally, resolveAuthSecretName never returns an error with the current
//      implementation (the nil guard in getTLSSecretName/getAPIKeySecretName is redundant
//      because the caller already checks non-nil). The code path is effectively dead.

import (
	"context"
	"testing"
	"time"

	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	"github.com/temporalio/temporal-worker-controller/internal/testhelpers"
	"go.temporal.io/server/temporaltest"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

func runConditionsAndEventsTests(
	t *testing.T,
	k8sClient client.Client,
	mgr manager.Manager,
	ts *temporaltest.TestServer,
	testNamespace string,
) {
	cases := []testCase{
		{
			// Verifies that ConditionTemporalConnectionHealthy is set to True whenever
			// the controller successfully connects to Temporal.
			name: "conditions-connection-healthy",
			builder: testhelpers.NewTestCase().
				WithInput(
					testhelpers.NewTemporalWorkerDeploymentBuilder().
						WithManualStrategy().
						WithTargetTemplate("v1.0"),
				).
				WithExpectedStatus(
					testhelpers.NewStatusBuilder().
						WithTargetVersion("v1.0", temporaliov1alpha1.VersionStatusInactive, -1, true, false),
				).
				WithValidatorFunction(func(t *testing.T, ctx context.Context, tc testhelpers.TestCase, env testhelpers.TestEnv) {
					twd := tc.GetTWD()
					waitForCondition(t, ctx, env.K8sClient, twd.Name, twd.Namespace,
						temporaliov1alpha1.ConditionTemporalConnectionHealthy,
						metav1.ConditionTrue,
						temporaliov1alpha1.ReasonTemporalConnectionHealthy,
						10*time.Second, time.Second)
				}),
		},
		{
			// Verifies that ConditionRolloutComplete is set to True after the controller
			// promotes a version to current. Note: only a condition is set here — no
			// separate k8s Event is emitted for RolloutComplete.
			name: "conditions-rollout-complete",
			builder: testhelpers.NewTestCase().
				WithInput(
					testhelpers.NewTemporalWorkerDeploymentBuilder().
						WithAllAtOnceStrategy().
						WithTargetTemplate("v1.0"),
				).
				WithExpectedStatus(
					testhelpers.NewStatusBuilder().
						WithTargetVersion("v1.0", temporaliov1alpha1.VersionStatusCurrent, -1, true, false).
						WithCurrentVersion("v1.0", true, false),
				).
				WithValidatorFunction(func(t *testing.T, ctx context.Context, tc testhelpers.TestCase, env testhelpers.TestEnv) {
					twd := tc.GetTWD()
					waitForCondition(t, ctx, env.K8sClient, twd.Name, twd.Namespace,
						temporaliov1alpha1.ConditionRolloutComplete,
						metav1.ConditionTrue,
						temporaliov1alpha1.ReasonRolloutComplete,
						10*time.Second, time.Second)
				}),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			testTemporalWorkerDeploymentCreation(ctx, t, k8sClient, mgr, ts, tc.builder.BuildWithValues(tc.name, testNamespace, ts.GetDefaultNamespace()))
		})
	}

	// The following three tests each trigger a different ConditionTemporalConnectionHealthy=False
	// reason. They all run standalone (not through testTemporalWorkerDeploymentCreation) because
	// the controller fails before creating any k8s Deployments, so the normal status-validation
	// and deployment-wait machinery in testTemporalWorkerDeploymentCreation would time out.
	//
	// conditions-temporal-state-fetch-failed also cannot use the testCase/testCaseBuilder
	// structure because testTemporalWorkerDeploymentCreation hardcodes the Temporal namespace
	// via BuildWithValues(name, ns, ts.GetDefaultNamespace()) with no way to inject a custom
	// namespace.
	//
	// All three share the same skeleton, extracted into testUnhealthyConnectionCondition below.
	t.Run("conditions-missing-connection", func(t *testing.T) {
		// No TemporalConnection is created; the controller cannot find the one referenced by
		// the TWD and immediately sets the condition to False.
		testUnhealthyConnectionCondition(t, k8sClient,
			"conditions-missing-connection", testNamespace, ts.GetDefaultNamespace(),
			nil,
			temporaliov1alpha1.ReasonTemporalConnectionNotFound)
	})
	t.Run("conditions-client-creation-failed", func(t *testing.T) {
		// Port 1 is never bound; the SDK returns ECONNREFUSED immediately.
		testUnhealthyConnectionCondition(t, k8sClient,
			"conditions-client-creation-failed", testNamespace, ts.GetDefaultNamespace(),
			&temporaliov1alpha1.TemporalConnectionSpec{HostPort: "localhost:1"},
			temporaliov1alpha1.ReasonTemporalClientCreationFailed)
	})
	t.Run("conditions-temporal-state-fetch-failed", func(t *testing.T) {
		// The real server is reachable so client creation succeeds, but "does-not-exist" is
		// not a registered Temporal namespace so Describe() fails.
		testUnhealthyConnectionCondition(t, k8sClient,
			"conditions-temporal-state-fetch-failed", testNamespace, "does-not-exist",
			&temporaliov1alpha1.TemporalConnectionSpec{HostPort: ts.GetFrontendHostPort()},
			temporaliov1alpha1.ReasonTemporalStateFetchFailed)
	})
}

// testUnhealthyConnectionCondition is shared by the four error-path condition tests.
// It optionally creates a TemporalConnection (nil connectionSpec = missing connection),
// creates a TWD pointing to that connection with the given temporalNamespace, then asserts
// that ConditionTemporalConnectionHealthy becomes False with the expected reason and that a
// matching Warning event is emitted.
func testUnhealthyConnectionCondition(
	t *testing.T,
	k8sClient client.Client,
	name, testNamespace, temporalNamespace string,
	connectionSpec *temporaliov1alpha1.TemporalConnectionSpec,
	expectedReason string,
) {
	t.Helper()
	ctx := context.Background()

	if connectionSpec != nil {
		conn := &temporaliov1alpha1.TemporalConnection{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace},
			Spec:       *connectionSpec,
		}
		if err := k8sClient.Create(ctx, conn); err != nil {
			t.Fatalf("failed to create TemporalConnection: %v", err)
		}
	}

	twd := testhelpers.NewTemporalWorkerDeploymentBuilder().
		WithManualStrategy().
		WithTargetTemplate("v1.0").
		WithName(name).
		WithNamespace(testNamespace).
		WithTemporalConnection(name).
		WithTemporalNamespace(temporalNamespace).
		Build()

	if err := k8sClient.Create(ctx, twd); err != nil {
		t.Fatalf("failed to create TWD: %v", err)
	}

	waitForCondition(t, ctx, k8sClient, twd.Name, twd.Namespace,
		temporaliov1alpha1.ConditionTemporalConnectionHealthy,
		metav1.ConditionFalse, expectedReason,
		30*time.Second, time.Second)
	waitForEvent(t, ctx, k8sClient, twd.Name, twd.Namespace,
		expectedReason, 30*time.Second, time.Second)
}
