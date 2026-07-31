package controller

import (
	"context"
	"errors"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestDetectDeprecatedCRDWatchesAvailable(t *testing.T) {
	client := newDeprecatedCRDWatchClient()

	got, err := DetectDeprecatedCRDWatches(context.Background(), client, nil)
	if err != nil {
		t.Fatalf("DetectDeprecatedCRDWatches returned error: %v", err)
	}
	want := DeprecatedCRDWatches{
		TemporalWorkerDeployments: true,
		TemporalConnections:       true,
	}
	if got != want {
		t.Fatalf("DetectDeprecatedCRDWatches() = %#v, want %#v", got, want)
	}
}

func TestDetectDeprecatedCRDWatchesMissingResource(t *testing.T) {
	client := newDeprecatedCRDWatchClient()
	client.PrependReactor("list", deprecatedTWDResource.Resource, func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewNotFound(deprecatedTWDResource.GroupResource(), "")
	})

	got, err := DetectDeprecatedCRDWatches(context.Background(), client, nil)
	if err != nil {
		t.Fatalf("DetectDeprecatedCRDWatches returned error: %v", err)
	}
	want := DeprecatedCRDWatches{
		TemporalConnections: true,
	}
	if got != want {
		t.Fatalf("DetectDeprecatedCRDWatches() = %#v, want %#v", got, want)
	}
}

func TestDetectDeprecatedCRDWatchesListForbidden(t *testing.T) {
	client := newDeprecatedCRDWatchClient()
	client.PrependReactor("list", deprecatedTWDResource.Resource, func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(deprecatedTWDResource.GroupResource(), "", errors.New("denied"))
	})

	got, err := DetectDeprecatedCRDWatches(context.Background(), client, nil)
	if err != nil {
		t.Fatalf("DetectDeprecatedCRDWatches returned error: %v", err)
	}
	want := DeprecatedCRDWatches{
		TemporalConnections: true,
	}
	if got != want {
		t.Fatalf("DetectDeprecatedCRDWatches() = %#v, want %#v", got, want)
	}
}

func TestDetectDeprecatedCRDWatchesWatchForbidden(t *testing.T) {
	client := newDeprecatedCRDWatchClient()
	client.PrependWatchReactor(deprecatedTCResource.Resource, func(k8stesting.Action) (bool, watch.Interface, error) {
		return true, nil, apierrors.NewForbidden(deprecatedTCResource.GroupResource(), "", errors.New("denied"))
	})

	got, err := DetectDeprecatedCRDWatches(context.Background(), client, nil)
	if err != nil {
		t.Fatalf("DetectDeprecatedCRDWatches returned error: %v", err)
	}
	want := DeprecatedCRDWatches{
		TemporalWorkerDeployments: true,
	}
	if got != want {
		t.Fatalf("DetectDeprecatedCRDWatches() = %#v, want %#v", got, want)
	}
}

func TestDetectDeprecatedCRDWatchesEveryNamespace(t *testing.T) {
	client := newDeprecatedCRDWatchClient()
	client.PrependWatchReactor(deprecatedTWDResource.Resource, func(action k8stesting.Action) (bool, watch.Interface, error) {
		if action.GetNamespace() == "ns-b" {
			return true, nil, apierrors.NewForbidden(deprecatedTWDResource.GroupResource(), "", errors.New("denied"))
		}
		return false, nil, nil
	})

	got, err := DetectDeprecatedCRDWatches(context.Background(), client, []string{"ns-a", "ns-b"})
	if err != nil {
		t.Fatalf("DetectDeprecatedCRDWatches returned error: %v", err)
	}
	want := DeprecatedCRDWatches{
		TemporalConnections: true,
	}
	if got != want {
		t.Fatalf("DetectDeprecatedCRDWatches() = %#v, want %#v", got, want)
	}
}

func TestDetectDeprecatedCRDWatchesUnexpectedError(t *testing.T) {
	client := newDeprecatedCRDWatchClient()
	client.PrependReactor("list", deprecatedTWDResource.Resource, func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("connection reset")
	})

	_, err := DetectDeprecatedCRDWatches(context.Background(), client, nil)
	if err == nil {
		t.Fatal("expected an error")
	}
}

func newDeprecatedCRDWatchClient() *fake.FakeDynamicClient {
	client := fake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{
			deprecatedTWDResource: "TemporalWorkerDeploymentList",
			deprecatedTCResource:  "TemporalConnectionList",
		},
	)
	client.AddWatchReactor("*", func(k8stesting.Action) (bool, watch.Interface, error) {
		return true, watch.NewFake(), nil
	})
	return client
}
