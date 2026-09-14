// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

// Package connectionprovider defines the seam between the WorkerDeployment
// reconciler and the connection resources it references.
//
// The OSS controller does not know any concrete connection kind. It resolves a
// ConnectionReference to a registered provider, which supplies the referenced
// object (for finalizer management), a cached SDK client, and a fingerprint
// (for drift detection). The default registry ships with providers for the
// temporal.io Connection and ClusterConnection kinds; a wrapper binary can
// register additional kinds at process start without upstream CRD changes.
package connectionprovider

import (
	"context"
	"fmt"

	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	sdkclient "go.temporal.io/sdk/client"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ConnectionProvider is the group-kind-level factory. One instance is registered per
// GroupKind at process start; the default registry ships with Connection and
// ClusterConnection. Fetch resolves a single reference into a ResolvedConnection
// whose methods carry the behavior for that specific connection.
type ConnectionProvider interface {
	// GroupKind this provider handles (e.g. temporal.io/Connection).
	GroupKind() schema.GroupKind

	// Fetch loads the referenced object and returns a ResolvedConnection bound
	// to it. k8sNamespace is ignored for cluster-scoped kinds.
	Fetch(ctx context.Context, ref temporaliov1alpha1.ConnectionReference, k8sNamespace string) (ResolvedConnection, error)

	// NewObject returns a zero-valued object of this kind, for setting up a
	// watch in SetupWithManager.
	NewObject() client.Object

	// IsClusterScoped reports whether this kind is cluster-scoped. Used to
	// scope the watch mapper: a namespaced kind lists WorkerDeployments in the
	// connection's own namespace; a cluster-scoped kind lists across all.
	IsClusterScoped() bool
}

// ResolvedConnection is the per-reconcile handle for a fetched connection. Its
// methods carry the behavior for that connection; callers pass a single object
// rather than separate behavior and data.
type ResolvedConnection interface {
	// Object returns the underlying connection object, for finalizer management.
	Object() client.Object

	// GetClient returns a cached SDK client for the connection.
	GetClient(ctx context.Context, temporalNamespace, k8sNamespace, identity string) (sdkclient.Client, error)

	// Fingerprint returns a string that changes when the connection's effective
	// configuration changes. Used for drift detection. Must be stable when the
	// config is unchanged.
	//
	// ApplyWorkerPodSpec writes this same value into the connection-spec-hash
	// annotation, so the two cannot drift apart: the planner compares
	// Fingerprint() against that annotation to decide whether an in-place
	// pod-spec update is needed.
	Fingerprint(ctx context.Context) (string, error)

	// ApplyWorkerPodSpec mutates the worker pod template for this kind and
	// records Fingerprint() in the connection-spec-hash annotation. Must be
	// idempotent so it serves both fresh-pod creation and in-place drift
	// updates. The annotation MUST equal Fingerprint() so drift detection
	// (which compares the two) is consistent; the default provider guarantees
	// this by delegating to Fingerprint(). A provider whose kind owns pod config
	// out of band implements the mutation as a no-op but must still write the
	// fingerprint annotation.
	ApplyWorkerPodSpec(podSpec *corev1.PodSpec, annotations map[string]string, opts PodSpecApplyOpts) error

	// Evict drops the cached client for this connection.
	Evict(temporalNamespace, k8sNamespace string)
}

// PodSpecApplyOpts carries the per-worker context ApplyWorkerPodSpec needs to
// inject into the pod template. Grouping these as a struct avoids positional
// string confusion at call sites (e.g. swapping workerDeploymentName and
// buildID, which are indistinguishable as bare strings).
type PodSpecApplyOpts struct {
	TemporalNamespace    string
	WorkerDeploymentName string
	BuildID              string
}

// UnknownKindError is returned by LookupProvider for an unregistered GroupKind.
// The reconciler surfaces it as a status condition, not a transient error.
type UnknownKindError struct{ GK schema.GroupKind }

func (e *UnknownKindError) Error() string {
	return fmt.Sprintf("no connection provider registered for GroupKind %s", e.GK)
}

// Is reports whether err is an *UnknownKindError.
func (*UnknownKindError) Is(err error) bool {
	_, ok := err.(*UnknownKindError)
	return ok
}

// AuthError is returned by GetClient when credentials are missing or invalid
// (e.g. a referenced Secret is misconfigured). The reconciler surfaces it as
// an auth-secret status condition.
type AuthError struct{ Err error }

func (e *AuthError) Error() string { return e.Err.Error() }
func (e *AuthError) Unwrap() error { return e.Err }

// DialError is returned by GetClient when the Temporal server is unreachable.
// Credentials were valid; the connection itself failed.
type DialError struct{ Err error }

func (e *DialError) Error() string { return e.Err.Error() }
func (e *DialError) Unwrap() error { return e.Err }

// LookupProvider returns the provider in providers that handles ref's
// GroupKind, or *UnknownKindError if none matches. The reconciler surfaces
// the error as a status condition. providers is a small startup-built slice
// (default: two kinds), so a linear scan is cheaper than a map lookup in
// practice.
func LookupProvider(providers []ConnectionProvider, ref temporaliov1alpha1.ConnectionReference) (ConnectionProvider, error) {
	gk := RefGroupKind(ref)
	for _, p := range providers {
		if p.GroupKind() == gk {
			return p, nil
		}
	}
	return nil, &UnknownKindError{GK: gk}
}

// RefGroupKind derives the GroupKind a ConnectionReference targets. The
// shorthand Name form selects temporal.io/Connection; the ObjectRef form
// carries full type information.
func RefGroupKind(ref temporaliov1alpha1.ConnectionReference) schema.GroupKind {
	if ref.ObjectRef != nil {
		group := ""
		if ref.ObjectRef.APIGroup != nil {
			group = *ref.ObjectRef.APIGroup
		}
		return schema.GroupKind{Group: group, Kind: ref.ObjectRef.Kind}
	}
	// Shorthand Name form: temporal.io/Connection.
	return schema.GroupKind{Group: temporaliov1alpha1.GroupVersion.Group, Kind: "Connection"}
}

// RefName returns the connection resource name from either reference form.
func RefName(ref temporaliov1alpha1.ConnectionReference) string {
	if ref.ObjectRef != nil {
		return ref.ObjectRef.Name
	}
	return ref.Name
}
