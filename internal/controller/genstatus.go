// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package controller

import (
	"context"
	"fmt"
	"math"
	"sort"

	"github.com/go-logr/logr"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/taskqueue/v1"
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
	// map of build IDs to redirect (keys) to other build IDs (values)
	redirectBuildIDFromTo map[string]string
	// map of build IDs to ramp percentages [0,100]
	rampPercentages map[string]uint8
	// map of build IDs to task queue stats
	stats map[string]temporaliov1alpha1.QueueStatistics
	// map of build IDs to reachability
	reachabilityStatus map[string]temporaliov1alpha1.ReachabilityStatus
}

func (c *versionedDeploymentCollection) getDeployment(buildID string) (*appsv1.Deployment, bool) {
	if redirectedBuildID, ok := c.redirectBuildIDFromTo[buildID]; ok {
		return c.getDeployment(redirectedBuildID)
	}
	d, ok := c.buildIDsToDeployments[buildID]
	return d, ok
}

func (c *versionedDeploymentCollection) getVersionedDeployment(buildID string) *temporaliov1alpha1.VersionedDeployment {
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
	if status, ok := c.reachabilityStatus[buildID]; ok {
		result.Reachability = status
	}

	// Set stats
	if stats, ok := c.stats[buildID]; ok {
		result.Statistics = &stats
	}

	return &result
}

func (c *versionedDeploymentCollection) addBuildIDRedirect(from, to string) {
	c.redirectBuildIDFromTo[from] = to
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

func (c *versionedDeploymentCollection) addReachability(buildID string, info *taskqueue.TaskQueueVersionInfo) error {
	info.GetTaskReachability()

	var reachability temporaliov1alpha1.ReachabilityStatus
	switch info.GetTaskReachability() {
	case enums.BUILD_ID_TASK_REACHABILITY_REACHABLE:
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

func newVersionedDeploymentCollection() versionedDeploymentCollection {
	return versionedDeploymentCollection{
		buildIDsToDeployments: make(map[string]*appsv1.Deployment),
		redirectBuildIDFromTo: make(map[string]string),
		rampPercentages:       make(map[string]uint8),
		stats:                 make(map[string]temporaliov1alpha1.QueueStatistics),
		reachabilityStatus:    make(map[string]temporaliov1alpha1.ReachabilityStatus),
	}
}

func (r *TemporalWorkerReconciler) generateStatus(ctx context.Context, l logr.Logger, temporalClient workflowservice.WorkflowServiceClient, req ctrl.Request, workerDeploy *temporaliov1alpha1.TemporalWorker) (*temporaliov1alpha1.TemporalWorkerStatus, *workflowservice.GetWorkerVersioningRulesResponse, error) {
	var (
		desiredBuildID, defaultBuildID string
		deployedBuildIDs               []string
		versions                       = newVersionedDeploymentCollection()
	)

	desiredBuildID = computeBuildID(&workerDeploy.Spec)

	// Get managed worker deployments
	var childDeploys appsv1.DeploymentList
	if err := r.List(ctx, &childDeploys, client.InNamespace(req.Namespace), client.MatchingFields{deployOwnerKey: req.Name}); err != nil {
		return nil, nil, fmt.Errorf("unable to list child deployments: %w", err)
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

	// Get worker versioning rules via Temporal API
	rules, err := temporalClient.GetWorkerVersioningRules(ctx, &workflowservice.GetWorkerVersioningRulesRequest{
		Namespace: workerDeploy.Spec.WorkerOptions.TemporalNamespace,
		TaskQueue: workerDeploy.Spec.WorkerOptions.TaskQueue,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("unable to get worker versioning rules: %w", err)
	}
	// Register redirect rules
	for _, rule := range rules.GetCompatibleRedirectRules() {
		versions.addBuildIDRedirect(rule.GetRule().GetSourceBuildId(), rule.GetRule().GetTargetBuildId())
	}
	// Set default version and deprecated versions based on assignment rules
	for _, rule := range rules.GetAssignmentRules() {
		ruleTargetBuildID := rule.GetRule().GetTargetBuildId()
		// Register the rule
		versions.addAssignmentRule(rule.GetRule())

		// TODO(jlegrone): Do rules need to be sorted by create time?

		// Only update defaultBuildID if it's not already set
		if defaultBuildID == "" {
			if rule.GetRule().GetRamp() == nil {
				// Set the default build ID if this is the first assignment rule without
				// a ramp.
				defaultBuildID = ruleTargetBuildID
				continue
			} else if rule.GetRule().GetPercentageRamp().GetRampPercentage() == 100 {
				// If the default build ID has a ramp of 100%, then it's the default
				// build ID.
				defaultBuildID = ruleTargetBuildID
				continue
			}
		}
		// Don't mark the desired build ID as deprecated
		if ruleTargetBuildID == desiredBuildID {
			continue
		}
		// All rules after this point are not applicable since there is already a default build ID,
		// so assume they are deprecated.
		// TODO(jlegrone): Double check that this is correct. We also might need to delete unused
		//                 assignment rules during the plan phase.
		// TODO(jlegrone): Do we need to garbage collect assignment rules for versions that have no deployment?
	}

	// Get reachability info for all build IDs associated with the task queue via the Temporal API
	tq, err := temporalClient.DescribeTaskQueue(ctx, &workflowservice.DescribeTaskQueueRequest{
		ApiMode:   enums.DESCRIBE_TASK_QUEUE_MODE_ENHANCED,
		Namespace: workerDeploy.Spec.WorkerOptions.TemporalNamespace,
		TaskQueue: &taskqueue.TaskQueue{
			Name: workerDeploy.Spec.WorkerOptions.TaskQueue,
			Kind: enums.TASK_QUEUE_KIND_NORMAL,
		},
		Versions: &taskqueue.TaskQueueVersionSelection{
			// Including deployed build IDs means that we'll observe the "UnReachable" status even for versions
			// that are no longer known to the server. Not including this option means we can see the "NotRegistered"
			// status and trigger deletion rather than scaling to zero.
			//
			// This can also lead to the following error: Too many build ids queried at once with ReportTaskReachability==true, limit: 5
			//BuildIds:  deployedBuildIDs,
			AllActive: true,
		},
		ReportStats:            true,
		ReportTaskReachability: true,
		ReportPollers:          false,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("unable to describe task queue: %w", err)
	}
	for buildID, info := range tq.GetVersionsInfo() {
		if err := versions.addReachability(buildID, info); err != nil {
			return nil, nil, fmt.Errorf("error computing reachability for build ID %q: %w", buildID, err)
		}
	}

	var deprecatedVersions []*temporaliov1alpha1.VersionedDeployment
	for _, buildID := range deployedBuildIDs {
		switch buildID {
		case desiredBuildID, defaultBuildID:
			continue
		}
		deprecatedVersions = append(deprecatedVersions, versions.getVersionedDeployment(buildID))
	}

	var (
		defaultVersion = versions.getVersionedDeployment(defaultBuildID)
		targetVersion  = versions.getVersionedDeployment(desiredBuildID)
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
		VersionConflictToken: rules.GetConflictToken(),
	}, rules, nil
}

func convertFloatToUint(val float32) uint8 {
	return uint8(math.Round(float64(val)))
}
