// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package controller

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"go.temporal.io/api/serviceerror"

	"github.com/go-logr/logr"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/taskqueue/v1"
	"go.temporal.io/api/workflow/v1"
	sdkclient "go.temporal.io/sdk/client"
	temporalClient "go.temporal.io/sdk/client"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	temporaliov1alpha1 "github.com/DataDog/temporal-worker-controller/api/v1alpha1"
)

const controllerIdentity = "temporal-worker-controller"

// TODO (Shivam): Should we be having a map of [versionID] -> Structure?
type deploymentVersionCollection struct {
	versionIDsToDeployments map[string]*appsv1.Deployment
	// map of version IDs to ramp percentages [0,100]
	rampPercentages map[string]float32
	// map of version IDs to task queue stats
	stats map[string]temporaliov1alpha1.QueueStatistics
	// map of version IDs to version status
	versionStatus map[string]temporaliov1alpha1.VersionStatus
	// map of version IDs to drained since timestamps.
	drainedSince map[string]*metav1.Time
	// map of version IDs to test workflow executions
	testWorkflowStatus map[string][]temporaliov1alpha1.WorkflowExecution
	// map of version IDs to task queues
	taskQueues map[string][]string
}

func (c *deploymentVersionCollection) getDeployment(versionID string) (*appsv1.Deployment, bool) {
	d, ok := c.versionIDsToDeployments[versionID]
	return d, ok
}

func (c *deploymentVersionCollection) getStatus(versionID string) (temporaliov1alpha1.VersionStatus, bool) {
	s, ok := c.versionStatus[versionID]
	return s, ok
}

func (c *deploymentVersionCollection) getDrainedSince(versionID string) (*metav1.Time, bool) {
	t, ok := c.drainedSince[versionID]
	return t, ok
}

func (c *deploymentVersionCollection) getWorkerDeploymentVersion(versionID string) (*temporaliov1alpha1.WorkerDeploymentVersion, *temporaliov1alpha1.QueueStatistics) {
	result := temporaliov1alpha1.WorkerDeploymentVersion{
		HealthySince:   nil,
		VersionID:      versionID,
		Status:         temporaliov1alpha1.VersionStatusNotRegistered,
		RampPercentage: nil,
		DrainedSince:   nil,
		Deployment:     nil,
	}

	// Set deployment ref and health status
	if d, ok := c.getDeployment(versionID); ok {
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
	// TODO(carlydf): Support setting any ramp in [0,100]
	if ramp, ok := c.rampPercentages[versionID]; ok && ramp != 100 {
		result.RampPercentage = &ramp
	}

	// Set version status
	if st, ok := c.versionStatus[versionID]; ok {
		result.Status = st
	}

	// Set drained since
	if ds, ok := c.drainedSince[versionID]; ok {
		result.DrainedSince = ds
	}

	var stats temporaliov1alpha1.QueueStatistics
	if s, ok := c.stats[versionID]; ok {
		stats = s
	}

	// Set test workflow status
	if testWorkflows, ok := c.testWorkflowStatus[versionID]; ok {
		result.TestWorkflows = testWorkflows
	}

	for _, tq := range c.taskQueues[versionID] {
		result.TaskQueues = append(result.TaskQueues, temporaliov1alpha1.TaskQueue{
			Name: tq,
		})
	}

	return &result, &stats
}

func (c *deploymentVersionCollection) addDeployment(versionID string, d *appsv1.Deployment) {
	c.versionIDsToDeployments[versionID] = d
}

func (c *deploymentVersionCollection) addAssignmentRule(rule *taskqueue.BuildIdAssignmentRule) {
	// Skip updating existing values (only the first one should take effect)
	if _, ok := c.rampPercentages[rule.GetTargetBuildId()]; ok {
		return
	}
	if ramp := rule.GetPercentageRamp(); ramp != nil {
		c.rampPercentages[rule.GetTargetBuildId()] = ramp.GetRampPercentage()
	} else {
		c.rampPercentages[rule.GetTargetBuildId()] = 100
	}
}

func (c *deploymentVersionCollection) addVersionStatus(version string, status temporaliov1alpha1.VersionStatus) {
	c.versionStatus[version] = status
}

func (c *deploymentVersionCollection) addDrainedSince(version string, drainedSince time.Time) {
	t := metav1.NewTime(drainedSince)
	c.drainedSince[version] = &t
}

func (c *deploymentVersionCollection) addTaskQueue(versionID, name string) {
	c.taskQueues[versionID] = append(c.taskQueues[versionID], name)
}

func (c *deploymentVersionCollection) addTestWorkflowStatus(versionID string, info *workflow.WorkflowExecutionInfo) error {
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
	c.testWorkflowStatus[versionID] = append(c.testWorkflowStatus[versionID], temporaliov1alpha1.WorkflowExecution{
		WorkflowID: info.GetExecution().GetWorkflowId(),
		RunID:      info.GetExecution().GetRunId(),
		TaskQueue:  info.GetTaskQueue(),
		Status:     s,
	})

	return nil
}

func (c *deploymentVersionCollection) addTaskQueueStats(versionID string, info *taskqueue.TaskQueueVersionInfo) error {
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
	c.stats[versionID] = totalStats

	return nil
}

func (c *deploymentVersionCollection) addStats(versionID string, info map[int32]*taskqueue.TaskQueueTypeInfo) error {
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
	c.stats[versionID] = totalStats

	return nil
}

func newDeploymentVersionCollection() deploymentVersionCollection {
	return deploymentVersionCollection{
		versionIDsToDeployments: make(map[string]*appsv1.Deployment),
		rampPercentages:         make(map[string]float32),
		stats:                   make(map[string]temporaliov1alpha1.QueueStatistics),
		versionStatus:           make(map[string]temporaliov1alpha1.VersionStatus),
		drainedSince:            make(map[string]*metav1.Time),
		testWorkflowStatus:      make(map[string][]temporaliov1alpha1.WorkflowExecution),
		taskQueues:              make(map[string][]string),
	}
}

func wasModifiedExternally(workerDeploymentInfo *sdkclient.WorkerDeploymentInfo) bool {
	return workerDeploymentInfo.LastModifierIdentity != controllerIdentity &&
		workerDeploymentInfo.LastModifierIdentity != ""
}

func (r *TemporalWorkerDeploymentReconciler) generateStatus(ctx context.Context, l logr.Logger, temporalClient temporalClient.Client, req ctrl.Request, workerDeploy *temporaliov1alpha1.TemporalWorkerDeployment) (*temporaliov1alpha1.TemporalWorkerDeploymentStatus, error) {
	var (
		desiredVersionID, defaultVersionID string
		deployedVersions                   []string
		versions                           = newDeploymentVersionCollection()
		externallyModified                 = false
	)

	workerDeploymentName := computeWorkerDeploymentName(workerDeploy)
	desiredVersionID = computeVersionID(workerDeploy)

	// List k8s deployments that correspond to managed worker deployment versions
	var childDeploys appsv1.DeploymentList
	if err := r.List(ctx, &childDeploys, client.InNamespace(req.Namespace), client.MatchingFields{deployOwnerKey: req.Name}); err != nil {
		return nil, fmt.Errorf("unable to list child deployments: %w", err)
	}
	// Sort deployments by creation timestamp
	sort.SliceStable(childDeploys.Items, func(i, j int) bool {
		return childDeploys.Items[i].ObjectMeta.CreationTimestamp.Before(&childDeploys.Items[j].ObjectMeta.CreationTimestamp)
	})
	// Track each k8s deployment by version ID
	for _, childDeploy := range childDeploys.Items {
		if buildID, ok := childDeploy.GetLabels()[buildIDLabel]; ok {
			versionID := workerDeploymentName + "." + buildID
			versions.addDeployment(versionID, &childDeploy)
			deployedVersions = append(deployedVersions, versionID)
			continue
		}
		// TODO(jlegrone): implement some error handling (maybe a human deleted the label?)
	}

	// Get deployment handler
	deploymentHandler := temporalClient.WorkerDeploymentClient().GetHandle(workerDeploymentName)

	// List deployment versions in Temporal
	describeResp, err := describeWorkerDeploymentHandleNotFound(ctx, deploymentHandler, workerDeploymentName)
	if err != nil {
		return nil, fmt.Errorf("unable to describe worker deployment %s: %w", workerDeploymentName, err)
	}
	workerDeploymentInfo := describeResp.Info
	routingConfig := workerDeploymentInfo.RoutingConfig
	defaultVersionID = routingConfig.CurrentVersion

	// Check if the worker deployment was modified out of band of the controller (eg. via the Temporal CLI)
	if wasModifiedExternally(&workerDeploymentInfo) {
		externallyModified = true
		l.Info("Worker deployment was modified by an external system", "lastModifier", workerDeploymentInfo.LastModifierIdentity)
	}

	var rampingSinceTime *metav1.Time
	var rampPercentage float32
	// For each version the server has registered in the worker deployment, compute the status.
	for _, version := range workerDeploymentInfo.VersionSummaries {
		drainageStatus := version.DrainageStatus
		var versionStatus temporaliov1alpha1.VersionStatus
		if version.Version == routingConfig.CurrentVersion {
			versionStatus = temporaliov1alpha1.VersionStatusCurrent
		} else if version.Version == routingConfig.RampingVersion {
			versionStatus = temporaliov1alpha1.VersionStatusRamping
			rt := metav1.NewTime(routingConfig.RampingVersionChangedTime)
			rampingSinceTime = &rt
			rampPercentage = routingConfig.RampingVersionPercentage
			l.Info(fmt.Sprintf("version %s has been ramping since %s, current ramp percentage %v", version.Version, rt.String(), rampPercentage))
		} else if drainageStatus == sdkclient.WorkerDeploymentVersionDrainageStatusDraining {
			versionStatus = temporaliov1alpha1.VersionStatusDraining
		} else if drainageStatus == sdkclient.WorkerDeploymentVersionDrainageStatusDrained {
			versionStatus = temporaliov1alpha1.VersionStatusDrained
			// see when it was drained
			versionResp, err := deploymentHandler.DescribeVersion(ctx, sdkclient.WorkerDeploymentDescribeVersionOptions{
				Version: version.Version,
			})
			if err != nil {
				l.Error(err, "unable to describe version to see when it drained")
				return nil, fmt.Errorf("unable to describe version for version %q: %w", version, err)
			}
			drainedSinceTime := versionResp.Info.DrainageInfo.LastChangedTime
			versions.addDrainedSince(version.Version, drainedSinceTime)
		} else {
			versionStatus = temporaliov1alpha1.VersionStatusInactive
		}
		versions.addVersionStatus(version.Version, versionStatus)
	}

	// Check the status of the test workflow for the next version, if rollout is still happening.
	if desiredVersionID != routingConfig.CurrentVersion {
		// Describe the desired version to get task queue information
		// Temporal will error if any task queue in the existing current version is not present in the new current version.
		// Temporal will also error if any task queue in the existing current version is not present in the new ramping version.
		versionResp, err := deploymentHandler.DescribeVersion(ctx, sdkclient.WorkerDeploymentDescribeVersionOptions{
			Version: desiredVersionID,
		})
		var notFound *serviceerror.NotFound
		if err != nil && !errors.As(err, &notFound) {
			// Ignore NotFound error, because if the version is not found, we know there are no test workflows running on it.
			return nil, fmt.Errorf("unable to describe worker deployment version for version %q: %w", desiredVersionID, err)
		}
		for _, tq := range versionResp.Info.TaskQueuesInfos {
			// Keep track of which task queues this version of the worker is polling on
			if tq.Type != sdkclient.TaskQueueTypeWorkflow {
				continue
			}
			versions.addTaskQueue(desiredVersionID, tq.Name)

			// If there is a test workflow associated with this task queue and build id, check its status.
			wf, err := temporalClient.DescribeWorkflowExecution(
				ctx,
				getTestWorkflowID(computeWorkerDeploymentName(workerDeploy), tq.Name, desiredVersionID),
				"",
			)
			// TODO(jlegrone): Detect "not found" errors properly
			if err != nil && !strings.Contains(err.Error(), "workflow not found") {
				return nil, fmt.Errorf("unable to describe test workflow: %w", err)
			}
			if err := versions.addTestWorkflowStatus(desiredVersionID, wf.GetWorkflowExecutionInfo()); err != nil {
				return nil, fmt.Errorf("error computing test workflow status for version %q: %w", desiredVersionID, err)
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

	// TODO(carlydf): make sure target version gets inactive status

	// Reconcile deployments that exist in k8s without a corresponding version in temporal.
	// Temporal has deleted these versions, which probably would have only happened if they
	// stopped polling the server, so these are likely already scaled to zero.
	var deprecatedVersions []*temporaliov1alpha1.WorkerDeploymentVersion
	for _, version := range deployedVersions {
		switch version {
		case desiredVersionID, defaultVersionID:
			continue
		}
		d, _ := versions.getWorkerDeploymentVersion(version) // TODO (Shivam): How do we know these versions have been deleted by Temporal? They could just be draining...
		deprecatedVersions = append(deprecatedVersions, d)
	}

	var (
		defaultVersion, _ = versions.getWorkerDeploymentVersion(defaultVersionID)
		targetVersion, _  = versions.getWorkerDeploymentVersion(desiredVersionID)
	)

	// Ugly hack to clear ramp percentages (not quite correctly) for now
	for _, d := range deprecatedVersions {
		d.RampPercentage = nil
	}
	if defaultVersion != nil {
		defaultVersion.RampPercentage = nil
		if defaultVersion.VersionID == targetVersion.VersionID {
			targetVersion.RampPercentage = nil
		}
	}
	if targetVersion != nil {
		targetVersion.RampPercentage = &rampPercentage
		targetVersion.RampingSince = rampingSinceTime
	}

	return &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
		DefaultVersion:       defaultVersion,
		TargetVersion:        targetVersion,
		DeprecatedVersions:   deprecatedVersions,
		VersionConflictToken: []byte("todo"),
		ExternallyModified:   externallyModified,
	}, nil
}
