// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package controller

import (
	"strings"

	"github.com/temporalio/temporal-worker-controller/internal/k8s"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// NewCacheOptions scopes the manager's Deployment cache to worker Deployments.
//
// Owns(&appsv1.Deployment{}) filters which Deployment events enqueue reconciles,
// but controller-runtime still lists, watches, and retains cached Deployment
// objects before those events reach the controller. Restricting the manager cache
// prevents unrelated cluster Deployments from growing the controller's memory use.
//
// When watchNamespaces is non-empty the cache (and therefore the controller's
// watches) is restricted to those namespaces; empty means all namespaces.
func NewCacheOptions(watchNamespaces []string) (cache.Options, error) {
	deploymentLabelReq, err := labels.NewRequirement(k8s.WorkerDeploymentNameLabel, selection.Exists, nil)
	if err != nil {
		return cache.Options{}, err
	}

	opts := cache.Options{
		ByObject: map[client.Object]cache.ByObject{
			&appsv1.Deployment{}: {
				Label: labels.NewSelector().Add(*deploymentLabelReq),
			},
		},
	}

	if len(watchNamespaces) > 0 {
		defaultNamespaces := make(map[string]cache.Config, len(watchNamespaces))
		for _, ns := range watchNamespaces {
			defaultNamespaces[ns] = cache.Config{}
		}
		opts.DefaultNamespaces = defaultNamespaces
	}

	return opts, nil
}

// ParseWatchNamespaces splits a comma-separated namespace list (from the
// --watch-namespaces flag or WATCH_NAMESPACES env var) into a slice, trimming
// whitespace and dropping empty entries. An empty input returns nil, which
// NewCacheOptions treats as "watch all namespaces".
func ParseWatchNamespaces(raw string) []string {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	namespaces := make([]string, 0, len(parts))
	for _, p := range parts {
		if ns := strings.TrimSpace(p); ns != "" {
			namespaces = append(namespaces, ns)
		}
	}
	return namespaces
}
