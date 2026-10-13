// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package controller

import (
	"cmp"
	"slices"

	"github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	"github.com/temporalio/temporal-worker-controller/internal/k8s"
	"github.com/temporalio/temporal-worker-controller/internal/temporal"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// stateMapper maps between Kubernetes and Temporal states
type stateMapper struct {
	k8sState             *k8s.DeploymentState
	temporalState        *temporal.TemporalWorkerState
	workerDeploymentName string
	// targetSpec, when set, lists the groups the target version must run before it is healthy.
	targetSpec *v1alpha1.WorkerDeploymentSpec
}

// newStateMapper creates a new state mapper
func newStateMapper(k8sState *k8s.DeploymentState, temporalState *temporal.TemporalWorkerState, workerDeploymentName string) *stateMapper {
	return &stateMapper{
		k8sState:             k8sState,
		temporalState:        temporalState,
		workerDeploymentName: workerDeploymentName,
	}
}

// mapToStatus converts the states to a CRD status
func (m *stateMapper) mapToStatus(targetBuildID string) *v1alpha1.WorkerDeploymentStatus {
	status := &v1alpha1.WorkerDeploymentStatus{
		VersionConflictToken: m.temporalState.VersionConflictToken,
	}

	status.LastModifierIdentity = m.temporalState.LastModifierIdentity
	status.ManagerIdentity = m.temporalState.ManagerIdentity

	// Get build IDs directly from temporal state
	currentBuildID := m.temporalState.CurrentBuildID
	rampingBuildID := m.temporalState.RampingBuildID

	// Set current version
	status.CurrentVersion = m.mapCurrentWorkerDeploymentVersionByBuildID(currentBuildID)

	// Set target version (desired version)
	status.TargetVersion = m.mapTargetWorkerDeploymentVersionByBuildID(targetBuildID)
	if rampingBuildID == targetBuildID {
		status.TargetVersion.RampingSince = m.temporalState.RampingSince
		status.TargetVersion.RampLastModifiedAt = m.temporalState.RampLastModifiedAt
		rampPercentage := m.temporalState.RampPercentage
		status.TargetVersion.RampPercentage = &rampPercentage
	}

	// Add deprecated versions
	var deprecatedVersions []*v1alpha1.DeprecatedWorkerDeploymentVersion
	for _, buildID := range m.k8sState.BuildIDs() {
		// Skip current and target versions
		if buildID == currentBuildID || buildID == targetBuildID {
			continue
		}

		versionStatus := m.mapDeprecatedWorkerDeploymentVersionByBuildID(buildID)
		if versionStatus != nil {
			deprecatedVersions = append(deprecatedVersions, versionStatus)
		}
	}
	// NOTE(jaypipes): Need to sort the deprecated versions here in order to
	// prevent sort order differences from causing unnecessary status
	// generation increments.
	//
	// See: https://github.com/temporalio/temporal-worker-controller/issues/415
	slices.SortStableFunc(
		deprecatedVersions,
		func(a, b *v1alpha1.DeprecatedWorkerDeploymentVersion) int {
			return cmp.Compare(a.BuildID, b.BuildID)
		},
	)
	status.DeprecatedVersions = deprecatedVersions

	// Set version count from temporal state (directly from VersionSummaries via Versions map)
	status.VersionCount = int32(len(m.temporalState.Versions))

	return status
}

// mapCurrentWorkerDeploymentVersionByBuildID creates a current version status from the states using buildID
func (m *stateMapper) mapCurrentWorkerDeploymentVersionByBuildID(buildID string) *v1alpha1.CurrentWorkerDeploymentVersion {
	if buildID == "" {
		return nil
	}

	version := &v1alpha1.CurrentWorkerDeploymentVersion{
		BaseWorkerDeploymentVersion: v1alpha1.BaseWorkerDeploymentVersion{
			BuildID: buildID,
			Status:  v1alpha1.VersionStatusNotRegistered,
		},
	}

	m.setVersionDeployments(&version.BaseWorkerDeploymentVersion, buildID)

	// Set version status from temporal state
	if temporalVersion, exists := m.temporalState.Versions[buildID]; exists {
		version.Status = temporalVersion.Status

		// Set task queues
		version.TaskQueues = append(version.TaskQueues, temporalVersion.TaskQueues...)
	}

	return version
}

// mapTargetWorkerDeploymentVersionByBuildID creates a target version status from the states using buildID
func (m *stateMapper) mapTargetWorkerDeploymentVersionByBuildID(buildID string) v1alpha1.TargetWorkerDeploymentVersion {
	version := v1alpha1.TargetWorkerDeploymentVersion{
		BaseWorkerDeploymentVersion: v1alpha1.BaseWorkerDeploymentVersion{
			BuildID: buildID,
			Status:  v1alpha1.VersionStatusNotRegistered,
		},
	}

	if buildID == "" {
		return version
	}

	m.setVersionDeployments(&version.BaseWorkerDeploymentVersion, buildID)
	// The stricter rule gates promotion only; a current target keeps the plain health check.
	if m.targetSpec != nil && m.targetSpec.HasWorkerGroups() && buildID != m.temporalState.CurrentBuildID {
		m.applyTargetGroupHealth(&version.BaseWorkerDeploymentVersion, buildID)
	}

	// Set version status from temporal state
	if temporalVersion, exists := m.temporalState.Versions[buildID]; exists {
		version.Status = temporalVersion.Status

		// Set ramp percentage if this is a ramping version
		if temporalVersion.Status == v1alpha1.VersionStatusRamping && m.temporalState.RampPercentage > 0 {
			rampPercentage := m.temporalState.RampPercentage
			version.RampPercentage = &rampPercentage
		}

		// Set task queues
		version.TaskQueues = append(version.TaskQueues, temporalVersion.TaskQueues...)

		// Set test workflows
		version.TestWorkflows = append(version.TestWorkflows, temporalVersion.TestWorkflows...)
	}

	return version
}

// mapDeprecatedWorkerDeploymentVersionByBuildID creates a deprecated version status from the states using buildID
func (m *stateMapper) mapDeprecatedWorkerDeploymentVersionByBuildID(buildID string) *v1alpha1.DeprecatedWorkerDeploymentVersion {
	if buildID == "" {
		return nil
	}

	// A drained version is eligible for server-side auto-deletion only if its pollers
	// have stopped. We approximate this by checking whether a controller-managed k8s
	// Deployment with active replicas exists; if so, pollers are likely still running
	// and the server cannot delete the version yet, making it ineligible for deletion.
	// Note: This only considers pollers managed by the worker-controller. External
	// pollers (e.g., manually deployed workers) are not accounted for.
	eligibleForDeletion := false
	if vInfo, exists := m.temporalState.Versions[buildID]; exists {
		hasActiveDeployment := false
		for _, d := range m.k8sState.VersionDeployments(buildID) {
			hasActiveDeployment = hasActiveDeployment || d.Status.Replicas > 0
		}
		eligibleForDeletion = vInfo.Status == v1alpha1.VersionStatusDrained && !hasActiveDeployment
	}

	version := &v1alpha1.DeprecatedWorkerDeploymentVersion{
		BaseWorkerDeploymentVersion: v1alpha1.BaseWorkerDeploymentVersion{
			BuildID: buildID,
			Status:  v1alpha1.VersionStatusNotRegistered,
		},
		EligibleForDeletion: eligibleForDeletion,
	}

	m.setVersionDeployments(&version.BaseWorkerDeploymentVersion, buildID)

	// Set version status from temporal state
	if temporalVersion, exists := m.temporalState.Versions[buildID]; exists {
		version.Status = temporalVersion.Status

		// Set drained since if available
		if temporalVersion.DrainedSince != nil {
			drainedSince := metav1.NewTime(*temporalVersion.DrainedSince)
			version.DrainedSince = &drainedSince
		}

		// Set task queues
		version.TaskQueues = append(version.TaskQueues, temporalVersion.TaskQueues...)
	}

	return version
}

// setVersionDeployments points a version at its default group's deployment and marks
// it healthy once every group's deployment is available.
func (m *stateMapper) setVersionDeployments(version *v1alpha1.BaseWorkerDeploymentVersion, buildID string) {
	deployments := m.k8sState.VersionDeployments(buildID)
	if len(deployments) == 0 {
		return
	}
	version.Deployment = m.k8sState.DeploymentRefs[buildID]
	version.HealthySince = versionHealthySince(deployments)
	version.WorkerGroups = groupStatuses(deployments)
}

// groupStatuses reports each group of a multi-group version, sorted by name so status
// doesn't churn. Versions without group labels keep an empty list.
func groupStatuses(deployments map[string]*appsv1.Deployment) []v1alpha1.WorkerGroupStatus {
	if !k8s.HasWorkerGroupLabel(deployments) {
		return nil
	}
	groups := make([]v1alpha1.WorkerGroupStatus, 0, len(deployments))
	for name, d := range deployments {
		group := v1alpha1.WorkerGroupStatus{Name: name, Deployment: k8s.NewObjectRef(d)}
		if healthy, since := k8s.IsDeploymentHealthy(d); healthy {
			group.HealthySince = since
		}
		groups = append(groups, group)
	}
	slices.SortFunc(groups, func(a, b v1alpha1.WorkerGroupStatus) int { return cmp.Compare(a.Name, b.Name) })
	return groups
}

// applyTargetGroupHealth marks each spec group of a multi-group target healthy only once it
// also has an available replica, and the version once every spec group is.
func (m *stateMapper) applyTargetGroupHealth(version *v1alpha1.BaseWorkerDeploymentVersion, buildID string) {
	deployments := m.k8sState.VersionDeployments(buildID)
	var times []*metav1.Time
	for _, group := range m.targetSpec.WorkerGroupNames() {
		var since *metav1.Time
		if d, ok := deployments[group]; ok {
			since = targetGroupHealthySince(m.targetSpec, group, d)
		}
		times = append(times, since)
		for i := range version.WorkerGroups {
			if version.WorkerGroups[i].Name == group {
				version.WorkerGroups[i].HealthySince = since
			}
		}
	}
	version.HealthySince = latestOrNil(times)
}

// targetGroupHealthySince also requires an available replica: a Deployment is Available at
// zero replicas, which would pass a group that never polled.
func targetGroupHealthySince(spec *v1alpha1.WorkerDeploymentSpec, group string, d *appsv1.Deployment) *metav1.Time {
	healthy, since := k8s.IsDeploymentHealthy(d)
	if !healthy {
		return nil
	}
	groupSpec, _ := spec.WorkerGroupDeploymentSpec(group)
	scaledToZero := groupSpec.Replicas != nil && *groupSpec.Replicas == 0
	if d.Status.AvailableReplicas < 1 && !scaledToZero {
		return nil
	}
	return since
}

// versionHealthySince returns when the last of the deployments became available, or
// nil while any of them is unavailable.
func versionHealthySince(deployments map[string]*appsv1.Deployment) *metav1.Time {
	times := make([]*metav1.Time, 0, len(deployments))
	for _, d := range deployments {
		_, since := k8s.IsDeploymentHealthy(d)
		times = append(times, since)
	}
	return latestOrNil(times)
}

// latestOrNil returns the latest of the times, or nil if any of them is nil.
func latestOrNil(times []*metav1.Time) *metav1.Time {
	var latest *metav1.Time
	for _, t := range times {
		if t == nil {
			return nil
		}
		if latest == nil || t.After(latest.Time) {
			latest = t
		}
	}
	return latest
}
