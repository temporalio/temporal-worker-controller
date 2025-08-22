// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package controller

import (
	"context"
	"errors"
	"fmt"
	"github.com/go-logr/logr"
	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	"github.com/temporalio/temporal-worker-controller/internal/k8s"
	"github.com/temporalio/temporal-worker-controller/internal/temporal"
	"go.temporal.io/api/serviceerror"
	temporalclient "go.temporal.io/sdk/client"
	ctrl "sigs.k8s.io/controller-runtime"
)

// ControllerIdentity is the identity the controller passes to all write calls.
const ControllerIdentity = "temporal-worker-controller"

func (r *TemporalWorkerDeploymentReconciler) generateStatus(
	ctx context.Context,
	l logr.Logger,
	temporalClient temporalclient.Client,
	req ctrl.Request,
	workerDeploy *temporaliov1alpha1.TemporalWorkerDeployment,
	temporalState *temporal.TemporalWorkerState,
) (*temporaliov1alpha1.TemporalWorkerDeploymentStatus, error) {
	workerDeploymentName := k8s.ComputeWorkerDeploymentName(workerDeploy)
	targetBuildID := k8s.ComputeBuildID(workerDeploy)

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

	// Fetch test workflow status for the desired version
	if targetBuildID != temporalState.CurrentBuildID {
		taskQueueInfos, err := temporal.GetVersionTaskQueueInfos(ctx, temporalClient, workerDeploymentName, targetBuildID)
		var notFound *serviceerror.NotFound
		var testWorkflows []temporaliov1alpha1.WorkflowExecution
		if err == nil {
			testWorkflows, err = temporal.GetTestWorkflowStatus(
				ctx,
				temporalClient,
				workerDeploymentName,
				targetBuildID,
				taskQueueInfos,
			)
			if err != nil {
				l.Error(err, "error getting test workflow status")
				// Continue without test workflow status
			}
			// Add test workflow status to version info if the version exists
			versionInfo, exists := temporalState.Versions[targetBuildID]
			if !exists {
				panic("CARLY SHOULD SEE THIS")
			}
			versionInfo.TestWorkflows = append(versionInfo.TestWorkflows, testWorkflows...)
			for _, tqInfo := range taskQueueInfos {
				var HasUnversionedPoller bool
				if workerDeploy.Status.CurrentVersion == nil && // todo(carlydf): make sure that's what unversioned looks like in the status
					workerDeploy.Spec.RolloutStrategy.Strategy == temporaliov1alpha1.UpdateProgressive {
					// check whether task queues have unversioned pollers
					HasUnversionedPoller, err = temporal.HasUnversionedPoller(ctx, temporalClient, tqInfo)
					if err != nil {
						l.Error(err, fmt.Sprintf("error getting pollers of target version %s:%s task queue %s, could not verify presence of unversioned pollers",
							workerDeploymentName, targetBuildID, tqInfo.Name))
						// Continue without pollers, assume that there might be no unversioned pollers, so target version should immediately become current.
					}
				}
				if exists {
					versionInfo.TaskQueues = append(temporalState.Versions[targetBuildID].TaskQueues, temporaliov1alpha1.TaskQueue{
						Name:                 tqInfo.Name,
						Type:                 temporal.TaskQueueTypeString(tqInfo.Type),
						HasUnversionedPoller: HasUnversionedPoller,
					})
				}
			}
		} else if err != nil && !errors.As(err, &notFound) {
			// Ignore NotFound error, because if the version is not found, we know there are no task queues or test workflows to describe.
			l.Error(err, "unable to describe worker deployment version: %w", err)
		}
	}

	// Target build ID already computed above

	// Use the state mapper to convert state objects to CRD status
	stateMapper := newStateMapper(k8sState, temporalState, workerDeploymentName)
	status := stateMapper.mapToStatus(targetBuildID)

	return status, nil
}
