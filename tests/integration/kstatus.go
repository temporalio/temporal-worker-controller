//go:build integration
// +build integration

package integration

// This file verifies that the conditions the controller writes are read the way
// kstatus intends, against a real API server. The unit tests in
// internal/controller/kstatus_test.go cover the same states with a fake client;
// what only a real API server can prove is that the CRD schema does not prune
// status.conditions[type=Stalled|Reconciling] or status.observedGeneration on the
// way in, and that the values survive a status-subresource round trip.
//
// kstatus is the library Helm --wait and Flux use to decide whether a custom
// resource is healthy, so these verdicts are what those tools will conclude.
//
// Covered:
//   - Current:    target version promoted to current
//   - InProgress: target registered but not yet promoted (Reconciling=True)
//   - Failed:     terminal blocking error (Stalled=True)
//   - InProgress: transient blocking error (Reconciling=True, no Stalled)

import (
	"context"
	"fmt"
	"testing"
	"time"

	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	"github.com/temporalio/temporal-worker-controller/internal/testhelpers"
	"go.temporal.io/server/temporaltest"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	kstatus "sigs.k8s.io/cli-utils/pkg/kstatus/status"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

// waitForKstatus polls the named WorkerDeployment, converts what the API server
// actually returns into unstructured, and runs the real kstatus decision tree over
// it until the verdict matches want, or fatals on timeout.
func waitForKstatus(
	ctx context.Context,
	t *testing.T,
	k8sClient client.Client,
	name, namespace string,
	want kstatus.Status,
	timeout, interval time.Duration,
) {
	t.Helper()
	eventually(t, timeout, interval, func() error {
		var wd temporaliov1alpha1.WorkerDeployment
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, &wd); err != nil {
			return fmt.Errorf("get WorkerDeployment: %w", err)
		}
		// Get strips TypeMeta; kstatus only uses it for message text, but set it so
		// failure messages name the kind.
		wd.TypeMeta = metav1.TypeMeta{
			APIVersion: temporaliov1alpha1.GroupVersion.String(),
			Kind:       "WorkerDeployment",
		}

		content, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&wd)
		if err != nil {
			return fmt.Errorf("convert to unstructured: %w", err)
		}
		res, err := kstatus.Compute(&unstructured.Unstructured{Object: content})
		if err != nil {
			return fmt.Errorf("kstatus.Compute: %w", err)
		}

		if res.Status != want {
			return fmt.Errorf("kstatus verdict: want %s, got %s (message: %q, conditions: %v)",
				want, res.Status, res.Message, conditionSummary(wd.Status.Conditions))
		}
		return nil
	})
}

// conditionSummary renders conditions compactly for failure messages.
func conditionSummary(conds []metav1.Condition) []string {
	out := make([]string, 0, len(conds))
	for _, c := range conds {
		out = append(out, fmt.Sprintf("%s=%s(%s)", c.Type, c.Status, c.Reason))
	}
	return out
}

// waitForObservedGenerationCurrent waits for status.observedGeneration to
// catch up with metadata.generation.
func waitForObservedGenerationCurrent(
	ctx context.Context,
	t *testing.T,
	k8sClient client.Client,
	name, namespace string,
	timeout, interval time.Duration,
) {
	t.Helper()
	eventually(t, timeout, interval, func() error {
		var wd temporaliov1alpha1.WorkerDeployment
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, &wd); err != nil {
			return fmt.Errorf("get WorkerDeployment: %w", err)
		}
		if wd.Status.ObservedGeneration != wd.Generation {
			return fmt.Errorf("status.observedGeneration != metadata.generation")
		}
		return nil
	})
}

// waitForObservedConnectionRef waits for status.observedConnectionRef to point
// to the supplied named connection.
func waitForObservedConnectionRef(
	ctx context.Context,
	t *testing.T,
	k8sClient client.Client,
	name, namespace string,
	wantConnectionName string,
	timeout, interval time.Duration,
) {
	t.Helper()
	eventually(t, timeout, interval, func() error {
		var wd temporaliov1alpha1.WorkerDeployment
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, &wd); err != nil {
			return fmt.Errorf("get WorkerDeployment: %w", err)
		}

		ocr := wd.Status.ObservedConnectionRef
		if ocr == nil {
			return fmt.Errorf(
				"status.observedConnectionRef is nil, expected non-nil pointing at connection with name %s",
				wantConnectionName,
			)
		}
		if ocr.Name != wantConnectionName {
			return fmt.Errorf(
				"status.observedConnectionRef.Name = %s, want %s",
				ocr.Name, wantConnectionName,
			)
		}
		return nil
	})
}

//nolint:revive // not much to be done about the complexity of this test case.
func runKstatusTests(
	t *testing.T,
	k8sClient client.Client,
	mgr manager.Manager,
	ts *temporaltest.TestServer,
	testNamespace string,
) {
	cases := []testCase{
		{
			// A completed rollout must read as Current, or Helm --wait never returns.
			name: "kstatus-current-when-rollout-complete",
			builder: testhelpers.NewTestCase().
				WithInput(
					testhelpers.NewWorkerDeploymentBuilder().
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
					waitForObservedGenerationCurrent(ctx, t, env.K8sClient, twd.Name, twd.Namespace, 30*time.Second, time.Second)
					waitForKstatus(ctx, t, env.K8sClient, twd.Name, twd.Namespace,
						kstatus.CurrentStatus, 30*time.Second, time.Second)
				}),
		},
		{
			// Registered but awaiting promotion: Reconciling=True should put kstatus on
			// its intended path rather than the Ready=False fallback.
			name: "kstatus-inprogress-while-awaiting-promotion",
			builder: testhelpers.NewTestCase().
				WithInput(
					testhelpers.NewWorkerDeploymentBuilder().
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
						temporaliov1alpha1.ConditionReconciling,
						metav1.ConditionTrue,
						temporaliov1alpha1.ReasonWaitingForPromotion,
						30*time.Second, time.Second)
					waitForObservedGenerationCurrent(ctx, t, env.K8sClient, twd.Name, twd.Namespace, 30*time.Second, time.Second)
					waitForKstatus(ctx, t, env.K8sClient, twd.Name, twd.Namespace,
						kstatus.InProgressStatus, 30*time.Second, time.Second)
				}),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			testWorkerDeploymentCreation(ctx, t, k8sClient, mgr, ts, tc.builder.BuildWithValues(tc.name, testNamespace, ts.GetDefaultNamespace()))
		})
	}

	// The blocking-error cases run standalone for the same reason the existing
	// condition tests do: the controller fails before creating any k8s Deployment, so
	// testWorkerDeploymentCreation's status-validation machinery would time out.

	t.Run("kstatus-failed-on-invalid-spec", func(t *testing.T) {
		ctx := context.Background()
		name := "kstatus-failed-invalid-spec"

		conn := &temporaliov1alpha1.Connection{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace},
			Spec:       temporaliov1alpha1.ConnectionSpec{HostPort: ts.GetFrontendHostPort()},
		}
		if err := k8sClient.Create(ctx, conn); err != nil {
			t.Fatalf("failed to create Connection: %v", err)
		}

		// Progressive ramp steps must strictly increase, so 50 followed by 10 is
		// invalid. The CRD schema cannot express that ordering rule (it does enforce
		// the 30s minimum pause used here), and the validating webhook is not
		// installed in this environment, so the API server accepts the object and the
		// controller reports ReasonInvalidSpec. Nothing arriving later can make this
		// spec valid, so it is terminal and must read as Failed rather than making a
		// CD tool wait out its timeout.
		twd := testhelpers.NewWorkerDeploymentBuilder().
			WithProgressiveStrategy(
				temporaliov1alpha1.RolloutStep{RampPercentage: 50, PauseDuration: metav1.Duration{Duration: 30 * time.Second}},
				temporaliov1alpha1.RolloutStep{RampPercentage: 10, PauseDuration: metav1.Duration{Duration: 30 * time.Second}},
			).
			WithTargetTemplate("v1.0").
			WithName(name).
			WithNamespace(testNamespace).
			WithConnection(name).
			WithTemporalNamespace(ts.GetDefaultNamespace()).
			Build()
		if err := k8sClient.Create(ctx, twd); err != nil {
			t.Fatalf("failed to create WorkerDeployment: %v", err)
		}

		waitForCondition(t, ctx, k8sClient, twd.Name, twd.Namespace,
			temporaliov1alpha1.ConditionStalled,
			metav1.ConditionTrue,
			temporaliov1alpha1.ReasonInvalidSpec,
			30*time.Second, time.Second)
		// The blocked path must still advance observedGeneration, or kstatus returns
		// InProgress from its generation check and never reads Stalled at all.
		waitForObservedGenerationCurrent(ctx, t, k8sClient, twd.Name, twd.Namespace, 30*time.Second, time.Second)
		waitForKstatus(ctx, t, k8sClient, twd.Name, twd.Namespace,
			kstatus.FailedStatus, 30*time.Second, time.Second)
	})

	t.Run("kstatus-inprogress-on-missing-connection", func(t *testing.T) {
		ctx := context.Background()
		name := "kstatus-inprogress-missing-conn"

		// No Connection is created, so the reference cannot resolve. This must stay
		// InProgress: a WorkerDeployment applied alongside its Connection has no
		// ordering guarantee, so the reference may simply not exist yet, and reporting
		// Failed would abort a deploy that was about to succeed.
		twd := testhelpers.NewWorkerDeploymentBuilder().
			WithManualStrategy().
			WithTargetTemplate("v1.0").
			WithName(name).
			WithNamespace(testNamespace).
			WithConnection(name).
			WithTemporalNamespace(ts.GetDefaultNamespace()).
			Build()
		if err := k8sClient.Create(ctx, twd); err != nil {
			t.Fatalf("failed to create WorkerDeployment: %v", err)
		}

		waitForCondition(t, ctx, k8sClient, twd.Name, twd.Namespace,
			temporaliov1alpha1.ConditionReconciling,
			metav1.ConditionTrue,
			temporaliov1alpha1.ReasonConnectionNotFound,
			30*time.Second, time.Second)
		waitForObservedGenerationCurrent(ctx, t, k8sClient, twd.Name, twd.Namespace, 30*time.Second, time.Second)
		waitForKstatus(ctx, t, k8sClient, twd.Name, twd.Namespace,
			kstatus.InProgressStatus, 30*time.Second, time.Second)

		var got temporaliov1alpha1.WorkerDeployment
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: twd.Name, Namespace: twd.Namespace}, &got); err != nil {
			t.Fatalf("get WorkerDeployment: %v", err)
		}
		for _, c := range got.Status.Conditions {
			if c.Type == temporaliov1alpha1.ConditionStalled && c.Status == metav1.ConditionTrue {
				t.Fatalf("a missing Connection must not set Stalled=True (reason %q)", c.Reason)
			}
		}
	})

	t.Run("kstatus-inprogress-on-transient-error", func(t *testing.T) {
		ctx := context.Background()
		name := "kstatus-inprogress-transient"

		// The Connection resolves and the client dials, but the Temporal namespace does
		// not exist, so the state fetch fails. ReasonTemporalStateFetchFailed is
		// deliberately NOT in stalledReasons: the controller keeps retrying, so
		// reporting Failed would abort a deploy over something self-resolving.
		conn := &temporaliov1alpha1.Connection{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace},
			Spec:       temporaliov1alpha1.ConnectionSpec{HostPort: ts.GetFrontendHostPort()},
		}
		if err := k8sClient.Create(ctx, conn); err != nil {
			t.Fatalf("failed to create Connection: %v", err)
		}

		twd := testhelpers.NewWorkerDeploymentBuilder().
			WithManualStrategy().
			WithTargetTemplate("v1.0").
			WithName(name).
			WithNamespace(testNamespace).
			WithConnection(name).
			WithTemporalNamespace("does-not-exist").
			Build()
		if err := k8sClient.Create(ctx, twd); err != nil {
			t.Fatalf("failed to create WorkerDeployment: %v", err)
		}

		waitForCondition(t, ctx, k8sClient, twd.Name, twd.Namespace,
			temporaliov1alpha1.ConditionReconciling,
			metav1.ConditionTrue,
			temporaliov1alpha1.ReasonTemporalStateFetchFailed,
			30*time.Second, time.Second)
		waitForObservedGenerationCurrent(ctx, t, k8sClient, twd.Name, twd.Namespace, 30*time.Second, time.Second)
		waitForKstatus(ctx, t, k8sClient, twd.Name, twd.Namespace,
			kstatus.InProgressStatus, 30*time.Second, time.Second)

		var got temporaliov1alpha1.WorkerDeployment
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: twd.Name, Namespace: twd.Namespace}, &got); err != nil {
			t.Fatalf("get WorkerDeployment: %v", err)
		}
		for _, c := range got.Status.Conditions {
			if c.Type == temporaliov1alpha1.ConditionStalled && c.Status == metav1.ConditionTrue {
				t.Fatalf("a transient failure must not set Stalled=True (reason %q)", c.Reason)
			}
		}
	})

	t.Run("kstatus-change-conn", func(t *testing.T) {
		// We test the following scenario: reconcile a WD with connection A (so
		// ObservedConnectionRef = A), then edit the spec to connection B and
		// makes the first reconcile block, create connection B and reconcile
		// again, and finally assert the finalizer is removed from connection
		// A.
		wdName := "kstatus-change-conn"
		ctx := context.Background()
		tc := testhelpers.NewTestCase().
			WithInput(
				testhelpers.NewWorkerDeploymentBuilder().
					WithAllAtOnceStrategy().
					WithGate(true).
					WithReplicas(2).
					WithTargetTemplate("v1.0"),
			).
			WithExpectedStatus(
				testhelpers.NewStatusBuilder().
					WithTargetVersion("v1.0", temporaliov1alpha1.VersionStatusCurrent, -1, true, false).
					WithCurrentVersion("v1.0", true, false),
			).BuildWithValues(wdName, testNamespace, ts.GetDefaultNamespace())

		testWorkerDeploymentCreation(ctx, t, k8sClient, mgr, ts, tc)

		connAName := wdName
		connBName := wdName + "-other"

		var twd temporaliov1alpha1.WorkerDeployment
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: wdName, Namespace: testNamespace}, &twd); err != nil {
			t.Fatalf("get WorkerDeployment: %v", err)
		}

		waitForObservedGenerationCurrent(ctx, t, k8sClient, twd.Name, twd.Namespace, 30*time.Second, time.Second)
		waitForKstatus(ctx, t, k8sClient, twd.Name, twd.Namespace,
			kstatus.CurrentStatus, 30*time.Second, time.Second)
		waitForObservedConnectionRef(ctx, t, k8sClient, twd.Name, twd.Namespace, connAName, 30*time.Second, time.Second)

		// Now we change the WD's connection to point to a yet-to-exist
		// connection B, check that the WD goes to Reconciling=True state and
		// that the observed connection ref continues to point to connection A.
		twd.Spec.WorkerOptions.ConnectionRef = temporaliov1alpha1.ConnectionReference{Name: connBName}
		if err := k8sClient.Update(ctx, &twd); err != nil {
			t.Fatalf("failed to update WD to Connection B: %v", err)
		}

		waitForCondition(t, ctx, k8sClient, twd.Name, twd.Namespace,
			temporaliov1alpha1.ConditionReconciling,
			metav1.ConditionTrue,
			temporaliov1alpha1.ReasonConnectionNotFound,
			30*time.Second, time.Second)
		waitForObservedGenerationCurrent(ctx, t, k8sClient, twd.Name, twd.Namespace, 30*time.Second, time.Second)
		waitForKstatus(ctx, t, k8sClient, twd.Name, twd.Namespace,
			kstatus.InProgressStatus, 30*time.Second, time.Second)
		// observed connection should still point to connection A until connection
		// B actually exists.
		waitForObservedConnectionRef(ctx, t, k8sClient, twd.Name, twd.Namespace, connAName, 30*time.Second, time.Second)

		var got temporaliov1alpha1.WorkerDeployment
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: twd.Name, Namespace: twd.Namespace}, &got); err != nil {
			t.Fatalf("get WorkerDeployment: %v", err)
		}
		for _, c := range got.Status.Conditions {
			if c.Type == temporaliov1alpha1.ConditionStalled && c.Status == metav1.ConditionTrue {
				t.Fatalf("a missing Connection must not set Stalled=True (reason %q)", c.Reason)
			}
		}

		// Now create connection B and wait for the WD to get to Current.
		connB := &temporaliov1alpha1.Connection{
			ObjectMeta: metav1.ObjectMeta{Name: connBName, Namespace: testNamespace},
			Spec:       temporaliov1alpha1.ConnectionSpec{HostPort: ts.GetFrontendHostPort()},
		}
		if err := k8sClient.Create(ctx, connB); err != nil {
			t.Fatalf("failed to create Connection B: %v", err)
		}
		waitForObservedGenerationCurrent(ctx, t, k8sClient, twd.Name, twd.Namespace, 30*time.Second, time.Second)
		waitForKstatus(ctx, t, k8sClient, twd.Name, twd.Namespace,
			kstatus.CurrentStatus, 30*time.Second, time.Second)
		waitForObservedConnectionRef(ctx, t, k8sClient, twd.Name, twd.Namespace, connBName, 30*time.Second, time.Second)
	})
}
