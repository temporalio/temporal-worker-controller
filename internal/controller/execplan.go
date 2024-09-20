// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package controller

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	"go.temporal.io/api/taskqueue/v1"
	"go.temporal.io/api/workflowservice/v1"
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

	// Register default version or ramp
	if vcfg := p.UpdateVersionConfig; vcfg != nil {
		if vcfg.setDefault {
			// Check out API here:
			// https://github.com/temporalio/api/blob/cfa1a15b960920a47de8ec272873a4ee4db574c4/temporal/api/workflowservice/v1/request_response.proto#L1073-L1132
			l.Info("registering new default version", "buildID", vcfg.buildID)

			if _, err := temporalClient.UpdateWorkerVersioningRules(ctx, &workflowservice.UpdateWorkerVersioningRulesRequest{
				Namespace:     p.TemporalNamespace,
				TaskQueue:     p.TaskQueue,
				ConflictToken: vcfg.conflictToken,
				Operation: &workflowservice.UpdateWorkerVersioningRulesRequest_InsertAssignmentRule{InsertAssignmentRule: &workflowservice.UpdateWorkerVersioningRulesRequest_InsertBuildIdAssignmentRule{
					RuleIndex: 0,
					Rule: &taskqueue.BuildIdAssignmentRule{
						TargetBuildId: vcfg.buildID,
						Ramp:          nil,
					},
				}},
			}); err != nil {
				return fmt.Errorf("unable to update versioning rules: %w", err)
			}

			//rules, err := r.WorkflowServiceClient.GetWorkerVersioningRules(ctx, &workflowservice.GetWorkerVersioningRulesRequest{
			//	Namespace: p.TemporalNamespace,
			//	TaskQueue: p.TaskQueue,
			//})
			//if err != nil {
			//	return fmt.Errorf("unable to get versioning rules: %w", err)
			//}
			//if len(rules.GetAssignmentRules()) > 0 {
			//	if _, err := r.WorkflowServiceClient.UpdateWorkerVersioningRules(ctx, &workflowservice.UpdateWorkerVersioningRulesRequest{
			//		Namespace:     p.TemporalNamespace,
			//		TaskQueue:     p.TaskQueue,
			//		ConflictToken: vcfg.conflictToken,
			//		Operation: &workflowservice.UpdateWorkerVersioningRulesRequest_ReplaceAssignmentRule{ReplaceAssignmentRule: &workflowservice.UpdateWorkerVersioningRulesRequest_ReplaceBuildIdAssignmentRule{
			//			RuleIndex: 0,
			//			Rule: &taskqueue.BuildIdAssignmentRule{
			//				TargetBuildId: vcfg.buildID,
			//				Ramp:          nil,
			//			},
			//			Force: false,
			//		}},
			//	}); err != nil {
			//		return fmt.Errorf("unable to update versioning rules: %w", err)
			//	}
			//}
		} else if ramp := vcfg.rampPercentage; ramp > 0 {
			// Apply ramp
			l.Info("applying ramp", "buildID", p.UpdateVersionConfig.buildID, "percentage", p.UpdateVersionConfig.rampPercentage)
			// TODO(jlegrone): override existing ramp value?
			_, err := temporalClient.UpdateWorkerVersioningRules(ctx, &workflowservice.UpdateWorkerVersioningRulesRequest{
				Namespace:     p.TemporalNamespace,
				TaskQueue:     p.TaskQueue,
				ConflictToken: vcfg.conflictToken,
				Operation: &workflowservice.UpdateWorkerVersioningRulesRequest_InsertAssignmentRule{InsertAssignmentRule: &workflowservice.UpdateWorkerVersioningRulesRequest_InsertBuildIdAssignmentRule{
					RuleIndex: 0,
					Rule: &taskqueue.BuildIdAssignmentRule{
						TargetBuildId: vcfg.buildID,
						Ramp: &taskqueue.BuildIdAssignmentRule_PercentageRamp{
							PercentageRamp: &taskqueue.RampByPercentage{
								RampPercentage: float32(ramp),
							},
						},
					},
				}},
			})
			if err != nil {
				return fmt.Errorf("unable to update versioning rules: %w", err)
			}
		}
	}

	return nil
}
