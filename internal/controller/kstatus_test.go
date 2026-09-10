// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package controller

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	kstatus "sigs.k8s.io/cli-utils/pkg/kstatus/status"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// computeKstatus feeds obj through the real kstatus decision tree
// (sigs.k8s.io/cli-utils) and returns its verdict. This is the same code path
// Argo Rollouts and Helm --wait use to decide whether a custom resource is
// healthy, so whatever this returns is what those tools will conclude.
//
// The conversion to unstructured is not test scaffolding: kstatus is a generic
// library that has never heard of our types, so in production it reads our
// objects as untyped JSON off the wire. Converting here reproduces that.
func computeKstatus(t *testing.T, obj runtime.Object) *kstatus.Result {
	t.Helper()

	content, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	require.NoError(t, err, "convert typed object to unstructured")

	res, err := kstatus.Compute(&unstructured.Unstructured{Object: content})
	require.NoError(t, err, "kstatus.Compute")
	require.NotNil(t, res, "kstatus.Compute returned a nil result")

	return res
}

// TestKstatusBaseline_WorkerDeployment records the verdict kstatus reaches for
// each rollout state today. It is a characterization test: `today` is what the
// current code produces, `desired` is what it should produce once issue #478 is
// resolved. Where they differ the test still passes and logs a KNOWN GAP, so
// this file is green on main and fails loudly when the behavior is fixed —
// at which point update `today` in the same commit as the fix.
func TestKstatusBaseline_WorkerDeployment(t *testing.T) {
	ctx := context.Background()

	cases := []struct {
		name    string
		build   func() *temporaliov1alpha1.WorkerDeployment
		today   kstatus.Status
		desired kstatus.Status
	}{
		{
			// Pods created, no worker has polled Temporal yet.
			name: "TargetNotRegistered",
			build: func() *temporaliov1alpha1.WorkerDeployment {
				r, _ := newTestReconciler(nil)
				wd := makeWD("wd", "default", "conn")
				wd.Status.ObservedGeneration = wd.Generation
				wd.Status.TargetVersion.Status = temporaliov1alpha1.VersionStatusNotRegistered
				r.syncConditions(wd)
				return wd
			},
			today:   kstatus.InProgressStatus,
			desired: kstatus.InProgressStatus,
		},
		{
			// Registered with Temporal, receiving no traffic, awaiting promotion.
			name: "TargetInactive",
			build: func() *temporaliov1alpha1.WorkerDeployment {
				r, _ := newTestReconciler(nil)
				wd := makeWD("wd", "default", "conn")
				wd.Status.ObservedGeneration = wd.Generation
				wd.Status.TargetVersion.Status = temporaliov1alpha1.VersionStatusInactive
				r.syncConditions(wd)
				return wd
			},
			today:   kstatus.InProgressStatus,
			desired: kstatus.InProgressStatus,
		},
		{
			// Mid-canary: receiving a percentage of new workflows.
			name: "TargetRamping",
			build: func() *temporaliov1alpha1.WorkerDeployment {
				r, _ := newTestReconciler(nil)
				wd := makeWD("wd", "default", "conn")
				wd.Status.ObservedGeneration = wd.Generation
				wd.Status.TargetVersion.Status = temporaliov1alpha1.VersionStatusRamping
				r.syncConditions(wd)
				return wd
			},
			today:   kstatus.InProgressStatus,
			desired: kstatus.InProgressStatus,
		},
		{
			// The finish line. This is the case that makes `helm upgrade --wait`
			// return successfully today; do not break it.
			name: "TargetIsCurrent",
			build: func() *temporaliov1alpha1.WorkerDeployment {
				r, _ := newTestReconciler(nil)
				wd := makeWD("wd", "default", "conn")
				wd.Status.ObservedGeneration = wd.Generation
				wd.Status.TargetVersion.Status = temporaliov1alpha1.VersionStatusCurrent
				r.syncConditions(wd)
				return wd
			},
			today:   kstatus.CurrentStatus,
			desired: kstatus.CurrentStatus,
		},
		{
			// Waiting on another object to exist. Applying a WorkerDeployment
			// alongside its Connection gives no ordering guarantee, so this can be a
			// normal few-second gap rather than a mistake — reporting Failed would
			// abort a deploy that was about to succeed. Stays InProgress, which is
			// also what the controller did before Stalled existed.
			name: "BlockedOnMissingConnection",
			build: func() *temporaliov1alpha1.WorkerDeployment {
				r, _ := newTestReconciler(nil)
				wd := makeWD("wd", "default", "conn")
				r.recordWarningAndSetBlocked(ctx, wd,
					temporaliov1alpha1.ReasonConnectionNotFound,
					`Connection "conn" not found`,
					`Connection "conn" not found`)
				return wd
			},
			today:   kstatus.InProgressStatus,
			desired: kstatus.InProgressStatus,
		},
		{
			// Same shape as the case above: ReasonAuthSecretInvalid also fires when
			// the credential Secret is merely absent, which a deploy can resolve on
			// its own moments later.
			name: "BlockedOnMissingCredentials",
			build: func() *temporaliov1alpha1.WorkerDeployment {
				r, _ := newTestReconciler(nil)
				wd := makeWD("wd", "default", "conn")
				r.recordWarningAndSetBlocked(ctx, wd,
					temporaliov1alpha1.ReasonAuthSecretInvalid,
					"Unable to resolve auth secret",
					"Unable to resolve auth secret")
				return wd
			},
			today:   kstatus.InProgressStatus,
			desired: kstatus.InProgressStatus,
		},
		{
			// A previously-healthy WorkerDeployment that just got an invalid spec:
			// generation advanced to 2 while observedGeneration was still 1. Uses a
			// terminal reason deliberately — kstatus returns at its generation check
			// before reading any condition, so only a case that should reach Failed
			// can prove recordWarningAndSetBlocked advances observedGeneration.
			name: "BlockedAfterBadSpecEdit",
			build: func() *temporaliov1alpha1.WorkerDeployment {
				r, _ := newTestReconciler(nil)
				wd := makeWD("wd", "default", "conn")
				wd.Generation = 2
				wd.Status.ObservedGeneration = 1
				r.recordWarningAndSetBlocked(ctx, wd,
					temporaliov1alpha1.ReasonInvalidSpec,
					"Invalid WorkerDeployment spec: rampPercentage must increase between each step",
					"rampPercentage must increase between each step")
				return wd
			},
			today:   kstatus.FailedStatus,
			desired: kstatus.FailedStatus,
		},
		{
			// Transient failures must NOT report Failed: the controller is still
			// retrying (the ResourceExhausted paths requeue after 30s), and aborting
			// a deploy because Temporal was briefly rate limited would be worse than
			// waiting. ReasonTemporalStateFetchFailed is absent from stalledReasons,
			// so no Stalled condition is set and kstatus stays on InProgress.
			name: "BlockedOnTransientTemporalError",
			build: func() *temporaliov1alpha1.WorkerDeployment {
				r, _ := newTestReconciler(nil)
				wd := makeWD("wd", "default", "conn")
				r.recordWarningAndSetBlocked(ctx, wd,
					temporaliov1alpha1.ReasonTemporalStateFetchFailed,
					"Got ResourceExhausted error fetching worker deployment state",
					"Got ResourceExhausted error fetching worker deployment state")
				return wd
			},
			today:   kstatus.InProgressStatus,
			desired: kstatus.InProgressStatus,
		},
		{
			// Recovery: a WorkerDeployment that was Stalled and then reconciled
			// successfully. meta.SetStatusCondition only upserts, so without the
			// RemoveStatusCondition call in syncConditions the Stalled=True set here
			// would be permanent and kstatus would report Failed forever.
			name: "RecoveredAfterBlocked",
			build: func() *temporaliov1alpha1.WorkerDeployment {
				r, _ := newTestReconciler(nil)
				wd := makeWD("wd", "default", "conn")
				r.recordWarningAndSetBlocked(ctx, wd,
					temporaliov1alpha1.ReasonConnectionNotFound,
					`Connection "conn" not found`,
					`Connection "conn" not found`)
				// The user creates the Connection; the next reconcile succeeds.
				wd.Status.ObservedGeneration = wd.Generation
				wd.Status.TargetVersion.Status = temporaliov1alpha1.VersionStatusCurrent
				r.syncConditions(wd)
				return wd
			},
			today:   kstatus.CurrentStatus,
			desired: kstatus.CurrentStatus,
		},
		{
			// kstatus checks metadata.deletionTimestamp first and ignores
			// everything else, so a Ready=True object still reports Terminating.
			// This one is already correct with no work from us; pinned so nobody
			// "fixes" it later with a condition that fights it.
			name: "BeingDeleted",
			build: func() *temporaliov1alpha1.WorkerDeployment {
				r, _ := newTestReconciler(nil)
				wd := makeWD("wd", "default", "conn")
				wd.DeletionTimestamp = &metav1.Time{Time: time.Now()}
				wd.Finalizers = []string{finalizerName}
				wd.Status.ObservedGeneration = wd.Generation
				wd.Status.TargetVersion.Status = temporaliov1alpha1.VersionStatusCurrent
				r.syncConditions(wd)
				return wd
			},
			today:   kstatus.TerminatingStatus,
			desired: kstatus.TerminatingStatus,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := computeKstatus(t, tc.build())

			assert.Equal(t, tc.today, res.Status,
				"kstatus verdict for %s (message: %q)", tc.name, res.Message)

			if tc.today != tc.desired {
				t.Logf("KNOWN GAP (issue #478): kstatus reports %s; should report %s. message=%q",
					tc.today, tc.desired, res.Message)
			}
		})
	}
}

// TestConditionsAreKstatusCompatible asserts the condition-level invariants the
// verdict table above cannot see. Chiefly: Reconciling and Stalled are never both
// True. kstatus scans status.conditions in array order and returns on the first
// match, so an object carrying both would get a verdict decided by insertion order.
//
// It also pins which route each state takes through kstatus. That cannot be checked
// from the verdict: reaching InProgress via Reconciling=True and via the Ready=False
// fallback produce a byte-identical kstatus.Result, so the assertion has to be made
// against the object's own conditions.
func TestConditionsAreKstatusCompatible(t *testing.T) {
	ctx := context.Background()

	isTrue := func(wd *temporaliov1alpha1.WorkerDeployment, condType string) bool {
		return apimeta.IsStatusConditionTrue(wd.Status.Conditions, condType)
	}

	for _, st := range []temporaliov1alpha1.VersionStatus{
		temporaliov1alpha1.VersionStatusNotRegistered,
		temporaliov1alpha1.VersionStatusInactive,
		temporaliov1alpha1.VersionStatusRamping,
		temporaliov1alpha1.VersionStatusCurrent,
	} {
		t.Run("Rollout"+string(st), func(t *testing.T) {
			r, _ := newTestReconciler(nil)
			wd := makeWD("wd", "default", "conn")
			wd.Status.ObservedGeneration = wd.Generation
			wd.Status.TargetVersion.Status = st
			r.syncConditions(wd)

			assert.False(t, isTrue(wd, temporaliov1alpha1.ConditionStalled),
				"a successful reconcile must never leave Stalled=True")

			inFlight := st != temporaliov1alpha1.VersionStatusCurrent
			assert.Equal(t, inFlight, isTrue(wd, temporaliov1alpha1.ConditionReconciling),
				"Reconciling should be True exactly while the rollout is in flight")
			assert.Equal(t,
				isTrue(wd, temporaliov1alpha1.ConditionProgressing),
				isTrue(wd, temporaliov1alpha1.ConditionReconciling),
				"on the success path Reconciling must mirror Progressing")
		})
	}

	// On the blocked path Progressing=False means "blocked" in the pre-kstatus
	// vocabulary, so Reconciling deliberately does NOT mirror it: a transient
	// failure is still being retried and must read as InProgress, not Failed.
	for _, tc := range []struct {
		name        string
		reason      string
		wantStalled bool
	}{
		{"TerminalInvalidSpec", temporaliov1alpha1.ReasonInvalidSpec, true},
		{"TerminalClusterConnectionUnsupported", temporaliov1alpha1.ReasonClusterConnectionUnsupported, true},
		// Waiting on another object to exist: never terminal, because the deploy that
		// creates it may simply not have got there yet.
		{"WaitingOnConnection", temporaliov1alpha1.ReasonConnectionNotFound, false},
		{"WaitingOnCredentials", temporaliov1alpha1.ReasonAuthSecretInvalid, false},
		// Transient infrastructure failure: retried with backoff.
		{"TransientTemporalError", temporaliov1alpha1.ReasonTemporalStateFetchFailed, false},
	} {
		t.Run("Blocked"+tc.name, func(t *testing.T) {
			r, _ := newTestReconciler(nil)
			wd := makeWD("wd", "default", "conn")
			r.recordWarningAndSetBlocked(ctx, wd, tc.reason, "boom", "boom")

			assert.Equal(t, tc.wantStalled, isTrue(wd, temporaliov1alpha1.ConditionStalled),
				"Stalled for reason %s", tc.reason)
			assert.Equal(t, !tc.wantStalled, isTrue(wd, temporaliov1alpha1.ConditionReconciling),
				"Reconciling for reason %s", tc.reason)
			assert.False(t,
				isTrue(wd, temporaliov1alpha1.ConditionStalled) && isTrue(wd, temporaliov1alpha1.ConditionReconciling),
				"Stalled and Reconciling must never both be True")
			assert.Equal(t, wd.Generation, wd.Status.ObservedGeneration,
				"a blocked reconcile must still record the generation it observed")
		})
	}

	t.Run("AbnormalConditionsClearedOnRecovery", func(t *testing.T) {
		r, _ := newTestReconciler(nil)
		wd := makeWD("wd", "default", "conn")
		r.recordWarningAndSetBlocked(ctx, wd, temporaliov1alpha1.ReasonInvalidSpec, "boom", "boom")
		require.True(t, isTrue(wd, temporaliov1alpha1.ConditionStalled), "precondition: Stalled was set")

		// The user fixes the spec; the next reconcile succeeds and completes.
		wd.Status.ObservedGeneration = wd.Generation
		wd.Status.TargetVersion.Status = temporaliov1alpha1.VersionStatusCurrent
		r.syncConditions(wd)

		assert.Nil(t, apimeta.FindStatusCondition(wd.Status.Conditions, temporaliov1alpha1.ConditionStalled),
			"Stalled should be removed, not set to False")
		assert.Nil(t, apimeta.FindStatusCondition(wd.Status.Conditions, temporaliov1alpha1.ConditionReconciling),
			"Reconciling should be removed once the rollout is complete")
	})
}

// TestKstatusBaseline_WorkerResourceTemplate covers the second CRD with
// conditions. WorkerResourceTemplateStatus has no top-level observedGeneration
// field (api/v1alpha1/workerresourcetemplate_types.go:100), so kstatus's
// generation check is always skipped for these objects. The per-condition
// ObservedGeneration set at execplan.go:649 does not help: kstatus reads only
// the top-level status.observedGeneration.
func TestKstatusBaseline_WorkerResourceTemplate(t *testing.T) {
	newWRT := func() *temporaliov1alpha1.WorkerResourceTemplate {
		return &temporaliov1alpha1.WorkerResourceTemplate{
			TypeMeta: metav1.TypeMeta{
				APIVersion: temporaliov1alpha1.GroupVersion.String(),
				Kind:       "WorkerResourceTemplate",
			},
			ObjectMeta: metav1.ObjectMeta{Name: "wrt", Namespace: "default", Generation: 1},
		}
	}
	// setCond mirrors what the apply loop in execplan.go writes.
	setCond := func(wrt *temporaliov1alpha1.WorkerResourceTemplate, condType string, st metav1.ConditionStatus, reason string) {
		apimeta.SetStatusCondition(&wrt.Status.Conditions, metav1.Condition{
			Type:               condType,
			Status:             st,
			Reason:             reason,
			ObservedGeneration: wrt.Generation,
		})
	}

	t.Run("AllVersionsApplied", func(t *testing.T) {
		wrt := newWRT()
		wrt.Status.ObservedGeneration = wrt.Generation
		setCond(wrt, temporaliov1alpha1.ConditionReady, metav1.ConditionTrue, temporaliov1alpha1.ReasonWRTAllVersionsApplied)

		res := computeKstatus(t, wrt)
		assert.Equal(t, kstatus.CurrentStatus, res.Status, "message: %q", res.Message)
	})

	t.Run("ApplyFailedTerminal", func(t *testing.T) {
		// A render failure or an API rejection (Invalid, Forbidden): only a spec or
		// RBAC change can fix it, so it must not read as work in progress.
		wrt := newWRT()
		wrt.Status.ObservedGeneration = wrt.Generation
		setCond(wrt, temporaliov1alpha1.ConditionReady, metav1.ConditionFalse, temporaliov1alpha1.ReasonWRTApplyFailed)
		setCond(wrt, temporaliov1alpha1.ConditionStalled, metav1.ConditionTrue, temporaliov1alpha1.ReasonWRTApplyFailed)

		res := computeKstatus(t, wrt)
		assert.Equal(t, kstatus.FailedStatus, res.Status, "message: %q", res.Message)
	})

	t.Run("ApplyFailedTransient", func(t *testing.T) {
		// Conflict, timeout, TooManyRequests: retried next reconcile, so InProgress.
		wrt := newWRT()
		wrt.Status.ObservedGeneration = wrt.Generation
		setCond(wrt, temporaliov1alpha1.ConditionReady, metav1.ConditionFalse, temporaliov1alpha1.ReasonWRTApplyFailed)
		setCond(wrt, temporaliov1alpha1.ConditionReconciling, metav1.ConditionTrue, temporaliov1alpha1.ReasonWRTApplyFailed)

		res := computeKstatus(t, wrt)
		assert.Equal(t, kstatus.InProgressStatus, res.Status, "message: %q", res.Message)
	})

	t.Run("SpecEditNotYetObserved", func(t *testing.T) {
		// Now reachable for WRTs at all, because the type finally has a top-level
		// observedGeneration for kstatus to compare against.
		wrt := newWRT()
		wrt.Generation = 2
		wrt.Status.ObservedGeneration = 1
		setCond(wrt, temporaliov1alpha1.ConditionReady, metav1.ConditionTrue, temporaliov1alpha1.ReasonWRTAllVersionsApplied)

		res := computeKstatus(t, wrt)
		assert.Equal(t, kstatus.InProgressStatus, res.Status)
		assert.Contains(t, res.Message, "latest observed generation is 1")
	})

	// Unlike the cases above, this one drives the real writer.
	t.Run("WorkerDeploymentNotFound", func(t *testing.T) {
		wrt := newWRT()
		wrt.Spec.WorkerDeploymentRef = &temporaliov1alpha1.WorkerDeploymentReference{Name: "missing-wd"}
		r, _ := newTestReconciler([]client.Object{wrt})

		require.NoError(t, r.markWRTsWDNotFound(context.Background(),
			types.NamespacedName{Name: "missing-wd", Namespace: "default"}))

		var got temporaliov1alpha1.WorkerResourceTemplate
		require.NoError(t, r.Get(context.Background(),
			types.NamespacedName{Name: "wrt", Namespace: "default"}, &got))
		got.TypeMeta = newWRT().TypeMeta // the fake client strips TypeMeta on Get

		assert.True(t, apimeta.IsStatusConditionTrue(got.Status.Conditions, temporaliov1alpha1.ConditionReconciling),
			"a WRT waiting for its WorkerDeployment is still reconciling")
		assert.Nil(t, apimeta.FindStatusCondition(got.Status.Conditions, temporaliov1alpha1.ConditionStalled),
			"creation ordering is expected and self-resolving, so it must not be Stalled")
		assert.Equal(t, got.Generation, got.Status.ObservedGeneration)

		res := computeKstatus(t, &got)
		assert.Equal(t, kstatus.InProgressStatus, res.Status, "message: %q", res.Message)
	})
}

// TestKstatusBaseline_RemainingKinds covers the four CRD kinds that deliberately do
// not participate in the kstatus conditions contract, so that "every Temporal custom
// resource type" is accounted for rather than merely untested.
//
// Neither group is an oversight, but neither is self-evident from the code either, so
// each verdict is pinned here: if someone later adds a Ready condition to Connection,
// or makes a migration stub report ready, a test fails and points at this comment.
func TestKstatusBaseline_RemainingKinds(t *testing.T) {
	ctx := context.Background()
	req := func(name, namespace string) ctrl.Request {
		return ctrl.Request{NamespacedName: types.NamespacedName{Name: name, Namespace: namespace}}
	}

	// Connection and ClusterConnection are configuration only: no controller, no
	// conditions, and no observedGeneration. kstatus finds nothing to assess and falls
	// through to "current", which is how it treats ConfigMap and Secret. Connection
	// problems surface on the referencing WorkerDeployment instead — see the
	// ConnectionNotFound and AuthSecretInvalid cases above.
	t.Run("Connection", func(t *testing.T) {
		conn := &temporaliov1alpha1.Connection{
			TypeMeta: metav1.TypeMeta{
				APIVersion: temporaliov1alpha1.GroupVersion.String(),
				Kind:       "Connection",
			},
			ObjectMeta: metav1.ObjectMeta{Name: "conn", Namespace: "default", Generation: 1},
			Spec:       temporaliov1alpha1.ConnectionSpec{HostPort: "temporal.example.com:7233"},
		}
		// ConnectionStatus has no Conditions field at all, so "carries no conditions"
		// is enforced by the compiler rather than asserted here.
		res := computeKstatus(t, conn)
		assert.Equal(t, kstatus.CurrentStatus, res.Status, "message: %q", res.Message)
	})

	t.Run("ClusterConnection", func(t *testing.T) {
		cc := &temporaliov1alpha1.ClusterConnection{
			TypeMeta: metav1.TypeMeta{
				APIVersion: temporaliov1alpha1.GroupVersion.String(),
				Kind:       "ClusterConnection",
			},
			ObjectMeta: metav1.ObjectMeta{Name: "cluster-conn", Generation: 1},
			Spec:       temporaliov1alpha1.ConnectionSpec{HostPort: "temporal.example.com:7233"},
		}
		res := computeKstatus(t, cc)
		assert.Equal(t, kstatus.CurrentStatus, res.Status, "message: %q", res.Message)
	})

	// The deprecated kinds are migration stubs: they are never reconciled against
	// Temporal and never report Ready=True, which docs/migration-crd-rename.md states
	// as intended behaviour. kstatus therefore reads them as InProgress for as long as
	// they exist. That is acceptable because the documented migration replaces and
	// deletes them in one step, and a resource with a deletionTimestamp reports
	// Terminating instead (kstatus checks that first) — so a release following the
	// guide is never left waiting. They are deliberately left alone rather than given
	// Stalled/Reconciling: adding behaviour to a type scheduled for removal creates a
	// contract someone can depend on.
	t.Run("TemporalWorkerDeployment", func(t *testing.T) {
		twd := makeTWDStub("my-worker", "default", nil)
		r := newDeprecatedTWDReconciler(twd)

		// First reconcile adds the finalizer; the second writes the condition.
		_, err := r.Reconcile(ctx, req("my-worker", "default"))
		require.NoError(t, err)
		_, err = r.Reconcile(ctx, req("my-worker", "default"))
		require.NoError(t, err)

		var got temporaliov1alpha1.TemporalWorkerDeployment
		require.NoError(t, r.Get(ctx, req("my-worker", "default").NamespacedName, &got))
		got.TypeMeta = metav1.TypeMeta{
			APIVersion: temporaliov1alpha1.GroupVersion.String(),
			Kind:       "TemporalWorkerDeployment",
		}

		require.False(t, apimeta.IsStatusConditionTrue(got.Status.Conditions, temporaliov1alpha1.ConditionReady),
			"a migration stub must never report Ready=True")
		res := computeKstatus(t, &got)
		assert.Equal(t, kstatus.InProgressStatus, res.Status, "message: %q", res.Message)
	})

	t.Run("TemporalConnection", func(t *testing.T) {
		tc := makeTCStub("my-conn", "default")
		r := newDeprecatedTCReconciler(tc)

		_, err := r.Reconcile(ctx, req("my-conn", "default"))
		require.NoError(t, err)
		_, err = r.Reconcile(ctx, req("my-conn", "default"))
		require.NoError(t, err)

		var got temporaliov1alpha1.TemporalConnection
		require.NoError(t, r.Get(ctx, req("my-conn", "default").NamespacedName, &got))
		got.TypeMeta = metav1.TypeMeta{
			APIVersion: temporaliov1alpha1.GroupVersion.String(),
			Kind:       "TemporalConnection",
		}

		require.False(t, apimeta.IsStatusConditionTrue(got.Status.Conditions, temporaliov1alpha1.ConditionReady),
			"a migration stub must never report Ready=True")
		res := computeKstatus(t, &got)
		assert.Equal(t, kstatus.InProgressStatus, res.Status, "message: %q", res.Message)
	})

	// A stub partway through the documented migration carries a deletionTimestamp,
	// which kstatus checks before anything else. This is the case that keeps the
	// InProgress verdicts above from stalling a real release.
	t.Run("TemporalWorkerDeploymentBeingDeleted", func(t *testing.T) {
		twd := makeTWDStub("my-worker", "default", nil)
		twd.DeletionTimestamp = &metav1.Time{Time: time.Now()}
		twd.Finalizers = []string{deprecatedMigrationFinalizer}
		twd.TypeMeta = metav1.TypeMeta{
			APIVersion: temporaliov1alpha1.GroupVersion.String(),
			Kind:       "TemporalWorkerDeployment",
		}
		apimeta.SetStatusCondition(&twd.Status.Conditions, metav1.Condition{
			Type:               temporaliov1alpha1.ConditionReady,
			Status:             metav1.ConditionFalse,
			Reason:             "DeletingPendingMigration",
			ObservedGeneration: twd.Generation,
		})

		res := computeKstatus(t, twd)
		assert.Equal(t, kstatus.TerminatingStatus, res.Status, "message: %q", res.Message)
	})
}

// TestIsTerminalWorkerResourceError pins the split that decides whether a failed WRT
// apply reports Failed or InProgress to kstatus. Getting it wrong in the permissive
// direction hangs a deploy until timeout; in the strict direction it aborts a deploy
// over a retryable blip.
func TestIsTerminalWorkerResourceError(t *testing.T) {
	gr := schema.GroupResource{Group: "apps", Resource: "deployments"}

	cases := []struct {
		name         string
		err          error
		renderFailed bool
		want         bool
	}{
		{"NoError", nil, false, false},
		{"RenderFailure", errors.New("template render failed: unknown field"), true, true},
		{"Invalid", apierrors.NewInvalid(schema.GroupKind{Group: "apps", Kind: "Deployment"}, "d", nil), false, true},
		{"Forbidden", apierrors.NewForbidden(gr, "d", errors.New("no RBAC")), false, true},
		{"Unauthorized", apierrors.NewUnauthorized("bad credentials"), false, true},
		{"BadRequest", apierrors.NewBadRequest("malformed patch"), false, true},
		{"MethodNotSupported", apierrors.NewMethodNotSupported(gr, "patch"), false, true},
		{"Conflict", apierrors.NewConflict(gr, "d", errors.New("modified")), false, false},
		{"TooManyRequests", apierrors.NewTooManyRequests("slow down", 1), false, false},
		{"ServerTimeout", apierrors.NewServerTimeout(gr, "patch", 1), false, false},
		{"InternalError", apierrors.NewInternalError(errors.New("boom")), false, false},
		{"PlainTransportError", errors.New("connection reset by peer"), false, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, isTerminalWorkerResourceError(tc.err, tc.renderFailed))
		})
	}
}
