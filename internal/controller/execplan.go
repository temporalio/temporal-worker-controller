// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package controller

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	enumspb "go.temporal.io/api/enums/v1"
	sdkclient "go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
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
		if _, err := temporalClient.ExecuteWorkflow(ctx, sdkclient.StartWorkflowOptions{
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
		}, wf.workflowType); err != nil {
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
				Percentage:    vcfg.RampPercentage,
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
					controllerIdentityKey: getControllerIdentity(),
					controllerVersionKey:  getControllerVersion(),
				},
			},
		}); err != nil { // would be cool to do this atomically with the update
			return fmt.Errorf("unable to update metadata after setting current deployment: %w", err)
		}
	}

	return nil
}
