// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package controller

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/go-logr/logr"
	"go.temporal.io/api/common/v1"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/taskqueue/v1"
	"go.temporal.io/api/workflow/v1"
	"go.temporal.io/api/workflowservice/v1"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	temporaliov1alpha1 "github.com/DataDog/temporal-worker-controller/api/v1alpha1"
)

type versionedDeploymentCollection struct {
	buildIDsToDeployments map[string]*appsv1.Deployment
	// map of build IDs to ramp percentages [0,100]
	rampPercentages map[string]uint8
	// map of build IDs to task queue stats
	stats map[string]temporaliov1alpha1.QueueStatistics
	// map of build IDs to reachability
	reachabilityStatus map[string]temporaliov1alpha1.ReachabilityStatus
	// map of build IDs to test workflow executions
	testWorkflowStatus map[string][]temporaliov1alpha1.WorkflowExecution
	// map of build IDs to task queues
	taskQueues map[string][]string
}

func (c *versionedDeploymentCollection) getDeployment(buildID string) (*appsv1.Deployment, bool) {
	d, ok := c.buildIDsToDeployments[buildID]
	return d, ok
}

func (c *versionedDeploymentCollection) getVersionedDeployment(buildID string) (*temporaliov1alpha1.VersionedDeployment, *temporaliov1alpha1.QueueStatistics) {
	result := temporaliov1alpha1.VersionedDeployment{
		HealthySince:       nil,
		BuildID:            buildID,
		CompatibleBuildIDs: nil,
		Reachability:       temporaliov1alpha1.ReachabilityStatusNotRegistered,
		RampPercentage:     nil,
		Deployment:         nil,
	}

	// Set deployment ref and health status
	if d, ok := c.getDeployment(buildID); ok {
		// Check if deployment condition is "available"
		var healthySince *metav1.Time
		// TODO(jlegrone): do we need to sort conditions by timestamp to check only latest?
		for _, c := range d.Status.Conditions {
			if c.Type == appsv1.DeploymentAvailable && c.Status == v1.ConditionTrue {
				healthySince = &c.LastTransitionTime
				break
			}
		}
		result.HealthySince = healthySince
		result.Deployment = newObjectRef(d)
	}

	// Set ramp percentage
	if ramp, ok := c.rampPercentages[buildID]; ok && ramp != 100 {
		result.RampPercentage = &ramp
	}

	// Set reachability
	if st, ok := c.reachabilityStatus[buildID]; ok {
		result.Reachability = st
	}

	var stats temporaliov1alpha1.QueueStatistics
	if s, ok := c.stats[buildID]; ok {
		stats = s
	}

	// Set test workflow status
	if testWorkflows, ok := c.testWorkflowStatus[buildID]; ok {
		result.TestWorkflows = testWorkflows
	}

	for _, tq := range c.taskQueues[buildID] {
		result.TaskQueues = append(result.TaskQueues, temporaliov1alpha1.TaskQueue{
			Name: tq,
		})
	}

	return &result, &stats
}

func (c *versionedDeploymentCollection) addDeployment(buildID string, d *appsv1.Deployment) {
	c.buildIDsToDeployments[buildID] = d
}

func (c *versionedDeploymentCollection) addAssignmentRule(rule *taskqueue.BuildIdAssignmentRule) {
	// Skip updating existing values (only the first one should take effect)
	if _, ok := c.rampPercentages[rule.GetTargetBuildId()]; ok {
		return
	}
	if ramp := rule.GetPercentageRamp(); ramp != nil {
		c.rampPercentages[rule.GetTargetBuildId()] = convertFloatToUint(ramp.GetRampPercentage())
	} else {
		c.rampPercentages[rule.GetTargetBuildId()] = 100
	}
}

func (c *versionedDeploymentCollection) addDeploymentReachability(buildID string, info enums.DeploymentReachability) error {
	var reachability temporaliov1alpha1.ReachabilityStatus
	switch info {
	case enums.DEPLOYMENT_REACHABILITY_REACHABLE, enums.DEPLOYMENT_REACHABILITY_UNSPECIFIED:
		reachability = temporaliov1alpha1.ReachabilityStatusReachable
	case enums.DEPLOYMENT_REACHABILITY_CLOSED_WORKFLOWS_ONLY:
		reachability = temporaliov1alpha1.ReachabilityStatusClosedOnly
	case enums.DEPLOYMENT_REACHABILITY_UNREACHABLE:
		reachability = temporaliov1alpha1.ReachabilityStatusUnreachable
	default:
		return fmt.Errorf("unhandled build id reachability: %s", info.String())
	}
	c.reachabilityStatus[buildID] = reachability

	return nil
}

func (c *versionedDeploymentCollection) addTaskQueue(buildID, name string) {
	c.taskQueues[buildID] = append(c.taskQueues[buildID], name)
}

func (c *versionedDeploymentCollection) addTestWorkflowStatus(buildID string, info *workflow.WorkflowExecutionInfo) error {
	var s temporaliov1alpha1.WorkflowExecutionStatus
	switch info.GetStatus() {
	case enums.WORKFLOW_EXECUTION_STATUS_UNSPECIFIED:
		// Don't set a status if the test workflow hasn't been started yet
		return nil
	case enums.WORKFLOW_EXECUTION_STATUS_RUNNING, enums.WORKFLOW_EXECUTION_STATUS_CONTINUED_AS_NEW:
		s = temporaliov1alpha1.WorkflowExecutionStatusRunning
	case enums.WORKFLOW_EXECUTION_STATUS_COMPLETED:
		s = temporaliov1alpha1.WorkflowExecutionStatusCompleted
	case enums.WORKFLOW_EXECUTION_STATUS_FAILED:
		s = temporaliov1alpha1.WorkflowExecutionStatusFailed
	case enums.WORKFLOW_EXECUTION_STATUS_CANCELED:
		s = temporaliov1alpha1.WorkflowExecutionStatusCanceled
	case enums.WORKFLOW_EXECUTION_STATUS_TERMINATED:
		s = temporaliov1alpha1.WorkflowExecutionStatusTerminated
	case enums.WORKFLOW_EXECUTION_STATUS_TIMED_OUT:
		s = temporaliov1alpha1.WorkflowExecutionStatusTimedOut
	default:
		return fmt.Errorf("unhandled test workflow status: %s", info.GetStatus().String())
	}
	c.testWorkflowStatus[buildID] = append(c.testWorkflowStatus[buildID], temporaliov1alpha1.WorkflowExecution{
		WorkflowID: info.GetExecution().GetWorkflowId(),
		RunID:      info.GetExecution().GetRunId(),
		TaskQueue:  info.GetTaskQueue(),
		Status:     s,
	})

	return nil
}

func (c *versionedDeploymentCollection) addReachability(buildID string, info *taskqueue.TaskQueueVersionInfo) error {
	var reachability temporaliov1alpha1.ReachabilityStatus
	switch info.GetTaskReachability() {
	case enums.BUILD_ID_TASK_REACHABILITY_REACHABLE, enums.BUILD_ID_TASK_REACHABILITY_UNSPECIFIED:
		reachability = temporaliov1alpha1.ReachabilityStatusReachable
	case enums.BUILD_ID_TASK_REACHABILITY_CLOSED_WORKFLOWS_ONLY:
		reachability = temporaliov1alpha1.ReachabilityStatusClosedOnly
	case enums.BUILD_ID_TASK_REACHABILITY_UNREACHABLE:
		reachability = temporaliov1alpha1.ReachabilityStatusUnreachable
	default:
		return fmt.Errorf("unhandled build id reachability: %s", info.GetTaskReachability().String())
	}
	c.reachabilityStatus[buildID] = reachability

	// Compute total stats
	var totalStats temporaliov1alpha1.QueueStatistics
	for _, stat := range info.GetTypesInfo() {
		if backlogAge := stat.GetStats().GetApproximateBacklogAge().AsDuration(); backlogAge > totalStats.ApproximateBacklogAge.Duration {
			totalStats.ApproximateBacklogAge = metav1.Duration{Duration: backlogAge}
		}
		totalStats.ApproximateBacklogCount += stat.GetStats().GetApproximateBacklogCount()
		totalStats.TasksAddRate += stat.GetStats().GetTasksAddRate()
		totalStats.TasksDispatchRate += stat.GetStats().GetTasksDispatchRate()
	}
	// TODO(jlegrone): Register stats after supported by temporal server
	c.stats[buildID] = totalStats

	return nil
}

func (c *versionedDeploymentCollection) addStats(buildID string, info map[int32]*taskqueue.TaskQueueTypeInfo) error {
	// Compute total stats
	var totalStats temporaliov1alpha1.QueueStatistics
	for _, stat := range info {
		if backlogAge := stat.GetStats().GetApproximateBacklogAge().AsDuration(); backlogAge > totalStats.ApproximateBacklogAge.Duration {
			totalStats.ApproximateBacklogAge = metav1.Duration{Duration: backlogAge}
		}
		totalStats.ApproximateBacklogCount += stat.GetStats().GetApproximateBacklogCount()
		totalStats.TasksAddRate += stat.GetStats().GetTasksAddRate()
		totalStats.TasksDispatchRate += stat.GetStats().GetTasksDispatchRate()
	}
	// TODO(jlegrone): Register stats after supported by temporal server
	c.stats[buildID] = totalStats

	return nil
}

func newVersionedDeploymentCollection() versionedDeploymentCollection {
	return versionedDeploymentCollection{
		buildIDsToDeployments: make(map[string]*appsv1.Deployment),
		rampPercentages:       make(map[string]uint8),
		stats:                 make(map[string]temporaliov1alpha1.QueueStatistics),
		reachabilityStatus:    make(map[string]temporaliov1alpha1.ReachabilityStatus),
		testWorkflowStatus:    make(map[string][]temporaliov1alpha1.WorkflowExecution),
		taskQueues:            make(map[string][]string),
	}
}

func (r *TemporalWorkerReconciler) generateStatus(ctx context.Context, l logr.Logger, temporalClient workflowservice.WorkflowServiceClient, req ctrl.Request, workerDeploy *temporaliov1alpha1.TemporalWorker) (*temporaliov1alpha1.TemporalWorkerStatus, error) {
	var (
		desiredBuildID, defaultBuildID string
		deployedBuildIDs               []string
		versions                       = newVersionedDeploymentCollection()
	)

	if workerDeploy.Spec.WorkerOptions.DeploymentSeries == "" {
		panic("nope")
	}

	desiredBuildID = computeBuildID(&workerDeploy.Spec)

	// Get managed worker deployments
	var childDeploys appsv1.DeploymentList
	if err := r.List(ctx, &childDeploys, client.InNamespace(req.Namespace), client.MatchingFields{deployOwnerKey: req.Name}); err != nil {
		return nil, fmt.Errorf("unable to list child deployments: %w", err)
	}
	// Sort deployments by creation timestamp
	sort.SliceStable(childDeploys.Items, func(i, j int) bool {
		return childDeploys.Items[i].ObjectMeta.CreationTimestamp.Before(&childDeploys.Items[j].ObjectMeta.CreationTimestamp)
	})
	// Track each deployment by build ID
	for _, childDeploy := range childDeploys.Items {
		if buildID, ok := childDeploy.GetLabels()[buildIDLabel]; ok {
			versions.addDeployment(buildID, &childDeploy)
			deployedBuildIDs = append(deployedBuildIDs, buildID)
			continue
		}
		// TODO(jlegrone): implement some error handling (maybe a human deleted the label?)
	}

	// List deployments in Temporal
	deploymentList, err := temporalClient.ListDeployments(ctx, &workflowservice.ListDeploymentsRequest{
		Namespace:     workerDeploy.Spec.WorkerOptions.TemporalNamespace,
		SeriesName:    workerDeploy.Spec.WorkerOptions.DeploymentSeries,
		PageSize:      0,
		NextPageToken: nil,
	})
	if err != nil {
		return nil, fmt.Errorf("unable to list deployments: %w", err)
	}

	if len(deploymentList.GetDeployments()) == 0 {
		l.Error(fmt.Errorf("no deployments found"), "no deployments found")
	}

	// TODO(jlegrone): Support ramp values
	for _, deployment := range deploymentList.GetDeployments() {
		// Set default build ID
		if deployment.GetIsCurrent() {
			defaultBuildID = deployment.GetDeployment().GetBuildId()

			// When a deployment is the default it will always be reachable, so skip querying the API.
			_ = versions.addDeploymentReachability(deployment.GetDeployment().GetBuildId(), enums.DEPLOYMENT_REACHABILITY_REACHABLE)

			// Check if the build ID was promoted to default out of band of the controller (eg. via the Temporal CLI)
			desc, err := temporalClient.DescribeDeployment(ctx, &workflowservice.DescribeDeploymentRequest{
				Namespace:  workerDeploy.Spec.WorkerOptions.TemporalNamespace,
				Deployment: deployment.GetDeployment(),
			})
			if err != nil {
				return nil, fmt.Errorf("unable to describe default deployment for build ID %q: %w", deployment.GetDeployment().GetBuildId(), err)
			}
			// TODO(jlegrone): check for controller identity in metadata
			desc.GetDeploymentInfo().GetMetadata()
		} else {
			// Get reachability status
			reachability, err := temporalClient.GetDeploymentReachability(ctx, &workflowservice.GetDeploymentReachabilityRequest{
				Namespace:  workerDeploy.Spec.WorkerOptions.TemporalNamespace,
				Deployment: deployment.GetDeployment(),
			})
			if err != nil {
				l.Error(err, "unable to get deployment reachability")
				return nil, fmt.Errorf("unable to get deployment reachability for build ID %q: %w", deployment.GetDeployment().GetBuildId(), err)
			}
			if err := versions.addDeploymentReachability(deployment.GetDeployment().GetBuildId(), reachability.GetReachability()); err != nil {
				return nil, fmt.Errorf("error computing reachability for build ID %q: %w", deployment.GetDeployment().GetBuildId(), err)
			}
		}

		// Check the status of the test workflow for the next version, if one is in progress.
		if !deployment.GetIsCurrent() && deployment.GetDeployment().GetBuildId() == desiredBuildID {
			// Describe the deployment to get task queue information
			deploymentInfo, err := temporalClient.DescribeDeployment(ctx, &workflowservice.DescribeDeploymentRequest{
				Namespace:  workerDeploy.Spec.WorkerOptions.TemporalNamespace,
				Deployment: deployment.GetDeployment(),
			})
			if err != nil {
				return nil, fmt.Errorf("unable to describe deployment for build ID %q: %w", deployment.GetDeployment().GetBuildId(), err)
			}

			// TODO(jlegrone): Validate that task queues from current default version are still present in this new version.
			for _, tq := range deploymentInfo.GetDeploymentInfo().GetTaskQueueInfos() {
				// Keep track of which task queues this version of the worker is polling on
				if tq.GetType() != enums.TASK_QUEUE_TYPE_WORKFLOW {
					continue
				}
				versions.addTaskQueue(desiredBuildID, tq.GetName())

				// If there is a test workflow associated with this task queue and build id, check its status.
				wf, err := temporalClient.DescribeWorkflowExecution(ctx, &workflowservice.DescribeWorkflowExecutionRequest{
					Namespace: workerDeploy.Spec.WorkerOptions.TemporalNamespace,
					Execution: &common.WorkflowExecution{
						WorkflowId: getTestWorkflowID(workerDeploy.Spec.WorkerOptions.DeploymentSeries, tq.GetName(), desiredBuildID),
					},
				})
				// TODO(jlegrone): Detect "not found" errors properly
				if err != nil && !strings.Contains(err.Error(), "workflow not found") {
					return nil, fmt.Errorf("unable to describe test workflow: %w", err)
				}
				if err := versions.addTestWorkflowStatus(desiredBuildID, wf.GetWorkflowExecutionInfo()); err != nil {
					return nil, fmt.Errorf("error computing test workflow status for build ID %q: %w", deployment.GetDeployment().GetBuildId(), err)
				}
			}
		}
	}

	// TODO(jlegrone): re-enable stats once available in versioning v3.
	// Get stats for all build IDs associated with the task queue via the Temporal API
	//tq, err := temporalClient.DescribeTaskQueue(ctx, &workflowservice.DescribeTaskQueueRequest{
	//	ApiMode:   enums.DESCRIBE_TASK_QUEUE_MODE_ENHANCED,
	//	Namespace: workerDeploy.Spec.WorkerOptions.TemporalNamespace,
	//	TaskQueue: &taskqueue.TaskQueue{
	//		Name: workerDeploy.Spec.WorkerOptions.TaskQueue,
	//		Kind: enums.TASK_QUEUE_KIND_NORMAL,
	//	},
	//	Versions: &taskqueue.TaskQueueVersionSelection{
	//		// Including deployed build IDs means that we'll observe the "UnReachable" status even for versions
	//		// that are no longer known to the server. Not including this option means we can see the "NotRegistered"
	//		// status and trigger deletion rather than scaling to zero.
	//		//
	//		// This can also lead to the following error: Too many build ids queried at once with ReportTaskReachability==true, limit: 5
	//		//BuildIds:  deployedBuildIDs,
	//		AllActive: true,
	//	},
	//	ReportStats:            true,
	//	ReportTaskReachability: false, // This used to be enabled, but now reachability is retrieved using the GetDeploymentReachability API.
	//	ReportPollers:          false,
	//})
	//if err != nil {
	//	return nil, fmt.Errorf("unable to describe task queue: %w", err)
	//}
	//for buildID, info := range tq.GetVersionsInfo() {
	//	if err := versions.addStats(buildID, info.GetTypesInfo()); err != nil {
	//		return nil, fmt.Errorf("error computing reachability for build ID %q: %w", buildID, err)
	//	}
	//}

	var deprecatedVersions []*temporaliov1alpha1.VersionedDeployment
	for _, buildID := range deployedBuildIDs {
		switch buildID {
		case desiredBuildID, defaultBuildID:
			continue
		}
		d, _ := versions.getVersionedDeployment(buildID)
		deprecatedVersions = append(deprecatedVersions, d)
	}

	var (
		defaultVersion, _ = versions.getVersionedDeployment(defaultBuildID)
		targetVersion, _  = versions.getVersionedDeployment(desiredBuildID)
	)

	// Ugly hack to clear ramp percentages (not quite correctly) for now
	for _, d := range deprecatedVersions {
		d.RampPercentage = nil
	}
	if defaultVersion != nil {
		defaultVersion.RampPercentage = nil
		if defaultVersion.BuildID == targetVersion.BuildID {
			targetVersion.RampPercentage = nil
		}
	}

	return &temporaliov1alpha1.TemporalWorkerStatus{
		DefaultVersion:       defaultVersion,
		TargetVersion:        targetVersion,
		DeprecatedVersions:   deprecatedVersions,
		VersionConflictToken: []byte("todo"),
	}, nil
}

func convertFloatToUint(val float32) uint8 {
	return uint8(math.Round(float64(val)))
}
