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
	workflowType string
	workflowID   string
	buildID      string
	taskQueue    string
	input        []byte
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
		w.Status.LastModifierIdentity != "" &&
		!temporalState.IgnoreLastModifier {
		l.Info(fmt.Sprintf("Forcing Manual rollout strategy since Worker Deployment was modified by a user with a different identity '%s'; to allow controller to make changes again, set 'temporal.io/ignore-last-modifier=true' in the metadata of your Current or Ramping Version; see ownership runbook at docs/ownership.md for more details.", w.Status.LastModifierIdentity))
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
		workerDeploymentName,
		r.MaxDeploymentVersionsIneligibleForDeletion,
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
	var gateInput []byte
	var gateInputErr error
	if rolloutStrategy.Gate != nil {
		gateInput, gateInputErr = r.resolveGateInput(ctx, w)
		if gateInputErr != nil {
			return nil, fmt.Errorf("unable to resolve gate input: %w", gateInputErr)
		}
	}
	for _, wf := range planResult.TestWorkflows {
		plan.startTestWorkflows = append(plan.startTestWorkflows, startWorkflowConfig{
			workflowType: wf.WorkflowType,
			workflowID:   wf.WorkflowID,
			buildID:      wf.BuildID,
			taskQueue:    wf.TaskQueue,
			input:        gateInput,
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

// resolveGateInput resolves the gate input from inline JSON or from a referenced ConfigMap/Secret
func (r *TemporalWorkerDeploymentReconciler) resolveGateInput(
	ctx context.Context,
	w *temporaliov1alpha1.TemporalWorkerDeployment,
) ([]byte, error) {
	gate := w.Spec.RolloutStrategy.Gate
	if gate == nil {
		return nil, nil
	}
	// If both are set, return error (webhook should prevent this, but double-check)
	if gate.Input != nil && gate.InputFrom != nil {
		return nil, fmt.Errorf("both spec.rollout.gate.input and spec.rollout.gate.inputFrom are set")
	}
	if gate.Input != nil {
		return gate.Input.Raw, nil
	}
	if gate.InputFrom == nil {
		return nil, nil
	}
	// Exactly one of ConfigMapKeyRef or SecretKeyRef should be set
	if (gate.InputFrom.ConfigMapKeyRef == nil && gate.InputFrom.SecretKeyRef == nil) ||
		(gate.InputFrom.ConfigMapKeyRef != nil && gate.InputFrom.SecretKeyRef != nil) {
		return nil, fmt.Errorf("spec.rollout.gate.inputFrom must set exactly one of configMapKeyRef or secretKeyRef")
	}
	if cmRef := gate.InputFrom.ConfigMapKeyRef; cmRef != nil {
		cm := &corev1.ConfigMap{}
		if err := r.Client.Get(ctx, types.NamespacedName{Namespace: w.Namespace, Name: cmRef.Name}, cm); err != nil {
			return nil, fmt.Errorf("failed to get ConfigMap %s/%s: %w", w.Namespace, cmRef.Name, err)
		}
		if val, ok := cm.Data[cmRef.Key]; ok {
			return []byte(val), nil
		}
		if bval, ok := cm.BinaryData[cmRef.Key]; ok {
			return bval, nil
		}
		return nil, fmt.Errorf("key %q not found in ConfigMap %s/%s", cmRef.Key, w.Namespace, cmRef.Name)
	}
	if secRef := gate.InputFrom.SecretKeyRef; secRef != nil {
		sec := &corev1.Secret{}
		if err := r.Client.Get(ctx, types.NamespacedName{Namespace: w.Namespace, Name: secRef.Name}, sec); err != nil {
			return nil, fmt.Errorf("failed to get Secret %s/%s: %w", w.Namespace, secRef.Name, err)
		}
		if bval, ok := sec.Data[secRef.Key]; ok {
			return bval, nil
		}
		return nil, fmt.Errorf("key %q not found in Secret %s/%s", secRef.Key, w.Namespace, secRef.Name)
	}
	return nil, nil
}

// Create a new deployment with owner reference
func (r *TemporalWorkerDeploymentReconciler) newDeployment(
	w *temporaliov1alpha1.TemporalWorkerDeployment,
	buildID string,
	connection temporaliov1alpha1.TemporalConnectionSpec,
) (*appsv1.Deployment, error) {
	return k8s.NewDeploymentWithControllerRef(w, buildID, connection, r.Scheme)
}
