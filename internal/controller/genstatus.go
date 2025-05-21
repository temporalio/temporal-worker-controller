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

func (r *TemporalWorkerDeploymentReconciler) generateStatus(
	ctx context.Context,
	l logr.Logger,
	temporalClient temporalClient.Client,
	req ctrl.Request,
	workerDeploy *temporaliov1alpha1.TemporalWorkerDeployment,
) (*temporaliov1alpha1.TemporalWorkerDeploymentStatus, error) {
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

func (r *TemporalWorkerDeploymentReconciler) generateStatus(ctx context.Context, l logr.Logger, temporalClient temporalClient.Client, req ctrl.Request, workerDeploy *temporaliov1alpha1.TemporalWorkerDeployment) (*temporaliov1alpha1.TemporalWorkerDeploymentStatus, error) {
	var (
		desiredVersionID, defaultVersionID string
		deployedVersions                   []string
		versions                           = newDeploymentVersionCollection()
	)

	workerDeploymentName := computeWorkerDeploymentName(workerDeploy)
	desiredVersionID := computeVersionID(workerDeploy)

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
	if desiredVersionID != temporalState.DefaultVersionID {
		testWorkflows, err := temporal.GetTestWorkflowStatus(
			ctx,
			temporalClient,
			workerDeploymentName,
			desiredVersionID,
			workerDeploy,
		)
		if err != nil {
			l.Error(err, "error getting test workflow status")
			// Continue without test workflow status
		}

		// Add test workflow status to version info if it doesn't exist
		if versionInfo, exists := temporalState.Versions[desiredVersionID]; exists {
			versionInfo.TestWorkflows = append(versionInfo.TestWorkflows, testWorkflows...)
		}
	}

	// Use the state mapper to convert state objects to CRD status
	stateMapper := newStateMapper(k8sState, temporalState)
	status := stateMapper.mapToStatus(desiredVersionID)

	return status, nil
}
