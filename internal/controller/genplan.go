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
	"k8s.io/apimachinery/pkg/types"
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

	// Build IDs of versions from which the controller should
	// remove IgnoreLastModifierKey from the version metadata
	RemoveIgnoreLastModifierBuilds []string
}

// startWorkflowConfig defines a workflow to be started
type startWorkflowConfig struct {
	workflowType  string
	workflowID    string
	buildID       string
	taskQueue     string
	input         []byte
	isInputSecret bool // indicates if input should be treated as sensitive
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
	targetBuildID := k8s.ComputeBuildID(w)

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
	if w.Status.LastModifierIdentity != getControllerIdentity() &&
		w.Status.LastModifierIdentity != serverDeleteVersionIdentity &&
		w.Status.LastModifierIdentity != "" &&
		!temporalState.IgnoreLastModifier {
		l.Info(fmt.Sprintf("Forcing Manual rollout strategy since Worker Deployment was modified by a user with a different identity '%s'; to allow controller to make changes again, set 'temporal.io/ignore-last-modifier=true' in the metadata of your Current or Ramping Version; see ownership runbook at docs/ownership.md for more details.", w.Status.LastModifierIdentity))
		rolloutStrategy.Strategy = temporaliov1alpha1.UpdateManual
	}

	// Resolve gate input if gate is configured
	var gateInput []byte
	var isGateInputSecret bool
	if rolloutStrategy.Gate != nil {
		// Fetch ConfigMap or Secret data if needed
		var configMapData map[string]string
		var configMapBinaryData map[string][]byte
		var secretData map[string][]byte

		if rolloutStrategy.Gate.InputFrom != nil {
			if cmRef := rolloutStrategy.Gate.InputFrom.ConfigMapKeyRef; cmRef != nil {
				cm := &corev1.ConfigMap{}
				if err := r.Client.Get(ctx, types.NamespacedName{Namespace: w.Namespace, Name: cmRef.Name}, cm); err != nil {
					return nil, fmt.Errorf("failed to get ConfigMap %s/%s: %w", w.Namespace, cmRef.Name, err)
				}
				configMapData = cm.Data
				configMapBinaryData = cm.BinaryData
			}
			if secRef := rolloutStrategy.Gate.InputFrom.SecretKeyRef; secRef != nil {
				sec := &corev1.Secret{}
				if err := r.Client.Get(ctx, types.NamespacedName{Namespace: w.Namespace, Name: secRef.Name}, sec); err != nil {
					return nil, fmt.Errorf("failed to get Secret %s/%s: %w", w.Namespace, secRef.Name, err)
				}
				secretData = sec.Data
			}
		}

		gateInput, isGateInputSecret, err = planner.ResolveGateInput(rolloutStrategy.Gate, w.Namespace, configMapData, configMapBinaryData, secretData)
		if err != nil {
			return nil, fmt.Errorf("unable to resolve gate input: %w", err)
		}
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
		workerDeploymentName,
		r.MaxDeploymentVersionsIneligibleForDeletion,
		gateInput,
		isGateInputSecret,
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

	plan.RemoveIgnoreLastModifierBuilds = planResult.RemoveIgnoreLastModifierBuilds

	// Convert test workflows
	for _, wf := range planResult.TestWorkflows {
		plan.startTestWorkflows = append(plan.startTestWorkflows, startWorkflowConfig{
			workflowType:  wf.WorkflowType,
			workflowID:    wf.WorkflowID,
			buildID:       wf.BuildID,
			taskQueue:     wf.TaskQueue,
			input:         []byte(wf.GateInput),
			isInputSecret: wf.IsInputSecret,
		})
	}

	// Handle deployment creation if needed
	if planResult.ShouldCreateDeployment {
		d, err := r.newDeployment(w, targetBuildID, connection)
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
