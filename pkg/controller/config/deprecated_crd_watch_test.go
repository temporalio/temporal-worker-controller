package config_test

import (
	"context"
	"errors"
	"testing"

	"github.com/temporalio/temporal-worker-controller/pkg/controller/config"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestDetectDeprecatedCRDWatchesAvailable(t *testing.T) {
	client := newDeprecatedCRDWatchClient()

	got, err := config.DetectDeprecatedCRDWatches(context.Background(), client, nil)
	if err != nil {
		t.Fatalf("DetectDeprecatedCRDWatches returned error: %v", err)
	}
	want := config.DeprecatedCRDWatches{
		TemporalWorkerDeployments: true,
		TemporalConnections:       true,
	}
	if got != want {
		t.Fatalf("DetectDeprecatedCRDWatches() = %#v, want %#v", got, want)
	}
}

func TestDetectDeprecatedCRDWatchesMissingResource(t *testing.T) {
	client := newDeprecatedCRDWatchClient()
	client.PrependReactor("list", config.DeprecatedTWDResource.Resource, func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewNotFound(config.DeprecatedTWDResource.GroupResource(), "")
	})

	got, err := config.DetectDeprecatedCRDWatches(context.Background(), client, nil)
	if err != nil {
		t.Fatalf("DetectDeprecatedCRDWatches returned error: %v", err)
	}
	want := config.DeprecatedCRDWatches{
		TemporalConnections: true,
	}
	if got != want {
		t.Fatalf("DetectDeprecatedCRDWatches() = %#v, want %#v", got, want)
	}
}

func TestDetectDeprecatedCRDWatchesListForbidden(t *testing.T) {
	client := newDeprecatedCRDWatchClient()
	client.PrependReactor("list", config.DeprecatedTWDResource.Resource, func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(config.DeprecatedTWDResource.GroupResource(), "", errors.New("denied"))
	})

	got, err := config.DetectDeprecatedCRDWatches(context.Background(), client, nil)
	if err != nil {
		t.Fatalf("DetectDeprecatedCRDWatches returned error: %v", err)
	}
	want := config.DeprecatedCRDWatches{
		TemporalConnections: true,
	}
	if got != want {
		t.Fatalf("DetectDeprecatedCRDWatches() = %#v, want %#v", got, want)
	}
}

func TestDetectDeprecatedCRDWatchesWatchForbidden(t *testing.T) {
	client := newDeprecatedCRDWatchClient()
	client.PrependWatchReactor(config.DeprecatedTCResource.Resource, func(k8stesting.Action) (bool, watch.Interface, error) {
		return true, nil, apierrors.NewForbidden(config.DeprecatedTCResource.GroupResource(), "", errors.New("denied"))
	})

	got, err := config.DetectDeprecatedCRDWatches(context.Background(), client, nil)
	if err != nil {
		t.Fatalf("DetectDeprecatedCRDWatches returned error: %v", err)
	}
	want := config.DeprecatedCRDWatches{
		TemporalWorkerDeployments: true,
	}
	if got != want {
		t.Fatalf("DetectDeprecatedCRDWatches() = %#v, want %#v", got, want)
	}
}

func TestDetectDeprecatedCRDWatchesEveryNamespace(t *testing.T) {
	client := newDeprecatedCRDWatchClient()
	client.PrependWatchReactor(config.DeprecatedTWDResource.Resource, func(action k8stesting.Action) (bool, watch.Interface, error) {
		if action.GetNamespace() == "ns-b" {
			return true, nil, apierrors.NewForbidden(config.DeprecatedTWDResource.GroupResource(), "", errors.New("denied"))
		}
		return false, nil, nil
	})

	got, err := config.DetectDeprecatedCRDWatches(context.Background(), client, []string{"ns-a", "ns-b"})
	if err != nil {
		t.Fatalf("DetectDeprecatedCRDWatches returned error: %v", err)
	}
	want := config.DeprecatedCRDWatches{
		TemporalConnections: true,
	}
	if got != want {
		t.Fatalf("DetectDeprecatedCRDWatches() = %#v, want %#v", got, want)
	}
}

func TestDetectDeprecatedCRDWatchesUnexpectedError(t *testing.T) {
	client := newDeprecatedCRDWatchClient()
	connectionResetErr := errors.New("connection reset")
	client.PrependReactor("list", config.DeprecatedTWDResource.Resource, func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, connectionResetErr
	})

	_, err := config.DetectDeprecatedCRDWatches(context.Background(), client, nil)
	if !errors.Is(err, connectionResetErr) {
		t.Fatalf("DetectDeprecatedCRDWatches() error = %v, want %v", err, connectionResetErr)
	}
	if got, want := err.Error(), "check TemporalWorkerDeployment availability: list temporalworkerdeployments: connection reset"; got != want {
		t.Fatalf("DetectDeprecatedCRDWatches() error = %q, want %q", got, want)
	}
}

func TestDetectDeprecatedCRDWatchesUnexpectedWatchError(t *testing.T) {
	client := newDeprecatedCRDWatchClient()
	connectionResetErr := errors.New("connection reset")
	client.PrependWatchReactor(config.DeprecatedTWDResource.Resource, func(k8stesting.Action) (bool, watch.Interface, error) {
		return true, nil, connectionResetErr
	})

	_, err := config.DetectDeprecatedCRDWatches(context.Background(), client, nil)
	if !errors.Is(err, connectionResetErr) {
		t.Fatalf("DetectDeprecatedCRDWatches() error = %v, want %v", err, connectionResetErr)
	}
	if got, want := err.Error(), "check TemporalWorkerDeployment availability: watch temporalworkerdeployments: connection reset"; got != want {
		t.Fatalf("DetectDeprecatedCRDWatches() error = %q, want %q", got, want)
	}
}

func newDeprecatedCRDWatchClient() *fake.FakeDynamicClient {
	client := fake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{
			config.DeprecatedTWDResource: "TemporalWorkerDeploymentList",
			config.DeprecatedTCResource:  "TemporalConnectionList",
		},
	)
	client.AddWatchReactor("*", func(k8stesting.Action) (bool, watch.Interface, error) {
		return true, watch.NewFake(), nil
	})
	return client
}
