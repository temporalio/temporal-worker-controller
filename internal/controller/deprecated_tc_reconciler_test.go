package controller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func makeTCStub(name, namespace string) *temporaliov1alpha1.TemporalConnection {
	return &temporaliov1alpha1.TemporalConnection{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
	}
}

func makeConnectionStub(name, namespace string) *temporaliov1alpha1.Connection {
	return &temporaliov1alpha1.Connection{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
	}
}

func newDeprecatedTCReconciler(objs ...client.Object) *DeprecatedTCReconciler {
	fakeClient := fake.NewClientBuilder().
		WithScheme(newTestScheme()).
		WithObjects(objs...).
		WithStatusSubresource(&temporaliov1alpha1.TemporalConnection{}).
		Build()
	return &DeprecatedTCReconciler{Client: fakeClient}
}

// ─── DeprecatedTCReconciler: condition states ─────────────────────────────────

func TestDeprecatedTCReconciler_ConditionDeprecated(t *testing.T) {
	tc := makeTCStub("my-conn", "default")
	r := newDeprecatedTCReconciler(tc)

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "my-conn", Namespace: "default"},
	})
	require.NoError(t, err)

	var got temporaliov1alpha1.TemporalConnection
	require.NoError(t, r.Get(context.Background(), types.NamespacedName{Name: "my-conn", Namespace: "default"}, &got))
	// First reconcile adds the finalizer, so condition isn't set yet.
	assert.Contains(t, got.Finalizers, deprecatedMigrationFinalizer)

	// Second reconcile sets the condition.
	_, err = r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "my-conn", Namespace: "default"},
	})
	require.NoError(t, err)
	require.NoError(t, r.Get(context.Background(), types.NamespacedName{Name: "my-conn", Namespace: "default"}, &got))
	assert.Equal(t, "Deprecated", conditionReason(got.Status.Conditions, "Ready"))
}

func TestDeprecatedTCReconciler_ConditionMigratedToConnection(t *testing.T) {
	tc := makeTCStub("my-conn", "default")
	conn := makeConnectionStub("my-conn", "default")
	r := newDeprecatedTCReconciler(tc, conn)

	// First reconcile adds the finalizer.
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "my-conn", Namespace: "default"},
	})
	require.NoError(t, err)

	// Second reconcile sets the MigratedToConnection condition.
	_, err = r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "my-conn", Namespace: "default"},
	})
	require.NoError(t, err)

	var got temporaliov1alpha1.TemporalConnection
	require.NoError(t, r.Get(context.Background(), types.NamespacedName{Name: "my-conn", Namespace: "default"}, &got))
	assert.Equal(t, "MigratedToConnection", conditionReason(got.Status.Conditions, "Ready"))
}

func TestDeprecatedTCReconciler_NotFound(t *testing.T) {
	r := newDeprecatedTCReconciler()
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "gone", Namespace: "default"},
	})
	require.NoError(t, err)
}

// ─── DeprecatedTCReconciler: migration-guard finalizer ───────────────────────

func TestDeprecatedTCReconciler_AddsFinalizer(t *testing.T) {
	tc := makeTCStub("my-conn", "default")
	r := newDeprecatedTCReconciler(tc)

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "my-conn", Namespace: "default"},
	})
	require.NoError(t, err)

	var got temporaliov1alpha1.TemporalConnection
	require.NoError(t, r.Get(context.Background(), types.NamespacedName{Name: "my-conn", Namespace: "default"}, &got))
	assert.Contains(t, got.Finalizers, deprecatedMigrationFinalizer)
}

func TestDeprecatedTCReconciler_BlocksDeletionWithoutConnection(t *testing.T) {
	// DeletionTimestamp set, finalizer present, no matching Connection → finalizer
	// must stay and condition must indicate pending migration.
	now := metav1.Now()
	tc := makeTCStub("my-conn", "default")
	tc.DeletionTimestamp = &now
	tc.Finalizers = []string{deprecatedMigrationFinalizer}
	r := newDeprecatedTCReconciler(tc)

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "my-conn", Namespace: "default"},
	})
	require.NoError(t, err)

	var got temporaliov1alpha1.TemporalConnection
	require.NoError(t, r.Get(context.Background(), types.NamespacedName{Name: "my-conn", Namespace: "default"}, &got))
	assert.Contains(t, got.Finalizers, deprecatedMigrationFinalizer, "finalizer must not be removed before Connection exists")
	assert.Equal(t, "DeletingPendingMigration", conditionReason(got.Status.Conditions, "Ready"))
}

func TestDeprecatedTCReconciler_AllowsDeletionWhenConnectionExists(t *testing.T) {
	// DeletionTimestamp set, finalizer present, matching Connection exists → finalizer
	// must be removed so Kubernetes can complete the deletion.
	// The fake client deletes the object once all finalizers are gone.
	now := metav1.Now()
	tc := makeTCStub("my-conn", "default")
	tc.DeletionTimestamp = &now
	tc.Finalizers = []string{deprecatedMigrationFinalizer}
	conn := makeConnectionStub("my-conn", "default")
	r := newDeprecatedTCReconciler(tc, conn)

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "my-conn", Namespace: "default"},
	})
	require.NoError(t, err)

	var got temporaliov1alpha1.TemporalConnection
	err = r.Get(context.Background(), types.NamespacedName{Name: "my-conn", Namespace: "default"}, &got)
	assert.True(t, apierrors.IsNotFound(err), "object must be gone once finalizer is removed")
}
