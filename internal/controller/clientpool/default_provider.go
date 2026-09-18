// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com). Copyright 2024 Datadog, Inc.

package clientpool

import (
	"context"
	"log/slog"
	"os"
	"slices"

	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	"github.com/temporalio/temporal-worker-controller/internal/controller/connectionprovider"
	"github.com/temporalio/temporal-worker-controller/internal/k8s"
	sdkclient "go.temporal.io/sdk/client"
	"go.temporal.io/sdk/log"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	runtimeclient "sigs.k8s.io/controller-runtime/pkg/client"
)

// DefaultProvider is the ConnectionProvider factory for the temporal.io Connection
// and ClusterConnection kinds. Fetch returns a ResolvedConnection bound to the
// loaded object and the shared ClientPool.
type DefaultProvider struct {
	pool          *ClientPool
	gk            schema.GroupKind
	clusterScoped bool
}

// NewDefaultProvider returns a ConnectionProvider for the given GroupKind.
func NewDefaultProvider(pool *ClientPool, gk schema.GroupKind, clusterScoped bool) *DefaultProvider {
	return &DefaultProvider{pool: pool, gk: gk, clusterScoped: clusterScoped}
}

// NewDefaultProviders returns the default Connection and ClusterConnection
// providers, sharing a ClientPool it constructs from c. The pool logs to
// stdout as JSON. Tests needing pool access should build DefaultProvider
// instances from their own pool.
func NewDefaultProviders(c runtimeclient.Client) []connectionprovider.ConnectionProvider {
	l := log.NewStructuredLogger(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		AddSource:   false,
		Level:       nil,
		ReplaceAttr: nil,
	})))
	pool := New(l, c)
	return []connectionprovider.ConnectionProvider{
		NewDefaultProvider(pool, schema.GroupKind{Group: temporaliov1alpha1.GroupVersion.Group, Kind: "Connection"}, false),
		NewDefaultProvider(pool, schema.GroupKind{Group: temporaliov1alpha1.GroupVersion.Group, Kind: "ClusterConnection"}, true),
	}
}

func (p *DefaultProvider) GroupKind() schema.GroupKind { return p.gk }

func (p *DefaultProvider) Fetch(ctx context.Context, ref temporaliov1alpha1.ConnectionReference, k8sNamespace string) (connectionprovider.ResolvedConnection, error) {
	name := connectionprovider.RefName(ref)
	if p.clusterScoped {
		var cc temporaliov1alpha1.ClusterConnection
		if err := p.pool.k8sClient.Get(ctx, types.NamespacedName{Name: name}, &cc); err != nil {
			return nil, err
		}
		return &defaultResolved{obj: &cc, spec: cc.Spec, pool: p.pool}, nil
	}
	var conn temporaliov1alpha1.Connection
	if err := p.pool.k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: k8sNamespace}, &conn); err != nil {
		return nil, err
	}
	return &defaultResolved{obj: &conn, spec: conn.Spec, pool: p.pool}, nil
}

func (p *DefaultProvider) NewObject() runtimeclient.Object {
	if p.clusterScoped {
		return &temporaliov1alpha1.ClusterConnection{}
	}
	return &temporaliov1alpha1.Connection{}
}

func (p *DefaultProvider) IsClusterScoped() bool { return p.clusterScoped }

// NewResolvedConnection wraps a ConnectionSpec and pool into a ResolvedConnection,
// for tests that drive the planner/deployment builders without a real Fetch.
func NewResolvedConnection(pool *ClientPool, spec temporaliov1alpha1.ConnectionSpec) connectionprovider.ResolvedConnection {
	return &defaultResolved{obj: &temporaliov1alpha1.Connection{Spec: spec}, spec: spec, pool: pool}
}

// defaultResolved is the ResolvedConnection for the default provider. The
// spec is opaque to the reconciler and never leaves this package.
type defaultResolved struct {
	obj  runtimeclient.Object
	spec temporaliov1alpha1.ConnectionSpec
	pool *ClientPool
}

func (r *defaultResolved) Object() runtimeclient.Object { return r.obj }

func (r *defaultResolved) GetClient(ctx context.Context, temporalNamespace, k8sNamespace, identity string) (sdkclient.Client, error) {
	spec := r.spec
	// Validate up front so an invalid secret surfaces as AuthError rather than
	// DialError.
	if err := spec.Validate(); err != nil {
		return nil, &connectionprovider.AuthError{Err: err}
	}
	authMode := spec.AuthMode()
	secretName := spec.SecretName()
	key := ClientPoolKey{
		HostPort:            spec.HostPort,
		TLSServerName:       spec.TLSServerName(),
		Namespace:           temporalNamespace,
		SecretName:          secretName,
		TLSCACertSecretName: spec.TLSCACertSecretName(),
		AuthMode:            authMode,
	}
	if c, ok := r.pool.GetSDKClient(key); ok {
		return c, nil
	}
	clientOpts, k, clientAuth, err := r.pool.ParseClientSecret(ctx, secretName, authMode, NewClientOptions{
		K8sNamespace:      k8sNamespace,
		TemporalNamespace: temporalNamespace,
		Spec:              spec,
		Identity:          identity,
	})
	if err != nil {
		return nil, &connectionprovider.AuthError{Err: err}
	}
	c, err := r.pool.DialAndUpsertClient(*clientOpts, *k, *clientAuth)
	if err != nil {
		return nil, &connectionprovider.DialError{Err: err}
	}
	return c, nil
}

func (r *defaultResolved) Fingerprint(_ context.Context) (string, error) {
	return k8s.ComputeConnectionSpecHash(r.spec), nil
}

func (r *defaultResolved) ApplyWorkerPodSpec(podSpec *corev1.PodSpec, annotations map[string]string, opts connectionprovider.PodSpecApplyOpts) error {
	applyDefaultWorkerPodSpecModifications(podSpec, r.spec, opts)
	// Write the fingerprint via Fingerprint() so the connection-spec-hash
	// annotation and the drift-detection value come from a single source and
	// cannot drift apart. The default Fingerprint ignores ctx.
	fp, err := r.Fingerprint(context.Background())
	if err != nil {
		return err
	}
	annotations[k8s.ConnectionSpecHashAnnotation] = fp
	return nil
}

func (r *defaultResolved) Evict(temporalNamespace, _ string) {
	key := ClientPoolKey{
		HostPort:            r.spec.HostPort,
		TLSServerName:       r.spec.TLSServerName(),
		Namespace:           temporalNamespace,
		SecretName:          r.spec.SecretName(),
		TLSCACertSecretName: r.spec.TLSCACertSecretName(),
		AuthMode:            r.spec.AuthMode(),
	}
	r.pool.EvictClient(key)
}

var _ connectionprovider.ResolvedConnection = (*defaultResolved)(nil)

// applyDefaultWorkerPodSpecModifications injects the connection-derived env
// vars and volumes into podSpec. Idempotent so it serves both fresh-pod
// creation and in-place drift updates. Does not write the
// connection-spec-hash annotation; ApplyWorkerPodSpec owns that.
func applyDefaultWorkerPodSpecModifications(
	podSpec *corev1.PodSpec,
	connection temporaliov1alpha1.ConnectionSpec,
	opts connectionprovider.PodSpecApplyOpts,
) {
	tlsServerName := connection.TLSServerName()
	mtls := connection.MutualTLSSecretRef != nil
	apiKey := !mtls && connection.APIKeySecretRef != nil

	for i := range podSpec.Containers {
		container := &podSpec.Containers[i]

		container.Env = setEnvVar(container.Env, "TEMPORAL_ADDRESS", connection.HostPort)
		container.Env = setEnvVar(container.Env, "TEMPORAL_NAMESPACE", opts.TemporalNamespace)
		container.Env = setEnvVar(container.Env, "TEMPORAL_DEPLOYMENT_NAME", opts.WorkerDeploymentName)
		container.Env = setEnvVar(container.Env, "TEMPORAL_WORKER_BUILD_ID", opts.BuildID)

		if tlsServerName != "" {
			container.Env = setEnvVar(container.Env, "TEMPORAL_TLS_SERVER_NAME", tlsServerName)
		} else {
			container.Env = removeEnvVar(container.Env, "TEMPORAL_TLS_SERVER_NAME")
		}

		if mtls {
			container.Env = setEnvVar(container.Env, "TEMPORAL_TLS", "true")
			container.Env = setEnvVar(container.Env, "TEMPORAL_TLS_CLIENT_KEY_PATH", "/etc/temporal/tls/tls.key")
			container.Env = setEnvVar(container.Env, "TEMPORAL_TLS_CLIENT_CERT_PATH", "/etc/temporal/tls/tls.crt")
			container.VolumeMounts = ensureTLSVolumeMount(container.VolumeMounts)
		} else {
			container.Env = removeEnvVar(container.Env, "TEMPORAL_TLS")
			container.Env = removeEnvVar(container.Env, "TEMPORAL_TLS_CLIENT_KEY_PATH")
			container.Env = removeEnvVar(container.Env, "TEMPORAL_TLS_CLIENT_CERT_PATH")
			container.VolumeMounts = removeTLSVolumeMount(container.VolumeMounts)
		}

		if apiKey {
			container.Env = setEnvVarFrom(container.Env, "TEMPORAL_API_KEY", &corev1.EnvVarSource{SecretKeyRef: connection.APIKeySecretRef})
		} else {
			container.Env = removeEnvVar(container.Env, "TEMPORAL_API_KEY")
		}
	}

	if mtls {
		podSpec.Volumes = ensureTLSVolume(podSpec.Volumes, connection.MutualTLSSecretRef.Name)
	} else {
		podSpec.Volumes = removeTLSVolume(podSpec.Volumes)
	}
}

func setEnvVar(envVars []corev1.EnvVar, name string, value string) []corev1.EnvVar {
	for i := range envVars {
		if envVars[i].Name == name {
			envVars[i].Value = value
			envVars[i].ValueFrom = nil
			return envVars
		}
	}
	return append(envVars, corev1.EnvVar{Name: name, Value: value})
}

func removeEnvVar(envVars []corev1.EnvVar, name string) []corev1.EnvVar {
	for i := range envVars {
		if envVars[i].Name == name {
			return append(envVars[:i], envVars[i+1:]...)
		}
	}
	return envVars
}

// setEnvVarFrom sets or replaces an env whose value comes from a source (e.g. a secret).
func setEnvVarFrom(envVars []corev1.EnvVar, name string, src *corev1.EnvVarSource) []corev1.EnvVar {
	for i := range envVars {
		if envVars[i].Name == name {
			envVars[i].Value = ""
			envVars[i].ValueFrom = src
			return envVars
		}
	}
	return append(envVars, corev1.EnvVar{Name: name, ValueFrom: src})
}

func ensureTLSVolume(volumes []corev1.Volume, secretName string) []corev1.Volume {
	for i := range volumes {
		if volumes[i].Name == "temporal-tls" {
			volumes[i].VolumeSource = corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{SecretName: secretName},
			}
			return volumes
		}
	}
	return append(volumes, corev1.Volume{
		Name:         "temporal-tls",
		VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: secretName}},
	})
}

func removeTLSVolume(volumes []corev1.Volume) []corev1.Volume {
	for i := range volumes {
		if volumes[i].Name == "temporal-tls" {
			return slices.Delete(volumes, i, i+1)
		}
	}
	return volumes
}

func ensureTLSVolumeMount(mounts []corev1.VolumeMount) []corev1.VolumeMount {
	for i := range mounts {
		if mounts[i].Name == "temporal-tls" {
			mounts[i].MountPath = "/etc/temporal/tls"
			return mounts
		}
	}
	return append(mounts, corev1.VolumeMount{Name: "temporal-tls", MountPath: "/etc/temporal/tls"})
}

func removeTLSVolumeMount(mounts []corev1.VolumeMount) []corev1.VolumeMount {
	for i := range mounts {
		if mounts[i].Name == "temporal-tls" {
			return slices.Delete(mounts, i, i+1)
		}
	}
	return mounts
}
