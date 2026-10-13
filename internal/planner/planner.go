// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package planner

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/go-logr/logr"
	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	"github.com/temporalio/temporal-worker-controller/internal/defaults"
	"github.com/temporalio/temporal-worker-controller/internal/k8s"
	"github.com/temporalio/temporal-worker-controller/internal/temporal"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
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
	DeleteDeployments []*appsv1.Deployment
	ScaleDeployments  map[*corev1.ObjectReference]uint32
	UpdateDeployments []*appsv1.Deployment
	VersionConfig     *VersionConfig
	TestWorkflows     []WorkflowConfig

	// CreateDeploymentWorkerGroups lists the groups that need a Deployment for the target version.
	CreateDeploymentWorkerGroups []string
	// DeleteWorkerGroupDeployments are target version Deployments of groups no longer in the spec.
	// Unlike DeleteDeployments, deleting them never deletes the version in Temporal.
	DeleteWorkerGroupDeployments []*appsv1.Deployment
	// BlockedReason explains a spec change the controller refuses to apply. The rest of
	// the plan still runs.
	BlockedReason string

	// ApplyWorkerResources holds resources to apply via SSA, one per (WRT × Build ID) pair.
	ApplyWorkerResources []WorkerResourceApply
	// DeleteWorkerResources lists rendered WRT resource copies to delete explicitly.
	// Populated when a versioned Deployment is being deleted (version sunset) and for
	// WRT status entries whose build ID no longer has a Deployment (retry of a delete
	// that was not confirmed). Rendered resources are owned by the WRT (not the
	// Deployment) and therefore are not GC'd automatically when the Deployment is
	// removed. See getDeleteWorkerResources.
	DeleteWorkerResources []WorkerResourceRef
	// EnsureWRTOwnerRefs holds (base, patched) pairs for WRTs that need a
	// controller owner reference added, ready for client.MergeFrom patching.
	// These point from each WRT → the TWD, so the WRT is GC'd when the TWD is
	// deleted. The controller sets this (rather than the webhook) because the owner
	// ref requires the TWD's UID, resolved from spec.temporalWorkerDeploymentRef.
	EnsureWRTOwnerRefs []WRTOwnerRefPatch
	// WRTsWithMissingWorkerGroup names the WRTs whose group no version has and the spec does
	// not declare.
	WRTsWithMissingWorkerGroup []string
	// WRTsWithStaleWorkerGroupNotFound names the WRTs still marked WorkerGroupNotFound whose group is
	// known again.
	WRTsWithStaleWorkerGroupNotFound []string
}

// WorkerResourceRef identifies a single rendered WRT resource copy to delete.
// WRTName and BuildID identify the WorkerResourceTemplate status entry that tracks
// the resource, so the controller can prune the entry once the delete succeeds.
// (Namespace is shared: rendered resources live in the WRT's namespace.)
type WorkerResourceRef struct {
	Namespace  string
	Name       string
	APIVersion string
	Kind       string
	WRTName    string
	BuildID    string
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
	// GateEncoding is the payload encoding to declare for GateInput when starting the
	// workflow. Empty means the SDK will use json/plain encoding.
	GateEncoding string
	// GateMessageType is the fully-qualified protobuf message name to record alongside
	// GateInput. Empty means no message type is declared.
	GateMessageType string
}

// Config holds the configuration for planning
type Config struct {
	// RolloutStrategy to use
	RolloutStrategy temporaliov1alpha1.RolloutStrategy
	// WRTHPAMatchLabelsStripTemporalPrefix removes the "temporal_" prefix from
	// controller-managed external metric matchLabels.
	WRTHPAMatchLabelsStripTemporalPrefix bool
}

// GeneratePlan creates a plan for updating the worker deployment
func GeneratePlan(
	l logr.Logger,
	k8sState *k8s.DeploymentState,
	status *temporaliov1alpha1.WorkerDeploymentStatus,
	spec *temporaliov1alpha1.WorkerDeploymentSpec,
	temporalState *temporal.TemporalWorkerState,
	connection temporaliov1alpha1.ConnectionSpec,
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
	plan.ScaleDeployments = getScaleDeployments(l, k8sState, status, spec)
	plan.CreateDeploymentWorkerGroups, plan.BlockedReason =
		getCreateDeploymentWorkerGroups(k8sState, status, spec, maxVersionsIneligibleForDeletion)
	plan.DeleteWorkerGroupDeployments = getDeleteWorkerGroupDeployments(k8sState, status, spec)
	plan.UpdateDeployments = getUpdateDeployments(k8sState, status, spec, connection)

	// Determine if we need to start any test workflows
	plan.TestWorkflows = getTestWorkflows(status, config, workerDeploymentName, gateInput, isGateInputSecret)

	// Determine version config changes
	plan.VersionConfig = getVersionConfigDiff(l, status, temporalState, config)

	// TODO(jlegrone): generate warnings/events on the WorkerDeployment resource when buildIDs are reachable
	//                 but have no corresponding Deployment.

	// Determine build IDs we're holding at zero for sunset. Their autoscalers must
	// not exist while that's true
	sunsetBuildIDs := getSunsetScaleDownBuildIDs(status, spec)

	deletingDeployments := slices.Concat(plan.DeleteDeployments, plan.DeleteWorkerGroupDeployments)
	plan.ApplyWorkerResources = getWorkerResourceApplies(
		l,
		wrts,
		k8sState,
		spec.WorkerOptions.TemporalNamespace,
		deletingDeployments,
		sunsetBuildIDs,
		config.WRTHPAMatchLabelsStripTemporalPrefix,
	)
	plan.DeleteWorkerResources = getDeleteWorkerResources(wrts, deletingDeployments, k8sState, sunsetBuildIDs)
	plan.EnsureWRTOwnerRefs = getWRTOwnerRefPatches(wrts, twdName, twdUID)
	plan.WRTsWithMissingWorkerGroup, plan.WRTsWithStaleWorkerGroupNotFound = getWRTGroupProblems(wrts, k8sState, spec)

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
	sunsetBuildIDs map[string]struct{},
	stripTemporalMetricLabelPrefix bool,
) []WorkerResourceApply {
	// Build a set of deployment names that are scheduled for deletion so we can
	// skip rendering WRTs for them. Their rendered resources are deleted explicitly
	// by the controller via DeleteWorkerResources (see getDeleteWorkerResources).
	deletingDeployments := make(map[string]struct{}, len(deleteDeployments))
	for _, d := range deleteDeployments {
		deletingDeployments[d.Name] = struct{}{}
	}

	buildIDs := k8sState.BuildIDs()
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

		hasScaleTarget := k8s.HasScaleTarget(wrt.Spec.Template.Raw)
		for _, buildID := range buildIDs {
			deployment, ok := k8sState.VersionDeployments(buildID)[wrt.Spec.EffectiveWorkerGroup()]
			if !ok {
				continue
			}
			if _, deleting := deletingDeployments[deployment.Name]; deleting {
				continue
			}
			if hasScaleTarget {
				// The controller is holding this version's replicas at zero, so
				// getDeleteWorkerResources removes its autoscaler. We don't want
				// to render this autoscaler again
				if _, sunsetting := sunsetBuildIDs[buildID]; sunsetting {
					continue
				}
			}
			rendered, renderErr := k8s.RenderWorkerResourceTemplate(
				wrt,
				deployment,
				buildID,
				temporalNamespace,
				stripTemporalMetricLabelPrefix,
			)
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
		Kind:               "WorkerDeployment",
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
// deleted by the controller, along with the (WRT, build ID) identity the controller needs to
// prune the matching status entry once a delete is confirmed. Since rendered resources are
// owned by the WRT (not the Deployment), they are not GC'd automatically and must be deleted
// by the controller. Deletion candidates come from two sources:
//
//  1. Versioned Deployments being deleted this cycle (version sunset).
//  2. WRT status entries whose build ID no longer has a versioned Deployment (orphaned
//     entries). Entries are pruned from WRT status only after a confirmed delete, so an
//     entry that outlives its Deployment means the rendered resource's deletion has not
//     been confirmed yet — e.g. the delete failed after the Deployment was already gone.
//     Re-deriving the delete from the entry retries it until it succeeds, and clears
//     stale entries whose LastAppliedHash would otherwise suppress the re-apply if the
//     same build ID were redeployed later (rollback).
func getDeleteWorkerResources(
	wrts []temporaliov1alpha1.WorkerResourceTemplate,
	deleteDeployments []*appsv1.Deployment,
	k8sState *k8s.DeploymentState,
	sunsetBuildIDs map[string]struct{},
) []WorkerResourceRef {
	if len(wrts) == 0 {
		return nil
	}

	// Collect the groups being deleted, by build ID.
	deletingGroups := make(map[string]map[string]bool)
	for _, d := range deleteDeployments {
		if bid, ok := d.Labels[k8s.BuildIDLabel]; ok && bid != "" {
			if deletingGroups[bid] == nil {
				deletingGroups[bid] = make(map[string]bool)
			}
			deletingGroups[bid][k8s.WorkerGroupName(d)] = true
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

		// Union of builds whose group Deployment is deleted this cycle, orphaned status
		// entries and builds pinned to zero (autoscalers only)
		group := wrt.Spec.EffectiveWorkerGroup()
		buildIDs := make([]string, 0, len(deletingGroups)+len(wrt.Status.Versions))
		for bid, groups := range deletingGroups {
			if groups[group] {
				buildIDs = append(buildIDs, bid)
			}
		}

		hasScaleTarget := k8s.HasScaleTarget(wrt.Spec.Template.Raw)
		for _, v := range wrt.Status.Versions {
			if v.BuildID == "" {
				continue
			}
			if k8sState != nil {
				if _, live := k8sState.VersionDeployments(v.BuildID)[group]; live {
					if hasScaleTarget {
						// Remove the autoscaler as soon as the controller starts holding
						// replicas at zero. The k8s Deployment outlives it by deleteDelay.
						// Any other rendered resource is cleaned up with the Deployment
						if _, sunsetting := sunsetBuildIDs[v.BuildID]; sunsetting {
							buildIDs = append(buildIDs, v.BuildID)
						}
					}
					continue // build still has a Deployment; its resource is managed by applies
				}
			}
			buildIDs = append(buildIDs, v.BuildID)
		}
		// Sort before Compact: duplicates in the union are usually non-adjacent (a build
		// sunset this cycle typically still has a status entry), and Compact only collapses
		// adjacent runs.
		slices.Sort(buildIDs)
		buildIDs = slices.Compact(buildIDs)

		for _, buildID := range buildIDs {
			// Compute the resource name deterministically — no status lookup needed.
			// This ensures cleanup even if the WRT was never successfully applied for this
			// buildID (e.g. the version was already eligible for deletion when the WRT was
			// first created). The Delete call is a no-op if the resource doesn't exist.
			resourceName := k8s.ComputeWorkerResourceTemplateName(
				wrt.Spec.EffectiveWorkerDeploymentName(), wrt.Name, buildID,
			)
			refs = append(refs, WorkerResourceRef{
				Namespace:  wrt.Namespace,
				Name:       resourceName,
				APIVersion: templateMeta.APIVersion,
				Kind:       templateMeta.Kind,
				WRTName:    wrt.Name,
				BuildID:    buildID,
			})
		}
	}
	return refs
}

// getWRTGroupProblems returns the names of WRTs whose group no version has and the spec
// does not declare, and of WRTs still marked WorkerGroupNotFound whose group is known again.
func getWRTGroupProblems(
	wrts []temporaliov1alpha1.WorkerResourceTemplate,
	k8sState *k8s.DeploymentState,
	spec *temporaliov1alpha1.WorkerDeploymentSpec,
) (missing, stale []string) {
	known := make(map[string]bool)
	for _, group := range spec.WorkerGroupNames() {
		known[group] = true
	}
	for _, buildID := range k8sState.BuildIDs() {
		for group := range k8sState.VersionDeployments(buildID) {
			known[group] = true
		}
	}
	for i := range wrts {
		wrt := &wrts[i]
		if !known[wrt.Spec.EffectiveWorkerGroup()] {
			missing = append(missing, wrt.Name)
		} else if cond := apimeta.FindStatusCondition(wrt.Status.Conditions, temporaliov1alpha1.ConditionReady); cond != nil &&
			cond.Reason == temporaliov1alpha1.ReasonWRTWorkerGroupNotFound {
			stale = append(stale, wrt.Name)
		}
	}
	return missing, stale
}

// updateDeploymentConnectionIfStale updates a deployment in-place when its connection
// spec hash differs from the provided ConnectionSpec, and reports whether it did.
func updateDeploymentConnectionIfStale(d *appsv1.Deployment, connection temporaliov1alpha1.ConnectionSpec) bool {
	if k8s.ComputeConnectionSpecHash(connection) == d.Spec.Template.Annotations[k8s.ConnectionSpecHashAnnotation] {
		return false
	}
	updateDeploymentWithConnection(d, connection)
	return true
}

// updateDeploymentWithConnection updates an existing deployment in-place to match a new ConnectionSpec.
// It rewrites the controller-managed connection env vars and the mTLS volume/mount, adding and removing them as needed,
// So switching auth mode (mTLS <-> API key or to/from no-credentials) yields a fully-configured pod.
// It operates on the deployment's own pod template, so each version keeps its own image.
func updateDeploymentWithConnection(deployment *appsv1.Deployment, connection temporaliov1alpha1.ConnectionSpec) {
	// Update the connection spec hash annotation
	deployment.Spec.Template.Annotations[k8s.ConnectionSpecHashAnnotation] = k8s.ComputeConnectionSpecHash(connection)

	tlsServerName := connection.TLSServerName()
	mtls := connection.MutualTLSSecretRef != nil
	apiKey := !mtls && connection.APIKeySecretRef != nil
	// TLSCACertSecretName is mutually exclusive with MutualTLSSecretRef (enforced by
	// ConnectionSpec's CEL validation) -- mTLS bundles its own CA into that secret's ca.crt
	// key instead, see ConnectionTLSConfig.CACertSecretRef.
	caCertSecretName := connection.TLSCACertSecretName()

	for i := range deployment.Spec.Template.Spec.Containers {
		container := &deployment.Spec.Template.Spec.Containers[i]

		container.Env = setEnvVar(container.Env, k8s.EnvTemporalAddress, connection.HostPort)

		if tlsServerName != "" {
			container.Env = setEnvVar(container.Env, k8s.EnvTemporalTLSServerName, tlsServerName)
		} else {
			container.Env = removeEnvVar(container.Env, k8s.EnvTemporalTLSServerName)
		}

		if mtls || caCertSecretName != "" {
			container.Env = setEnvVar(container.Env, k8s.EnvTemporalTLS, "true")
		} else {
			container.Env = removeEnvVar(container.Env, k8s.EnvTemporalTLS)
		}

		if mtls {
			container.Env = setEnvVar(container.Env, k8s.EnvTemporalTLSClientKeyPath, k8s.TemporalTLSClientKeyPath)
			container.Env = setEnvVar(container.Env, k8s.EnvTemporalTLSClientCertPath, k8s.TemporalTLSClientCertPath)
			container.VolumeMounts = k8s.EnsureTLSVolumeMount(container.VolumeMounts)
		} else {
			container.Env = removeEnvVar(container.Env, k8s.EnvTemporalTLSClientKeyPath)
			container.Env = removeEnvVar(container.Env, k8s.EnvTemporalTLSClientCertPath)
			container.VolumeMounts = k8s.RemoveTLSVolumeMount(container.VolumeMounts)
		}

		if caCertSecretName != "" {
			container.Env = setEnvVar(container.Env, k8s.EnvTemporalTLSServerCACertPath, k8s.TemporalTLSCACertPath)
			container.VolumeMounts = k8s.EnsureTLSCAVolumeMount(container.VolumeMounts)
		} else {
			container.Env = removeEnvVar(container.Env, k8s.EnvTemporalTLSServerCACertPath)
			container.VolumeMounts = k8s.RemoveTLSCAVolumeMount(container.VolumeMounts)
		}

		if apiKey {
			container.Env = setEnvVarFrom(container.Env, k8s.EnvTemporalAPIKey, &corev1.EnvVarSource{SecretKeyRef: connection.APIKeySecretRef})
		} else {
			container.Env = removeEnvVar(container.Env, k8s.EnvTemporalAPIKey)
		}
	}

	if mtls {
		deployment.Spec.Template.Spec.Volumes = k8s.EnsureTLSVolume(deployment.Spec.Template.Spec.Volumes,
			connection.MutualTLSSecretRef.Name)
	} else {
		deployment.Spec.Template.Spec.Volumes = k8s.RemoveTLSVolume(deployment.Spec.Template.Spec.Volumes)
	}

	if caCertSecretName != "" {
		deployment.Spec.Template.Spec.Volumes = k8s.EnsureTLSCAVolume(deployment.Spec.Template.Spec.Volumes, caCertSecretName)
	} else {
		deployment.Spec.Template.Spec.Volumes = k8s.RemoveTLSCAVolume(deployment.Spec.Template.Spec.Volumes)
	}
}

func setEnvVar(envVars []corev1.EnvVar, name string, value string) []corev1.EnvVar {
	for i := range envVars {
		if envVars[i].Name == name {
			envVars[i].Value = value
			envVars[i].ValueFrom = nil
			return envVars
		}
	}
	return append(envVars, corev1.EnvVar{Name: name, Value: value})
}

func removeEnvVar(envVars []corev1.EnvVar, name string) []corev1.EnvVar {
	for i := range envVars {
		if envVars[i].Name == name {
			return append(envVars[:i], envVars[i+1:]...)
		}
	}
	return envVars
}

// setEnvVarFrom sets or replaces an env whose value comes from a source(ex. a secret).
// Mirrors of setEnvVar for ValueFrom-style vars
func setEnvVarFrom(envVars []corev1.EnvVar, name string, src *corev1.EnvVarSource) []corev1.EnvVar {
	for i := range envVars {
		if envVars[i].Name == name {
			envVars[i].Value = ""
			envVars[i].ValueFrom = src
			return envVars
		}
	}
	return append(envVars, corev1.EnvVar{Name: name, ValueFrom: src})
}

// checkAndUpdateGroupPodTemplateSpec rebuilds a group's Deployment in place when its pod template
// drifted under a stable unsafeCustomBuildID, and reports whether it did.
func checkAndUpdateGroupPodTemplateSpec(
	existingDeployment *appsv1.Deployment,
	spec *temporaliov1alpha1.WorkerDeploymentSpec,
	connection temporaliov1alpha1.ConnectionSpec,
) bool {
	// Only check for drift when UnsafeCustomBuildID is explicitly set by the user.
	// If buildID is auto-generated, any spec change would generate a new buildID anyway.
	if spec.WorkerOptions.UnsafeCustomBuildID == "" {
		return false
	}

	groupSpec, inSpec := spec.WorkerGroupDeploymentSpec(k8s.WorkerGroupName(existingDeployment))
	if !inSpec {
		return false
	}

	// Get the stored hash from the existing deployment's pod template annotations
	storedHash := ""
	if existingDeployment.Spec.Template.Annotations != nil {
		storedHash = existingDeployment.Spec.Template.Annotations[k8s.PodTemplateSpecHashAnnotation]
	}

	// Backwards compatibility: if no hash annotation exists (legacy deployment),
	// don't trigger an update - the hash will be added on the next spec change
	if storedHash == "" {
		return false
	}

	// If hashes match, no drift detected
	if storedHash == k8s.ComputePodTemplateSpecHash(groupSpec.Template) {
		return false
	}

	// Pod template has changed - rebuild the pod spec from the group's spec
	// This applies all controller modifications (env vars, TLS mounts, etc.)
	updateDeploymentWithPodTemplateSpec(existingDeployment, groupSpec, spec.WorkerOptions.TemporalNamespace, connection)
	return true
}

// updateDeploymentWithPodTemplateSpec updates an existing Kubernetes
// Deployment with a new pod template spec from its group's DeploymentSpec. This
// applies all the controller modifications that NewDeploymentWithOwnerRef
// does.
func updateDeploymentWithPodTemplateSpec(
	deployment *appsv1.Deployment,
	wdDepSpec appsv1.DeploymentSpec,
	temporalNamespace string,
	connection temporaliov1alpha1.ConnectionSpec,
) {

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

	// Hash the user's template before controller modifications, as NewDeploymentWithOwnerRef
	// does, so the next drift check compares like with like.
	podTemplateSpecHash := k8s.ComputePodTemplateSpecHash(wdDepSpec.Template)

	// Apply controller-managed environment variables and volume mounts
	// Uses the same shared helper as NewDeploymentWithOwnerRef
	k8s.ApplyControllerPodSpecModifications(
		&wdDepSpec.Template.Spec,
		connection,
		temporalNamespace,
		workerDeploymentName,
		buildID,
	)

	// Build new pod annotations
	podAnnotations := make(map[string]string)
	for k, v := range wdDepSpec.Template.Annotations {
		podAnnotations[k] = v
	}
	podAnnotations[k8s.ConnectionSpecHashAnnotation] = k8s.ComputeConnectionSpecHash(connection)
	podAnnotations[k8s.PodTemplateSpecHashAnnotation] = podTemplateSpecHash

	// Preserve existing pod labels and add/update required labels
	podLabels := make(map[string]string)
	for k, v := range wdDepSpec.Template.Labels {
		podLabels[k] = v
	}
	// Copy selector labels from existing deployment
	wdDepSpec.Selector = deployment.Spec.Selector
	for k, v := range deployment.Spec.Selector.MatchLabels {
		podLabels[k] = v
	}

	// Only set replicas when the controller is managing them (spec.Replicas non-nil).
	// When nil, an external autoscaler owns replicas; preserving the current value
	// avoids competing with it on every Update.
	origReplicas := deployment.Spec.Replicas
	deployment.Spec = wdDepSpec
	deployment.Spec.Replicas = origReplicas

	// Update the deployment's pod template
	deployment.Spec.Template.ObjectMeta.Labels = podLabels
	deployment.Spec.Template.ObjectMeta.Annotations = podAnnotations
}

func getUpdateDeployments(
	k8sState *k8s.DeploymentState,
	status *temporaliov1alpha1.WorkerDeploymentStatus,
	spec *temporaliov1alpha1.WorkerDeploymentSpec,
	connection temporaliov1alpha1.ConnectionSpec,
) []*appsv1.Deployment {
	var updateDeployments []*appsv1.Deployment
	// Track which deployments we've already added to avoid duplicates
	updated := make(map[*appsv1.Deployment]bool)
	add := func(d *appsv1.Deployment) {
		if !updated[d] {
			updated[d] = true
			updateDeployments = append(updateDeployments, d)
		}
	}

	// Check the target version's deployments for pod template spec drift
	// This enables rolling updates when the build ID is stable but spec changed
	for _, d := range k8sState.VersionDeploymentList(status.TargetVersion.BuildID) {
		if checkAndUpdateGroupPodTemplateSpec(d, spec, connection) {
			add(d)
		}
	}

	// Check the target and current versions' deployments for an expired connection spec
	// hash (skipping deployments already rebuilt by the pod template check)
	connectionBuildIDs := []string{status.TargetVersion.BuildID}
	if status.CurrentVersion != nil {
		connectionBuildIDs = append(connectionBuildIDs, status.CurrentVersion.BuildID)
	}
	for _, buildID := range connectionBuildIDs {
		if buildID == "" {
			continue
		}
		for _, d := range k8sState.VersionDeploymentList(buildID) {
			if !updated[d] && updateDeploymentConnectionIfStale(d, connection) {
				add(d)
			}
		}
	}

	// Sync Deployment rolling-update strategy on all Kubernetes Deployments
	// associated with WorkerDeploymentVersions managed by Temporal Worker
	// Controller for this Temporal Worker Deployment. Do this after the
	// pod-template / connection checks so a Kubernetes Deployment already
	// queued for update also picks up strategy changes in the same write.
	for _, buildID := range ownedBuildIDs(status) {
		for _, d := range k8sState.VersionDeploymentList(buildID) {
			groupSpec, inSpec := spec.WorkerGroupDeploymentSpec(k8s.WorkerGroupName(d))
			if !inSpec || apiequality.Semantic.DeepEqual(groupSpec.Strategy, d.Spec.Strategy) {
				continue
			}
			d.Spec.Strategy = groupSpec.Strategy
			add(d)
		}
	}

	return updateDeployments
}

func ownedBuildIDs(status *temporaliov1alpha1.WorkerDeploymentStatus) []string {
	var buildIDs []string
	seen := make(map[string]bool)
	add := func(buildID string) {
		if buildID == "" || seen[buildID] {
			return
		}
		seen[buildID] = true
		buildIDs = append(buildIDs, buildID)
	}
	add(status.TargetVersion.BuildID)
	if status.CurrentVersion != nil {
		add(status.CurrentVersion.BuildID)
	}
	for _, version := range status.DeprecatedVersions {
		add(version.BuildID)
	}
	return buildIDs
}

// getDeleteDeployments determines which deployments should be deleted
func getDeleteDeployments(
	k8sState *k8s.DeploymentState,
	status *temporaliov1alpha1.WorkerDeploymentStatus,
	spec *temporaliov1alpha1.WorkerDeploymentSpec,
	foundDeploymentInTemporal bool,
) []*appsv1.Deployment {
	var deleteDeployments []*appsv1.Deployment

	for _, version := range status.DeprecatedVersions {
		// Every group of a version is deleted together; the default group goes last.
		deployments := k8sState.VersionDeploymentList(version.BuildID)
		if len(deployments) == 0 {
			continue
		}

		switch version.Status {
		case temporaliov1alpha1.VersionStatusInactive:
			// Superseded versions that never received routed traffic never become Drained.
			// Wait for scale-down to finish; execution checks pinned workflows before pruning.
			if foundDeploymentInTemporal && status.TargetVersion.BuildID != version.BuildID &&
				(status.CurrentVersion == nil || status.CurrentVersion.BuildID != version.BuildID) &&
				allDeployments(deployments, isFullyScaledDown) {
				deleteDeployments = append(deleteDeployments, deployments...)
			}
		case temporaliov1alpha1.VersionStatusDrained:
			// Deleting a deployment is only possible when:
			// 1. The deployment has been drained for deleteDelay + scaledownDelay.
			// 2. The deployment is scaled to 0 replicas.
			// 3. The version is eligible for deletion (drained with no active
			//    worker pods, i.e. Status.Replicas == 0). Requiring this lets
			//    executePlan prune the Temporal-side version record in the same
			//    reconcile as the Deployment delete: EligibleForDeletion is only
			//    computable while the Deployment (and thus this DeprecatedVersions
			//    entry) still exists, so this is the only point that can reliably
			//    prune it. See execplan.deleteDeprecatedVersions.
			if version.DrainedSince != nil &&
				(time.Since(version.DrainedSince.Time) > spec.SunsetStrategy.DeleteDelay.Duration+spec.SunsetStrategy.ScaledownDelay.Duration) &&
				allDeployments(deployments, isScaledToZero) &&
				version.EligibleForDeletion {
				deleteDeployments = append(deleteDeployments, deployments...)
			}
		case temporaliov1alpha1.VersionStatusNotRegistered:
			// Only delete Deployments of NotRegistered versions if temporalState was not empty
			if foundDeploymentInTemporal &&
				// NotRegistered versions are versions that the server doesn't know about.
				// Only delete if it's not the target version.
				status.TargetVersion.BuildID != version.BuildID {
				// Consider: Could call DescribeVersion here to assert NotFound before deleting, in case version summaries have diverged from version state
				deleteDeployments = append(deleteDeployments, deployments...)
			}
		}
	}

	return deleteDeployments
}

func allDeployments(deployments []*appsv1.Deployment, pred func(*appsv1.Deployment) bool) bool {
	for _, d := range deployments {
		if !pred(d) {
			return false
		}
	}
	return true
}

func isScaledToZero(d *appsv1.Deployment) bool {
	return d.Spec.Replicas != nil && *d.Spec.Replicas == 0
}

func isFullyScaledDown(d *appsv1.Deployment) bool {
	return isScaledToZero(d) &&
		d.Status.ObservedGeneration >= d.Generation && d.Status.Replicas == 0 &&
		(d.Status.TerminatingReplicas == nil || *d.Status.TerminatingReplicas == 0)
}

// getScaleDeployments determines which deployments should be explicitly scaled and to what size.
// Drained versions and inactive versions that are not the rollout target are always scaled to
// zero during sunset.
func getScaleDeployments(
	l logr.Logger,
	k8sState *k8s.DeploymentState,
	status *temporaliov1alpha1.WorkerDeploymentStatus,
	spec *temporaliov1alpha1.WorkerDeploymentSpec,
) map[*corev1.ObjectReference]uint32 {
	scaleDeployments := make(map[*corev1.ObjectReference]uint32)

	// Scale the current version if needed
	if status.CurrentVersion != nil {
		for group, d := range k8sState.VersionDeployments(status.CurrentVersion.BuildID) {
			ref := groupDeploymentRef(status.CurrentVersion.Deployment, group, d)
			groupSpec, inSpec := spec.WorkerGroupDeploymentSpec(group)
			if ref == nil || !inSpec {
				continue
			}
			scaleToSpecReplicas(scaleDeployments, d, ref, groupSpec.Replicas)
		}
	}

	// Scale the target version if it exists, and isn't current
	if status.CurrentVersion == nil || status.CurrentVersion.BuildID != status.TargetVersion.BuildID {
		for group, d := range k8sState.VersionDeployments(status.TargetVersion.BuildID) {
			ref := groupDeploymentRef(status.TargetVersion.Deployment, group, d)
			groupSpec, inSpec := spec.WorkerGroupDeploymentSpec(group)
			if ref == nil || !inSpec {
				continue
			}

			// If the Target Version is an already-existing Deployment that was scaled to zero by the controller
			// due to Sunset Policy, and the TWD has nil replicas because a scaler is managing the replicas, then
			// no one will scale the Target Version back up, so we need to scale it back to 1 replica, which is what
			// would happen if the Deployment was being created from scratch with nil replicas.
			if groupSpec.Replicas != nil || (d.Spec.Replicas != nil && *d.Spec.Replicas == 0) {
				replicas := int32(1) // just scale up to 1 if we are in the spec.Replicas == nil && d.Spec.Replicas == 0 case.
				if groupSpec.Replicas != nil {
					replicas = *groupSpec.Replicas
				}
				if d.Spec.Replicas == nil || *d.Spec.Replicas != replicas {
					scaleDeployments[ref] = uint32(replicas)
				}
			}
		}
	}

	// Scale other versions based on status
	for _, version := range status.DeprecatedVersions {
		for group, d := range k8sState.VersionDeployments(version.BuildID) {
			ref := groupDeploymentRef(version.Deployment, group, d)
			if ref == nil {
				continue
			}
			var replicas *int32
			if groupSpec, inSpec := spec.WorkerGroupDeploymentSpec(group); inSpec {
				replicas = groupSpec.Replicas
			}
			scaleDeprecatedDeployment(l, scaleDeployments, version, d, ref, status.TargetVersion.BuildID, spec.SunsetStrategy.ScaledownDelay, replicas)
		}
	}

	return scaleDeployments
}

// groupDeploymentRef returns the reference used to scale a group's deployment. The
// default group keeps the reference from status so callers can match on it.
func groupDeploymentRef(defaultRef *corev1.ObjectReference, group string, d *appsv1.Deployment) *corev1.ObjectReference {
	if group == temporaliov1alpha1.DefaultWorkerGroupName {
		return defaultRef
	}
	return k8s.NewObjectRef(d)
}

// scaleDeprecatedDeployment applies the sunset scaling rules to one group's deployment of a
// deprecated version. specReplicas is nil when a scaler manages the group.
func scaleDeprecatedDeployment(
	l logr.Logger,
	scaleDeployments map[*corev1.ObjectReference]uint32,
	version *temporaliov1alpha1.DeprecatedWorkerDeploymentVersion,
	d *appsv1.Deployment,
	ref *corev1.ObjectReference,
	targetBuildID string,
	scaledownDelay *metav1.Duration,
	specReplicas *int32,
) {
	switch version.Status {
	case temporaliov1alpha1.VersionStatusInactive:
		// Scale down inactive versions that are not the target
		if targetBuildID == version.BuildID {
			// TODO(carlydf): I'm not convinced this case actually happens, because Target and Current Versions are excluded from DeprecatedVersions. Leaving it unchanged since I don't want to add to this PRs scope.
			scaleToSpecReplicas(scaleDeployments, d, ref, specReplicas)
		} else if !isScaledToZero(d) { // these are non-target inactive versions with nil replicas or >0 replicas
			scaleDeployments[ref] = 0
		}
	case temporaliov1alpha1.VersionStatusRamping, temporaliov1alpha1.VersionStatusCurrent:
		// TODO(carlydf): Also not convinced this case actually happens, because Target and Current Versions are excluded from DeprecatedVersions. Leaving it unchanged since I don't want to add to this PRs scope.
		scaleToSpecReplicas(scaleDeployments, d, ref, specReplicas)
	case temporaliov1alpha1.VersionStatusDraining:
		scaleDrainingBackUp(l, scaleDeployments, version, d, ref, specReplicas)
	case temporaliov1alpha1.VersionStatusDrained:
		// Scale down drained deployments after delay
		if version.DrainedSince != nil && time.Since(version.DrainedSince.Time) > scaledownDelay.Duration &&
			!isScaledToZero(d) { // these are non-target drained versions with nil replicas or >0 replicas
			scaleDeployments[ref] = 0
		}
	default:
		// NotRegistered and Created versions are left as they are.
	}
}

// scaleToSpecReplicas scales a deployment to its group's replicas when the controller manages them.
func scaleToSpecReplicas(
	scaleDeployments map[*corev1.ObjectReference]uint32,
	d *appsv1.Deployment,
	ref *corev1.ObjectReference,
	specReplicas *int32,
) {
	if specReplicas != nil && d.Spec.Replicas != nil && *d.Spec.Replicas != *specReplicas {
		scaleDeployments[ref] = uint32(*specReplicas)
	}
}

// scaleDrainingBackUp scales a draining deployment up from 0 replicas: with no pollers its
// version would never finish draining.
func scaleDrainingBackUp(
	l logr.Logger,
	scaleDeployments map[*corev1.ObjectReference]uint32,
	version *temporaliov1alpha1.DeprecatedWorkerDeploymentVersion,
	d *appsv1.Deployment,
	ref *corev1.ObjectReference,
	specReplicas *int32,
) {
	if !isScaledToZero(d) {
		return
	}
	// If the controller manages the replicas we set it to the spec's value. If a
	// scaler manages them, we explicitly set it to 1 to unblock drainage.
	replicas := int32(1)
	if specReplicas != nil {
		replicas = *specReplicas
	}
	// spec.Replicas may legitimately be 0, so we guard it.
	if replicas == 0 {
		return
	}
	l.Info("scaling draining version back up from 0 replicas",
		"buildID", version.BuildID,
		"deployment", ref.Name,
		"replicas", replicas,
	)
	scaleDeployments[ref] = uint32(replicas)
}

// getSunsetScaleDownBuildIDs returns the build IDs of drained versions the controller has
// begun forcing to zero.
func getSunsetScaleDownBuildIDs(
	status *temporaliov1alpha1.WorkerDeploymentStatus,
	spec *temporaliov1alpha1.WorkerDeploymentSpec,
) map[string]struct{} {
	buildIDs := map[string]struct{}{}
	for _, version := range status.DeprecatedVersions {
		if version.Status == temporaliov1alpha1.VersionStatusDrained &&
			version.DrainedSince != nil && time.Since(version.DrainedSince.Time) > spec.SunsetStrategy.ScaledownDelay.Duration {
			buildIDs[version.BuildID] = struct{}{}
		}
	}
	return buildIDs
}

// getCreateDeploymentWorkerGroups returns the groups of the target version that need a Deployment,
// and why it refused, if it did.
func getCreateDeploymentWorkerGroups(
	k8sState *k8s.DeploymentState,
	status *temporaliov1alpha1.WorkerDeploymentStatus,
	spec *temporaliov1alpha1.WorkerDeploymentSpec,
	maxVersionsIneligibleForDeletion int32,
) (groups []string, blockedReason string) {
	existing := k8sState.VersionDeployments(status.TargetVersion.BuildID)
	if len(existing) == 0 {
		if !shouldCreateDeployment(status, maxVersionsIneligibleForDeletion) {
			return nil, ""
		}
		return spec.WorkerGroupNames(), ""
	}
	// A Deployment's selector is immutable, and group and non-group selectors would overlap.
	if k8s.HasWorkerGroupLabel(existing) != spec.HasWorkerGroups() {
		return nil, fmt.Sprintf(
			"adding or removing spec.workerGroups requires a new unsafeCustomBuildID: build ID %q already has Deployments", status.TargetVersion.BuildID)
	}
	for _, group := range spec.WorkerGroupNames() {
		if _, ok := existing[group]; !ok {
			groups = append(groups, group)
		}
	}
	return groups, ""
}

// getDeleteWorkerGroupDeployments returns the target version's Deployments of groups that are
// no longer in the spec, which only happens under a stable custom build ID.
func getDeleteWorkerGroupDeployments(
	k8sState *k8s.DeploymentState,
	status *temporaliov1alpha1.WorkerDeploymentStatus,
	spec *temporaliov1alpha1.WorkerDeploymentSpec,
) []*appsv1.Deployment {
	existing := k8sState.VersionDeployments(status.TargetVersion.BuildID)
	if !spec.HasWorkerGroups() || !k8s.HasWorkerGroupLabel(existing) {
		return nil
	}
	var deletes []*appsv1.Deployment
	for _, d := range k8sState.VersionDeploymentList(status.TargetVersion.BuildID) {
		if !spec.HasWorkerGroup(k8s.WorkerGroupName(d)) {
			deletes = append(deletes, d)
		}
	}
	return deletes
}

// shouldCreateDeployment determines if a new deployment needs to be created
func shouldCreateDeployment(
	status *temporaliov1alpha1.WorkerDeploymentStatus,
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
	status *temporaliov1alpha1.WorkerDeploymentStatus,
	config *Config,
	workerDeploymentName string,
	gateInput []byte,
	isGateInputSecret bool,
) []WorkflowConfig {
	var testWorkflows []WorkflowConfig

	// Skip if there's no gate workflow defined, if the target version is already the current, or if the target
	// version is not yet ready to run workflows
	if config.RolloutStrategy.Gate == nil ||
		(status.CurrentVersion != nil && status.CurrentVersion.BuildID == status.TargetVersion.BuildID) ||
		status.TargetVersion.Status == temporaliov1alpha1.VersionStatusNotRegistered ||
		status.TargetVersion.Status == temporaliov1alpha1.VersionStatusCreated {
		return nil
	}
	// A registered multi-group version may still have groups that are not polling, whose
	// queues would send the gate's activities to another version.
	if len(status.TargetVersion.WorkerGroups) > 0 && status.TargetVersion.HealthySince == nil {
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
				WorkflowType:    config.RolloutStrategy.Gate.WorkflowType,
				WorkflowID:      temporal.GetTestWorkflowID(workerDeploymentName, targetVersion.BuildID, tq.Name),
				BuildID:         targetVersion.BuildID,
				TaskQueue:       tq.Name,
				GateInput:       string(gateInput),
				IsInputSecret:   isGateInputSecret,
				GateEncoding:    string(config.RolloutStrategy.Gate.Encoding),
				GateMessageType: config.RolloutStrategy.Gate.MessageType,
			})
		}
	}

	return testWorkflows
}

// getVersionConfigDiff determines the version configuration based on the rollout/rollback strategies
func getVersionConfigDiff(
	l logr.Logger,
	status *temporaliov1alpha1.WorkerDeploymentStatus,
	temporalState *temporal.TemporalWorkerState,
	config *Config,
) *VersionConfig {
	var strategy temporaliov1alpha1.RolloutStrategy
	if isRollbackScenario(l, status, temporalState, config) {
		strategy = temporaliov1alpha1.RolloutStrategy{Strategy: temporaliov1alpha1.UpdateAllAtOnce}
	} else {
		strategy = config.RolloutStrategy
	}

	if strategy.Strategy == temporaliov1alpha1.UpdateManual {
		return nil
	}

	// Do nothing if the target Deployment is not healthy yet, or until Temporal reports
	// the version as Inactive, indicating that workers have started polling. Created
	// versions exist in Temporal but do not have pollers yet.
	if status.TargetVersion.HealthySince == nil ||
		status.TargetVersion.Status == temporaliov1alpha1.VersionStatusNotRegistered ||
		status.TargetVersion.Status == temporaliov1alpha1.VersionStatusCreated {
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
		ConflictToken:   status.VersionConflictToken,
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

func isRollbackScenario(
	l logr.Logger,
	status *temporaliov1alpha1.WorkerDeploymentStatus,
	temporalState *temporal.TemporalWorkerState,
	config *Config,
) bool {
	// Do not rollback when the user takes control of deployments with manual mode
	if config.RolloutStrategy.Strategy == temporaliov1alpha1.UpdateManual {
		return false
	}

	// No versions yet to rollback to
	if temporalState == nil {
		return false
	}

	// The target version is already current, so there is nothing to roll back to
	if status.CurrentVersion != nil && status.CurrentVersion.BuildID == status.TargetVersion.BuildID {
		return false
	}

	// The target version was not seen before, rollback is not possible
	targetVersionInfo, exists := temporalState.Versions[status.TargetVersion.BuildID]
	if !exists {
		return false
	}

	// The target version never became current before, so keep rollout
	if targetVersionInfo.LastCurrentTime == nil {
		return false
	}

	// The target version was last current more than an hour ago, making it too old to trust immediate rollback
	if time.Since(*targetVersionInfo.LastCurrentTime) > defaults.RollbackMaxVersionAge {
		l.Info("Skipping rollback: the version's last current time exceeds the max rollback version age",
			"targetBuildID", status.TargetVersion.BuildID,
			"lastCurrentTime", targetVersionInfo.LastCurrentTime,
			"maxVersionAge", defaults.RollbackMaxVersionAge)
		return false
	}

	// The target version was current in the last 1h, so rollback immediately
	l.Info("Detected rollback scenario using LastCurrentTime. "+
		"Warning: Auto-upgrade workflows that upgraded from a previous version to the current version may fail during this rollback, "+
		"as they may not handle downgrades properly. Monitor workflow executions for failures.",
		"targetBuildID", status.TargetVersion.BuildID,
		"lastCurrentTime", targetVersionInfo.LastCurrentTime)

	return true
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
