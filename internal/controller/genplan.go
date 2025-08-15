// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package controller

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	"github.com/temporalio/temporal-worker-controller/internal/k8s"
	"github.com/temporalio/temporal-worker-controller/internal/planner"
	"github.com/temporalio/temporal-worker-controller/internal/temporal"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
)

// plan holds the actions to execute during reconciliation
type plan struct {
	// Where to take actions
	TemporalNamespace    string
	WorkerDeploymentName string

	// Which actions to take
	DeleteDeployments []*appsv1.Deployment
	CreateDeployment  *appsv1.Deployment
	ScaleDeployments  map[*corev1.ObjectReference]uint32
	UpdateDeployments []*appsv1.Deployment
	// Register new versions as current or with ramp
	UpdateVersionConfig *planner.VersionConfig

	// Start a workflow
	startTestWorkflows []startWorkflowConfig
}

// startWorkflowConfig defines a workflow to be started
type startWorkflowConfig struct {
	workflowType string
	workflowID   string
	versionID    string
	taskQueue    string
}

// generatePlan creates a plan for the controller to execute
func (r *TemporalWorkerDeploymentReconciler) generatePlan(
	ctx context.Context,
	l logr.Logger,
	w *temporaliov1alpha1.TemporalWorkerDeployment,
	connection temporaliov1alpha1.TemporalConnectionSpec,
	temporalState *temporal.TemporalWorkerState,
) (*plan, error) {
	workerDeploymentName := k8s.ComputeWorkerDeploymentName(w)
	targetVersionID := k8s.ComputeVersionID(w)

	// Fetch Kubernetes deployment state
	k8sState, err := k8s.GetDeploymentState(
		ctx,
		r.Client,
		w.Namespace,
		w.Name,
		workerDeploymentName,
	)
	if err != nil {
		return nil, fmt.Errorf("unable to get Kubernetes deployment state: %w", err)
	}

	// Create a simple plan structure
	plan := &plan{
		TemporalNamespace:    w.Spec.WorkerOptions.TemporalNamespace,
		WorkerDeploymentName: workerDeploymentName,
		ScaleDeployments:     make(map[*corev1.ObjectReference]uint32),
	}

	// Check if we need to force manual strategy due to external modification
	rolloutStrategy := w.Spec.RolloutStrategy
	if w.Status.LastModifierIdentity != ControllerIdentity && w.Status.LastModifierIdentity != "" {
		l.Info("Forcing manual rollout strategy since deployment was modified externally")
		rolloutStrategy.Strategy = temporaliov1alpha1.UpdateManual
	}

	// Generate the plan using the planner package
	plannerConfig := &planner.Config{
		RolloutStrategy: rolloutStrategy,
	}

	planResult, err := planner.GeneratePlan(
		l,
		k8sState,
		&w.Status,
		&w.Spec,
		temporalState,
		connection,
		plannerConfig,
	)
	if err != nil {
		return nil, fmt.Errorf("error generating plan: %w", err)
	}

	// Convert planner result to controller plan
	plan.DeleteDeployments = planResult.DeleteDeployments
	plan.ScaleDeployments = planResult.ScaleDeployments
	plan.UpdateDeployments = planResult.UpdateDeployments

	// Convert version config
	plan.UpdateVersionConfig = planResult.VersionConfig

	// Convert test workflows
	for _, wf := range planResult.TestWorkflows {
		plan.startTestWorkflows = append(plan.startTestWorkflows, startWorkflowConfig{
			workflowType: wf.WorkflowType,
			workflowID:   wf.WorkflowID,
			versionID:    wf.VersionID,
			taskQueue:    wf.TaskQueue,
		})
	}

	// Handle deployment creation if needed
	if planResult.ShouldCreateDeployment {
		_, buildID, _ := k8s.SplitVersionID(targetVersionID)
		d, err := r.newDeployment(w, buildID, connection)
		if err != nil {
			return nil, err
		}
		plan.CreateDeployment = d
	}

	return plan, nil
}

// Create a new deployment with owner reference
func (r *TemporalWorkerDeploymentReconciler) newDeployment(
	w *temporaliov1alpha1.TemporalWorkerDeployment,
	buildID string,
	connection temporaliov1alpha1.TemporalConnectionSpec,
) (*appsv1.Deployment, error) {
	return k8s.NewDeploymentWithControllerRef(w, buildID, connection, r.Scheme)
}
