// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package controller

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"

	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// PatchApplier contains the logic for applying version-specific overrides
type PatchApplier struct {
	client client.Client
	logger logr.Logger
}

// NewPatchApplier creates a new PatchApplier
func NewPatchApplier(client client.Client, logger logr.Logger) *PatchApplier {
	return &PatchApplier{
		client: client,
		logger: logger,
	}
}

// getPatches retrieves all patches targeting the given TemporalWorkerDeployment
func (pa *PatchApplier) getPatches(
	ctx context.Context,
	workerDeployment *temporaliov1alpha1.TemporalWorkerDeployment,
) ([]temporaliov1alpha1.TemporalWorkerDeploymentPatch, error) {
	patchList := &temporaliov1alpha1.TemporalWorkerDeploymentPatchList{}

	// List all patches in the same namespace targeting this worker deployment
	err := pa.client.List(ctx, patchList, client.MatchingFields{
		patchTargetKey: workerDeployment.Name,
	}, client.InNamespace(workerDeployment.Namespace))
	if err != nil {
		return nil, fmt.Errorf("failed to list patches: %w", err)
	}

	return patchList.Items, nil
}

// UpdatePatchStatuses updates the status of patches based on the current state of versions
func (pa *PatchApplier) UpdatePatchStatuses(
	ctx context.Context,
	workerDeployment *temporaliov1alpha1.TemporalWorkerDeployment,
) error {
	patches, err := pa.getPatches(ctx, workerDeployment)
	if err != nil {
		return fmt.Errorf("failed to get patches: %w", err)
	}

	// Get current version IDs from the worker deployment status
	activeVersions := pa.getActiveVersionIDs(workerDeployment)

	for _, patch := range patches {
		newStatus := pa.determinePatchStatus(patch, workerDeployment, activeVersions)

		if patch.Status.Status != newStatus {
			patch.Status.Status = newStatus
			patch.Status.ObservedGeneration = patch.Generation

			switch newStatus {
			case temporaliov1alpha1.PatchStatusActive:
				now := metav1.Now()
				patch.Status.AppliedAt = &now
				patch.Status.Message = "Patch successfully applied to active version"
			case temporaliov1alpha1.PatchStatusOrphaned:
				patch.Status.Message = "Referenced version no longer exists"
			case temporaliov1alpha1.PatchStatusInvalid:
				patch.Status.Message = "Referenced TemporalWorkerDeployment not found"
			}

			if err := pa.client.Status().Update(ctx, &patch); err != nil {
				pa.logger.Error(err, "Failed to update patch status", "patch", patch.Name)
				continue
			}
		}
	}

	return nil
}

// determinePatchStatus determines the appropriate status for a patch
func (pa *PatchApplier) determinePatchStatus(
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
func (pa *PatchApplier) getActiveVersionIDs(
	workerDeployment *temporaliov1alpha1.TemporalWorkerDeployment,
) map[string]bool {
	versions := make(map[string]bool)

	if workerDeployment.Status.CurrentVersion != nil {
		versions[workerDeployment.Status.CurrentVersion.VersionID] = true
	}

	if workerDeployment.Status.TargetVersion != nil {
		versions[workerDeployment.Status.TargetVersion.VersionID] = true
	}

	if workerDeployment.Status.RampingVersion != nil {
		versions[workerDeployment.Status.RampingVersion.VersionID] = true
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
