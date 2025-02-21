// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package controller

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	"go.temporal.io/api/common/v1"
	"go.temporal.io/api/deployment/v1"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/taskqueue/v1"
	"go.temporal.io/api/workflow/v1"
	"go.temporal.io/api/workflowservice/v1"
	"google.golang.org/protobuf/types/known/durationpb"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func (r *TemporalWorkerReconciler) executePlan(ctx context.Context, l logr.Logger, temporalClient workflowservice.WorkflowServiceClient, p *plan) error {
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

	for _, wf := range p.startTestWorkflows {
		if _, err := temporalClient.StartWorkflowExecution(ctx, &workflowservice.StartWorkflowExecutionRequest{
			Namespace:  p.TemporalNamespace,
			WorkflowId: wf.workflowID,
			WorkflowType: &common.WorkflowType{
				Name: wf.workflowType,
			},
			TaskQueue: &taskqueue.TaskQueue{
				Name: wf.taskQueue,
			},
			VersioningOverride: &workflow.VersioningOverride{
				Behavior: enums.VERSIONING_BEHAVIOR_PINNED,
				Deployment: &deployment.Deployment{
					SeriesName: p.DeploymentSeries,
					BuildId:    wf.buildID,
				},
			},
			// TODO(jlegrone): make this configurable
			WorkflowExecutionTimeout: durationpb.New(time.Hour),
			Identity:                 "",
			RequestId:                "",
			WorkflowIdReusePolicy:    enums.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE,
			WorkflowIdConflictPolicy: enums.WORKFLOW_ID_CONFLICT_POLICY_FAIL,
		}); err != nil {
			return fmt.Errorf("unable to start test workflow execution: %w", err)
		}
	}

	// Register default version or ramp
	if vcfg := p.UpdateVersionConfig; vcfg != nil {
		if vcfg.setDefault {
			l.Info("registering new default version", "buildID", vcfg.buildID)
			if _, err := temporalClient.SetCurrentDeployment(ctx, &workflowservice.SetCurrentDeploymentRequest{
				Namespace: p.TemporalNamespace,
				Deployment: &deployment.Deployment{
					SeriesName: p.DeploymentSeries,
					BuildId:    vcfg.buildID,
				},
				Identity: "temporal-worker-controller", // TODO(jlegrone): Set this to a unique identity, should match metadata.
				UpdateMetadata: &deployment.UpdateDeploymentMetadata{
					UpsertEntries: map[string]*common.Payload{
						// TODO(jlegrone): Add controller identity
						"temporal.io/managed-by": nil,
					},
				},
			}); err != nil {
				return fmt.Errorf("unable to set current deployment: %w", err)
			}
		} else if ramp := vcfg.rampPercentage; ramp > 0 {
			// Apply ramp
			l.Info("applying ramp", "buildID", p.UpdateVersionConfig.buildID, "percentage", p.UpdateVersionConfig.rampPercentage)
			return fmt.Errorf("ramp not implemented")
		}
	}

	return nil
}
