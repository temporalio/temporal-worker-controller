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
	ctrl "sigs.k8s.io/controller-runtime"
)

func (r *TemporalWorkerDeploymentReconciler) generateStatus(
	ctx context.Context,
	l logr.Logger,
	temporalClient temporalclient.Client,
	req ctrl.Request,
	workerDeploy *temporaliov1alpha1.TemporalWorkerDeployment,
	temporalState *temporal.TemporalWorkerState,
	k8sState *k8s.DeploymentState,
) (*temporaliov1alpha1.TemporalWorkerDeploymentStatus, error) {
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
