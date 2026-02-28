// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	"github.com/temporalio/temporal-worker-controller/internal/k8s"
	"github.com/temporalio/temporal-worker-controller/internal/temporal"
	enumspb "go.temporal.io/api/enums/v1"
	sdkclient "go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func (r *TemporalWorkerDeploymentReconciler) executePlan(ctx context.Context, l logr.Logger, temporalClient sdkclient.Client, p *plan) error {
	// Create deployment
	if p.CreateDeployment != nil {
		l.Info("creating deployment", "deployment", p.CreateDeployment)
		if err := r.Create(ctx, p.CreateDeployment); err != nil {
			l.Error(err, "unable to create deployment", "deployment", p.CreateDeployment)
			return err
		}
	}

	// Delete deployments
	for _, d := range p.DeleteDeployments {
		l.Info("deleting deployment", "deployment", d)
		if err := r.Delete(ctx, d); err != nil {
			l.Error(err, "unable to delete deployment", "deployment", d)
			return err
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
			return fmt.Errorf("unable to scale deployment: %w", err)
		}
	}

	// Update deployments
	for _, d := range p.UpdateDeployments {
		l.Info("updating deployment", "deployment", d.Name, "namespace", d.Namespace)
		if err := r.Update(ctx, d); err != nil {
			l.Error(err, "unable to update deployment", "deployment", d)
			return fmt.Errorf("unable to update deployment: %w", err)
		}
	}

	// Get deployment handler
	deploymentHandler := temporalClient.WorkerDeploymentClient().GetHandle(p.WorkerDeploymentName)

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
					BuildId:        wf.buildID,
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
			return fmt.Errorf("unable to start test workflow execution: %w", err)
		}
	}

	// Register current version or ramps
	if vcfg := p.UpdateVersionConfig; vcfg != nil {
		if vcfg.SetCurrent {
			l.Info("registering new current version", "buildID", vcfg.BuildID)
			if _, err := deploymentHandler.SetCurrentVersion(ctx, sdkclient.WorkerDeploymentSetCurrentVersionOptions{
				BuildID:       vcfg.BuildID,
				ConflictToken: vcfg.ConflictToken,
				Identity:      getControllerIdentity(),
			}); err != nil {
				return fmt.Errorf("unable to set current deployment version: %w", err)
			}
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
				return fmt.Errorf("unable to set ramping deployment version: %w", err)
			}
		}
		if _, err := deploymentHandler.UpdateVersionMetadata(ctx, sdkclient.WorkerDeploymentUpdateVersionMetadataOptions{
			Version: worker.WorkerDeploymentVersion{
				DeploymentName: p.WorkerDeploymentName,
				BuildId:        vcfg.BuildID,
			},
			MetadataUpdate: sdkclient.WorkerDeploymentMetadataUpdate{
				UpsertEntries: map[string]interface{}{
					controllerIdentityMetadataKey: getControllerIdentity(),
					controllerVersionMetadataKey:  getControllerVersion(),
				},
			},
		}); err != nil { // would be cool to do this atomically with the update
			return fmt.Errorf("unable to update metadata after setting current deployment: %w", err)
		}
	}

	for _, buildId := range p.RemoveIgnoreLastModifierBuilds {
		if _, err := deploymentHandler.UpdateVersionMetadata(ctx, sdkclient.WorkerDeploymentUpdateVersionMetadataOptions{
			Version: worker.WorkerDeploymentVersion{
				DeploymentName: p.WorkerDeploymentName,
				BuildId:        buildId,
			},
			MetadataUpdate: sdkclient.WorkerDeploymentMetadataUpdate{
				RemoveEntries: []string{temporal.IgnoreLastModifierKey},
			},
		}); err != nil {
			return fmt.Errorf("unable to update metadata to remove %s deployment: %w", temporal.IgnoreLastModifierKey, err)
		}
	}

	// Patch any TWORs that are missing the owner reference to this TWD.
	// Failures are logged but do not block the owned-resource apply step below —
	// a TWOR may have been deleted between plan generation and execution, and
	// applying resources is more important than setting owner references.
	for _, ownerPatch := range p.EnsureTWOROwnerRefs {
		if err := r.Patch(ctx, ownerPatch.Patched, client.MergeFrom(ownerPatch.Base)); err != nil {
			l.Error(err, "failed to patch TWOR with controller reference",
				"namespace", ownerPatch.Patched.Namespace,
				"name", ownerPatch.Patched.Name,
			)
		}
	}

	// Apply owned resources via Server-Side Apply.
	// Partial failure isolation: all resources are attempted even if some fail;
	// errors are collected and returned together.
	type tworKey struct{ namespace, name string }
	type applyResult struct {
		buildID      string
		resourceName string
		err          error
	}
	tworResults := make(map[tworKey][]applyResult)

	for _, apply := range p.ApplyOwnedResources {
		l.Info("applying owned resource",
			"name", apply.Resource.GetName(),
			"kind", apply.Resource.GetKind(),
			"fieldManager", apply.FieldManager,
		)
		// client.Apply uses Server-Side Apply, which is a create-or-update operation:
		// if the resource does not yet exist the API server creates it; if it already
		// exists the API server merges only the fields owned by this field manager,
		// leaving fields owned by other managers (e.g. the HPA controller) untouched.
		// client.ForceOwnership allows this field manager to claim any fields that were
		// previously owned by a different manager (e.g. after a field manager rename).
		applyErr := r.Client.Patch(
			ctx,
			apply.Resource,
			client.Apply,
			client.ForceOwnership,
			client.FieldOwner(apply.FieldManager),
		)
		if applyErr != nil {
			l.Error(applyErr, "unable to apply owned resource",
				"name", apply.Resource.GetName(),
				"kind", apply.Resource.GetKind(),
			)
		}
		key := tworKey{apply.TWORNamespace, apply.TWORName}
		tworResults[key] = append(tworResults[key], applyResult{
			buildID:      apply.BuildID,
			resourceName: apply.Resource.GetName(),
			err:          applyErr,
		})
	}

	// Write per-Build-ID status back to each TWOR.
	// Done after all applies so a single failed apply does not prevent status
	// updates for the other (TWOR, Build ID) pairs.
	var applyErrs, statusErrs []error
	for key, results := range tworResults {
		versions := make([]temporaliov1alpha1.OwnedResourceVersionStatus, 0, len(results))
		for _, result := range results {
			var msg string
			if result.err != nil {
				applyErrs = append(applyErrs, result.err)
				msg = result.err.Error()
			}
			versions = append(versions, k8s.OwnedResourceVersionStatusForBuildID(
				result.buildID, result.resourceName, result.err == nil, msg,
			))
		}

		twor := &temporaliov1alpha1.TemporalWorkerOwnedResource{}
		if err := r.Get(ctx, types.NamespacedName{Namespace: key.namespace, Name: key.name}, twor); err != nil {
			statusErrs = append(statusErrs, fmt.Errorf("get TWOR %s/%s for status update: %w", key.namespace, key.name, err))
			continue
		}
		twor.Status.Versions = versions
		if err := r.Status().Update(ctx, twor); err != nil {
			statusErrs = append(statusErrs, fmt.Errorf("update status for TWOR %s/%s: %w", key.namespace, key.name, err))
		}
	}

	return errors.Join(append(applyErrs, statusErrs...)...)
}
