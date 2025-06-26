// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package controller

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	temporalClient "go.temporal.io/sdk/client"
	ctrl "sigs.k8s.io/controller-runtime"

	temporaliov1alpha1 "github.com/DataDog/temporal-worker-controller/api/v1alpha1"
	"github.com/DataDog/temporal-worker-controller/internal/k8s"
	"github.com/DataDog/temporal-worker-controller/internal/temporal"
)

const controllerIdentity = "temporal-worker-controller"

func (r *TemporalWorkerDeploymentReconciler) generateStatus(
	ctx context.Context,
	l logr.Logger,
	temporalClient temporalClient.Client,
	req ctrl.Request,
	workerDeploy *temporaliov1alpha1.TemporalWorkerDeployment,
) (*temporaliov1alpha1.TemporalWorkerDeploymentStatus, error) {
	workerDeploymentName := k8s.ComputeWorkerDeploymentName(workerDeploy)
	targetVersionID := k8s.ComputeVersionID(workerDeploy)

	// Fetch Kubernetes deployment state
	k8sState, err := k8s.GetDeploymentState(
		ctx,
		r.Client,
		req.Namespace,
		req.Name,
		workerDeploymentName,
	)
	if err != nil {
		return nil, fmt.Errorf("unable to get Kubernetes deployment state: %w", err)
	}

	// Fetch Temporal worker deployment state
	temporalState, err := temporal.GetWorkerDeploymentState(
		ctx,
		temporalClient,
		workerDeploymentName,
		workerDeploy.Spec.WorkerOptions.TemporalNamespace,
	)
	if err != nil {
		return nil, fmt.Errorf("unable to get Temporal worker deployment state: %w", err)
	}

	// Fetch test workflow status for the desired version
	if targetVersionID != temporalState.CurrentVersionID {
		testWorkflows, err := temporal.GetTestWorkflowStatus(
			ctx,
			temporalClient,
			workerDeploymentName,
			targetVersionID,
			workerDeploy,
		)
		if err != nil {
			l.Error(err, "error getting test workflow status")
			// Continue without test workflow status
		}

		// Add test workflow status to version info if it doesn't exist
		if versionInfo, exists := temporalState.Versions[targetVersionID]; exists {
			versionInfo.TestWorkflows = append(versionInfo.TestWorkflows, testWorkflows...)
		}
	}

	// Use the state mapper to convert state objects to CRD status
	stateMapper := newStateMapper(k8sState, temporalState)
	status := stateMapper.mapToStatus(targetVersionID)

	return status, nil
}
