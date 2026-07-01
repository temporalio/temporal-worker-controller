// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package controller

import (
	"reflect"
	"testing"

	"github.com/temporalio/temporal-worker-controller/internal/k8s"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/labels"
)

func TestNewCacheOptionsScopesDeploymentsByWorkerLabel(t *testing.T) {
	opts, err := NewCacheOptions(nil)
	if err != nil {
		t.Fatalf("NewCacheOptions returned error: %v", err)
	}

	var deploymentSelector labels.Selector
	for obj, cfg := range opts.ByObject {
		if _, ok := obj.(*appsv1.Deployment); ok {
			deploymentSelector = cfg.Label
			break
		}
	}

	if deploymentSelector == nil {
		t.Fatal("expected Deployment cache selector to be configured")
	}

	if !deploymentSelector.Matches(labels.Set{k8s.WorkerDeploymentNameLabel: "my-worker"}) {
		t.Fatalf("expected selector to match Deployment with %s", k8s.WorkerDeploymentNameLabel)
	}
	if deploymentSelector.Matches(labels.Set{k8s.BuildIDLabel: "build-123"}) {
		t.Fatalf("expected selector not to match Deployment with only %s", k8s.BuildIDLabel)
	}
	if deploymentSelector.Matches(labels.Set{}) {
		t.Fatal("expected selector not to match unlabeled Deployment")
	}
}

func TestNewCacheOptionsScopesToWatchNamespaces(t *testing.T) {
	opts, err := NewCacheOptions([]string{"ns-a", "ns-b"})
	if err != nil {
		t.Fatalf("NewCacheOptions returned error: %v", err)
	}

	if len(opts.DefaultNamespaces) != 2 {
		t.Fatalf("expected 2 default namespaces, got %d", len(opts.DefaultNamespaces))
	}
	for _, ns := range []string{"ns-a", "ns-b"} {
		if _, ok := opts.DefaultNamespaces[ns]; !ok {
			t.Fatalf("expected namespace %q in DefaultNamespaces", ns)
		}
	}
}

func TestNewCacheOptionsWatchesAllNamespacesWhenEmpty(t *testing.T) {
	opts, err := NewCacheOptions(nil)
	if err != nil {
		t.Fatalf("NewCacheOptions returned error: %v", err)
	}

	if len(opts.DefaultNamespaces) != 0 {
		t.Fatalf("expected no default namespaces, got %d", len(opts.DefaultNamespaces))
	}
}

func TestParseWatchNamespaces(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want []string
	}{
		{name: "empty returns nil", raw: "", want: nil},
		{name: "single namespace", raw: "ns-a", want: []string{"ns-a"}},
		{name: "comma separated", raw: "ns-a,ns-b", want: []string{"ns-a", "ns-b"}},
		{name: "trims whitespace and drops empties", raw: " ns-a , , ns-b ,", want: []string{"ns-a", "ns-b"}},
		{name: "separators only returns empty slice", raw: " , , ", want: []string{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseWatchNamespaces(tt.raw)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("ParseWatchNamespaces(%q) = %#v, want %#v", tt.raw, got, tt.want)
			}
		})
	}
}
