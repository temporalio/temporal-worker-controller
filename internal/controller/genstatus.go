// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package controller

import (
	"context"

	"github.com/go-logr/logr"
	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	"github.com/temporalio/temporal-worker-controller/internal/k8s"
	"github.com/temporalio/temporal-worker-controller/internal/temporal"
	temporalclient "go.temporal.io/sdk/client"
	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
)

// isGateWorkflowTerminalFailure reports whether a test/gate workflow status
// represents an ended-but-not-successful terminal state worth alerting on.
func isGateWorkflowTerminalFailure(status temporaliov1alpha1.WorkflowExecutionStatus) bool {
	switch status {
	case temporaliov1alpha1.WorkflowExecutionStatusFailed,
		temporaliov1alpha1.WorkflowExecutionStatusCanceled,
		temporaliov1alpha1.WorkflowExecutionStatusTerminated,
		temporaliov1alpha1.WorkflowExecutionStatusTimedOut:
		return true
	default:
		return false
	}
}

func (r *WorkerDeploymentReconciler) generateStatus(
	ctx context.Context,
	l logr.Logger,
	temporalClient temporalclient.Client,
	req ctrl.Request,
	workerDeploy *temporaliov1alpha1.WorkerDeployment,
	temporalState *temporal.TemporalWorkerState,
	k8sState *k8s.DeploymentState,
) (*temporaliov1alpha1.WorkerDeploymentStatus, error) {
	workerDeploymentName := k8s.ComputeWorkerDeploymentName(workerDeploy)
	targetBuildID := k8s.ComputeBuildID(workerDeploy)

	// Fetch test workflow status for the desired version
	if targetBuildID != temporalState.CurrentBuildID {
		testWorkflows, err := temporal.GetTestWorkflowStatus(
			ctx,
			temporalClient,
			workerDeploymentName,
			targetBuildID,
			workerDeploy,
			temporalState,
		)
		if err != nil {
			l.Error(err, "error getting test workflow status")
			// Continue without test workflow status
		}

		// Emit a Warning event the first time a gate/test workflow is observed to have
		// ended in a non-successful terminal state. Compare against the previous
		// reconcile's recorded status (still on workerDeploy.Status at this point, since
		// it hasn't been overwritten yet) so this doesn't re-fire on every loop.
		prevStatusByWorkflowID := make(map[string]temporaliov1alpha1.WorkflowExecutionStatus, len(workerDeploy.Status.TargetVersion.TestWorkflows))
		for _, wf := range workerDeploy.Status.TargetVersion.TestWorkflows {
			prevStatusByWorkflowID[wf.WorkflowID] = wf.Status
		}
		for _, wf := range testWorkflows {
			if !isGateWorkflowTerminalFailure(wf.Status) {
				continue
			}
			if prevStatusByWorkflowID[wf.WorkflowID] == wf.Status {
				continue
			}
			r.Recorder.Eventf(workerDeploy, corev1.EventTypeWarning, ReasonGateWorkflowFailed,
				"Gate/test workflow %s for version %s ended with status %s", wf.WorkflowID, targetBuildID, wf.Status)
		}

		// Add test workflow status to version info if it doesn't exist
		if versionInfo, exists := temporalState.Versions[targetBuildID]; exists {
			versionInfo.TestWorkflows = append(versionInfo.TestWorkflows, testWorkflows...)
		}
	}

	// Target build ID already computed above

	// Use the state mapper to convert state objects to CRD status
	stateMapper := newStateMapper(k8sState, temporalState, workerDeploymentName)
	status := stateMapper.mapToStatus(targetBuildID)

	return status, nil
}
