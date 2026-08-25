package controller

import (
	"context"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// makeClusterConnection creates a minimal cluster-scoped ClusterConnection.
func makeClusterConnection(name, hostPort string) *temporaliov1alpha1.ClusterConnection {
	return &temporaliov1alpha1.ClusterConnection{
		TypeMeta: metav1.TypeMeta{
			APIVersion: temporaliov1alpha1.GroupVersion.String(),
			Kind:       "ClusterConnection",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: name, // intentionally no Namespace
		},
		Spec: temporaliov1alpha1.ConnectionSpec{
			HostPort: hostPort,
		},
	}
}

// makeWDWithKind builds a WorkerDeployment whose connectionRef carries an
// explicit Kind ("", "Connection", or "ClusterConnection").
func makeWDWithKind(name, namespace, connName, kind string) *temporaliov1alpha1.WorkerDeployment {
	wd := makeWD(name, namespace, connName)
	wd.Spec.WorkerOptions.ConnectionRef.Kind = kind
	return wd
}

// hasFinalizer re-Gets obj from the client and reports whether it still carries our finalizer
func hasFinalizer(t *testing.T, c client.Client, obj client.Object, key types.NamespacedName) bool {
	t.Helper()
	require.NoError(t, c.Get(context.Background(), key, obj))
	return controllerutil.ContainsFinalizer(obj, finalizerName)
}

func TestResolveConnection(t *testing.T) {
	ctx := context.Background()

	t.Run("KindConnection_fetchesNamespaced", func(t *testing.T) {
		conn := makeNoCredsConnection("conn", "default", "h:7233")
		wd := makeWDWithKind("wd", "default", "conn", "Connection")
		r, _ := newTestReconciler([]client.Object{conn, wd})

		spec, obj, err := r.resolveConnection(ctx, wd)
		require.NoError(t, err)
		assert.Equal(t, "h:7233", spec.HostPort)
		_, ok := obj.(*temporaliov1alpha1.Connection)
		assert.True(t, ok, "expected a *Connection object")
	})

	t.Run("KindEmpty_identicalToConnection", func(t *testing.T) {
		conn := makeNoCredsConnection("conn", "default", "h:7233")
		wd := makeWDWithKind("wd", "default", "conn", "")
		r, _ := newTestReconciler([]client.Object{conn, wd})

		spec, obj, err := r.resolveConnection(ctx, wd)
		require.NoError(t, err)
		assert.Equal(t, "h:7233", spec.HostPort)
		_, ok := obj.(*temporaliov1alpha1.Connection)
		assert.True(t, ok, "empty kind must resolve to a namespaced Connection")
	})

	t.Run("KindClusterConnection_fetchesClusterScoped", func(t *testing.T) {
		cc := makeClusterConnection("conn", "h:7233")
		wd := makeWDWithKind("wd", "default", "conn", "ClusterConnection")
		r, _ := newTestReconciler([]client.Object{cc, wd})

		spec, obj, err := r.resolveConnection(ctx, wd)
		require.NoError(t, err)
		assert.Equal(t, "h:7233", spec.HostPort)
		got, ok := obj.(*temporaliov1alpha1.ClusterConnection)
		require.True(t, ok, "expected a *ClusterConnection object")
		assert.Empty(t, got.Namespace, "cluster-scoped object must have no namespace")
	})

	t.Run("SameName_bothKinds_resolveDistinctObjects", func(t *testing.T) {
		conn := makeNoCredsConnection("foo", "default", "ns-conn:7233")
		cc := makeClusterConnection("foo", "cluster-conn:7233")
		wdNS := makeWDWithKind("wd-ns", "default", "foo", "Connection")
		wdCC := makeWDWithKind("wd-cc", "default", "foo", "ClusterConnection")
		r, _ := newTestReconciler([]client.Object{conn, cc, wdNS, wdCC})

		specNS, objNS, err := r.resolveConnection(ctx, wdNS)
		require.NoError(t, err)
		assert.Equal(t, "ns-conn:7233", specNS.HostPort)
		_, ok := objNS.(*temporaliov1alpha1.Connection)
		assert.True(t, ok)

		specCC, objCC, err := r.resolveConnection(ctx, wdCC)
		require.NoError(t, err)
		assert.Equal(t, "cluster-conn:7233", specCC.HostPort)
		_, ok = objCC.(*temporaliov1alpha1.ClusterConnection)
		assert.True(t, ok)
	})

	t.Run("ClusterExists_butNamespacedRequested_notFound", func(t *testing.T) {
		cc := makeClusterConnection("foo", "cluster-conn:7233")
		wd := makeWDWithKind("wd", "default", "foo", "Connection")
		r, _ := newTestReconciler([]client.Object{cc, wd})

		_, obj, err := r.resolveConnection(ctx, wd)
		require.Error(t, err)
		assert.True(t, apierrors.IsNotFound(err), "must be NotFound, not a stray cluster resolve")
		assert.Nil(t, obj)
	})

	t.Run("NotFound_namespaced", func(t *testing.T) {
		wd := makeWDWithKind("wd", "default", "missing", "Connection")
		r, _ := newTestReconciler([]client.Object{wd})

		_, obj, err := r.resolveConnection(ctx, wd)
		require.Error(t, err)
		assert.True(t, apierrors.IsNotFound(err))
		assert.Nil(t, obj)
	})

	t.Run("NotFound_cluster", func(t *testing.T) {
		wd := makeWDWithKind("wd", "default", "missing", "ClusterConnection")
		r, _ := newTestReconciler([]client.Object{wd})

		_, obj, err := r.resolveConnection(ctx, wd)
		require.Error(t, err)
		assert.True(t, apierrors.IsNotFound(err))
		assert.Nil(t, obj)
	})
}

func TestEnsureConnectionFinalizer(t *testing.T) {
	ctx := context.Background()
	newConn := func() *temporaliov1alpha1.Connection {
		return makeNoCredsConnection("conn", "default", "h:7233")
	}
	newCC := func() *temporaliov1alpha1.ClusterConnection {
		return makeClusterConnection("conn", "h:7233")
	}
	tests := []struct {
		name            string
		seed            client.Object
		key             types.NamespacedName
		refetch         client.Object
		wantUpdateCount int
	}{
		{
			name:            "Connection_addsFinalizer",
			seed:            newConn(),
			key:             types.NamespacedName{Name: "conn", Namespace: "default"},
			refetch:         &temporaliov1alpha1.Connection{},
			wantUpdateCount: 1,
		},
		{
			name:            "ClusterConnection_addsFinalizer",
			seed:            newCC(),
			key:             types.NamespacedName{Name: "conn"}, // cluster-scoped: no namespace
			refetch:         &temporaliov1alpha1.ClusterConnection{},
			wantUpdateCount: 1,
		},
		{
			name: "Connection_idempotent_noUpdate",
			seed: func() client.Object {
				c := newConn()
				c.Finalizers = []string{finalizerName}
				return c
			}(),
			key:             types.NamespacedName{Name: "conn", Namespace: "default"},
			refetch:         &temporaliov1alpha1.Connection{},
			wantUpdateCount: 0,
		},
		{
			name: "ClusterConnection_idempotent_noUpdate",
			seed: func() client.Object {
				c := newCC()
				c.Finalizers = []string{finalizerName}
				return c
			}(),
			key:             types.NamespacedName{Name: "conn"},
			refetch:         &temporaliov1alpha1.ClusterConnection{},
			wantUpdateCount: 0,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var updateCount int
			funcs := interceptor.Funcs{
				Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
					updateCount++
					return c.Update(ctx, obj, opts...)
				},
			}
			r, _ := newTestReconcilerWithInterceptors([]client.Object{tc.seed}, funcs)
			err := r.ensureConnectionFinalizer(ctx, logr.Discard(), tc.seed)
			require.NoError(t, err)
			assert.Equal(t, tc.wantUpdateCount, updateCount, "unexpected number of Update calls")
			assert.True(t, hasFinalizer(t, r.Client, tc.refetch, tc.key), "finalizer must be present afterwards")
		})
	}
}

func TestRemoveConnectionFinalizerIfUnused(t *testing.T) {
	ctx := context.Background()

	// connection object that already has the finalizer
	connWithFinalizer := func(name, ns, hostPort string) *temporaliov1alpha1.Connection {
		c := makeNoCredsConnection(name, ns, hostPort)
		c.Finalizers = []string{finalizerName}
		return c
	}
	ccWithFinalizer := func(name, hostPort string) *temporaliov1alpha1.ClusterConnection {
		c := makeClusterConnection(name, hostPort)
		c.Finalizers = []string{finalizerName}
		return c
	}

	t.Run("namespaced_unused_removes", func(t *testing.T) {
		conn := connWithFinalizer("conn", "default", "h:7233")
		del := makeWDWithKind("del", "default", "conn", "Connection")
		r, _ := newTestReconciler([]client.Object{conn, del})

		err := r.removeConnectionFinalizerIfUnused(ctx, logr.Discard(), del)
		require.NoError(t, err)
		assert.False(t, hasFinalizer(t, r.Client, &temporaliov1alpha1.Connection{},
			types.NamespacedName{Name: "conn", Namespace: "default"}))
	})

	t.Run("namespaced_stillUsed_keeps", func(t *testing.T) {
		conn := connWithFinalizer("conn", "default", "h:7233")
		del := makeWDWithKind("del", "default", "conn", "Connection")
		other := makeWDWithKind("other", "default", "conn", "Connection")
		r, _ := newTestReconciler([]client.Object{conn, del, other})

		err := r.removeConnectionFinalizerIfUnused(ctx, logr.Discard(), del)
		require.NoError(t, err)
		assert.True(t, hasFinalizer(t, r.Client, &temporaliov1alpha1.Connection{},
			types.NamespacedName{Name: "conn", Namespace: "default"}))
	})

	t.Run("namespaced_otherNamespace_ignored_removes", func(t *testing.T) {
		conn := connWithFinalizer("conn", "default", "h:7233")
		del := makeWDWithKind("del", "default", "conn", "Connection")
		// same-named WD in a DIFFERENT namespace — must not count as a referrer
		otherNS := makeWDWithKind("other", "ns-b", "conn", "Connection")
		r, _ := newTestReconciler([]client.Object{conn, del, otherNS})

		err := r.removeConnectionFinalizerIfUnused(ctx, logr.Discard(), del)
		require.NoError(t, err)
		assert.False(t, hasFinalizer(t, r.Client, &temporaliov1alpha1.Connection{},
			types.NamespacedName{Name: "conn", Namespace: "default"}))
	})

	t.Run("cluster_unused_removes", func(t *testing.T) {
		cc := ccWithFinalizer("conn", "h:7233")
		del := makeWDWithKind("del", "ns-a", "conn", "ClusterConnection")
		r, _ := newTestReconciler([]client.Object{cc, del})

		err := r.removeConnectionFinalizerIfUnused(ctx, logr.Discard(), del)
		require.NoError(t, err)
		assert.False(t, hasFinalizer(t, r.Client, &temporaliov1alpha1.ClusterConnection{},
			types.NamespacedName{Name: "conn"}))
	})

	// A ClusterConnection referenced by a WD in another namespace must keep its
	// finalizer when one referrer is deleted. If InNamespace is ever reintroduced
	// on the cluster path, this test fails while everything else passes.
	t.Run("cluster_stillUsedFromAnotherNamespace_keeps", func(t *testing.T) {
		cc := ccWithFinalizer("conn", "h:7233")
		del := makeWDWithKind("del", "ns-a", "conn", "ClusterConnection")
		otherNS := makeWDWithKind("other", "ns-b", "conn", "ClusterConnection")
		r, _ := newTestReconciler([]client.Object{cc, del, otherNS})

		err := r.removeConnectionFinalizerIfUnused(ctx, logr.Discard(), del)
		require.NoError(t, err)
		assert.True(t, hasFinalizer(t, r.Client, &temporaliov1alpha1.ClusterConnection{},
			types.NamespacedName{Name: "conn"}),
			"ClusterConnection must NOT be released while another namespace still references it")
	})

	t.Run("kindDisambiguation_clusterReleased_namespacedUntouched", func(t *testing.T) {
		// Connection "foo" and ClusterConnection "foo" coexist.
		conn := connWithFinalizer("foo", "default", "ns-conn:7233")
		cc := ccWithFinalizer("foo", "cluster-conn:7233")
		// deleting the WD that used the ClusterConnection
		del := makeWDWithKind("del", "default", "foo", "ClusterConnection")
		// a WD still using the NAMESPACED Connection "foo"
		nsUser := makeWDWithKind("ns-user", "default", "foo", "Connection")
		r, _ := newTestReconciler([]client.Object{conn, cc, del, nsUser})

		err := r.removeConnectionFinalizerIfUnused(ctx, logr.Discard(), del)
		require.NoError(t, err)
		// ClusterConnection "foo" released (no other cluster referrer)
		assert.False(t, hasFinalizer(t, r.Client, &temporaliov1alpha1.ClusterConnection{},
			types.NamespacedName{Name: "foo"}))
		// namespaced Connection "foo" untouched (still used by ns-user)
		assert.True(t, hasFinalizer(t, r.Client, &temporaliov1alpha1.Connection{},
			types.NamespacedName{Name: "foo", Namespace: "default"}))
	})

	t.Run("skipSelf_sameNameDifferentNamespace_keeps", func(t *testing.T) {
		cc := ccWithFinalizer("conn", "h:7233")
		// two WDs with the SAME name in different namespaces
		del := makeWDWithKind("samename", "ns-a", "conn", "ClusterConnection")
		other := makeWDWithKind("samename", "ns-b", "conn", "ClusterConnection")
		r, _ := newTestReconciler([]client.Object{cc, del, other})

		err := r.removeConnectionFinalizerIfUnused(ctx, logr.Discard(), del)
		require.NoError(t, err)
		// the other "samename" in ns-b is a real referrer, not "self" — keep finalizer
		assert.True(t, hasFinalizer(t, r.Client, &temporaliov1alpha1.ClusterConnection{},
			types.NamespacedName{Name: "conn"}),
			"same-name WD in another namespace must be counted as a referrer, not skipped as self")
	})

	t.Run("alreadyGone_noError", func(t *testing.T) {
		// connection object does not exist when removal runs
		del := makeWDWithKind("del", "default", "missing", "Connection")
		r, _ := newTestReconciler([]client.Object{del})

		err := r.removeConnectionFinalizerIfUnused(ctx, logr.Discard(), del)
		require.NoError(t, err, "missing connection must be treated as already released")
	})
}

func TestFindTWDsUsingConnection(t *testing.T) {
	ctx := context.Background()

	t.Run("namespacedConnection_enqueuesMatchingWDs", func(t *testing.T) {
		conn := makeNoCredsConnection("conn", "default", "h:7233")
		wd := makeWDWithKind("wd", "default", "conn", "Connection")
		r, _ := newTestReconciler([]client.Object{conn, wd})

		reqs := r.findTWDsUsingConnection(ctx, conn)
		assert.Contains(t, reqs, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: "wd", Namespace: "default"},
		})
	})

	t.Run("clusterConnectionRefOfSameName_notEnqueued", func(t *testing.T) {
		conn := makeNoCredsConnection("foo", "default", "h:7233")
		// this WD points at a ClusterConnection named "foo", not the namespaced one
		wd := makeWDWithKind("wd", "default", "foo", "ClusterConnection")
		r, _ := newTestReconciler([]client.Object{conn, wd})

		reqs := r.findTWDsUsingConnection(ctx, conn)
		assert.Empty(t, reqs, "cluster-ref WD must not be enqueued by the namespaced Connection mapper")
	})

	t.Run("noMatches_emptySlice", func(t *testing.T) {
		conn := makeNoCredsConnection("conn", "default", "h:7233")
		wd := makeWDWithKind("wd", "default", "other-conn", "Connection")
		r, _ := newTestReconciler([]client.Object{conn, wd})

		reqs := r.findTWDsUsingConnection(ctx, conn)
		assert.Empty(t, reqs)
	})
}

func TestFindTWDsUsingClusterConnection(t *testing.T) {
	ctx := context.Background()

	t.Run("multipleNamespaces_allEnqueued", func(t *testing.T) {
		cc := makeClusterConnection("conn", "h:7233")
		wdA := makeWDWithKind("wd-a", "ns-a", "conn", "ClusterConnection")
		wdB := makeWDWithKind("wd-b", "ns-b", "conn", "ClusterConnection")
		r, _ := newTestReconciler([]client.Object{cc, wdA, wdB})

		reqs := r.findTWDsUsingClusterConnection(ctx, cc)
		assert.Contains(t, reqs, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: "wd-a", Namespace: "ns-a"},
		})
		assert.Contains(t, reqs, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: "wd-b", Namespace: "ns-b"},
		})
		assert.Len(t, reqs, 2)
	})

	t.Run("namespacedConnectionRefOfSameName_notEnqueued", func(t *testing.T) {
		cc := makeClusterConnection("foo", "h:7233")
		// this WD points at a namespaced Connection named "foo", not the cluster one
		wd := makeWDWithKind("wd", "default", "foo", "Connection")
		r, _ := newTestReconciler([]client.Object{cc, wd})

		reqs := r.findTWDsUsingClusterConnection(ctx, cc)
		assert.Empty(t, reqs, "namespaced-ref WD must not be enqueued by the cluster mapper")
	})

	t.Run("sameNameDifferentNamespaces_bothEnqueued", func(t *testing.T) {
		cc := makeClusterConnection("conn", "h:7233")
		// two WDs with the same NAME in different namespaces
		wdA := makeWDWithKind("samename", "ns-a", "conn", "ClusterConnection")
		wdB := makeWDWithKind("samename", "ns-b", "conn", "ClusterConnection")
		r, _ := newTestReconciler([]client.Object{cc, wdA, wdB})

		reqs := r.findTWDsUsingClusterConnection(ctx, cc)
		assert.Contains(t, reqs, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: "samename", Namespace: "ns-a"},
		})
		assert.Contains(t, reqs, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: "samename", Namespace: "ns-b"},
		})
		assert.Len(t, reqs, 2, "same-name WDs in different namespaces must both be distinct requests")
	})

	t.Run("noMatches_emptySlice", func(t *testing.T) {
		cc := makeClusterConnection("conn", "h:7233")
		wd := makeWDWithKind("wd", "default", "other-conn", "ClusterConnection")
		r, _ := newTestReconciler([]client.Object{cc, wd})

		reqs := r.findTWDsUsingClusterConnection(ctx, cc)
		assert.Empty(t, reqs)
	})
}

// ReleaseConnectionFinalizerIfUnused: migration path

// After a WD switches to a different connection,
// releasing the OLD (now-unused) connection's finalizer must succeed.
func TestReleaseConnectionFinalizerIfUnused_ReleasesUnused(t *testing.T) {
	ctx := context.Background()
	// old-conn still carries the finalizer from before the switch.
	oldConn := makeNoCredsConnection("old-conn", "default", "h:7233")
	oldConn.Finalizers = []string{finalizerName}
	// The WD ("w") now points at a DIFFERENT connection, so nothing references
	// old-conn anymore.
	self := makeWDWithKind("w", "default", "new-conn", "Connection")
	r, _ := newTestReconciler([]client.Object{oldConn, self})

	oldRef := temporaliov1alpha1.ConnectionReference{Name: "old-conn", Kind: "Connection"}
	require.NoError(t, r.releaseConnectionFinalizerIfUnused(ctx, logr.Discard(), oldRef, "default", "w"))

	assert.False(t, hasFinalizer(t, r.Client, &temporaliov1alpha1.Connection{},
		types.NamespacedName{Name: "old-conn", Namespace: "default"}),
		"old connection's finalizer must be released once no WD references it")
}

// Migrating away from a shared ClusterConnection must NOT
// release its finalizer while a WD in another namespace still references it.
func TestReleaseConnectionFinalizerIfUnused_KeepsSharedStillUsed(t *testing.T) {
	ctx := context.Background()
	shared := makeClusterConnection("shared", "h:7233")
	shared.Finalizers = []string{finalizerName}
	// A WD in ns-b still references the shared ClusterConnection.
	wdB := makeWDWithKind("wb", "ns-b", "shared", "ClusterConnection")
	r, _ := newTestReconciler([]client.Object{shared, wdB})

	// Simulate the WD "w" in ns-a migrating away from "shared".
	sharedRef := temporaliov1alpha1.ConnectionReference{Name: "shared", Kind: "ClusterConnection"}
	require.NoError(t, r.releaseConnectionFinalizerIfUnused(ctx, logr.Discard(), sharedRef, "ns-a", "w"))

	assert.True(t, hasFinalizer(t, r.Client, &temporaliov1alpha1.ClusterConnection{},
		types.NamespacedName{Name: "shared"}),
		"shared ClusterConnection finalizer must be KEPT while a WD in another namespace references it")
}

// A connectionRef whose Kind was defaulted from
// "" to "Connection" must NOT be seen as a change, or every pre-existing WD would
// try to release its own connection on the first reconcile after upgrade.
func TestSameConnectionRef(t *testing.T) {
	ref := func(name, kind string) temporaliov1alpha1.ConnectionReference {
		return temporaliov1alpha1.ConnectionReference{Name: name, Kind: kind}
	}

	tests := []struct {
		name string
		a, b temporaliov1alpha1.ConnectionReference
		want bool
	}{
		{
			name: "empty kind equals Connection (normalization)",
			a:    ref("c", ""),
			b:    ref("c", "Connection"),
			want: true,
		},
		{
			name: "Connection differs from ClusterConnection",
			a:    ref("c", "Connection"),
			b:    ref("c", "ClusterConnection"),
			want: false,
		},
		{
			name: "different names differ",
			a:    ref("a", "Connection"),
			b:    ref("b", "Connection"),
			want: false,
		},
		{
			name: "same cluster ref equals itself",
			a:    ref("c", "ClusterConnection"),
			b:    ref("c", "ClusterConnection"),
			want: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, sameConnectionRef(tc.a, tc.b))
		})
	}
}
