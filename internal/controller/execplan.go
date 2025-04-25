// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package controller

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	"go.temporal.io/api/enums/v1"
	sdkclient "go.temporal.io/sdk/client"
	"go.temporal.io/sdk/workflow"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

	// Get deployment handler
	deploymentHandler := temporalClient.WorkerDeploymentClient().GetHandle(p.WorkerDeploymentName)

	for _, wf := range p.startTestWorkflows {
		err := awaitVersionRegistration(ctx, l, deploymentHandler, p.TemporalNamespace, wf.versionID)
		if err != nil {
			return fmt.Errorf("error waiting for version to register, did your pollers start successfully?: %w", err)
		}
		if _, err = temporalClient.ExecuteWorkflow(ctx, sdkclient.StartWorkflowOptions{
			ID:                       wf.workflowID,
			TaskQueue:                wf.taskQueue,
			WorkflowExecutionTimeout: time.Hour,
			WorkflowIDReusePolicy:    enums.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE,
			WorkflowIDConflictPolicy: enums.WORKFLOW_ID_CONFLICT_POLICY_FAIL,
			VersioningOverride: sdkclient.VersioningOverride{
				Behavior:      workflow.VersioningBehaviorPinned,
				PinnedVersion: wf.versionID,
			},
		}, wf.workflowType); err != nil {
			return fmt.Errorf("unable to start test workflow execution: %w", err)
		}
	}

	// Register default version or ramp
	if vcfg := p.UpdateVersionConfig; vcfg != nil {
		if vcfg.setDefault {
			err := awaitVersionRegistration(ctx, l, deploymentHandler, p.TemporalNamespace, vcfg.versionID)
			if err != nil {
				return fmt.Errorf("error waiting for version to register, did your pollers start successfully?: %w", err)
			}

			l.Info("registering new default version", "version", vcfg.versionID)
			resp, err := deploymentHandler.Describe(ctx, sdkclient.WorkerDeploymentDescribeOptions{})
			if err != nil {
				return fmt.Errorf("unable to describe worker deployment: %w", err)
			}
			if _, err := deploymentHandler.SetCurrentVersion(ctx, sdkclient.WorkerDeploymentSetCurrentVersionOptions{
				Version:       vcfg.versionID,
				ConflictToken: resp.ConflictToken,
				Identity:      "temporal-worker-controller", // TODO(jlegrone): Set this to a unique identity, should match metadata.
			}); err != nil {
				return fmt.Errorf("unable to set current deployment version: %w", err)
			}
			if _, err := deploymentHandler.UpdateVersionMetadata(ctx, sdkclient.WorkerDeploymentUpdateVersionMetadataOptions{
				Version: vcfg.versionID,
				MetadataUpdate: sdkclient.WorkerDeploymentMetadataUpdate{
					UpsertEntries: map[string]interface{}{
						// TODO(jlegrone): Add controller identity
						// TODO(carlydf): Add info about which k8s resource initiated the last write to the deployment
						"temporal.io/managed-by": nil,
					},
				},
			}); err != nil { // would be cool to do this atomically with the update
				return fmt.Errorf("unable to update metadata after setting current deployment: %w", err)
			}
		} else if ramp := vcfg.rampPercentage; ramp > 0 || vcfg.unsetRamp { // TODO(carlydf): Support setting any ramp in [0,100]
			err := awaitVersionRegistration(ctx, l, deploymentHandler, p.TemporalNamespace, vcfg.versionID)
			if err != nil {
				return fmt.Errorf("error waiting for version to register, did your pollers start successfully?: %w", err)
			}

			desiredRampingVersion := vcfg.versionID
			if vcfg.unsetRamp {
				desiredRampingVersion = ""
			}

			l.Info("applying ramp", "version", desiredRampingVersion, "percentage", p.UpdateVersionConfig.rampPercentage)
			resp, err := deploymentHandler.Describe(ctx, sdkclient.WorkerDeploymentDescribeOptions{})
			if err != nil {
				return fmt.Errorf("unable to describe worker deployment: %w", err)
			}
			if _, err := deploymentHandler.SetRampingVersion(ctx, sdkclient.WorkerDeploymentSetRampingVersionOptions{
				Version:       desiredRampingVersion,
				Percentage:    vcfg.rampPercentage,
				ConflictToken: resp.ConflictToken,
				Identity:      "temporal-worker-controller", // TODO(jlegrone): Set this to a unique identity, should match metadata.
			}); err != nil {
				return fmt.Errorf("unable to set ramping deployment: %w", err)
			}
			if _, err := deploymentHandler.UpdateVersionMetadata(ctx, sdkclient.WorkerDeploymentUpdateVersionMetadataOptions{
				Version: vcfg.versionID,
				MetadataUpdate: sdkclient.WorkerDeploymentMetadataUpdate{
					UpsertEntries: map[string]interface{}{
						// TODO(jlegrone): Add controller identity
						// TODO(carlydf): Add info about which k8s resource initiated the last write to the deployment
						"temporal.io/managed-by": nil,
					},
				},
			}); err != nil { // would be cool to do this atomically with the update
				return fmt.Errorf("unable to update metadata after setting ramping deployment: %w", err)
			}
		}
	}

	return nil
}
