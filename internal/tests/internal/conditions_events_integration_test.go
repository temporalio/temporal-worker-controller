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
//
// Not yet covered, ranked by ease of triggering:
//   1. ReasonAuthSecretInvalid (Easy): create a TemporalConnection with conflicting/incomplete
//      auth config, e.g. mTLS mode with no secret ref.
//   2. ReasonTemporalClientCreationFailed (Medium): set hostPort to an unreachable address;
//      depends on whether the Temporal SDK validates the connection eagerly at UpsertClient.
//   3. ReasonTemporalStateFetchFailed (Medium): set temporalNamespace on the TWD to a namespace
//      that doesn't exist on the test server; the deployment state query would fail.
//   4. ReasonTestWorkflowStartFailed (Medium): need Temporal to reject StartWorkflow; could do
//      this by naming a Gate workflow that is not registered on the worker.
//   5. ReasonDeploymentCreateFailed / UpdateFailed / ScaleFailed / DeleteFailed (Hard):
//      need the k8s API server in envtest to reject the operation (e.g. via a webhook or quota).
//   6. ReasonVersionPromotionFailed / ReasonMetadataUpdateFailed (Hard): need SetCurrentVersion
//      or UpdateVersionMetadata to fail on an otherwise healthy Temporal server.
//   7. ReasonPlanGenerationFailed / ReasonPlanExecutionFailed (Hard): meta-errors that wrap the
//      above; would fire automatically if any of the above are triggered.

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

	// conditions-missing-connection runs standalone because it deliberately omits the
	// TemporalConnection resource that testTemporalWorkerDeploymentCreation always creates.
	// We don't need to create a test Temporal server for this test case because the connection
	// is missing anyways.
	t.Run("conditions-missing-connection", func(t *testing.T) {
		ctx := context.Background()

		twd := testhelpers.NewTemporalWorkerDeploymentBuilder().
			WithManualStrategy().
			WithTargetTemplate("v1.0").
			WithName("conditions-missing-connection").
			WithNamespace(testNamespace).
			WithTemporalConnection("does-not-exist").
			WithTemporalNamespace(ts.GetDefaultNamespace()).
			Build()

		if err := k8sClient.Create(ctx, twd); err != nil {
			t.Fatalf("failed to create TWD: %v", err)
		}

		waitForCondition(t, ctx, k8sClient, twd.Name, twd.Namespace,
			temporaliov1alpha1.ConditionTemporalConnectionHealthy,
			metav1.ConditionFalse,
			temporaliov1alpha1.ReasonTemporalConnectionNotFound,
			30*time.Second, time.Second)
		waitForEvent(t, ctx, k8sClient, twd.Name, twd.Namespace,
			temporaliov1alpha1.ReasonTemporalConnectionNotFound,
			30*time.Second, time.Second)
	})
}
