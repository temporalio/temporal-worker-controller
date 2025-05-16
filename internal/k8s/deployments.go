// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package k8s

import (
	"context"
	"fmt"
	"sort"

	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	buildIDLabel   = "temporal.io/build-id"
	deployOwnerKey = ".metadata.controller"
)

// DeploymentState represents the Kubernetes state of all deployments for a temporal worker deployment
type DeploymentState struct {
	// Map of versionID to deployment
	Deployments map[string]*appsv1.Deployment
	// Sorted deployments by creation time
	DeploymentsByTime []*appsv1.Deployment
	// Map of deployment references
	DeploymentRefs map[string]*v1.ObjectReference
}

// GetDeploymentState queries Kubernetes to get the state of all deployments
// associated with a TemporalWorkerDeployment
func GetDeploymentState(
	ctx context.Context,
	k8sClient client.Client,
	namespace string,
	ownerName string,
	workerDeploymentName string,
) (*DeploymentState, error) {
	state := &DeploymentState{
		Deployments:       make(map[string]*appsv1.Deployment),
		DeploymentsByTime: []*appsv1.Deployment{},
		DeploymentRefs:    make(map[string]*v1.ObjectReference),
	}

	// List k8s deployments that correspond to managed worker deployment versions
	var childDeploys appsv1.DeploymentList
	if err := k8sClient.List(
		ctx,
		&childDeploys,
		client.InNamespace(namespace),
		client.MatchingFields{deployOwnerKey: ownerName},
	); err != nil {
		return nil, fmt.Errorf("unable to list child deployments: %w", err)
	}

	// Sort deployments by creation timestamp
	sort.SliceStable(childDeploys.Items, func(i, j int) bool {
		return childDeploys.Items[i].ObjectMeta.CreationTimestamp.Before(&childDeploys.Items[j].ObjectMeta.CreationTimestamp)
	})

	// Track each k8s deployment by version ID
	for i := range childDeploys.Items {
		deploy := &childDeploys.Items[i]
		if buildID, ok := deploy.GetLabels()[buildIDLabel]; ok {
			versionID := workerDeploymentName + "." + buildID
			state.Deployments[versionID] = deploy
			state.DeploymentsByTime = append(state.DeploymentsByTime, deploy)
			state.DeploymentRefs[versionID] = newObjectRef(deploy)
		}
		// Any deployments without the build ID label are ignored
	}

	return state, nil
}

// IsDeploymentHealthy checks if a deployment is in the "Available" state
func IsDeploymentHealthy(deployment *appsv1.Deployment) (bool, *metav1.Time) {
	// TODO(jlegrone): do we need to sort conditions by timestamp to check only latest?
	for _, c := range deployment.Status.Conditions {
		if c.Type == appsv1.DeploymentAvailable && c.Status == v1.ConditionTrue {
			return true, &c.LastTransitionTime
		}
	}
	return false, nil
}

// newObjectRef creates a reference to a Kubernetes object
func newObjectRef(obj client.Object) *v1.ObjectReference {
	return &v1.ObjectReference{
		APIVersion: obj.GetObjectKind().GroupVersionKind().GroupVersion().String(),
		Kind:       obj.GetObjectKind().GroupVersionKind().Kind,
		Name:       obj.GetName(),
		Namespace:  obj.GetNamespace(),
		UID:        obj.GetUID(),
	}
}
