// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package controller

import (
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
func NewCacheOptions() (cache.Options, error) {
	deploymentLabelReq, err := labels.NewRequirement(k8s.WorkerDeploymentNameLabel, selection.Exists, nil)
	if err != nil {
		return cache.Options{}, err
	}

	return cache.Options{
		ByObject: map[client.Object]cache.ByObject{
			&appsv1.Deployment{}: {
				Label: labels.NewSelector().Add(*deploymentLabelReq),
			},
		},
	}, nil
}
