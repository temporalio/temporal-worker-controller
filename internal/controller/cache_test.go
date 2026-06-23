// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package controller

import (
	"testing"

	"github.com/temporalio/temporal-worker-controller/internal/k8s"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/labels"
)

func TestNewCacheOptionsScopesDeploymentsByWorkerLabel(t *testing.T) {
	opts, err := NewCacheOptions()
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
