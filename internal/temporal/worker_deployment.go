// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package temporal

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	temporalClient "go.temporal.io/sdk/client"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	temporaliov1alpha1 "github.com/DataDog/temporal-worker-controller/api/v1alpha1"
)

// VersionInfo contains information about a specific version
type VersionInfo struct {
	VersionID      string
	Status         temporaliov1alpha1.VersionStatus
	DrainedSince   *time.Time
	RampPercentage float32
	TaskQueues     []temporaliov1alpha1.TaskQueue
	TestWorkflows  []temporaliov1alpha1.WorkflowExecution
}

// TemporalWorkerState represents the state of a worker deployment in Temporal
type TemporalWorkerState struct {
	DefaultVersionID     string
	VersionConflictToken []byte
	RampingVersionID     string
	RampPercentage       float32
	RampingSince         *metav1.Time
	Versions             map[string]*VersionInfo
	LastModifierIdentity string
}

// GetWorkerDeploymentState queries Temporal to get the state of a worker deployment
func GetWorkerDeploymentState(
	ctx context.Context,
	client temporalClient.Client,
	workerDeploymentName string,
	namespace string,
) (*TemporalWorkerState, error) {
	state := &TemporalWorkerState{
		Versions: make(map[string]*VersionInfo),
	}

	// Get deployment handler
	deploymentHandler := client.WorkerDeploymentClient().GetHandle(workerDeploymentName)

	// Describe the worker deployment
	resp, err := deploymentHandler.Describe(ctx, temporalClient.WorkerDeploymentDescribeOptions{})
	if err != nil {
		var notFound *serviceerror.NotFound
		if errors.As(err, &notFound) {
			// If deployment not found, return empty state
			return state, nil
		}
		return nil, fmt.Errorf("unable to describe worker deployment %s: %w", workerDeploymentName, err)
	}

	workerDeploymentInfo := resp.Info
	routingConfig := workerDeploymentInfo.RoutingConfig

	// Set basic information
	state.DefaultVersionID = routingConfig.CurrentVersion
	state.RampingVersionID = routingConfig.RampingVersion
	state.RampPercentage = routingConfig.RampingVersionPercentage
	state.LastModifierIdentity = workerDeploymentInfo.LastModifierIdentity
	state.VersionConflictToken = resp.ConflictToken

	// TODO(jlegrone): Re-enable stats once available in versioning v3.

	// Set ramping since time if applicable
	if routingConfig.RampingVersion != "" {
		rt := metav1.NewTime(routingConfig.RampingVersionChangedTime)
		state.RampingSince = &rt
	}

	// Process each version
	for _, version := range workerDeploymentInfo.VersionSummaries {
		versionInfo := &VersionInfo{
			VersionID: version.Version,
		}

		// Determine version status
		drainageStatus := version.DrainageStatus
		if version.Version == routingConfig.CurrentVersion {
			versionInfo.Status = temporaliov1alpha1.VersionStatusCurrent
		} else if version.Version == routingConfig.RampingVersion {
			versionInfo.Status = temporaliov1alpha1.VersionStatusRamping
			versionInfo.RampPercentage = routingConfig.RampingVersionPercentage
		} else if drainageStatus == temporalClient.WorkerDeploymentVersionDrainageStatusDraining {
			versionInfo.Status = temporaliov1alpha1.VersionStatusDraining
		} else if drainageStatus == temporalClient.WorkerDeploymentVersionDrainageStatusDrained {
			versionInfo.Status = temporaliov1alpha1.VersionStatusDrained

			// Get drain time information
			versionResp, err := deploymentHandler.DescribeVersion(ctx, temporalClient.WorkerDeploymentDescribeVersionOptions{
				Version: version.Version,
			})
			if err == nil {
				drainedSince := versionResp.Info.DrainageInfo.LastChangedTime
				versionInfo.DrainedSince = &drainedSince
			}
		} else {
			versionInfo.Status = temporaliov1alpha1.VersionStatusInactive
		}

		state.Versions[version.Version] = versionInfo
	}

	return state, nil
}

// GetTestWorkflowStatus queries Temporal to get the status of test workflows for a version
func GetTestWorkflowStatus(
	ctx context.Context,
	client temporalClient.Client,
	workerDeploymentName string,
	versionID string,
	workerDeploy *temporaliov1alpha1.TemporalWorkerDeployment,
) ([]temporaliov1alpha1.WorkflowExecution, error) {
	var results []temporaliov1alpha1.WorkflowExecution

	// Get deployment handler
	deploymentHandler := client.WorkerDeploymentClient().GetHandle(workerDeploymentName)

	// Describe the version to get task queue information
	versionResp, err := deploymentHandler.DescribeVersion(ctx, temporalClient.WorkerDeploymentDescribeVersionOptions{
		Version: versionID,
	})

	var notFound *serviceerror.NotFound
	if err != nil && !errors.As(err, &notFound) {
		// Ignore NotFound error, because if the version is not found, we know there are no test workflows running on it.
		return nil, fmt.Errorf("unable to describe worker deployment version for version %q: %w", versionID, err)
	}

	// Check test workflows for each task queue
	for _, tq := range versionResp.Info.TaskQueuesInfos {
		// Skip non-workflow task queues
		if tq.Type != temporalClient.TaskQueueTypeWorkflow {
			continue
		}

		// Check if there is a test workflow for this task queue
		testWorkflowID := getTestWorkflowID(workerDeploymentName, tq.Name, versionID)
		wf, err := client.DescribeWorkflowExecution(
			ctx,
			testWorkflowID,
			"",
		)

		// Ignore "not found" errors
		if err != nil && !strings.Contains(err.Error(), "workflow not found") {
			return nil, fmt.Errorf("unable to describe test workflow: %w", err)
		}

		// Add workflow execution info
		if err == nil {
			info := wf.GetWorkflowExecutionInfo()
			workflowInfo := temporaliov1alpha1.WorkflowExecution{
				WorkflowID: info.GetExecution().GetWorkflowId(),
				RunID:      info.GetExecution().GetRunId(),
				TaskQueue:  info.GetTaskQueue(),
				Status:     mapWorkflowStatus(info.GetStatus()),
			}
			results = append(results, workflowInfo)
		}
	}

	return results, nil
}

// Helper functions

// mapWorkflowStatus converts Temporal workflow status to our CRD status
func mapWorkflowStatus(status enums.WorkflowExecutionStatus) temporaliov1alpha1.WorkflowExecutionStatus {
	switch status {
	case enums.WORKFLOW_EXECUTION_STATUS_RUNNING, enums.WORKFLOW_EXECUTION_STATUS_CONTINUED_AS_NEW:
		return temporaliov1alpha1.WorkflowExecutionStatusRunning
	case enums.WORKFLOW_EXECUTION_STATUS_COMPLETED:
		return temporaliov1alpha1.WorkflowExecutionStatusCompleted
	case enums.WORKFLOW_EXECUTION_STATUS_FAILED:
		return temporaliov1alpha1.WorkflowExecutionStatusFailed
	case enums.WORKFLOW_EXECUTION_STATUS_CANCELED:
		return temporaliov1alpha1.WorkflowExecutionStatusCanceled
	case enums.WORKFLOW_EXECUTION_STATUS_TERMINATED:
		return temporaliov1alpha1.WorkflowExecutionStatusTerminated
	case enums.WORKFLOW_EXECUTION_STATUS_TIMED_OUT:
		return temporaliov1alpha1.WorkflowExecutionStatusTimedOut
	default:
		// Default to running for unspecified or any other status
		return temporaliov1alpha1.WorkflowExecutionStatusRunning
	}
}

// getTestWorkflowID generates a consistent ID for test workflows
func getTestWorkflowID(deploymentName, taskQueue, versionID string) string {
	return fmt.Sprintf("test-%s-%s-%s", deploymentName, taskQueue, versionID)
}
