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

	"github.com/go-logr/logr"
	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	deploymentpb "go.temporal.io/api/deployment/v1"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	taskqueuepb "go.temporal.io/api/taskqueue/v1"
	"go.temporal.io/api/workflowservice/v1"
	temporalClient "go.temporal.io/sdk/client"
	appsv1 "k8s.io/api/apps/v1"
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

	// True if all task queues in this version have at least one unversioned poller.
	// False could just mean unknown / not checked / not checked successfully.
	// Only checked for Target Version when Current Version is nil and strategy is Progressive.
	// Used to decide whether to fast track the rollout; rollout will be AllAtOnce if:
	//   - Current Version is nil
	//   - Strategy is Progressive, and
	//   - Presence of unversioned pollers in all task queues of target version cannot be confirmed.
	AllTaskQueuesHaveUnversionedPoller bool
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
	ManagerIdentity      string
}

// GetWorkerDeploymentState queries Temporal to get the state of a worker deployment
func GetWorkerDeploymentState(
	ctx context.Context,
	l logr.Logger,
	client temporalClient.Client,
	workerDeploymentName string,
	namespace string,
	k8sDeployments map[string]*appsv1.Deployment,
	targetBuildID string,
	strategy temporaliov1alpha1.DefaultVersionUpdateStrategy,
	controllerIdentity string,
) (*TemporalWorkerState, error) {
	state := &TemporalWorkerState{
		Versions: make(map[string]*VersionInfo),
	}

	// Get deployment handler
	deploymentHandler := client.WorkerDeploymentClient().GetHandle(workerDeploymentName)

	// Describe the worker deployment using gRPC API directly to get full version summary info
	// (including drainage timestamps) without needing per-version DescribeVersion calls.
	resp, err := client.WorkflowService().DescribeWorkerDeployment(ctx, &workflowservice.DescribeWorkerDeploymentRequest{
		Namespace:      namespace,
		DeploymentName: workerDeploymentName,
	})
	if err != nil {
		var notFound *serviceerror.NotFound
		if errors.As(err, &notFound) {
			// If deployment not found, return empty state. Need to scale up workers in order to create Deployment Temporal-side
			return state, nil
		}
		return nil, fmt.Errorf("unable to describe worker deployment %s: %w", workerDeploymentName, err)
	}

	workerDeploymentInfo := resp.GetWorkerDeploymentInfo()
	if workerDeploymentInfo == nil {
		return state, nil
	}
	routingConfig := workerDeploymentInfo.GetRoutingConfig()
	if routingConfig == nil {
		return state, nil
	}

	// Set basic information
	if routingConfig.CurrentDeploymentVersion != nil {
		state.CurrentBuildID = routingConfig.CurrentDeploymentVersion.BuildId
	}
	if routingConfig.RampingDeploymentVersion != nil {
		state.RampingBuildID = routingConfig.RampingDeploymentVersion.BuildId
	}
	state.RampPercentage = routingConfig.RampingVersionPercentage
	state.LastModifierIdentity = workerDeploymentInfo.LastModifierIdentity
	state.ManagerIdentity = workerDeploymentInfo.ManagerIdentity
	state.VersionConflictToken = resp.ConflictToken

	// TODO(jlegrone): Re-enable stats once available in versioning v3.

	// Set ramping since time if applicable
	if routingConfig.RampingDeploymentVersion != nil {
		if routingConfig.RampingVersionChangedTime != nil {
			rampingSinceTime := metav1.NewTime(routingConfig.RampingVersionChangedTime.AsTime())
			state.RampingSince = &rampingSinceTime
		}
		if routingConfig.RampingVersionPercentageChangedTime != nil {
			lastRampUpdateTime := metav1.NewTime(routingConfig.RampingVersionPercentageChangedTime.AsTime())
			state.RampLastModifiedAt = &lastRampUpdateTime
		}
	}

	// Process each version
	for _, version := range workerDeploymentInfo.VersionSummaries {
		versionInfo := versionInfoFromVersionSummary(
			ctx, l, client, targetBuildID, strategy,
			deploymentHandler, routingConfig, version,
		)
		state.Versions[version.DeploymentVersion.BuildId] = versionInfo
	}

	// A version could be missing from the VersionSummaries in the odd event that there is
	// state divergence between the deployment workflow's local state and the actual versions
	// that are present. For versions that are known to TWC but absent from the Worker Deployment
	// description's version summaries, double-check their state before allowing them to map to
	// NotRegistered and get scaled down.
	for buildID := range k8sDeployments {
		if _, exists := state.Versions[buildID]; exists {
			continue
		}

		desc, err := client.WorkflowService().DescribeWorkerDeploymentVersion(
			ctx,
			&workflowservice.DescribeWorkerDeploymentVersionRequest{
				Namespace: namespace,
				DeploymentVersion: &deploymentpb.WorkerDeploymentVersion{
					DeploymentName: workerDeploymentName,
					BuildId:        buildID,
				},
			},
		)
		if err != nil {
			var notFound *serviceerror.NotFound
			if errors.As(err, &notFound) {
				// This means that the version is truly absent from both entities, i.e, from the worker-deployment summary list
				// and that there is no presence of it's own version workflow in Temporal. This is enough evidence to conclude that we can scale
				// this k8s Deployment down.
				continue
			}
			return nil, fmt.Errorf("unable to describe worker deployment version for buildID %q: %w", buildID, err)
		}

		info := desc.GetWorkerDeploymentVersionInfo()
		if info == nil || info.GetDeploymentVersion() == nil {
			return nil, fmt.Errorf("describe worker deployment version for buildID %q returned no version info", buildID)
		}

		versionInfo := versionInfoFromVersionSummary(
			ctx, l, client, targetBuildID, strategy, deploymentHandler, routingConfig,
			&deploymentpb.WorkerDeploymentInfo_WorkerDeploymentVersionSummary{
				DeploymentVersion: info.GetDeploymentVersion(),
				Status:            info.GetStatus(),
				DrainageInfo:      info.GetDrainageInfo(),
				LastCurrentTime:   info.GetLastCurrentTime(),
			},
		)
		if versionInfo == nil || versionInfo.Status == temporaliov1alpha1.VersionStatusNotRegistered {
			return nil, fmt.Errorf("describe worker deployment version for buildID %q returned no registered status", buildID)
		}
		state.Versions[buildID] = versionInfo
	}

	return state, nil
}

// versionStatusMap translates between the temporal SDK VersionStatus and the
// TWC k8s API VersionStatus
var versionStatusMap = map[enumspb.WorkerDeploymentVersionStatus]temporaliov1alpha1.VersionStatus{
	enumspb.WORKER_DEPLOYMENT_VERSION_STATUS_CURRENT:     temporaliov1alpha1.VersionStatusCurrent,
	enumspb.WORKER_DEPLOYMENT_VERSION_STATUS_CREATED:     temporaliov1alpha1.VersionStatusCreated,
	enumspb.WORKER_DEPLOYMENT_VERSION_STATUS_DRAINING:    temporaliov1alpha1.VersionStatusDraining,
	enumspb.WORKER_DEPLOYMENT_VERSION_STATUS_DRAINED:     temporaliov1alpha1.VersionStatusDrained,
	enumspb.WORKER_DEPLOYMENT_VERSION_STATUS_INACTIVE:    temporaliov1alpha1.VersionStatusInactive,
	enumspb.WORKER_DEPLOYMENT_VERSION_STATUS_RAMPING:     temporaliov1alpha1.VersionStatusRamping,
	enumspb.WORKER_DEPLOYMENT_VERSION_STATUS_UNSPECIFIED: temporaliov1alpha1.VersionStatusNotRegistered,
}

// versionInfoFromVersionSummary returns a VersionInfo constructed from the
// supplied Temporal VersionSummary message.
//
//nolint:revive // cyclomatic complexity acceptable given breadth of plan execution
func versionInfoFromVersionSummary(
	ctx context.Context,
	l logr.Logger,
	client temporalClient.Client,
	targetBuildID string,
	strategy temporaliov1alpha1.DefaultVersionUpdateStrategy,
	depHandle temporalClient.WorkerDeploymentHandle,
	routingConfig *deploymentpb.RoutingConfig,
	summary *deploymentpb.WorkerDeploymentInfo_WorkerDeploymentVersionSummary,
) *VersionInfo {
	out := &VersionInfo{
		DeploymentName: summary.DeploymentVersion.DeploymentName,
		BuildID:        summary.DeploymentVersion.BuildId,
	}

	apiVersionStatus, ok := versionStatusMap[summary.GetStatus()]
	if !ok {
		l.Error(fmt.Errorf("unknown worker version status %s", summary.GetStatus()), "unable to determine version status")
		return nil
	}
	out.Status = apiVersionStatus

	sumDeploymentName := summary.DeploymentVersion.DeploymentName
	sumBuildID := summary.DeploymentVersion.BuildId

	// There may be inconsistencies in the data returned from temporal server. Check
	// for these inconsistencies and warn if found.
	if apiVersionStatus == temporaliov1alpha1.VersionStatusCurrent {
		if routingConfig.CurrentDeploymentVersion != nil {
			rcDeployName := routingConfig.CurrentDeploymentVersion.DeploymentName
			rcBuildID := routingConfig.CurrentDeploymentVersion.BuildId
			if sumDeploymentName != rcDeployName || sumBuildID != rcBuildID {
				l.Info(
					"warning: version reports Current but routing config identifies a different Current version; trusting routing config",
					"buildID", sumBuildID,
					"routingConfigBuildID", rcBuildID,
				)
			}
		}
	} else if apiVersionStatus == temporaliov1alpha1.VersionStatusRamping {
		if routingConfig.RampingDeploymentVersion != nil {
			rcDeployName := routingConfig.RampingDeploymentVersion.DeploymentName
			rcBuildID := routingConfig.RampingDeploymentVersion.BuildId
			if sumDeploymentName != rcDeployName || sumBuildID != rcBuildID {
				l.Info(
					"warning: version reports Ramping but routing config identifies a different Ramping version; trusting routing config",
					"buildID", sumBuildID,
					"routingConfigBuildID", rcBuildID,
				)
			}
		}
	} else if apiVersionStatus == temporaliov1alpha1.VersionStatusInactive {
		// get unversioned poller info to decide whether to fast-track rollout
		if summary.DeploymentVersion.BuildId == targetBuildID &&
			routingConfig.CurrentDeploymentVersion == nil &&
			strategy == temporaliov1alpha1.UpdateProgressive {
			var err error
			var desc temporalClient.WorkerDeploymentVersionDescription
			describeVersion := func() error {
				desc, err = depHandle.DescribeVersion(
					ctx,
					temporalClient.WorkerDeploymentDescribeVersionOptions{
						BuildID: summary.DeploymentVersion.BuildId,
					},
				)
				return err
			}
			// At first, summary is found in DeploymentInfo.VersionSummaries but not ready for describe, so we have
			// to describe with backoff.
			//
			// Note: We can only check whether the task queues that we know of have unversioned pollers.
			//       If, later on, a poll request arrives tying a new task queue to the target summary, we
			//       don't know whether that task queue has unversioned pollers.
			if err = withBackoff(10*time.Second, 1*time.Second, describeVersion); err == nil { //revive:disable-line:max-control-nesting
				out.AllTaskQueuesHaveUnversionedPoller = allTaskQueuesHaveUnversionedPoller(ctx, client, desc.Info.TaskQueuesInfos)
			}
			// NOTE(jaypipes): We swallow any non-nil error here. Should we
			// at least log the error?
		}
	} else if apiVersionStatus == temporaliov1alpha1.VersionStatusDrained {
		// Extract DrainedSince directly from the summary's drainage info,
		// avoiding a per-summary DescribeVersion call.
		if summary.DrainageInfo != nil && summary.DrainageInfo.LastChangedTime != nil {
			drainedSince := summary.DrainageInfo.LastChangedTime.AsTime()
			out.DrainedSince = &drainedSince
		}
	}

	return out
}

func withBackoff(timeout time.Duration, tick time.Duration, fn func() error) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		err := fn()
		if err == nil {
			return nil
		}
		lastErr = err
		time.Sleep(tick)
	}
	return lastErr
}

// GetTestWorkflowStatus queries Temporal to get the status of test workflows for a version
func GetTestWorkflowStatus(
	ctx context.Context,
	client temporalClient.Client,
	workerDeploymentName string,
	buildID string,
	workerDeploy *temporaliov1alpha1.WorkerDeployment,
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

func getPollers(ctx context.Context,
	client temporalClient.Client,
	taskQueueInfo temporalClient.WorkerDeploymentTaskQueueInfo,
) ([]*taskqueuepb.PollerInfo, error) {
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

func allTaskQueuesHaveUnversionedPoller(
	ctx context.Context,
	client temporalClient.Client,
	tqs []temporalClient.WorkerDeploymentTaskQueueInfo,
) bool {
	countHasUnversionedPoller := 0
	for _, tqInfo := range tqs {
		hasUnversionedPoller, _ := HasUnversionedPoller(ctx, client, tqInfo) // TODO(carlydf): consider logging this error
		if hasUnversionedPoller {
			countHasUnversionedPoller++
		}
	}
	return countHasUnversionedPoller == len(tqs)
}
