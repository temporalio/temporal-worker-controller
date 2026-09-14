// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package connectionprovider

import (
	"context"
	"errors"
	"testing"

	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// testProvider is a minimal ConnectionProvider for LookupProvider tests; only
// GroupKind is meaningful and the other methods are no-ops.
type testProvider struct {
	gk            schema.GroupKind
	clusterScoped bool
}

func (s *testProvider) GroupKind() schema.GroupKind { return s.gk }
func (s *testProvider) Fetch(context.Context, temporaliov1alpha1.ConnectionReference, string) (ResolvedConnection, error) {
	return nil, nil
}
func (s *testProvider) NewObject() client.Object { return &temporaliov1alpha1.Connection{} }
func (s *testProvider) IsClusterScoped() bool    { return s.clusterScoped }

var _ ConnectionProvider = (*testProvider)(nil)

func TestLookupProvider_UnknownKindReturnsUnknownKindError(t *testing.T) {
	providers := []ConnectionProvider{
		&testProvider{gk: schema.GroupKind{Group: "temporal.io", Kind: "Connection"}},
	}
	apiGroup := "temporal.io"
	ref := temporaliov1alpha1.ConnectionReference{
		ObjectRef: &corev1.TypedObjectReference{APIGroup: &apiGroup, Kind: "SomeOtherKind", Name: "my-conn"},
	}
	_, err := LookupProvider(providers, ref)
	if err == nil {
		t.Fatal("expected an error for an unregistered GroupKind")
	}
	var unknown *UnknownKindError
	if !errors.As(err, &unknown) {
		t.Fatalf("expected *UnknownKindError, got %T", err)
	}
	if unknown.GK.Kind != "SomeOtherKind" {
		t.Fatalf("expected GK.Kind=SomeOtherKind, got %q", unknown.GK.Kind)
	}
}

func TestLookupProvider_FindsRegisteredKind(t *testing.T) {
	ccGK := schema.GroupKind{Group: "temporal.io", Kind: "ClusterConnection"}
	providers := []ConnectionProvider{
		&testProvider{gk: schema.GroupKind{Group: "temporal.io", Kind: "Connection"}},
		&testProvider{gk: ccGK},
	}
	apiGroup := "temporal.io"
	ref := temporaliov1alpha1.ConnectionReference{
		ObjectRef: &corev1.TypedObjectReference{APIGroup: &apiGroup, Kind: "ClusterConnection", Name: "shared"},
	}

	p, err := LookupProvider(providers, ref)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.GroupKind() != ccGK {
		t.Fatalf("expected %s, got %s", ccGK, p.GroupKind())
	}
}

func TestLookupProvider_EmptySliceReturnsUnknownKindError(t *testing.T) {
	// Shorthand Name form resolves to temporal.io/Connection.
	_, err := LookupProvider(nil, temporaliov1alpha1.ConnectionReference{Name: "foo"})
	if err == nil {
		t.Fatal("expected an error for an empty provider slice")
	}
	var unknown *UnknownKindError
	if !errors.As(err, &unknown) {
		t.Fatalf("expected *UnknownKindError, got %T", err)
	}
}

func TestRefGroupKind(t *testing.T) {
	connGK := schema.GroupKind{Group: temporaliov1alpha1.GroupVersion.Group, Kind: "Connection"}
	ccGK := schema.GroupKind{Group: temporaliov1alpha1.GroupVersion.Group, Kind: "ClusterConnection"}

	// Shorthand Name form selects temporal.io/Connection.
	if gk := RefGroupKind(temporaliov1alpha1.ConnectionReference{Name: "foo"}); gk != connGK {
		t.Fatalf("shorthand Name: expected %s, got %s", connGK, gk)
	}
	// ObjectRef form carries full type information.
	apiGroup := "temporal.io"
	if gk := RefGroupKind(temporaliov1alpha1.ConnectionReference{
		ObjectRef: &corev1.TypedObjectReference{APIGroup: &apiGroup, Kind: "ClusterConnection", Name: "bar"},
	}); gk != ccGK {
		t.Fatalf("ObjectRef: expected %s, got %s", ccGK, gk)
	}
	// A core (empty) apiGroup ObjectRef resolves to an empty group.
	if gk := RefGroupKind(temporaliov1alpha1.ConnectionReference{
		ObjectRef: &corev1.TypedObjectReference{Kind: "Connection", Name: "baz"},
	}); gk.Group != "" || gk.Kind != "Connection" {
		t.Fatalf("empty apiGroup: expected empty group, got %s", gk)
	}
}

func TestRefName(t *testing.T) {
	apiGroup := "temporal.io"

	// Shorthand Name form.
	ref := (temporaliov1alpha1.ConnectionReference{Name: "foo"})
	if RefName(ref) != "foo" {
		t.Fatalf("expected foo, got %q", RefName(ref))
	}
	// ObjectRef form carries the name on objectRef.name.
	ccRef := temporaliov1alpha1.ConnectionReference{
		ObjectRef: &corev1.TypedObjectReference{APIGroup: &apiGroup, Kind: "ClusterConnection", Name: "shared"},
	}
	if RefName(ccRef) != "shared" {
		t.Fatalf("expected shared, got %q", RefName(ccRef))
	}
}

func TestUnknownKindError_Is(t *testing.T) {
	err := &UnknownKindError{GK: schema.GroupKind{Group: "temporal.io", Kind: "SomeOtherKind"}}
	if !errors.Is(err, &UnknownKindError{}) {
		t.Fatal("errors.Is should report true for *UnknownKindError")
	}
	if errors.Is(errors.New("other"), &UnknownKindError{}) {
		t.Fatal("errors.Is should report false for an unrelated error")
	}
}
