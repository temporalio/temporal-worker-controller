package controller

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// makeTWDStub builds a minimal TemporalWorkerDeployment stub for testing.
func makeTWDStub(name, namespace string, labels map[string]string) *temporaliov1alpha1.TemporalWorkerDeployment {
	return &temporaliov1alpha1.TemporalWorkerDeployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    labels,
		},
	}
}

// newDeprecatedTWDReconciler returns a DeprecatedTWDReconciler backed by a fake client.
func newDeprecatedTWDReconciler(objs ...client.Object) *DeprecatedTWDReconciler {
	fakeClient := fake.NewClientBuilder().
		WithScheme(newTestScheme()).
		WithObjects(objs...).
		WithStatusSubresource(&temporaliov1alpha1.TemporalWorkerDeployment{}).
		Build()
	return &DeprecatedTWDReconciler{Client: fakeClient}
}

// conditionReason returns the Reason of the first condition with the given Type, or "".
func conditionReason(conditions []metav1.Condition, condType string) string {
	for _, c := range conditions {
		if c.Type == condType {
			return c.Reason
		}
	}
	return ""
}

// ─── DeprecatedTWDReconciler: three condition states ─────────────────────────

func TestDeprecatedTWDReconciler_ConditionDeprecated(t *testing.T) {
	// No WorkerDeployment with same name → Reason="Deprecated".
	// First reconcile adds the finalizer; second reconcile sets the condition.
	twd := makeTWDStub("my-worker", "default", nil)
	r := newDeprecatedTWDReconciler(twd)

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "my-worker", Namespace: "default"}}
	_, err := r.Reconcile(context.Background(), req)
	require.NoError(t, err)
	_, err = r.Reconcile(context.Background(), req)
	require.NoError(t, err)

	var got temporaliov1alpha1.TemporalWorkerDeployment
	require.NoError(t, r.Get(context.Background(), req.NamespacedName, &got))

	assert.Equal(t, "Deprecated", conditionReason(got.Status.Conditions, "Ready"))
	assert.Equal(t, metav1.ConditionFalse, got.Status.Conditions[0].Status)
}

func TestDeprecatedTWDReconciler_ConditionWorkerDeploymentExists(t *testing.T) {
	// WorkerDeployment with same name exists, TWD not yet migrated → Reason="WorkerDeploymentExists".
	// First reconcile adds the finalizer; second reconcile sets the condition.
	twd := makeTWDStub("my-worker", "default", nil)
	wd := makeWD("my-worker", "default", "my-conn")
	r := newDeprecatedTWDReconciler(twd, wd)

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "my-worker", Namespace: "default"}}
	_, err := r.Reconcile(context.Background(), req)
	require.NoError(t, err)
	_, err = r.Reconcile(context.Background(), req)
	require.NoError(t, err)

	var got temporaliov1alpha1.TemporalWorkerDeployment
	require.NoError(t, r.Get(context.Background(), req.NamespacedName, &got))

	assert.Equal(t, "WorkerDeploymentExists", conditionReason(got.Status.Conditions, "Ready"))
}

func TestDeprecatedTWDReconciler_ConditionMigratedToWorkerDeployment(t *testing.T) {
	// TWD carries migrated label → Reason="MigratedToWorkerDeployment".
	// First reconcile adds the finalizer; second reconcile sets the condition.
	twd := makeTWDStub("my-worker", "default", map[string]string{
		deprecatedTWDMigratedLabel: "true",
	})
	r := newDeprecatedTWDReconciler(twd)

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "my-worker", Namespace: "default"}}
	_, err := r.Reconcile(context.Background(), req)
	require.NoError(t, err)
	_, err = r.Reconcile(context.Background(), req)
	require.NoError(t, err)

	var got temporaliov1alpha1.TemporalWorkerDeployment
	require.NoError(t, r.Get(context.Background(), req.NamespacedName, &got))

	assert.Equal(t, "MigratedToWorkerDeployment", conditionReason(got.Status.Conditions, "Ready"))
}

// ─── DeprecatedTWDReconciler: migration-guard finalizer ──────────────────────

func TestDeprecatedTWDReconciler_AddsFinalizer(t *testing.T) {
	// No finalizer present, not being deleted → finalizer must be added.
	twd := makeTWDStub("my-worker", "default", nil)
	r := newDeprecatedTWDReconciler(twd)

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "my-worker", Namespace: "default"},
	})
	require.NoError(t, err)

	var got temporaliov1alpha1.TemporalWorkerDeployment
	require.NoError(t, r.Get(context.Background(), types.NamespacedName{Name: "my-worker", Namespace: "default"}, &got))
	assert.Contains(t, got.Finalizers, deprecatedMigrationFinalizer)
}

func TestDeprecatedTWDReconciler_BlocksDeletionWithoutMigratedLabel(t *testing.T) {
	// DeletionTimestamp set, finalizer present, migrated label absent → finalizer
	// must stay and condition must indicate pending migration.
	// Even if a matching WorkerDeployment exists, label absence is authoritative.
	now := metav1.Now()
	twd := makeTWDStub("my-worker", "default", nil)
	twd.DeletionTimestamp = &now
	twd.Finalizers = []string{deprecatedMigrationFinalizer}

	wd := makeWD("my-worker", "default", "my-conn")
	r := newDeprecatedTWDReconciler(twd, wd)

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "my-worker", Namespace: "default"},
	})
	require.NoError(t, err)

	var got temporaliov1alpha1.TemporalWorkerDeployment
	require.NoError(t, r.Get(context.Background(), types.NamespacedName{Name: "my-worker", Namespace: "default"}, &got))
	assert.Contains(t, got.Finalizers, deprecatedMigrationFinalizer, "finalizer must not be removed before migration is confirmed")
	assert.Equal(t, "DeletingPendingMigration", conditionReason(got.Status.Conditions, "Ready"))
}

func TestDeprecatedTWDReconciler_AllowsDeletionWhenMigratedLabel(t *testing.T) {
	// DeletionTimestamp set, finalizer present, migrated label present → finalizer
	// must be removed so Kubernetes can complete the deletion.
	// The fake client deletes the object once all finalizers are gone.
	now := metav1.Now()
	twd := makeTWDStub("my-worker", "default", map[string]string{
		deprecatedTWDMigratedLabel: "true",
	})
	twd.DeletionTimestamp = &now
	twd.Finalizers = []string{deprecatedMigrationFinalizer}
	r := newDeprecatedTWDReconciler(twd)

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "my-worker", Namespace: "default"},
	})
	require.NoError(t, err)

	var got temporaliov1alpha1.TemporalWorkerDeployment
	err = r.Get(context.Background(), types.NamespacedName{Name: "my-worker", Namespace: "default"}, &got)
	assert.True(t, apierrors.IsNotFound(err), "object must be gone once finalizer is removed")
}

func TestDeprecatedTWDReconciler_NotFound(t *testing.T) {
	// TWD doesn't exist → no error (not-found is ignored)
	r := newDeprecatedTWDReconciler()
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "gone", Namespace: "default"},
	})
	require.NoError(t, err)
}

// ─── migrateFromDeprecatedTWD ─────────────────────────────────────────────────

func TestMigrateFromDeprecatedTWD_TransfersOwnerRefs(t *testing.T) {
	const (
		name      = "my-worker"
		namespace = "default"
		twdUID    = types.UID("deprecated-twd-uid")
	)

	twd := makeTWDStub(name, namespace, nil)
	twd.UID = twdUID
	wd := makeWD(name, namespace, "my-conn")

	// Deployment owned by the deprecated TWD.
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-worker-abc123",
			Namespace: namespace,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion:         temporaliov1alpha1.GroupVersion.String(),
				Kind:               "TemporalWorkerDeployment",
				Name:               name,
				UID:                twdUID,
				Controller:         ptr(true),
				BlockOwnerDeletion: ptr(true),
			}},
		},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "test"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "test"}},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "worker", Image: "test:v1"}},
				},
			},
		},
	}

	// WorkerResourceTemplate owned by the deprecated TWD.
	raw, _ := json.Marshal(map[string]interface{}{
		"apiVersion": "autoscaling/v2",
		"kind":       "HorizontalPodAutoscaler",
	})
	wrt := &temporaliov1alpha1.WorkerResourceTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-hpa",
			Namespace: namespace,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion:         temporaliov1alpha1.GroupVersion.String(),
				Kind:               "TemporalWorkerDeployment",
				Name:               name,
				UID:                twdUID,
				Controller:         ptr(true),
				BlockOwnerDeletion: ptr(true),
			}},
		},
		Spec: temporaliov1alpha1.WorkerResourceTemplateSpec{
			WorkerDeploymentRef: &temporaliov1alpha1.WorkerDeploymentReference{Name: name},
			Template:            runtime.RawExtension{Raw: raw},
		},
	}

	scheme := newTestScheme()
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(twd, wd, dep, wrt).
		Build()

	r := &WorkerDeploymentReconciler{Client: fakeClient, Scheme: scheme}

	err := r.migrateFromDeprecatedTWD(context.Background(), logr.Discard(), wd)
	require.NoError(t, err)

	// TWD must be labeled as migrated.
	var gotTWD temporaliov1alpha1.TemporalWorkerDeployment
	require.NoError(t, fakeClient.Get(context.Background(), types.NamespacedName{Name: name, Namespace: namespace}, &gotTWD))
	assert.Equal(t, "true", gotTWD.Labels[deprecatedTWDMigratedLabel])

	// Deployment ownerRef must now point to the WD.
	var gotDep appsv1.Deployment
	require.NoError(t, fakeClient.Get(context.Background(), types.NamespacedName{Name: dep.Name, Namespace: namespace}, &gotDep))
	require.Len(t, gotDep.OwnerReferences, 1)
	assert.Equal(t, "WorkerDeployment", gotDep.OwnerReferences[0].Kind)
	assert.Equal(t, wd.UID, gotDep.OwnerReferences[0].UID)

	// WRT ownerRef must now point to the WD.
	var gotWRT temporaliov1alpha1.WorkerResourceTemplate
	require.NoError(t, fakeClient.Get(context.Background(), types.NamespacedName{Name: wrt.Name, Namespace: namespace}, &gotWRT))
	require.Len(t, gotWRT.OwnerReferences, 1)
	assert.Equal(t, "WorkerDeployment", gotWRT.OwnerReferences[0].Kind)
	assert.Equal(t, wd.UID, gotWRT.OwnerReferences[0].UID)
}

func TestMigrateFromDeprecatedTWD_SkipsIfAlreadyMigrated(t *testing.T) {
	// TWD already carries migrated label → no patching, no error.
	twd := makeTWDStub("my-worker", "default", map[string]string{
		deprecatedTWDMigratedLabel: "true",
	})
	twd.UID = "old-uid"
	wd := makeWD("my-worker", "default", "my-conn")

	scheme := newTestScheme()
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(twd, wd).
		Build()

	r := &WorkerDeploymentReconciler{Client: fakeClient, Scheme: scheme}
	err := r.migrateFromDeprecatedTWD(context.Background(), logr.Discard(), wd)
	require.NoError(t, err)

	// Label must still be set.
	var gotTWD temporaliov1alpha1.TemporalWorkerDeployment
	require.NoError(t, fakeClient.Get(context.Background(), types.NamespacedName{Name: "my-worker", Namespace: "default"}, &gotTWD))
	assert.Equal(t, "true", gotTWD.Labels[deprecatedTWDMigratedLabel])
}

func TestMigrateFromDeprecatedTWD_NoTWD(t *testing.T) {
	// No deprecated TWD exists → no-op.
	wd := makeWD("my-worker", "default", "my-conn")

	scheme := newTestScheme()
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(wd).
		Build()

	r := &WorkerDeploymentReconciler{Client: fakeClient, Scheme: scheme}
	err := r.migrateFromDeprecatedTWD(context.Background(), logr.Discard(), wd)
	require.NoError(t, err)
}

func TestMigrateFromDeprecatedTWD_UnrelatedDeploymentUntouched(t *testing.T) {
	// A Deployment owned by a different object must not be patched.
	const namespace = "default"
	twdUID := types.UID("deprecated-twd-uid")
	otherUID := types.UID("some-other-uid")

	twd := makeTWDStub("my-worker", namespace, nil)
	twd.UID = twdUID
	wd := makeWD("my-worker", namespace, "my-conn")

	unrelated := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "unrelated-deploy",
			Namespace: namespace,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "apps/v1",
				Kind:       "ReplicaSet",
				Name:       "rs-1",
				UID:        otherUID,
				Controller: ptr(true),
			}},
		},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "other"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "other"}},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "app", Image: "app:v1"}},
				},
			},
		},
	}

	scheme := newTestScheme()
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(twd, wd, unrelated).
		Build()

	r := &WorkerDeploymentReconciler{Client: fakeClient, Scheme: scheme}
	err := r.migrateFromDeprecatedTWD(context.Background(), logr.Discard(), wd)
	require.NoError(t, err)

	// Unrelated Deployment ownerRef must be unchanged.
	var gotDep appsv1.Deployment
	require.NoError(t, fakeClient.Get(context.Background(), types.NamespacedName{Name: "unrelated-deploy", Namespace: namespace}, &gotDep))
	require.Len(t, gotDep.OwnerReferences, 1)
	assert.Equal(t, otherUID, gotDep.OwnerReferences[0].UID, "unrelated deployment ownerRef must not be modified")
}
