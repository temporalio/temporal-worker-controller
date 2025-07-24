// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package controller

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// updatePatchStatuses updates the status of patches based on the current state of versions
func updatePatchStatuses(
	ctx context.Context,
	k8sClient client.Client,
	logger logr.Logger,
	workerDeployment *temporaliov1alpha1.TemporalWorkerDeployment,
) error {
	// List all patches targeting this worker deployment
	patchList := &temporaliov1alpha1.TemporalWorkerDeploymentPatchList{}
	err := k8sClient.List(ctx, patchList, client.MatchingFields{
		patchTargetKey: workerDeployment.Name,
	}, client.InNamespace(workerDeployment.Namespace))
	if err != nil {
		return fmt.Errorf("failed to list patches: %w", err)
	}

	// Get current version IDs from the worker deployment status
	activeVersions := getActiveVersionIDs(workerDeployment)

	for _, patch := range patchList.Items {
		patchNeedsUpdate := false
		statusNeedsUpdate := false

		// Check if we need to set OwnerReference
		if !hasOwnerReference(&patch, workerDeployment) {
			setOwnerReference(&patch, workerDeployment)
			patchNeedsUpdate = true
		}

		newStatus := determinePatchStatus(patch, workerDeployment, activeVersions)

		if patch.Status.Status != newStatus {
			patch.Status.Status = newStatus
			patch.Status.ObservedGeneration = patch.Generation
			statusNeedsUpdate = true
		}

		// Update the patch object if OwnerReference was added
		if patchNeedsUpdate {
			if err := k8sClient.Update(ctx, &patch); err != nil {
				logger.Error(err, "Failed to update patch", "patch", patch.Name)
				continue
			}
		}

		// Update status separately if status was changed
		if statusNeedsUpdate {
			if err := k8sClient.Status().Update(ctx, &patch); err != nil {
				logger.Error(err, "Failed to update patch status", "patch", patch.Name)
				continue
			}
		}
	}

	return nil
}

// determinePatchStatus determines the appropriate status for a patch
func determinePatchStatus(
	patch temporaliov1alpha1.TemporalWorkerDeploymentPatch,
	workerDeployment *temporaliov1alpha1.TemporalWorkerDeployment,
	activeVersions map[string]bool,
) temporaliov1alpha1.PatchStatus {
	// Check if the target deployment name matches (namespace is assumed to be the same)
	if workerDeployment.Name != patch.Spec.TemporalWorkerDeploymentName {
		return temporaliov1alpha1.PatchStatusInvalid
	}

	// Check if the version exists
	if activeVersions[patch.Spec.VersionID] {
		return temporaliov1alpha1.PatchStatusActive
	}

	return temporaliov1alpha1.PatchStatusOrphaned
}

// getActiveVersionIDs extracts all active version IDs from the worker deployment status
func getActiveVersionIDs(
	workerDeployment *temporaliov1alpha1.TemporalWorkerDeployment,
) map[string]bool {
	versions := make(map[string]bool)

	versions[workerDeployment.Status.TargetVersion.VersionID] = true

	if workerDeployment.Status.CurrentVersion != nil {
		versions[workerDeployment.Status.CurrentVersion.VersionID] = true
	}

	for _, deprecatedVersion := range workerDeployment.Status.DeprecatedVersions {
		versions[deprecatedVersion.VersionID] = true
	}

	return versions
}

// enqueuePatchHandler handles events from TemporalWorkerDeploymentPatch resources
// and enqueues reconciliation requests for the target TemporalWorkerDeployment
type enqueuePatchHandler struct {
	client client.Client
}

// Create implements handler.TypedEventHandler
func (h *enqueuePatchHandler) Create(ctx context.Context, e event.TypedCreateEvent[client.Object], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	h.enqueuePatch(e.Object, q)
}

// Update implements handler.TypedEventHandler
func (h *enqueuePatchHandler) Update(ctx context.Context, e event.TypedUpdateEvent[client.Object], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	h.enqueuePatch(e.ObjectNew, q)
}

// Delete implements handler.TypedEventHandler
func (h *enqueuePatchHandler) Delete(ctx context.Context, e event.TypedDeleteEvent[client.Object], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	h.enqueuePatch(e.Object, q)
}

// Generic implements handler.TypedEventHandler
func (h *enqueuePatchHandler) Generic(ctx context.Context, e event.TypedGenericEvent[client.Object], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	h.enqueuePatch(e.Object, q)
}

// enqueuePatch enqueues a reconciliation request for the TemporalWorkerDeployment
// that this patch targets
func (h *enqueuePatchHandler) enqueuePatch(obj client.Object, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	patch, ok := obj.(*temporaliov1alpha1.TemporalWorkerDeploymentPatch)
	if !ok {
		return
	}

	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      patch.Spec.TemporalWorkerDeploymentName,
			Namespace: patch.Namespace,
		},
	}

	q.Add(req)
}

// Ensure enqueuePatchHandler implements the correct event handler interface
var _ handler.TypedEventHandler[client.Object, reconcile.Request] = &enqueuePatchHandler{}

// hasOwnerReference checks if the patch already has an OwnerReference to the specified TemporalWorkerDeployment
func hasOwnerReference(
	patch *temporaliov1alpha1.TemporalWorkerDeploymentPatch,
	workerDeployment *temporaliov1alpha1.TemporalWorkerDeployment,
) bool {
	for _, ownerRef := range patch.GetOwnerReferences() {
		if ownerRef.UID == workerDeployment.GetUID() &&
			ownerRef.Kind == "TemporalWorkerDeployment" &&
			ownerRef.APIVersion == temporaliov1alpha1.GroupVersion.String() {
			return true
		}
	}
	return false
}

// setOwnerReference sets the OwnerReference on the patch to point to the TemporalWorkerDeployment
func setOwnerReference(
	patch *temporaliov1alpha1.TemporalWorkerDeploymentPatch,
	workerDeployment *temporaliov1alpha1.TemporalWorkerDeployment,
) {
	blockOwnerDeletion := true

	ownerRef := metav1.OwnerReference{
		APIVersion:         temporaliov1alpha1.GroupVersion.String(),
		Kind:               "TemporalWorkerDeployment",
		Name:               workerDeployment.GetName(),
		UID:                workerDeployment.GetUID(),
		BlockOwnerDeletion: &blockOwnerDeletion,
		Controller:         nil,
	}

	patch.SetOwnerReferences(append(patch.GetOwnerReferences(), ownerRef))
}
