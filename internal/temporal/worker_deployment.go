// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package temporal

import (
	"context"
	"errors"
	"fmt"
	"go.temporal.io/api/taskqueue/v1"
	"go.temporal.io/api/workflowservice/v1"
	"strings"
	"time"

	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	temporalClient "go.temporal.io/sdk/client"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// VersionInfo contains information about a specific version
type VersionInfo struct {
	DeploymentName string
	BuildID        string
	Status         temporaliov1alpha1.VersionStatus
	DrainedSince   *time.Time
	TaskQueues     []temporaliov1alpha1.TaskQueue
	TestWorkflows  []temporaliov1alpha1.WorkflowExecution

	// AllTaskQueuesHaveUnversionedPoller is true if we confirm that all task queues in the version have at least one
	// unversioned poller. False means none exist or unknown.
	// Unversioned Task Queue pollers are only queried for the Target Version, and only if both are true:
	//   - The Current Version is nil / unversioned
	//   - The rollout strategy is Progressive
	// If those conditions are not met, this will always be false (unknown).
	//
	// If Current Version is nil and a Task Queue in the Target Version has no unversioned pollers, gradually ramping
	// N% of traffic to the Target Version would mean that the remaining traffic is routed to the unversioned queue,
	// where it would not get picked up, leading to task timeouts.
	// To prevent that, if the Current Version is nil, and we cannot confirm that there are unversioned pollers on the
	// Target Version's task queues, we promote the Target Version to Current immediately, skipping Progressive steps.
	AllTaskQueuesHaveUnversionedPoller bool

	// AllTaskQueuesHaveNoVersionedPollers is true if we confirm that none of the task queues in the version have any
	// versioned pollers polling this version. False means at least one versioned poller exists or unknown.
	// Versioned Task Queue pollers are only queried for Drained Versions, so if the version this Task Queue is in is
	// not Drained, this will always be false (unknown).
	// If a version is Drained and has no versioned pollers, it is eligible for deletion and can be automatically
	// and won't count against the max
	// non-deletable versions of your Worker Deployment.
	NoTaskQueuesHaveVersionedPollers bool
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
	if routingConfig.CurrentVersion != nil {
		state.CurrentBuildID = routingConfig.CurrentVersion.BuildId
	}
	if routingConfig.RampingVersion != nil {
		state.RampingBuildID = routingConfig.RampingVersion.BuildId
	}
	state.RampPercentage = routingConfig.RampingVersionPercentage
	state.LastModifierIdentity = workerDeploymentInfo.LastModifierIdentity
	state.VersionConflictToken = resp.ConflictToken

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
				for _, tqInfo := range versionResp.Info.TaskQueuesInfos {
					hasNoVersionedPollers, _ := HasNoVersionedPollers(ctx, client, tqInfo) // consider logging this error
					versionInfo.TaskQueues = append(versionInfo.TaskQueues, temporaliov1alpha1.TaskQueue{
						Name:                  tqInfo.Name,
						Type:                  TaskQueueTypeString(tqInfo.Type),
						HasNoVersionedPollers: hasNoVersionedPollers,
					})
				}
			}
		} else {
			versionInfo.Status = temporaliov1alpha1.VersionStatusInactive
		}

		state.Versions[version.Version.BuildId] = versionInfo
	}

	return state, nil
}

func GetVersionTaskQueueInfos(ctx context.Context,
	client temporalClient.Client,
	workerDeploymentName string,
	buildID string,
) ([]temporalClient.WorkerDeploymentTaskQueueInfo, error) {
	// Get deployment handler
	deploymentHandler := client.WorkerDeploymentClient().GetHandle(workerDeploymentName)

	// Describe the version to get task queue information
	versionResp, err := deploymentHandler.DescribeVersion(ctx, temporalClient.WorkerDeploymentDescribeVersionOptions{
		BuildID: buildID,
	})
	if err != nil {
		return nil, err
	}
	return versionResp.Info.TaskQueuesInfos, nil
}

func HasUnversionedPoller(ctx context.Context,
	client temporalClient.Client,
	taskQueueInfo temporalClient.WorkerDeploymentTaskQueueInfo,
) (bool, error) {
	pollers, err := getPollers(ctx, client, taskQueueInfo)
	if err != nil {
		return false, fmt.Errorf("unable to confirm presence of unversioned pollers: %w", err)
	}
	for _, p := range pollers {
		switch p.GetDeploymentOptions().GetWorkerVersioningMode() {
		case temporalClient.WorkerVersioningModeUnversioned, temporalClient.WorkerVersioningModeUnspecified:
			return true, nil
		case temporalClient.WorkerVersioningModeVersioned:
		}
	}
	return false, nil
}

func HasNoVersionedPollers(ctx context.Context,
	client temporalClient.Client,
	taskQueueInfo temporalClient.WorkerDeploymentTaskQueueInfo,
) (bool, error) {
	pollers, err := getPollers(ctx, client, taskQueueInfo)
	if err != nil {
		return false, fmt.Errorf("unable to confirm absence of versioned pollers: %w", err)
	}
	for _, p := range pollers {
		switch p.GetDeploymentOptions().GetWorkerVersioningMode() {
		case temporalClient.WorkerVersioningModeUnversioned, temporalClient.WorkerVersioningModeUnspecified:
		case temporalClient.WorkerVersioningModeVersioned:
			return false, nil
		}
	}
	return true, nil
}

func getPollers(ctx context.Context,
	client temporalClient.Client,
	taskQueueInfo temporalClient.WorkerDeploymentTaskQueueInfo,
) ([]*taskqueue.PollerInfo, error) {
	var resp *workflowservice.DescribeTaskQueueResponse
	var err error
	switch taskQueueInfo.Type {
	case temporalClient.TaskQueueTypeWorkflow:
		resp, err = client.DescribeTaskQueue(ctx, taskQueueInfo.Name, temporalClient.TaskQueueTypeWorkflow)
	case temporalClient.TaskQueueTypeActivity:
		resp, err = client.DescribeTaskQueue(ctx, taskQueueInfo.Name, temporalClient.TaskQueueTypeActivity)
	}
	if err != nil {
		return nil, fmt.Errorf("unable to describe task queue %s: %w", taskQueueInfo.Name, err)
	}
	return resp.GetPollers(), nil
}

// GetTestWorkflowStatus queries Temporal to get the status of test workflows for a version
func GetTestWorkflowStatus(
	ctx context.Context,
	client temporalClient.Client,
	workerDeploymentName string,
	buildID string,
	taskQueueInfos []temporalClient.WorkerDeploymentTaskQueueInfo,
) ([]temporaliov1alpha1.WorkflowExecution, error) {
	var results []temporaliov1alpha1.WorkflowExecution

	// Check test workflows for each task queue
	for _, tq := range taskQueueInfos {
		// Skip non-workflow task queues
		if tq.Type != temporalClient.TaskQueueTypeWorkflow {
			continue
		}

		// Check if there is a test workflow for this task queue
		testWorkflowID := GetTestWorkflowID(workerDeploymentName, buildID, tq.Name)
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

func TaskQueueTypeString(tqType temporalClient.TaskQueueType) string {
	if tqType == temporalClient.TaskQueueTypeActivity {
		return "Activity"
	}
	return "Workflow"
}

// GetTestWorkflowID generates a workflowID for test workflows
func GetTestWorkflowID(deploymentName, buildID, taskQueue string) string {
	return fmt.Sprintf("test-%s:%s-%s", deploymentName, buildID, taskQueue)
}
