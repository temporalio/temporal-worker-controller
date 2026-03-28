// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package planner

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	"github.com/temporalio/temporal-worker-controller/internal/k8s"
	"github.com/temporalio/temporal-worker-controller/internal/temporal"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

// WorkerResourceApply holds a rendered worker resource template to apply via Server-Side Apply.
// If RenderError is non-nil, Resource is nil and the apply must be skipped; the error is
// surfaced in the WRT status and Ready condition just like an SSA apply failure.
type WorkerResourceApply struct {
	Resource     *unstructured.Unstructured
	WRTName      string
	WRTNamespace string
	BuildID      string
	// RenderError is set when rendering spec.template failed. No SSA apply is attempted;
	// the error is recorded in the per-BuildID status entry and reflected in the Ready condition.
	RenderError error
	// RenderedHash is the hash of the rendered Resource object (see k8s.ComputeRenderedObjectHash).
	// Empty string means hashing failed; the apply will proceed unconditionally.
	RenderedHash string
	// LastAppliedHash is the hash recorded in the WRT status from the last successful apply.
	// If RenderedHash == LastAppliedHash (and both are non-empty), the controller skips the
	// SSA apply because the rendered output is identical to what is already on the cluster.
	LastAppliedHash string
}

// Plan holds the actions to execute during reconciliation
type Plan struct {
	// Which actions to take
	DeleteDeployments      []*appsv1.Deployment
	ScaleDeployments       map[*corev1.ObjectReference]uint32
	UpdateDeployments      []*appsv1.Deployment
	ShouldCreateDeployment bool
	VersionConfig          *VersionConfig
	TestWorkflows          []WorkflowConfig

	// ApplyWorkerResources holds resources to apply via SSA, one per (WRT × Build ID) pair.
	ApplyWorkerResources []WorkerResourceApply
	// DeleteWorkerResources lists rendered WRT resource copies to delete explicitly.
	// Populated when a versioned Deployment is being deleted (version sunset), since
	// rendered resources are now owned by the WRT (not the Deployment) and therefore
	// are not GC'd automatically when the Deployment is removed.
	DeleteWorkerResources []WorkerResourceRef
	// EnsureWRTOwnerRefs holds (base, patched) pairs for WRTs that need a
	// controller owner reference added, ready for client.MergeFrom patching.
	// These point from each WRT → the TWD, so the WRT is GC'd when the TWD is
	// deleted. The controller sets this (rather than the webhook) because the owner
	// ref requires the TWD's UID, resolved from spec.temporalWorkerDeploymentRef.
	EnsureWRTOwnerRefs []WRTOwnerRefPatch
}

// WorkerResourceRef identifies a single rendered WRT resource copy to delete.
type WorkerResourceRef struct {
	Namespace  string
	Name       string
	APIVersion string
	Kind       string
}

// WRTOwnerRefPatch holds a WRT pair for a single merge-patch:
// Base is the unmodified object (used as the patch base), Patched has the
// controller owner reference already appended.
type WRTOwnerRefPatch struct {
	Base    *temporaliov1alpha1.WorkerResourceTemplate
	Patched *temporaliov1alpha1.WorkerResourceTemplate
}

// VersionConfig defines version configuration for Temporal
type VersionConfig struct {
	// Token to use for conflict detection
	ConflictToken []byte
	// Build ID for the version
	BuildID string

	// One of RampPercentage OR SetCurrent must be set to a non-zero value.

	// Set this as the build ID for all new executions
	SetCurrent bool
	// Acceptable values [0,100]
	RampPercentage int32

	// ManagerIdentity is the current manager identity of the worker deployment in Temporal.
	// An empty string indicates the controller should claim the identity before applying
	// any routing config changes.
	ManagerIdentity string
}

// WorkflowConfig defines a workflow to be started
type WorkflowConfig struct {
	WorkflowType string
	WorkflowID   string
	BuildID      string
	TaskQueue    string
	GateInput    string
	// IsInputSecret indicates whether the GateInput came from a Secret reference
	// and should be treated as sensitive (not logged)
	IsInputSecret bool
}

// Config holds the configuration for planning
type Config struct {
	// RolloutStrategy to use
	RolloutStrategy temporaliov1alpha1.RolloutStrategy
}

// GeneratePlan creates a plan for updating the worker deployment
func GeneratePlan(
	l logr.Logger,
	k8sState *k8s.DeploymentState,
	status *temporaliov1alpha1.TemporalWorkerDeploymentStatus,
	spec *temporaliov1alpha1.TemporalWorkerDeploymentSpec,
	temporalState *temporal.TemporalWorkerState,
	connection temporaliov1alpha1.TemporalConnectionSpec,
	config *Config,
	workerDeploymentName string,
	maxVersionsIneligibleForDeletion int32,
	gateInput []byte,
	isGateInputSecret bool,
	wrts []temporaliov1alpha1.WorkerResourceTemplate,
	twdName string,
	twdUID types.UID,
) (*Plan, error) {
	plan := &Plan{
		ScaleDeployments: make(map[*corev1.ObjectReference]uint32),
	}

	// If Deployment was not found in temporal, which always happens on the first worker deployment version
	// and sometimes happens transiently thereafter, the versions list will be empty. If the deployment
	// exists and was found, there will always be at least one version in the list.
	foundDeploymentInTemporal := temporalState != nil && len(temporalState.Versions) > 0

	// Add delete/scale operations based on version status
	plan.DeleteDeployments = getDeleteDeployments(k8sState, status, spec, foundDeploymentInTemporal)
	plan.ScaleDeployments = getScaleDeployments(k8sState, status, spec)
	plan.ShouldCreateDeployment = shouldCreateDeployment(status, maxVersionsIneligibleForDeletion)
	plan.UpdateDeployments = getUpdateDeployments(k8sState, status, spec, connection)

	// Determine if we need to start any test workflows
	plan.TestWorkflows = getTestWorkflows(status, config, workerDeploymentName, gateInput, isGateInputSecret)

	// Determine version config changes
	plan.VersionConfig = getVersionConfigDiff(l, status, temporalState, config, workerDeploymentName)

	// TODO(jlegrone): generate warnings/events on the TemporalWorkerDeployment resource when buildIDs are reachable
	//                 but have no corresponding Deployment.

	plan.ApplyWorkerResources = getWorkerResourceApplies(l, wrts, k8sState, spec.WorkerOptions.TemporalNamespace, plan.DeleteDeployments)
	plan.DeleteWorkerResources = getDeleteWorkerResources(wrts, plan.DeleteDeployments)
	plan.EnsureWRTOwnerRefs = getWRTOwnerRefPatches(wrts, twdName, twdUID)

	return plan, nil
}

// getWorkerResourceApplies renders one WorkerResourceApply for each (WRT × active Build ID) pair.
// Pairs that fail to render are included with RenderError set so the failure is surfaced in the
// WRT status and Ready condition; they do not block the rest.
func getWorkerResourceApplies(
	l logr.Logger,
	wrts []temporaliov1alpha1.WorkerResourceTemplate,
	k8sState *k8s.DeploymentState,
	temporalNamespace string,
	deleteDeployments []*appsv1.Deployment,
) []WorkerResourceApply {
	// Build a set of deployment names that are scheduled for deletion so we can
	// skip rendering WRTs for them. Their rendered resources are deleted explicitly
	// by the controller via DeleteWorkerResources (see getDeleteWorkerResources).
	deletingDeployments := make(map[string]struct{}, len(deleteDeployments))
	for _, d := range deleteDeployments {
		deletingDeployments[d.Name] = struct{}{}
	}

	var applies []WorkerResourceApply
	for i := range wrts {
		wrt := &wrts[i]
		if wrt.Spec.Template.Raw == nil {
			l.Info("skipping WorkerResourceTemplate with empty spec.template", "name", wrt.Name)
			continue
		}
		// Build a map of existing status entries for O(1) lookup by BuildID.
		existingStatus := make(map[string]temporaliov1alpha1.WorkerResourceTemplateVersionStatus, len(wrt.Status.Versions))
		for _, v := range wrt.Status.Versions {
			existingStatus[v.BuildID] = v
		}

		for buildID, deployment := range k8sState.Deployments {
			if _, deleting := deletingDeployments[deployment.Name]; deleting {
				continue
			}
			rendered, renderErr := k8s.RenderWorkerResourceTemplate(wrt, deployment, buildID, temporalNamespace)
			if renderErr != nil {
				l.Error(renderErr, "failed to render WorkerResourceTemplate",
					"wrt", wrt.Name,
					"buildID", buildID,
				)
				// Record the failure so execplan surfaces it in the WRT status and
				// Ready condition instead of silently dropping it.
				applies = append(applies, WorkerResourceApply{
					RenderError:  renderErr,
					WRTName:      wrt.Name,
					WRTNamespace: wrt.Namespace,
					BuildID:      buildID,
				})
				continue
			}

			renderedHash := k8s.ComputeRenderedObjectHash(rendered)

			// Look up the hash recorded by the last successful apply for this BuildID.
			// A non-empty LastAppliedHash implies the previous apply succeeded;
			// on error the controller records an empty hash so the next cycle retries.
			var lastAppliedHash string
			if prev, ok := existingStatus[buildID]; ok {
				lastAppliedHash = prev.LastAppliedHash
			}

			applies = append(applies, WorkerResourceApply{
				Resource:        rendered,
				WRTName:         wrt.Name,
				WRTNamespace:    wrt.Namespace,
				BuildID:         buildID,
				RenderedHash:    renderedHash,
				LastAppliedHash: lastAppliedHash,
			})
		}
	}
	return applies
}

// getWRTOwnerRefPatches returns (base, patched) pairs for each WRT that does not
// yet have a controller owner reference pointing to the given TWD. The patched copy
// has the owner reference appended so that executePlan can apply a merge-patch to
// add it without a full Update.
func getWRTOwnerRefPatches(
	wrts []temporaliov1alpha1.WorkerResourceTemplate,
	twdName string,
	twdUID types.UID,
) []WRTOwnerRefPatch {
	isController := true
	blockOwnerDeletion := true
	ownerRef := metav1.OwnerReference{
		APIVersion:         temporaliov1alpha1.GroupVersion.String(),
		Kind:               "TemporalWorkerDeployment",
		Name:               twdName,
		UID:                twdUID,
		Controller:         &isController,
		BlockOwnerDeletion: &blockOwnerDeletion,
	}
	var patches []WRTOwnerRefPatch
	for i := range wrts {
		wrt := &wrts[i]
		// Skip if this TWD is already the controller owner.
		alreadyOwned := false
		for _, ref := range wrt.OwnerReferences {
			if ref.Controller != nil && *ref.Controller && ref.UID == twdUID {
				alreadyOwned = true
				break
			}
		}
		if alreadyOwned {
			continue
		}
		patched := wrt.DeepCopy()
		patched.OwnerReferences = append(patched.OwnerReferences, ownerRef)
		patches = append(patches, WRTOwnerRefPatch{Base: wrt, Patched: patched})
	}
	return patches
}

// getDeleteWorkerResources returns the rendered WRT resource copies that should be explicitly
// deleted by the controller. Called when versioned Deployments are being deleted (version sunset):
// since rendered resources are owned by the WRT (not the Deployment), they are not GC'd
// automatically and must be deleted by the controller.
func getDeleteWorkerResources(
	wrts []temporaliov1alpha1.WorkerResourceTemplate,
	deleteDeployments []*appsv1.Deployment,
) []WorkerResourceRef {
	if len(deleteDeployments) == 0 || len(wrts) == 0 {
		return nil
	}

	// Collect the build IDs that are being deleted.
	var deletingBuildIDs []string
	for _, d := range deleteDeployments {
		if bid, ok := d.Labels[k8s.BuildIDLabel]; ok && bid != "" {
			deletingBuildIDs = append(deletingBuildIDs, bid)
		}
	}

	var refs []WorkerResourceRef
	for i := range wrts {
		wrt := &wrts[i]

		// Parse apiVersion and kind from the WRT's spec.template.
		var templateMeta struct {
			APIVersion string `json:"apiVersion"`
			Kind       string `json:"kind"`
		}
		if err := json.Unmarshal(wrt.Spec.Template.Raw, &templateMeta); err != nil {
			continue // skip if template is unparseable
		}
		if templateMeta.APIVersion == "" || templateMeta.Kind == "" {
			continue
		}

		for _, buildID := range deletingBuildIDs {
			// Compute the resource name deterministically — no status lookup needed.
			// This ensures cleanup even if the WRT was never successfully applied for this
			// buildID (e.g. the version was already eligible for deletion when the WRT was
			// first created). The Delete call is a no-op if the resource doesn't exist.
			resourceName := k8s.ComputeWorkerResourceTemplateName(
				wrt.Spec.TemporalWorkerDeploymentRef.Name, wrt.Name, buildID,
			)
			refs = append(refs, WorkerResourceRef{
				Namespace:  wrt.Namespace,
				Name:       resourceName,
				APIVersion: templateMeta.APIVersion,
				Kind:       templateMeta.Kind,
			})
		}
	}
	return refs
}

// checkAndUpdateDeploymentConnectionSpec determines whether the Deployment for the given buildID is
// out-of-date with respect to the provided TemporalConnectionSpec. If an update is required, it mutates
// the existing Deployment in-place and returns a pointer to that Deployment. If no update is needed or
// the Deployment does not exist, it returns nil.
func checkAndUpdateDeploymentConnectionSpec(
	buildID string,
	k8sState *k8s.DeploymentState,
	connection temporaliov1alpha1.TemporalConnectionSpec,
) *appsv1.Deployment {
	existingDeployment, exists := k8sState.Deployments[buildID]
	if !exists {
		return nil
	}

	// If the connection spec hash has changed, update the deployment
	currentHash := k8s.ComputeConnectionSpecHash(connection)
	if currentHash != existingDeployment.Spec.Template.Annotations[k8s.ConnectionSpecHashAnnotation] {

		// Update the deployment in-place with new connection info
		updateDeploymentWithConnection(existingDeployment, connection)
		return existingDeployment // Return the modified deployment
	}

	return nil
}

// updateDeploymentWithConnection updates an existing deployment with new TemporalConnectionSpec
func updateDeploymentWithConnection(deployment *appsv1.Deployment, connection temporaliov1alpha1.TemporalConnectionSpec) {
	// Update the connection spec hash annotation
	deployment.Spec.Template.Annotations[k8s.ConnectionSpecHashAnnotation] = k8s.ComputeConnectionSpecHash(connection)

	// Update secret volume if mTLS is enabled
	if connection.MutualTLSSecretRef != nil {
		for i, volume := range deployment.Spec.Template.Spec.Volumes {
			if volume.Name == "temporal-tls" && volume.Secret != nil {
				deployment.Spec.Template.Spec.Volumes[i].Secret.SecretName = connection.MutualTLSSecretRef.Name
				break
			}
		}
	}

	// Update any environment variables that reference the connection
	for i, container := range deployment.Spec.Template.Spec.Containers {
		for j, env := range container.Env {
			if env.Name == "TEMPORAL_ADDRESS" {
				deployment.Spec.Template.Spec.Containers[i].Env[j].Value = connection.HostPort
			}
		}
	}
}

// checkAndUpdateDeploymentPodTemplateSpec determines whether the Deployment for the given buildID is
// out-of-date with respect to the user-provided pod template spec. This enables rolling updates when
// the build ID is stable (e.g., using spec.workerOptions.buildID) but the pod spec has changed.
// If an update is required, it rebuilds the deployment spec and returns a pointer to that Deployment.
// If no update is needed or the Deployment does not exist, it returns nil.
func checkAndUpdateDeploymentPodTemplateSpec(
	buildID string,
	k8sState *k8s.DeploymentState,
	spec *temporaliov1alpha1.TemporalWorkerDeploymentSpec,
	connection temporaliov1alpha1.TemporalConnectionSpec,
) *appsv1.Deployment {
	existingDeployment, exists := k8sState.Deployments[buildID]
	if !exists {
		return nil
	}

	// Only check for drift when UnsafeCustomBuildID is explicitly set by the user.
	// If buildID is auto-generated, any spec change would generate a new buildID anyway.
	if spec.WorkerOptions.UnsafeCustomBuildID == "" {
		return nil
	}

	// Get the stored hash from the existing deployment's pod template annotations
	storedHash := ""
	if existingDeployment.Spec.Template.Annotations != nil {
		storedHash = existingDeployment.Spec.Template.Annotations[k8s.PodTemplateSpecHashAnnotation]
	}

	// Backwards compatibility: if no hash annotation exists (legacy deployment),
	// don't trigger an update - the hash will be added on the next spec change
	if storedHash == "" {
		return nil
	}

	// Compute the hash of the current user-provided pod template spec
	currentHash := k8s.ComputePodTemplateSpecHash(spec.Template)

	// If hashes match, no drift detected
	if storedHash == currentHash {
		return nil
	}

	// Pod template has changed - rebuild the pod spec from the TWD spec
	// This applies all controller modifications (env vars, TLS mounts, etc.)
	updateDeploymentWithPodTemplateSpec(existingDeployment, spec, connection)

	return existingDeployment
}

// updateDeploymentWithPodTemplateSpec updates an existing deployment with a new pod template spec
// from the TWD spec. This applies all the controller modifications that NewDeploymentWithOwnerRef does.
func updateDeploymentWithPodTemplateSpec(
	deployment *appsv1.Deployment,
	spec *temporaliov1alpha1.TemporalWorkerDeploymentSpec,
	connection temporaliov1alpha1.TemporalConnectionSpec,
) {
	// Deep copy the user-provided pod spec to avoid mutating the original
	podSpec := spec.Template.Spec.DeepCopy()

	// Extract the build ID from the deployment's labels (with nil safety)
	var buildID string
	if deployment.Labels != nil {
		buildID = deployment.Labels[k8s.BuildIDLabel]
	}

	// Extract the worker deployment name from existing env vars
	var workerDeploymentName string
	for _, container := range deployment.Spec.Template.Spec.Containers {
		for _, env := range container.Env {
			if env.Name == "TEMPORAL_DEPLOYMENT_NAME" {
				workerDeploymentName = env.Value
				break
			}
		}
		if workerDeploymentName != "" {
			break
		}
	}

	// Apply controller-managed environment variables and volume mounts
	// Uses the same shared helper as NewDeploymentWithOwnerRef
	k8s.ApplyControllerPodSpecModifications(podSpec, connection, spec.WorkerOptions.TemporalNamespace, workerDeploymentName, buildID)

	// Build new pod annotations
	podAnnotations := make(map[string]string)
	for k, v := range spec.Template.Annotations {
		podAnnotations[k] = v
	}
	podAnnotations[k8s.ConnectionSpecHashAnnotation] = k8s.ComputeConnectionSpecHash(connection)
	// Store the new pod template spec hash
	podAnnotations[k8s.PodTemplateSpecHashAnnotation] = k8s.ComputePodTemplateSpecHash(spec.Template)

	// Preserve existing pod labels and add/update required labels
	podLabels := make(map[string]string)
	for k, v := range spec.Template.Labels {
		podLabels[k] = v
	}
	// Copy selector labels from existing deployment
	for k, v := range deployment.Spec.Selector.MatchLabels {
		podLabels[k] = v
	}

	// Update the deployment's pod template
	deployment.Spec.Template.ObjectMeta.Labels = podLabels
	deployment.Spec.Template.ObjectMeta.Annotations = podAnnotations
	deployment.Spec.Template.Spec = *podSpec

	// Only set replicas when the controller is managing them (spec.Replicas non-nil).
	// When nil, an external autoscaler owns replicas; preserving the current value
	// avoids competing with it on every Update.
	if spec.Replicas != nil {
		deployment.Spec.Replicas = spec.Replicas
	}
	deployment.Spec.MinReadySeconds = spec.MinReadySeconds
}

func getUpdateDeployments(
	k8sState *k8s.DeploymentState,
	status *temporaliov1alpha1.TemporalWorkerDeploymentStatus,
	spec *temporaliov1alpha1.TemporalWorkerDeploymentSpec,
	connection temporaliov1alpha1.TemporalConnectionSpec,
) []*appsv1.Deployment {
	var updateDeployments []*appsv1.Deployment
	// Track which deployments we've already added to avoid duplicates
	updatedBuildIDs := make(map[string]bool)

	// Check target version deployment for pod template spec drift
	// This enables rolling updates when the build ID is stable but spec changed
	if status.TargetVersion.BuildID != "" {
		if deployment := checkAndUpdateDeploymentPodTemplateSpec(status.TargetVersion.BuildID, k8sState, spec, connection); deployment != nil {
			updateDeployments = append(updateDeployments, deployment)
			updatedBuildIDs[status.TargetVersion.BuildID] = true
		}
	}

	// Check target version deployment if it has an expired connection spec hash
	// (only if not already updated by pod template check)
	if status.TargetVersion.BuildID != "" && !updatedBuildIDs[status.TargetVersion.BuildID] {
		if deployment := checkAndUpdateDeploymentConnectionSpec(status.TargetVersion.BuildID, k8sState, connection); deployment != nil {
			updateDeployments = append(updateDeployments, deployment)
			updatedBuildIDs[status.TargetVersion.BuildID] = true
		}
	}

	// Check current version deployment if it has an expired connection spec hash
	if status.CurrentVersion != nil && status.CurrentVersion.BuildID != "" && !updatedBuildIDs[status.CurrentVersion.BuildID] {
		if deployment := checkAndUpdateDeploymentConnectionSpec(status.CurrentVersion.BuildID, k8sState, connection); deployment != nil {
			updateDeployments = append(updateDeployments, deployment)
			updatedBuildIDs[status.CurrentVersion.BuildID] = true
		}
	}

	// Check deprecated versions for expired connection spec hashes
	for _, version := range status.DeprecatedVersions {
		if !updatedBuildIDs[version.BuildID] {
			if deployment := checkAndUpdateDeploymentConnectionSpec(version.BuildID, k8sState, connection); deployment != nil {
				updateDeployments = append(updateDeployments, deployment)
				updatedBuildIDs[version.BuildID] = true
			}
		}
	}

	return updateDeployments
}

// getDeleteDeployments determines which deployments should be deleted
func getDeleteDeployments(
	k8sState *k8s.DeploymentState,
	status *temporaliov1alpha1.TemporalWorkerDeploymentStatus,
	spec *temporaliov1alpha1.TemporalWorkerDeploymentSpec,
	foundDeploymentInTemporal bool,
) []*appsv1.Deployment {
	var deleteDeployments []*appsv1.Deployment

	for _, version := range status.DeprecatedVersions {
		if version.Deployment == nil {
			continue
		}

		// Look up the deployment using buildID
		d, exists := k8sState.Deployments[version.BuildID]
		if !exists {
			continue
		}

		switch version.Status {
		case temporaliov1alpha1.VersionStatusDrained:
			// Deleting a deployment is only possible when:
			// 1. The deployment has been drained for deleteDelay + scaledownDelay.
			// 2. The deployment is scaled to 0 replicas.
			if version.DrainedSince != nil &&
				(time.Since(version.DrainedSince.Time) > spec.SunsetStrategy.DeleteDelay.Duration+spec.SunsetStrategy.ScaledownDelay.Duration) &&
				d.Spec.Replicas != nil && *d.Spec.Replicas == 0 {
				deleteDeployments = append(deleteDeployments, d)
			}
		case temporaliov1alpha1.VersionStatusNotRegistered:
			// Only delete Deployments of NotRegistered versions if temporalState was not empty
			if foundDeploymentInTemporal &&
				// NotRegistered versions are versions that the server doesn't know about.
				// Only delete if it's not the target version.
				status.TargetVersion.BuildID != version.BuildID {
				deleteDeployments = append(deleteDeployments, d)
			}
		}
	}

	return deleteDeployments
}

// getScaleDeployments determines which deployments should be explicitly scaled and to what size.
// It only runs when spec.Replicas is set (controller-managed mode). Drained versions and inactive
// versions that are not the rollout target are always scaled to zero during sunset.
func getScaleDeployments(
	k8sState *k8s.DeploymentState,
	status *temporaliov1alpha1.TemporalWorkerDeploymentStatus,
	spec *temporaliov1alpha1.TemporalWorkerDeploymentSpec,
) map[*corev1.ObjectReference]uint32 {
	scaleDeployments := make(map[*corev1.ObjectReference]uint32)

	// Scale the current version if needed
	if status.CurrentVersion != nil && status.CurrentVersion.Deployment != nil {
		// If spec.Replicas is non-nil, the controller is managing replicas instead of a scaler resource.
		// Scale the Current Version per the TemporalWorkerDeploymentSpec.Replicas value.
		if spec.Replicas != nil {
			replicas := *spec.Replicas
			ref := status.CurrentVersion.Deployment
			if d, exists := k8sState.Deployments[status.CurrentVersion.BuildID]; exists {
				if d.Spec.Replicas != nil && *d.Spec.Replicas != replicas {
					scaleDeployments[ref] = uint32(replicas)
				}
			}
		}
	}

	// Scale the target version if it exists, and isn't current
	if (status.CurrentVersion == nil || status.CurrentVersion.BuildID != status.TargetVersion.BuildID) &&
		status.TargetVersion.Deployment != nil {
		if d, exists := k8sState.Deployments[status.TargetVersion.BuildID]; exists {

			// If the Target Version is an already-existing Deployment that was scaled to zero by the controller
			// due to Sunset Policy, and the TWD has nil replicas because a scaler is managing the replicas, then
			// no one will scale the Target Version back up, so we need to scale it back to 1 replica, which is what
			// would happen if the Deployment was being created from scratch with nil replicas.
			if spec.Replicas != nil || (spec.Replicas == nil && d.Spec.Replicas != nil && *d.Spec.Replicas == 0) {
				replicas := int32(1) // just scale up to 1 if we are in the spec.Replicas == nil && d.Spec.Replicas == 0 case.
				if spec.Replicas != nil {
					replicas = *spec.Replicas
				}
				if d.Spec.Replicas == nil || *d.Spec.Replicas != replicas {
					scaleDeployments[status.TargetVersion.Deployment] = uint32(replicas)
				}
			}
		}
	}

	// Scale other versions based on status
	for _, version := range status.DeprecatedVersions {
		if version.Deployment == nil {
			continue
		}

		d, exists := k8sState.Deployments[version.BuildID]
		if !exists {
			continue
		}

		switch version.Status {
		case temporaliov1alpha1.VersionStatusInactive:
			// Scale down inactive versions that are not the target
			if status.TargetVersion.BuildID == version.BuildID {
				// TODO(carlydf): I'm not convinced this case actually happens, because Target and Current Versions are excluded from DeprecatedVersions. Leaving it unchanged since I don't want to add to this PRs scope.
				if spec.Replicas != nil {
					replicas := *spec.Replicas
					if d.Spec.Replicas != nil && *d.Spec.Replicas != replicas {
						scaleDeployments[version.Deployment] = uint32(replicas)
					}
				}
			} else if !(d.Spec.Replicas != nil && *d.Spec.Replicas == 0) { // these are non-target inactive versions with nil replicas or >0 replicas
				scaleDeployments[version.Deployment] = 0
			}
		case temporaliov1alpha1.VersionStatusRamping, temporaliov1alpha1.VersionStatusCurrent:
			// TODO(carlydf): Also not convinced this case actually happens, because Target and Current Versions are excluded from DeprecatedVersions. Leaving it unchanged since I don't want to add to this PRs scope.
			// Scale up these deployments
			if spec.Replicas != nil {
				replicas := *spec.Replicas
				if d.Spec.Replicas != nil && *d.Spec.Replicas != replicas {
					scaleDeployments[version.Deployment] = uint32(replicas)
				}
			}
		case temporaliov1alpha1.VersionStatusDrained:
			if version.DrainedSince != nil && time.Since(version.DrainedSince.Time) > spec.SunsetStrategy.ScaledownDelay.Duration {
				// Scale down drained deployments after delay
				if !(d.Spec.Replicas != nil && *d.Spec.Replicas == 0) { // these are non-target drained versions with nil replicas or >0 replicas
					scaleDeployments[version.Deployment] = 0
				}
			}
		}
	}

	return scaleDeployments
}

// shouldCreateDeployment determines if a new deployment needs to be created
func shouldCreateDeployment(
	status *temporaliov1alpha1.TemporalWorkerDeploymentStatus,
	maxVersionsIneligibleForDeletion int32,
) bool {
	// Check if target version already has a deployment
	if status.TargetVersion.Deployment != nil {
		return false
	}

	versionCountIneligibleForDeletion := int32(0)

	for _, v := range status.DeprecatedVersions {
		if !v.EligibleForDeletion {
			versionCountIneligibleForDeletion++
		}
	}

	if versionCountIneligibleForDeletion >= maxVersionsIneligibleForDeletion {
		return false
	}

	return true
}

// getTestWorkflows determines which test workflows should be started
func getTestWorkflows(
	status *temporaliov1alpha1.TemporalWorkerDeploymentStatus,
	config *Config,
	workerDeploymentName string,
	gateInput []byte,
	isGateInputSecret bool,
) []WorkflowConfig {
	var testWorkflows []WorkflowConfig

	// Skip if there's no gate workflow defined, if the target version is already the current, or if the target
	// version is not yet registered in temporal
	if config.RolloutStrategy.Gate == nil ||
		(status.CurrentVersion != nil && status.CurrentVersion.BuildID == status.TargetVersion.BuildID) ||
		status.TargetVersion.Status == temporaliov1alpha1.VersionStatusNotRegistered {
		return nil
	}

	targetVersion := status.TargetVersion

	// Create a map of task queues that already have running test workflows
	taskQueuesWithWorkflows := make(map[string]struct{})
	for _, wf := range targetVersion.TestWorkflows {
		taskQueuesWithWorkflows[wf.TaskQueue] = struct{}{}
	}

	// For each task queue without a running test workflow, create a config
	for _, tq := range targetVersion.TaskQueues {
		if _, ok := taskQueuesWithWorkflows[tq.Name]; !ok {
			testWorkflows = append(testWorkflows, WorkflowConfig{
				WorkflowType:  config.RolloutStrategy.Gate.WorkflowType,
				WorkflowID:    temporal.GetTestWorkflowID(workerDeploymentName, targetVersion.BuildID, tq.Name),
				BuildID:       targetVersion.BuildID,
				TaskQueue:     tq.Name,
				GateInput:     string(gateInput),
				IsInputSecret: isGateInputSecret,
			})
		}
	}

	return testWorkflows
}

// getVersionConfigDiff determines the version configuration based on the rollout strategy
func getVersionConfigDiff(
	l logr.Logger,
	status *temporaliov1alpha1.TemporalWorkerDeploymentStatus,
	temporalState *temporal.TemporalWorkerState,
	config *Config,
	workerDeploymentName string,
) *VersionConfig {
	strategy := config.RolloutStrategy
	conflictToken := status.VersionConflictToken

	if strategy.Strategy == temporaliov1alpha1.UpdateManual {
		return nil
	}

	// Do nothing if target version's deployment is not healthy yet, or if the version is not yet registered in temporal
	if status.TargetVersion.HealthySince == nil ||
		status.TargetVersion.Status == temporaliov1alpha1.VersionStatusNotRegistered {
		return nil
	}

	// Do nothing if the test workflows have not completed successfully
	if strategy.Gate != nil {
		if len(status.TargetVersion.TaskQueues) == 0 {
			return nil
		}
		if len(status.TargetVersion.TestWorkflows) < len(status.TargetVersion.TaskQueues) {
			return nil
		}
		for _, wf := range status.TargetVersion.TestWorkflows {
			if wf.Status != temporaliov1alpha1.WorkflowExecutionStatusCompleted {
				return nil
			}
		}
	}

	managerIdentity := ""
	if temporalState != nil {
		managerIdentity = temporalState.ManagerIdentity
	}
	vcfg := &VersionConfig{
		ConflictToken:   conflictToken,
		BuildID:         status.TargetVersion.BuildID,
		ManagerIdentity: managerIdentity,
	}

	// If there is no current version and presence of unversioned pollers is not confirmed for all
	// target version task queues, set the target version as the current version right away.
	if status.CurrentVersion == nil &&
		status.TargetVersion.Status == temporaliov1alpha1.VersionStatusInactive &&
		!temporalState.Versions[status.TargetVersion.BuildID].AllTaskQueuesHaveUnversionedPoller {
		vcfg.SetCurrent = true
		return vcfg
	}

	// If the current version is the target version
	if status.CurrentVersion != nil && status.CurrentVersion.BuildID == status.TargetVersion.BuildID {
		// Reset ramp if needed, this would happen if a ramp has been rolled back before completing
		if temporalState.RampingBuildID != "" {
			vcfg.BuildID = ""
			vcfg.RampPercentage = 0
			return vcfg
		}
		// Otherwise, do nothing
		return nil
	}

	switch strategy.Strategy {
	case temporaliov1alpha1.UpdateManual:
		return nil
	case temporaliov1alpha1.UpdateAllAtOnce:
		// Set new current version immediately
		vcfg.SetCurrent = true
		return vcfg
	case temporaliov1alpha1.UpdateProgressive:
		return handleProgressiveRollout(strategy.Steps, time.Now(), status.TargetVersion.RampLastModifiedAt, status.TargetVersion.RampPercentage, vcfg)
	}

	return nil
}

// handleProgressiveRollout handles the progressive rollout strategy logic
func handleProgressiveRollout(
	steps []temporaliov1alpha1.RolloutStep,
	currentTime time.Time, // avoid calling time.Now() inside function to make it easier to test
	rampLastModifiedAt *metav1.Time,
	targetRampPercentage *float32,
	vcfg *VersionConfig,
) *VersionConfig {
	// Protect against modifying the current version right away if there are no steps.
	//
	// The validating admission webhook _should_ prevent creating rollouts with 0 steps,
	// but just in case validation is skipped we should go with the more conservative
	// behavior of not updating the current version from the controller.
	if len(steps) == 0 {
		return nil
	}

	// Get the currently active step
	i := getCurrentStepIndex(steps, targetRampPercentage)
	currentStep := steps[i]

	// If this is the first step and there is no ramp percentage set, set the ramp percentage
	// to the step's ramp percentage.
	if targetRampPercentage == nil {
		vcfg.RampPercentage = int32(currentStep.RampPercentage)
		return vcfg
	}

	// If the target ramp percentage doesn't match the current step's defined ramp, the ramp
	// is reset immediately. This might be considered overly conservative, but it guarantees that
	// rollouts resume from the earliest possible step, and that at least the last step is always
	// respected (both % and duration).
	if *targetRampPercentage != float32(currentStep.RampPercentage) {
		vcfg.RampPercentage = int32(currentStep.RampPercentage)
		return vcfg
	}

	// Move to the next step if it has been long enough since the last update
	if rampLastModifiedAt != nil {
		if rampLastModifiedAt.Add(currentStep.PauseDuration.Duration).Before(currentTime) {
			if i < len(steps)-1 {
				vcfg.RampPercentage = int32(steps[i+1].RampPercentage)
				return vcfg
			} else {
				vcfg.SetCurrent = true
				return vcfg
			}
		}
	}

	// In all other cases, do nothing
	return nil
}

func getCurrentStepIndex(steps []temporaliov1alpha1.RolloutStep, targetRampPercentage *float32) int {
	if targetRampPercentage == nil {
		return 0
	}

	var result int
	for i, s := range steps {
		// Break if ramp percentage is greater than current (use last index)
		if float32(s.RampPercentage) > *targetRampPercentage {
			break
		}
		result = i
	}

	return result
}

// validateGateInputConfig validates that gate input is configured correctly
func validateGateInputConfig(gate *temporaliov1alpha1.GateWorkflowConfig) error {
	if gate == nil {
		return nil
	}
	// If both are set, return error (webhook should prevent this, but double-check)
	if gate.Input != nil && gate.InputFrom != nil {
		return errors.New("both spec.rollout.gate.input and spec.rollout.gate.inputFrom are set")
	}
	if gate.InputFrom == nil {
		return nil
	}
	// Exactly one of ConfigMapKeyRef or SecretKeyRef should be set
	cmSet := gate.InputFrom.ConfigMapKeyRef != nil
	secSet := gate.InputFrom.SecretKeyRef != nil
	if (cmSet && secSet) || (!cmSet && !secSet) {
		return errors.New("spec.rollout.gate.inputFrom must set exactly one of configMapKeyRef or secretKeyRef")
	}
	return nil
}

// ResolveGateInput resolves the gate input from inline JSON or from a referenced ConfigMap/Secret
// Returns the input bytes and a boolean indicating whether the input came from a Secret
func ResolveGateInput(gate *temporaliov1alpha1.GateWorkflowConfig, namespace string, configMapData map[string]string, configMapBinaryData map[string][]byte, secretData map[string][]byte) ([]byte, bool, error) {
	if gate == nil {
		return nil, false, nil
	}
	if err := validateGateInputConfig(gate); err != nil {
		return nil, false, err
	}
	if gate.Input != nil {
		return gate.Input.Raw, false, nil
	}
	if gate.InputFrom == nil {
		return nil, false, nil
	}
	if cmRef := gate.InputFrom.ConfigMapKeyRef; cmRef != nil {
		if configMapData != nil {
			if val, ok := configMapData[cmRef.Key]; ok {
				return []byte(val), false, nil
			}
		}
		if configMapBinaryData != nil {
			if bval, ok := configMapBinaryData[cmRef.Key]; ok {
				return bval, false, nil
			}
		}
		return nil, false, fmt.Errorf("key %q not found in ConfigMap %s/%s", cmRef.Key, namespace, cmRef.Name)
	}
	if secRef := gate.InputFrom.SecretKeyRef; secRef != nil {
		if secretData != nil {
			if bval, ok := secretData[secRef.Key]; ok {
				return bval, true, nil // true indicates this came from a Secret
			}
		}
		return nil, false, fmt.Errorf("key %q not found in Secret %s/%s", secRef.Key, namespace, secRef.Name)
	}
	return nil, false, nil
}
