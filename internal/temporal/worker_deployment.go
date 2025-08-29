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

	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	temporalClient "go.temporal.io/sdk/client"
	"go.temporal.io/sdk/converter"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	IgnoreLastModifierKey = "temporal.io/ignore-last-modifier"
)

// VersionInfo contains information about a specific version
type VersionInfo struct {
	DeploymentName string
	BuildID        string
	Status         temporaliov1alpha1.VersionStatus
	DrainedSince   *time.Time
	TaskQueues     []temporaliov1alpha1.TaskQueue
	TestWorkflows  []temporaliov1alpha1.WorkflowExecution
}

// TemporalWorkerState represents the state of a worker deployment in Temporal
type TemporalWorkerState struct {
	CurrentBuildID       string
	VersionConflictToken []byte
	RampingBuildID       string
	RampPercentage       float32
	// RampingSince is the time when the current ramping version was set.
	RampingSince       *metav1.Time
	RampLastModifiedAt *metav1.Time
	// Versions indexed by build ID
	Versions             map[string]*VersionInfo
	LastModifierIdentity string
	IgnoreLastModifier   bool
}

// GetWorkerDeploymentState queries Temporal to get the state of a worker deployment
func GetWorkerDeploymentState(
	ctx context.Context,
	client temporalClient.Client,
	workerDeploymentName string,
	namespace string,
	controllerIdentity string,
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
	if routingConfig.CurrentVersion != nil {
		state.CurrentBuildID = routingConfig.CurrentVersion.BuildId
	}
	if routingConfig.RampingVersion != nil {
		state.RampingBuildID = routingConfig.RampingVersion.BuildId
	}
	state.RampPercentage = routingConfig.RampingVersionPercentage
	state.LastModifierIdentity = workerDeploymentInfo.LastModifierIdentity
	state.VersionConflictToken = resp.ConflictToken

	// Decide whether to ignore LastModifierIdentity
	if state.LastModifierIdentity != controllerIdentity && state.LastModifierIdentity != "" {
		if routingConfig.CurrentVersion != nil {
			state.IgnoreLastModifier, err = getShouldIgnoreLastModifier(ctx, deploymentHandler, routingConfig.CurrentVersion.BuildId)
			if err != nil {
				return nil, err
			}
		}
		if !state.IgnoreLastModifier && // if someone has a non-nil Current Version, but set the metadata in their Ramping Version instead, still count it
			routingConfig.RampingVersion != nil {
			state.IgnoreLastModifier, err = getShouldIgnoreLastModifier(ctx, deploymentHandler, routingConfig.CurrentVersion.BuildId)
			if err != nil {
				return nil, err
			}
		}
	}

	// TODO(jlegrone): Re-enable stats once available in versioning v3.

	// Set ramping since time if applicable
	if routingConfig.RampingVersion != nil {
		var (
			rampingSinceTime   = metav1.NewTime(routingConfig.RampingVersionChangedTime)
			lastRampUpdateTime = metav1.NewTime(routingConfig.RampingVersionPercentageChangedTime)
		)
		state.RampingSince = &rampingSinceTime
		state.RampLastModifiedAt = &lastRampUpdateTime
	}

	// Process each version
	for _, version := range workerDeploymentInfo.VersionSummaries {
		versionInfo := &VersionInfo{
			DeploymentName: version.Version.DeploymentName,
			BuildID:        version.Version.BuildId,
		}

		// Determine version status
		drainageStatus := version.DrainageStatus
		if routingConfig.CurrentVersion != nil &&
			version.Version.DeploymentName == routingConfig.CurrentVersion.DeploymentName &&
			version.Version.BuildId == routingConfig.CurrentVersion.BuildId {
			versionInfo.Status = temporaliov1alpha1.VersionStatusCurrent
		} else if routingConfig.RampingVersion != nil &&
			version.Version.DeploymentName == routingConfig.RampingVersion.DeploymentName &&
			version.Version.BuildId == routingConfig.RampingVersion.BuildId {
			versionInfo.Status = temporaliov1alpha1.VersionStatusRamping
		} else if drainageStatus == temporalClient.WorkerDeploymentVersionDrainageStatusDraining {
			versionInfo.Status = temporaliov1alpha1.VersionStatusDraining
		} else if drainageStatus == temporalClient.WorkerDeploymentVersionDrainageStatusDrained {
			versionInfo.Status = temporaliov1alpha1.VersionStatusDrained

			// Get drain time information
			versionResp, err := deploymentHandler.DescribeVersion(ctx, temporalClient.WorkerDeploymentDescribeVersionOptions{
				BuildID: version.Version.BuildId,
			})
			if err == nil {
				drainedSince := versionResp.Info.DrainageInfo.LastChangedTime
				versionInfo.DrainedSince = &drainedSince
			}
		} else {
			versionInfo.Status = temporaliov1alpha1.VersionStatusInactive
		}

		state.Versions[version.Version.BuildId] = versionInfo
	}

	return state, nil
}

// GetTestWorkflowStatus queries Temporal to get the status of test workflows for a version
func GetTestWorkflowStatus(
	ctx context.Context,
	client temporalClient.Client,
	workerDeploymentName string,
	buildID string,
	workerDeploy *temporaliov1alpha1.TemporalWorkerDeployment,
	temporalState *TemporalWorkerState,
) ([]temporaliov1alpha1.WorkflowExecution, error) {
	var results []temporaliov1alpha1.WorkflowExecution

	// Get deployment handler
	deploymentHandler := client.WorkerDeploymentClient().GetHandle(workerDeploymentName)

	// Get version info from temporal state to get deployment name
	versionInfo, exists := temporalState.Versions[buildID]
	if !exists {
		return results, nil
	}

	// Describe the version to get task queue information
	versionResp, err := deploymentHandler.DescribeVersion(ctx, temporalClient.WorkerDeploymentDescribeVersionOptions{
		BuildID: versionInfo.BuildID,
	})

	var notFound *serviceerror.NotFound
	if err != nil && !errors.As(err, &notFound) {
		// Ignore NotFound error, because if the version is not found, we know there are no test workflows running on it.
		return nil, fmt.Errorf("unable to describe worker deployment version for buildID %q: %w", buildID, err)
	}

	// Check test workflows for each task queue
	for _, tq := range versionResp.Info.TaskQueuesInfos {
		// Skip non-workflow task queues
		if tq.Type != temporalClient.TaskQueueTypeWorkflow {
			continue
		}

		// Adding task queue information to the current temporal state
		temporalState.Versions[buildID].TaskQueues = append(temporalState.Versions[buildID].TaskQueues, temporaliov1alpha1.TaskQueue{
			Name: tq.Name,
		})

		// Check if there is a test workflow for this task queue
		testWorkflowID := GetTestWorkflowID(versionInfo.DeploymentName, versionInfo.BuildID, tq.Name)
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
func mapWorkflowStatus(status enumspb.WorkflowExecutionStatus) temporaliov1alpha1.WorkflowExecutionStatus {
	switch status {
	case enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING, enumspb.WORKFLOW_EXECUTION_STATUS_CONTINUED_AS_NEW:
		return temporaliov1alpha1.WorkflowExecutionStatusRunning
	case enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED:
		return temporaliov1alpha1.WorkflowExecutionStatusCompleted
	case enumspb.WORKFLOW_EXECUTION_STATUS_FAILED:
		return temporaliov1alpha1.WorkflowExecutionStatusFailed
	case enumspb.WORKFLOW_EXECUTION_STATUS_CANCELED:
		return temporaliov1alpha1.WorkflowExecutionStatusCanceled
	case enumspb.WORKFLOW_EXECUTION_STATUS_TERMINATED:
		return temporaliov1alpha1.WorkflowExecutionStatusTerminated
	case enumspb.WORKFLOW_EXECUTION_STATUS_TIMED_OUT:
		return temporaliov1alpha1.WorkflowExecutionStatusTimedOut
	default:
		// Default to running for unspecified or any other status
		return temporaliov1alpha1.WorkflowExecutionStatusRunning
	}
}

// GetTestWorkflowID generates a workflowID for test workflows
func GetTestWorkflowID(deploymentName, buildID, taskQueue string) string {
	return fmt.Sprintf("test-%s:%s-%s", deploymentName, buildID, taskQueue)
}

func getShouldIgnoreLastModifier(
	ctx context.Context,
	deploymentHandler temporalClient.WorkerDeploymentHandle,
	buildId string,
) (bool, error) {
	desc, err := deploymentHandler.DescribeVersion(ctx, temporalClient.WorkerDeploymentDescribeVersionOptions{
		BuildID: buildId,
	})
	if err != nil {
		return false, fmt.Errorf("unable to describe version: %w", err)
	}
	for k, v := range desc.Info.Metadata {
		if k == IgnoreLastModifierKey {
			var s string
			err = converter.GetDefaultDataConverter().FromPayload(v, &s)
			if err != nil {
				return false, fmt.Errorf("unable to decode metadata payload for key %s: %w", IgnoreLastModifierKey, err)
			}
			return s == "true", nil
		}
	}
	return false, nil
}
