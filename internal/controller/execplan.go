// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/go-logr/logr"
	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	"github.com/temporalio/temporal-worker-controller/internal/k8s"
	"github.com/temporalio/temporal-worker-controller/internal/planner"
	enumspb "go.temporal.io/api/enums/v1"
	sdkclient "go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func (r *TemporalWorkerDeploymentReconciler) executeK8sOperations(ctx context.Context, l logr.Logger, workerDeploy *temporaliov1alpha1.TemporalWorkerDeployment, p *plan) error {
	// Create deployment
	if p.CreateDeployment != nil {
		l.Info("creating deployment", "deployment", p.CreateDeployment)
		if err := r.Create(ctx, p.CreateDeployment); err != nil {
			l.Error(err, "unable to create deployment", "deployment", p.CreateDeployment)
			r.Recorder.Eventf(workerDeploy, corev1.EventTypeWarning, ReasonDeploymentCreateFailed,
				"Failed to create Deployment %q: %v", p.CreateDeployment.Name, err)
			return err
		}
	}

	// Delete deployments
	for _, d := range p.DeleteDeployments {
		l.Info("deleting deployment", "deployment", d)
		if err := r.Delete(ctx, d); err != nil {
			l.Error(err, "unable to delete deployment", "deployment", d)
			r.Recorder.Eventf(workerDeploy, corev1.EventTypeWarning, ReasonDeploymentDeleteFailed,
				"Failed to delete Deployment %q: %v", d.Name, err)
			return err
		}
	}

	// Delete rendered WRT resources whose versioned Deployment is being sunset.
	// Rendered resources are owned by the WRT (not the Deployment), so k8s GC does not
	// clean them up when the Deployment is deleted; the controller does it here instead.
	// Errors are logged and skipped — a failed delete will be retried next reconcile
	// (the Deployment is already gone so its build ID won't produce a new apply).
	for _, res := range p.DeleteWorkerResources {
		obj := &unstructured.Unstructured{}
		gv, err := schema.ParseGroupVersion(res.APIVersion)
		if err != nil {
			l.Error(err, "unable to parse APIVersion for worker resource delete",
				"apiVersion", res.APIVersion, "kind", res.Kind, "name", res.Name)
			continue
		}
		obj.SetGroupVersionKind(schema.GroupVersionKind{Group: gv.Group, Version: gv.Version, Kind: res.Kind})
		obj.SetNamespace(res.Namespace)
		obj.SetName(res.Name)
		if err := r.Delete(ctx, obj); client.IgnoreNotFound(err) != nil {
			l.Error(err, "unable to delete worker resource on version sunset",
				"apiVersion", res.APIVersion, "kind", res.Kind, "name", res.Name)
		} else {
			l.Info("deleted worker resource on version sunset",
				"apiVersion", res.APIVersion, "kind", res.Kind, "name", res.Name)
		}
	}

	// Scale deployments
	for d, replicas := range p.ScaleDeployments {
		l.Info("scaling deployment", "deployment", d, "replicas", replicas)
		dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{
			Namespace:       d.Namespace,
			Name:            d.Name,
			ResourceVersion: d.ResourceVersion,
			UID:             d.UID,
		}}

		scale := &autoscalingv1.Scale{Spec: autoscalingv1.ScaleSpec{Replicas: int32(replicas)}}
		if err := r.Client.SubResource("scale").Update(ctx, dep, client.WithSubResourceBody(scale)); err != nil {
			l.Error(err, "unable to scale deployment", "deployment", d, "replicas", replicas)
			r.Recorder.Eventf(workerDeploy, corev1.EventTypeWarning, ReasonDeploymentScaleFailed,
				"Failed to scale Deployment %q to %d replicas: %v", d.Name, replicas, err)
			return fmt.Errorf("unable to scale deployment: %w", err)
		}
	}

	// Update deployments
	for _, d := range p.UpdateDeployments {
		l.Info("updating deployment", "deployment", d.Name, "namespace", d.Namespace)
		if err := r.Update(ctx, d); err != nil {
			l.Error(err, "unable to update deployment", "deployment", d)
			r.Recorder.Eventf(workerDeploy, corev1.EventTypeWarning, ReasonDeploymentUpdateFailed,
				"Failed to update Deployment %q: %v", d.Name, err)
			return fmt.Errorf("unable to update deployment: %w", err)
		}
	}

	return nil
}

func (r *TemporalWorkerDeploymentReconciler) startTestWorkflows(ctx context.Context, l logr.Logger, workerDeploy *temporaliov1alpha1.TemporalWorkerDeployment, temporalClient sdkclient.Client, p *plan) error {
	for _, wf := range p.startTestWorkflows {
		// Log workflow start details
		if len(wf.input) > 0 {
			if wf.isInputSecret {
				// Don't log the actual input if it came from a Secret
				l.Info("starting gate workflow",
					"workflowType", wf.workflowType,
					"taskQueue", wf.taskQueue,
					"buildID", wf.buildID,
					"inputBytes", len(wf.input),
					"inputSource", "SecretRef (contents hidden)",
				)
			} else {
				// For non-secret sources, parse JSON and extract keys
				var inputKeys []string
				if len(wf.input) > 0 {
					var jsonData map[string]interface{}
					if err := json.Unmarshal(wf.input, &jsonData); err == nil {
						for key := range jsonData {
							inputKeys = append(inputKeys, key)
						}
					}
				}

				// Log the input keys for non-secret sources (inline or ConfigMap)
				l.Info("starting gate workflow",
					"workflowType", wf.workflowType,
					"taskQueue", wf.taskQueue,
					"buildID", wf.buildID,
					"inputBytes", len(wf.input),
					"inputKeys", inputKeys,
				)
			}
		} else {
			l.Info("starting gate workflow",
				"workflowType", wf.workflowType,
				"taskQueue", wf.taskQueue,
				"buildID", wf.buildID,
				"inputBytes", 0,
			)
		}
		opts := sdkclient.StartWorkflowOptions{
			ID:                       wf.workflowID,
			TaskQueue:                wf.taskQueue,
			WorkflowExecutionTimeout: time.Hour,
			WorkflowIDReusePolicy:    enumspb.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE,
			WorkflowIDConflictPolicy: enumspb.WORKFLOW_ID_CONFLICT_POLICY_FAIL,
			VersioningOverride: &sdkclient.PinnedVersioningOverride{
				Version: worker.WorkerDeploymentVersion{
					DeploymentName: p.WorkerDeploymentName,
					BuildID:        wf.buildID,
				},
			},
		}
		var err error
		if len(wf.input) > 0 {
			_, err = temporalClient.ExecuteWorkflow(ctx, opts, wf.workflowType, json.RawMessage(wf.input))
		} else {
			_, err = temporalClient.ExecuteWorkflow(ctx, opts, wf.workflowType)
		}
		if err != nil {
			l.Error(err, "unable to start test workflow execution", "workflowType", wf.workflowType, "buildID", wf.buildID, "taskQueue", wf.taskQueue)
			r.Recorder.Eventf(workerDeploy, corev1.EventTypeWarning, ReasonTestWorkflowStartFailed,
				"Failed to start gate workflow %q (buildID %s, taskQueue %s): %v", wf.workflowType, wf.buildID, wf.taskQueue, err)
			return fmt.Errorf("unable to start test workflow execution: %w", err)
		}
	}
	return nil
}

func (r *TemporalWorkerDeploymentReconciler) shouldClaimManagerIdentity(vcfg *planner.VersionConfig) bool {
	return vcfg.ManagerIdentity == ""
}

func (r *TemporalWorkerDeploymentReconciler) claimManagerIdentity(
	ctx context.Context,
	l logr.Logger,
	workerDeploy *temporaliov1alpha1.TemporalWorkerDeployment,
	deploymentHandler sdkclient.WorkerDeploymentHandle,
	vcfg *planner.VersionConfig,
) error {
	resp, err := deploymentHandler.SetManagerIdentity(ctx, sdkclient.WorkerDeploymentSetManagerIdentityOptions{
		Self:          true,
		ConflictToken: vcfg.ConflictToken,
		Identity:      getControllerIdentity(),
	})
	if err != nil {
		l.Error(err, "unable to claim manager identity")
		r.Recorder.Eventf(workerDeploy, corev1.EventTypeWarning, ReasonManagerIdentityClaimFailed,
			"Failed to claim manager identity: %v", err)
		return err
	}
	l.Info("claimed manager identity", "identity", getControllerIdentity())
	// Use the updated conflict token for the subsequent routing config change.
	vcfg.ConflictToken = resp.ConflictToken
	return nil
}

func (r *TemporalWorkerDeploymentReconciler) updateVersionConfig(ctx context.Context, l logr.Logger, workerDeploy *temporaliov1alpha1.TemporalWorkerDeployment, deploymentHandler sdkclient.WorkerDeploymentHandle, p *plan) error {
	vcfg := p.UpdateVersionConfig
	if vcfg == nil {
		return nil
	}

	if r.shouldClaimManagerIdentity(vcfg) {
		if err := r.claimManagerIdentity(ctx, l, workerDeploy, deploymentHandler, vcfg); err != nil {
			return fmt.Errorf("unable to claim manager identity: %w", err)
		}
	}

	if vcfg.SetCurrent {
		l.Info("registering new current version", "buildID", vcfg.BuildID)
		if _, err := deploymentHandler.SetCurrentVersion(ctx, sdkclient.WorkerDeploymentSetCurrentVersionOptions{
			BuildID:       vcfg.BuildID,
			ConflictToken: vcfg.ConflictToken,
			Identity:      getControllerIdentity(),
		}); err != nil {
			l.Error(err, "unable to set current deployment version", "buildID", vcfg.BuildID)
			r.Recorder.Eventf(workerDeploy, corev1.EventTypeWarning, ReasonVersionPromotionFailed,
				"Failed to set buildID %q as current version: %v", vcfg.BuildID, err)
			return fmt.Errorf("unable to set current deployment version: %w", err)
		}
		// Update the in-memory status to reflect the promotion. The status was mapped
		// from Temporal state before plan execution, so it is stale at this point.
		// syncConditions (called at end of reconcile) derives Ready/Progressing from
		// TargetVersion.Status, so it must be current to avoid a one-cycle lag.
		workerDeploy.Status.TargetVersion.Status = temporaliov1alpha1.VersionStatusCurrent
	} else {
		if vcfg.RampPercentage > 0 {
			l.Info("applying ramp", "buildID", vcfg.BuildID, "percentage", vcfg.RampPercentage)
		} else {
			l.Info("deleting ramp", "buildID", vcfg.BuildID)
		}

		if _, err := deploymentHandler.SetRampingVersion(ctx, sdkclient.WorkerDeploymentSetRampingVersionOptions{
			BuildID:       vcfg.BuildID,
			Percentage:    float32(vcfg.RampPercentage),
			ConflictToken: vcfg.ConflictToken,
			Identity:      getControllerIdentity(),
		}); err != nil {
			l.Error(err, "unable to set ramping deployment version", "buildID", vcfg.BuildID, "percentage", vcfg.RampPercentage)
			r.Recorder.Eventf(workerDeploy, corev1.EventTypeWarning, ReasonVersionPromotionFailed,
				"Failed to set buildID %q as ramping version (%d%%): %v", vcfg.BuildID, vcfg.RampPercentage, err)
			return fmt.Errorf("unable to set ramping deployment version: %w", err)
		}
		// Same reasoning as the SetCurrent path above: update the in-memory status
		// so syncConditions sees the correct state on this reconcile cycle.
		if vcfg.RampPercentage > 0 {
			workerDeploy.Status.TargetVersion.Status = temporaliov1alpha1.VersionStatusRamping
		}
		// When RampPercentage == 0 we are clearing a stale ramp on a different build ID
		// (see planner: "Reset ramp if needed"). The target version is already Current,
		// so no in-memory status update is needed here.
	}

	if _, err := deploymentHandler.UpdateVersionMetadata(ctx, sdkclient.WorkerDeploymentUpdateVersionMetadataOptions{
		Version: worker.WorkerDeploymentVersion{
			DeploymentName: p.WorkerDeploymentName,
			BuildID:        vcfg.BuildID,
		},
		MetadataUpdate: sdkclient.WorkerDeploymentMetadataUpdate{
			UpsertEntries: map[string]interface{}{
				controllerIdentityMetadataKey: getControllerIdentity(),
				controllerVersionMetadataKey:  getControllerVersion(),
			},
		},
	}); err != nil { // would be cool to do this atomically with the update
		l.Error(err, "unable to update version metadata", "buildID", vcfg.BuildID)
		r.Recorder.Eventf(workerDeploy, corev1.EventTypeWarning, ReasonMetadataUpdateFailed,
			"Failed to update version metadata for buildID %q: %v", vcfg.BuildID, err)
		return fmt.Errorf("unable to update metadata after setting current deployment: %w", err)
	}

	return nil
}

func (r *TemporalWorkerDeploymentReconciler) executePlan(ctx context.Context, l logr.Logger, workerDeploy *temporaliov1alpha1.TemporalWorkerDeployment, temporalClient sdkclient.Client, p *plan) error {
	if err := r.executeK8sOperations(ctx, l, workerDeploy, p); err != nil {
		return err
	}

	deploymentHandler := temporalClient.WorkerDeploymentClient().GetHandle(p.WorkerDeploymentName)

	if err := r.startTestWorkflows(ctx, l, workerDeploy, temporalClient, p); err != nil {
		return err
	}

	if err := r.updateVersionConfig(ctx, l, workerDeploy, deploymentHandler, p); err != nil {
		return err
	}

	// Patch any WRTs that are missing the owner reference to this TWD.
	// Failures are logged but do not block the worker resource template apply step below —
	// a WRT may have been deleted between plan generation and execution, and
	// applying resources is more important than setting owner references.
	for _, ownerPatch := range p.EnsureWRTOwnerRefs {
		if err := r.Patch(ctx, ownerPatch.Patched, client.MergeFrom(ownerPatch.Base)); err != nil {
			l.Error(err, "failed to patch WRT with controller reference",
				"namespace", ownerPatch.Patched.Namespace,
				"name", ownerPatch.Patched.Name,
			)
		}
	}

	// Apply worker resource templates via Server-Side Apply.
	// Partial failure isolation: all resources are attempted even if some fail;
	// errors are collected and returned together.
	type wrtKey struct{ namespace, name string }
	type applyResult struct {
		buildID      string
		resourceName string
		hash         string // rendered hash recorded on successful apply; "" on error
		err          error
		skipped      bool // true if the apply was skipped because the rendered hash is unchanged
	}
	wrtResults := make(map[wrtKey][]applyResult)

	for _, apply := range p.ApplyWorkerResources {
		key := wrtKey{apply.WRTNamespace, apply.WRTName}

		// Render failure: record the error in status without attempting an SSA apply.
		if apply.RenderError != nil {
			l.Error(apply.RenderError, "skipping SSA apply due to render failure",
				"wrt", apply.WRTName,
				"buildID", apply.BuildID,
			)
			wrtResults[key] = append(wrtResults[key], applyResult{
				buildID: apply.BuildID,
				err:     apply.RenderError,
			})
			continue
		}

		// Skip the SSA apply if the rendered object is identical to what was last
		// successfully applied. This avoids unnecessary API server load at scale
		// (hundreds of TWDs × hundreds of versions × multiple WRTs).
		// An empty RenderedHash means hashing failed; always apply in that case.
		if apply.RenderedHash != "" && apply.RenderedHash == apply.LastAppliedHash {
			wrtResults[key] = append(wrtResults[key], applyResult{
				buildID:      apply.BuildID,
				resourceName: apply.Resource.GetName(),
				hash:         apply.RenderedHash,
				skipped:      true,
			})
			continue
		}

		l.Info("applying rendered worker resource template",
			"name", apply.Resource.GetName(),
			"kind", apply.Resource.GetKind(),
			"fieldManager", k8s.FieldManager,
		)
		// client.Apply uses Server-Side Apply, which is a create-or-update operation:
		// if the resource does not yet exist the API server creates it; if it already
		// exists the API server merges only the fields owned by this field manager,
		// leaving fields owned by other managers (e.g. a user patching the resource
		// directly via kubectl) untouched.
		// Note: the HPA controller does not compete with SSA here — it only writes to
		// the status subresource (currentReplicas, desiredReplicas, conditions), which
		// is a separate API endpoint that SSA apply never touches.
		// client.ForceOwnership allows this field manager to claim any fields that were
		// previously owned by a different manager (e.g. after a field manager rename).
		applyErr := r.Client.Patch(
			ctx,
			apply.Resource,
			client.Apply,
			client.ForceOwnership,
			client.FieldOwner(k8s.FieldManager),
		)
		if applyErr != nil {
			l.Error(applyErr, "unable to apply rendered worker resource template",
				"name", apply.Resource.GetName(),
				"kind", apply.Resource.GetKind(),
			)
		}
		// Only record the hash on success so a transient error forces a retry next cycle.
		var appliedHash string
		if applyErr == nil {
			appliedHash = apply.RenderedHash
		}
		wrtResults[key] = append(wrtResults[key], applyResult{
			buildID:      apply.BuildID,
			resourceName: apply.Resource.GetName(),
			hash:         appliedHash,
			err:          applyErr,
		})
	}

	// Write per-Build-ID status back to each WRT.
	// Done after all applies so a single failed apply does not prevent status
	// updates for the other (WRT, Build ID) pairs.
	// If every result for a WRT was skipped (hash unchanged since last successful
	// apply), the status is already correct — skip the status write entirely to
	// avoid unnecessary resourceVersion bumps.
	var applyErrs, statusErrs []error
	for key, results := range wrtResults {
		allSkipped := true
		for _, res := range results {
			if !res.skipped {
				allSkipped = false
				break
			}
		}
		if allSkipped {
			continue
		}

		wrt := &temporaliov1alpha1.WorkerResourceTemplate{}
		if err := r.Get(ctx, types.NamespacedName{Namespace: key.namespace, Name: key.name}, wrt); err != nil {
			statusErrs = append(statusErrs, fmt.Errorf("get WRT %s/%s for status update: %w", key.namespace, key.name, err))
			continue
		}

		// Build the new per-Build-ID version list.
		// For BuildIDs whose apply was skipped (rendered hash unchanged), we copy the
		// existing status entry verbatim — the stored LastAppliedHash and
		// LastTransitionTime must not be overwritten with a no-op entry.
		existingByBuildID := make(map[string]temporaliov1alpha1.WorkerResourceTemplateVersionStatus, len(wrt.Status.Versions))
		for _, v := range wrt.Status.Versions {
			existingByBuildID[v.BuildID] = v
		}

		versions := make([]temporaliov1alpha1.WorkerResourceTemplateVersionStatus, 0, len(results))
		anyFailed := false
		for _, result := range results {
			if result.skipped {
				if existing, ok := existingByBuildID[result.buildID]; ok {
					versions = append(versions, existing)
				}
				continue
			}
			var applyErr string
			var appliedGeneration int64
			if result.err != nil {
				applyErrs = append(applyErrs, result.err)
				applyErr = result.err.Error()
				anyFailed = true
				// 0 means "unset" / "not yet successfully applied at current generation".
				// Failure Message and LastTransitionTime are still recorded below.
				appliedGeneration = 0
			} else {
				appliedGeneration = wrt.Generation
			}
			versions = append(versions, k8s.WorkerResourceTemplateVersionStatusForBuildID(
				result.buildID, result.resourceName, appliedGeneration, result.hash, applyErr,
			))
		}

		// Compute the top-level Ready condition.
		// True:  all active Build IDs applied at the current generation (or already current —
		//        skipped ones carry a non-zero LastAppliedGeneration from their last successful apply).
		// False: one or more apply calls failed this cycle.
		condStatus := metav1.ConditionTrue
		condReason := temporaliov1alpha1.ReasonWRTAllVersionsApplied
		condMessage := ""
		if anyFailed {
			condStatus = metav1.ConditionFalse
			condReason = temporaliov1alpha1.ReasonWRTApplyFailed
			// Use the first apply error as the condition message; full per-version details
			// are available in status.versions[*].applyError.
			for _, result := range results {
				if result.err != nil {
					condMessage = result.err.Error()
					break
				}
			}
		}
		apimeta.SetStatusCondition(&wrt.Status.Conditions, metav1.Condition{
			Type:               temporaliov1alpha1.ConditionReady,
			Status:             condStatus,
			Reason:             condReason,
			Message:            condMessage,
			ObservedGeneration: wrt.Generation,
		})

		// Sort the versions by BuildID for deterministic status output.
		slices.SortFunc(versions, func(a, b temporaliov1alpha1.WorkerResourceTemplateVersionStatus) int {
			return strings.Compare(a.BuildID, b.BuildID)
		})
		wrt.Status.Versions = versions
		if err := r.Status().Update(ctx, wrt); err != nil {
			statusErrs = append(statusErrs, fmt.Errorf("update status for WRT %s/%s: %w", key.namespace, key.name, err))
		}
	}

	return errors.Join(append(applyErrs, statusErrs...)...)
}
