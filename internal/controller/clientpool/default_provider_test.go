// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com). Copyright 2024 Datadog, Inc.

package clientpool

import (
	"context"
	"crypto/x509"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	"github.com/temporalio/temporal-worker-controller/internal/controller/connectionprovider"
	"github.com/temporalio/temporal-worker-controller/internal/k8s"
	sdkclient "go.temporal.io/sdk/client"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// newTestScheme returns a scheme with the temporal.io and corev1 types registered.
func newTestScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = corev1.AddToScheme(s)
	_ = temporaliov1alpha1.AddToScheme(s)
	return s
}

// fakeK8sClient builds a fake client builder seeded with the given objects.
func fakeK8sClient(objs ...runtime.Object) *fake.ClientBuilder {
	return fake.NewClientBuilder().WithScheme(newTestScheme()).WithRuntimeObjects(objs...)
}

// newDefaultTestPool builds a pool backed by a fake k8s client that has both
// corev1 and temporaliov1alpha1 registered (newDefaultTestPool only
// registers corev1, so it can't seed Connection/ClusterConnection objects).
func newDefaultTestPool(objs ...runtime.Object) *ClientPool {
	k8sClient := fakeK8sClient(objs...).Build()
	return &ClientPool{
		logger:           noopLogger{},
		clients:          make(map[ClientPoolKey]ClientInfo),
		k8sClient:        k8sClient,
		dialFn:           sdkclient.Dial,
		systemCertPoolFn: x509.SystemCertPool,
	}
}

func makeConnection(name, namespace, hostPort string) *temporaliov1alpha1.Connection {
	return &temporaliov1alpha1.Connection{
		TypeMeta:   metav1.TypeMeta{APIVersion: temporaliov1alpha1.GroupVersion.String(), Kind: "Connection"},
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec:       temporaliov1alpha1.ConnectionSpec{HostPort: hostPort},
	}
}

func makeClusterConnection(name, hostPort string) *temporaliov1alpha1.ClusterConnection {
	return &temporaliov1alpha1.ClusterConnection{
		TypeMeta:   metav1.TypeMeta{APIVersion: temporaliov1alpha1.GroupVersion.String(), Kind: "ClusterConnection"},
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       temporaliov1alpha1.ConnectionSpec{HostPort: hostPort},
	}
}

// ─── Fetch ───────────────────────────────────────────────────────────────────

func TestDefaultProvider_Fetch_Namespaced(t *testing.T) {
	conn := makeConnection("my-conn", "default", "h:7233")
	pool := newDefaultTestPool(conn)
	prov := NewDefaultProvider(pool, schema.GroupKind{Group: temporaliov1alpha1.GroupVersion.Group, Kind: "Connection"}, false)

	resolved, err := prov.Fetch(context.Background(), temporaliov1alpha1.ConnectionReference{Name: "my-conn"}, "default")
	require.NoError(t, err)
	require.NotNil(t, resolved)

	obj := resolved.Object()
	got, ok := obj.(*temporaliov1alpha1.Connection)
	require.True(t, ok, "expected *Connection, got %T", obj)
	assert.Equal(t, "h:7233", got.Spec.HostPort)
}

func TestDefaultProvider_Fetch_ClusterScoped(t *testing.T) {
	cc := makeClusterConnection("shared", "cc:7233")
	pool := newDefaultTestPool(cc)
	prov := NewDefaultProvider(pool, schema.GroupKind{Group: temporaliov1alpha1.GroupVersion.Group, Kind: "ClusterConnection"}, true)

	// k8sNamespace is ignored for cluster-scoped kinds.
	resolved, err := prov.Fetch(context.Background(), temporaliov1alpha1.ConnectionReference{
		ObjectRef: &corev1.TypedObjectReference{
			APIGroup: ptr(temporaliov1alpha1.GroupVersion.Group), Kind: "ClusterConnection", Name: "shared",
		},
	}, "should-be-ignored")
	require.NoError(t, err)

	got, ok := resolved.Object().(*temporaliov1alpha1.ClusterConnection)
	require.True(t, ok, "expected *ClusterConnection")
	assert.Equal(t, "cc:7233", got.Spec.HostPort)
	assert.Empty(t, got.Namespace, "cluster-scoped object must have no namespace")
}

func TestDefaultProvider_Fetch_NotFound(t *testing.T) {
	pool := newDefaultTestPool() // no objects
	prov := NewDefaultProvider(pool, schema.GroupKind{Group: temporaliov1alpha1.GroupVersion.Group, Kind: "Connection"}, false)

	_, err := prov.Fetch(context.Background(), temporaliov1alpha1.ConnectionReference{Name: "missing"}, "default")
	require.Error(t, err)
}

// ─── GetClient: error wrapping ────────────────────────────────────────────────

// stubSDKClient is a minimal sdkclient.Client whose Close is a no-op so the pool
// can evict it without panicking.
type stubSDKClient struct {
	sdkclient.Client
}

func (stubSDKClient) Close() {}

func TestDefaultProvider_GetClient_InvalidSpec_ReturnsAuthError(t *testing.T) {
	// TLS mode with an empty secret name fails ConnectionSpec.Validate, which
	// GetClient must surface as *connectionprovider.AuthError (not DialError).
	pool := newDefaultTestPool()
	resolved := NewResolvedConnection(pool, temporaliov1alpha1.ConnectionSpec{
		HostPort:           "h:7233",
		MutualTLSSecretRef: &temporaliov1alpha1.SecretReference{Name: ""},
	})

	_, err := resolved.GetClient(context.Background(), "ns", "default", "identity")
	require.Error(t, err)
	var authErr *connectionprovider.AuthError
	require.ErrorAs(t, err, &authErr, "invalid spec must surface as AuthError")
}

func TestDefaultProvider_GetClient_DialFailure_ReturnsDialError(t *testing.T) {
	pool := &ClientPool{
		logger:           noopLogger{},
		clients:          make(map[ClientPoolKey]ClientInfo),
		dialFn:           func(_ sdkclient.Options) (sdkclient.Client, error) { return nil, errors.New("boom") },
		systemCertPoolFn: x509.SystemCertPool,
	}
	resolved := NewResolvedConnection(pool, temporaliov1alpha1.ConnectionSpec{HostPort: "h:7233"})

	_, err := resolved.GetClient(context.Background(), "ns", "default", "identity")
	require.Error(t, err)
	var dialErr *connectionprovider.DialError
	require.ErrorAs(t, err, &dialErr, "dial failure must surface as DialError")
}

func TestDefaultProvider_GetClient_CacheHit(t *testing.T) {
	pool := newDefaultTestPool()
	resolved := NewResolvedConnection(pool, temporaliov1alpha1.ConnectionSpec{HostPort: "h:7233"})

	// Seed the pool with a stub client so GetClient returns it without dialing.
	key := ClientPoolKey{HostPort: "h:7233", Namespace: "ns", AuthMode: temporaliov1alpha1.AuthModeNoCredentials}
	stub := &stubSDKClient{}
	pool.SetClientForTesting(key, stub)

	got, err := resolved.GetClient(context.Background(), "ns", "default", "identity")
	require.NoError(t, err)
	assert.Same(t, stub, got, "GetClient must return the cached client on a hit")
}

// ─── Fingerprint ─────────────────────────────────────────────────────────────

func TestDefaultProvider_Fingerprint_StableAndChanging(t *testing.T) {
	pool := newDefaultTestPool()
	specA := temporaliov1alpha1.ConnectionSpec{HostPort: "a:7233"}
	specB := temporaliov1alpha1.ConnectionSpec{HostPort: "b:7233"}

	h1, err := NewResolvedConnection(pool, specA).Fingerprint(context.Background())
	require.NoError(t, err)
	h2, err := NewResolvedConnection(pool, specA).Fingerprint(context.Background())
	require.NoError(t, err)
	h3, err := NewResolvedConnection(pool, specB).Fingerprint(context.Background())
	require.NoError(t, err)

	assert.Equal(t, h1, h2, "same spec must produce the same fingerprint")
	assert.NotEqual(t, h1, h3, "different specs must produce different fingerprints")
}

// ─── ApplyWorkerPodSpec ──────────────────────────────────────────────────────

func TestDefaultProvider_ApplyWorkerPodSpec_InjectsEnvAndAnnotation(t *testing.T) {
	pool := newDefaultTestPool()
	resolved := NewResolvedConnection(pool, temporaliov1alpha1.ConnectionSpec{HostPort: "h:7233"})

	podSpec := &corev1.PodSpec{Containers: []corev1.Container{{}}}
	ann := map[string]string{}
	require.NoError(t, resolved.ApplyWorkerPodSpec(podSpec, ann, connectionprovider.PodSpecApplyOpts{
		TemporalNamespace:    "ns",
		WorkerDeploymentName: "wd",
		BuildID:              "build1",
	}))

	// Env injection.
	env := podSpec.Containers[0].Env
	assertEnv(t, env, "TEMPORAL_ADDRESS", "h:7233")
	assertEnv(t, env, "TEMPORAL_NAMESPACE", "ns")
	assertEnv(t, env, "TEMPORAL_DEPLOYMENT_NAME", "wd")
	assertEnv(t, env, "TEMPORAL_WORKER_BUILD_ID", "build1")

	// Annotation written.
	assert.NotEmpty(t, ann[k8s.ConnectionSpecHashAnnotation], "connection-spec-hash annotation must be set")
}

func TestDefaultProvider_ApplyWorkerPodSpec_Idempotent(t *testing.T) {
	pool := newDefaultTestPool()
	resolved := NewResolvedConnection(pool, temporaliov1alpha1.ConnectionSpec{HostPort: "h:7233"})

	podSpec := &corev1.PodSpec{Containers: []corev1.Container{{}}}
	ann := map[string]string{}
	opts := connectionprovider.PodSpecApplyOpts{TemporalNamespace: "ns", WorkerDeploymentName: "wd", BuildID: "build1"}
	_ = resolved.ApplyWorkerPodSpec(podSpec, ann, opts)
	_ = resolved.ApplyWorkerPodSpec(podSpec, ann, opts)

	// Two applications must not duplicate the env vars.
	env := podSpec.Containers[0].Env
	count := 0
	for _, e := range env {
		if e.Name == "TEMPORAL_ADDRESS" {
			count++
		}
	}
	assert.Equal(t, 1, count, "idempotent apply must not duplicate env vars")
}

// ─── ApplyWorkerPodSpec: fingerprint coupling ───────────────────────────────

func TestDefaultProvider_ApplyWorkerPodSpec_AnnotationMatchesFingerprint(t *testing.T) {
	pool := newDefaultTestPool()
	spec := temporaliov1alpha1.ConnectionSpec{HostPort: "h:7233", MutualTLSSecretRef: &temporaliov1alpha1.SecretReference{Name: "tls-secret"}}
	resolved := NewResolvedConnection(pool, spec)

	podSpec := &corev1.PodSpec{Containers: []corev1.Container{{}}}
	ann := map[string]string{}
	require.NoError(t, resolved.ApplyWorkerPodSpec(podSpec, ann, connectionprovider.PodSpecApplyOpts{
		TemporalNamespace:    "ns",
		WorkerDeploymentName: "wd",
		BuildID:              "build1",
	}))

	fp, err := resolved.Fingerprint(context.Background())
	require.NoError(t, err)
	assert.Equal(t, fp, ann[k8s.ConnectionSpecHashAnnotation],
		"connection-spec-hash annotation must equal Fingerprint() so drift detection is consistent")
}

// ─── Evict ──────────────────────────────────────────────────────────────────

func TestDefaultProvider_Evict_DropsCachedClient(t *testing.T) {
	pool := newDefaultTestPool()
	spec := temporaliov1alpha1.ConnectionSpec{HostPort: "h:7233"}
	resolved := NewResolvedConnection(pool, spec)

	key := ClientPoolKey{HostPort: "h:7233", Namespace: "ns", AuthMode: temporaliov1alpha1.AuthModeNoCredentials}
	pool.SetClientForTesting(key, &stubSDKClient{})

	// Sanity: the client is cached.
	_, ok := pool.GetSDKClient(key)
	require.True(t, ok)

	resolved.Evict("ns", "default")

	_, ok = pool.GetSDKClient(key)
	assert.False(t, ok, "Evict must drop the cached client")
}

// ─── NewObject / IsClusterScoped ──────────────────────────────────────────────

func TestDefaultProvider_NewObject(t *testing.T) {
	pool := newDefaultTestPool()

	nsProv := NewDefaultProvider(pool, schema.GroupKind{Group: temporaliov1alpha1.GroupVersion.Group, Kind: "Connection"}, false)
	_, ok := nsProv.NewObject().(*temporaliov1alpha1.Connection)
	assert.True(t, ok, "namespaced provider must return a *Connection")

	ccProv := NewDefaultProvider(pool, schema.GroupKind{Group: temporaliov1alpha1.GroupVersion.Group, Kind: "ClusterConnection"}, true)
	_, ok = ccProv.NewObject().(*temporaliov1alpha1.ClusterConnection)
	assert.True(t, ok, "cluster-scoped provider must return a *ClusterConnection")
}

func TestDefaultProvider_IsClusterScoped(t *testing.T) {
	pool := newDefaultTestPool()
	nsProv := NewDefaultProvider(pool, schema.GroupKind{Group: temporaliov1alpha1.GroupVersion.Group, Kind: "Connection"}, false)
	ccProv := NewDefaultProvider(pool, schema.GroupKind{Group: temporaliov1alpha1.GroupVersion.Group, Kind: "ClusterConnection"}, true)
	assert.False(t, nsProv.IsClusterScoped())
	assert.True(t, ccProv.IsClusterScoped())
}

// ─── NewDefaultProviders ─────────────────────────────────────────────────────

func TestNewDefaultProviders_ReturnsTwoRegisteredKinds(t *testing.T) {
	provs := NewDefaultProviders(fakeK8sClient().Build())
	require.Len(t, provs, 2)

	assert.Equal(t, schema.GroupKind{Group: temporaliov1alpha1.GroupVersion.Group, Kind: "Connection"}, provs[0].GroupKind())
	assert.False(t, provs[0].IsClusterScoped())

	assert.Equal(t, schema.GroupKind{Group: temporaliov1alpha1.GroupVersion.Group, Kind: "ClusterConnection"}, provs[1].GroupKind())
	assert.True(t, provs[1].IsClusterScoped())
}

// ─── helpers ──────────────────────────────────────────────────────────────────

func ptr[T any](v T) *T { return &v }

func assertEnv(t *testing.T, env []corev1.EnvVar, name, value string) {
	t.Helper()
	for _, e := range env {
		if e.Name == name {
			assert.Equal(t, value, e.Value, "env %s", name)
			return
		}
	}
	t.Errorf("expected env var %s to be present", name)
}
