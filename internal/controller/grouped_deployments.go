// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

// Grouped worker reconciliation runs inside the existing WorkerDeployment
// controller. Saved build records own member templates and counts; Temporal owns
// routing and drainage. Missing members are restored from their saved build.
package controller

import (
	"fmt"
	"slices"
	"time"

	"github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	"github.com/temporalio/temporal-worker-controller/internal/k8s"
	"github.com/temporalio/temporal-worker-controller/internal/temporal"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// planGroupedDeployments restores saved members and applies retirement to the
// whole build. A new resource specification never rewrites a historical build.
func (r *WorkerDeploymentReconciler) planGroupedDeployments(
	w *v1alpha1.WorkerDeployment,
	connection v1alpha1.ConnectionSpec,
	temporalState *temporal.TemporalWorkerState,
	state *k8s.DeploymentState,
	p *plan,
	mayCreate bool,
) error {
	target := k8s.ComputeBuildID(w)
	if len(w.Spec.AdditionalDeployments) > 0 || state.Revisions[target] != nil {
		requested, err := k8s.NewGroupRevision(w, target, r.Scheme)
		if err != nil {
			return err
		}
		if saved := state.Revisions[target]; saved != nil {
			if !slices.Equal(saved.Object.Data.Raw, requested.Object.Data.Raw) {
				return fmt.Errorf("grouped build %q already has different saved settings; choose a new build ID", target)
			}
		} else if state.Deployments[target] != nil || temporalState.Versions[target] != nil {
			return fmt.Errorf("build %q already exists without saved grouped settings; choose a new build ID", target)
		} else if mayCreate {
			if state.Revisions == nil {
				state.Revisions = make(map[string]*k8s.GroupRevision)
			}
			state.Revisions[target] = requested
			p.CreateRevisions = append(p.CreateRevisions, requested.Object)
		}
	}

	for buildID, saved := range state.Revisions {
		members, err := saved.Deployments(w, buildID, connection, r.Scheme)
		if err != nil {
			return err
		}
		version := temporalState.Versions[buildID]
		markedForDeletion := saved.Object.Annotations[k8s.GroupDeletionAnnotation] == "true"
		retiring := markedForDeletion
		deleting := markedForDeletion
		if buildID != target && buildID != temporalState.CurrentBuildID && buildID != temporalState.RampingBuildID && version != nil && version.Status == v1alpha1.VersionStatusDrained && version.DrainedSince != nil {
			elapsed := time.Since(*version.DrainedSince)
			retiring = retiring || elapsed > saved.Spec.SunsetStrategy.ScaledownDelay.Duration
			deleting = deleting || elapsed > saved.Spec.SunsetStrategy.ScaledownDelay.Duration+saved.Spec.SunsetStrategy.DeleteDelay.Duration && groupStopped(state.Members[buildID], members)
		}
		if deleting && !markedForDeletion {
			p.MarkRevisionsForDeletion = append(p.MarkRevisionsForDeletion, saved.Object)
		}
		for name, desired := range members {
			actual := state.Members[buildID][name]
			if deleting {
				if actual != nil {
					p.DeleteDeployments = append(p.DeleteDeployments, actual)
				}
				continue
			}
			if retiring {
				zero := int32(0)
				desired.Spec.Replicas = &zero
			}
			if actual == nil {
				p.CreateDeployments = append(p.CreateDeployments, desired)
				continue
			}
			if actual.Spec.Replicas == nil || *actual.Spec.Replicas != *desired.Spec.Replicas {
				p.ScaleDeployments[k8s.NewObjectRef(actual)] = uint32(*desired.Spec.Replicas)
			}
		}
		if deleting && len(state.Members[buildID]) == 0 {
			p.DeleteRevisions = append(p.DeleteRevisions, saved.Object)
		}
	}
	if saved := state.Revisions[target]; saved != nil {
		pendingMemberUpdate := slices.ContainsFunc(p.UpdateDeployments, func(deployment *appsv1.Deployment) bool {
			return deployment.Labels[k8s.BuildIDLabel] == target && deployment.Labels[k8s.MemberLabel] != ""
		})

		ready := !pendingMemberUpdate && saved.Object.Annotations[k8s.GroupDeletionAnnotation] != "true" && groupReady(state.Members[target], saved) && expectedQueuesRegistered(target, saved.Spec.ExpectedTaskQueues, temporalState.Versions[target])
		if !ready {
			// This plan can observe a newer member state than status mapping,
			// or replace pods after a connection change. Keep readiness and gate
			// starts waiting until the intended pods and queues are observed.
			w.Status.TargetVersion.HealthySince = nil
			if w.Status.CurrentVersion != nil && w.Status.CurrentVersion.BuildID == target {
				w.Status.CurrentVersion.HealthySince = nil
			}
			p.startTestWorkflows = nil
		}
		if !ready || !groupGateSucceeded(saved, w.Status.TargetVersion.TestWorkflows) {
			p.UpdateVersionConfig = nil
		}
	}
	return nil
}

// applyGroupedStatus reports health only after every saved member has its exact
// intended pod count available and all declared task queue kinds have registered.
func applyGroupedStatus(
	status *v1alpha1.WorkerDeploymentStatus,
	state *k8s.DeploymentState,
	temporalState *temporal.TemporalWorkerState,
) {
	for buildID, saved := range state.Revisions {
		var base *v1alpha1.BaseWorkerDeploymentVersion
		switch {
		case status.TargetVersion.BuildID == buildID:
			base = &status.TargetVersion.BaseWorkerDeploymentVersion
		case status.CurrentVersion != nil && status.CurrentVersion.BuildID == buildID:
			base = &status.CurrentVersion.BaseWorkerDeploymentVersion
		default:
			for _, deprecated := range status.DeprecatedVersions {
				if deprecated.BuildID == buildID {
					base = &deprecated.BaseWorkerDeploymentVersion
					deprecated.EligibleForDeletion = groupHasNoPods(state.Members[buildID]) && deprecated.Status == v1alpha1.VersionStatusDrained
					break
				}
			}
			if base == nil {
				deprecated := &v1alpha1.DeprecatedWorkerDeploymentVersion{BaseWorkerDeploymentVersion: v1alpha1.BaseWorkerDeploymentVersion{BuildID: buildID, Status: v1alpha1.VersionStatusNotRegistered}}
				if version := temporalState.Versions[buildID]; version != nil {
					deprecated.Status = version.Status
					if version.DrainedSince != nil {
						t := metav1.NewTime(*version.DrainedSince)
						deprecated.DrainedSince = &t
					}
				}
				deprecated.EligibleForDeletion = groupHasNoPods(state.Members[buildID]) && deprecated.Status == v1alpha1.VersionStatusDrained
				status.DeprecatedVersions = append(status.DeprecatedVersions, deprecated)
				base = &deprecated.BaseWorkerDeploymentVersion
			}
		}
		base.HealthySince = nil
		if groupReady(state.Members[buildID], saved) {
			latest := metav1.Time{}
			for _, member := range state.Members[buildID] {
				_, since := k8s.IsDeploymentHealthy(member)
				if since != nil && since.After(latest.Time) {
					latest = *since
				}
			}
			base.HealthySince = &latest
		}
		if buildID == status.TargetVersion.BuildID && !expectedQueuesRegistered(buildID, saved.Spec.ExpectedTaskQueues, temporalState.Versions[buildID]) {
			base.HealthySince = nil
		}
		if status.CurrentVersion != nil && status.CurrentVersion.BuildID == buildID {
			status.CurrentVersion.HealthySince = base.HealthySince
		}
	}
	slices.SortFunc(status.DeprecatedVersions, func(a, b *v1alpha1.DeprecatedWorkerDeploymentVersion) int {
		if a.BuildID < b.BuildID {
			return -1
		}
		if a.BuildID > b.BuildID {
			return 1
		}
		return 0
	})
}

// groupReady checks each declared member, so one healthy member cannot conceal
// another member's failed rollout or a member not yet recreated after deletion.
func groupReady(actual map[string]*appsv1.Deployment, saved *k8s.GroupRevision) bool {
	primary := saved.Spec.DeploymentSpec()
	counts := map[string]int32{k8s.PrimaryMember: *primary.Replicas}
	for _, member := range saved.Spec.AdditionalDeployments {
		counts[member.Name] = member.Replicas
	}
	for name, count := range counts {
		deployment := actual[name]
		if deployment == nil || deployment.DeletionTimestamp != nil || deployment.Spec.Replicas == nil {
			return false
		}
		if *deployment.Spec.Replicas != count ||
			deployment.Status.ObservedGeneration < deployment.Generation ||
			deployment.Status.Replicas != count ||
			deployment.Status.UpdatedReplicas != count ||
			deployment.Status.ReadyReplicas != count ||
			deployment.Status.AvailableReplicas != count ||
			(deployment.Status.TerminatingReplicas != nil && *deployment.Status.TerminatingReplicas != 0) {
			return false
		}
		if available, _ := k8s.IsDeploymentHealthy(deployment); !available {
			return false
		}
	}
	return true
}

// expectedQueuesRegistered matches queue names and kinds observed by Temporal
// for this exact build. Workflow and activity queues with the same name differ.
func expectedQueuesRegistered(
	buildID string,
	expected []v1alpha1.ExpectedTaskQueue,
	version *temporal.VersionInfo,
) bool {
	if version == nil || version.BuildID != buildID || version.Status == v1alpha1.VersionStatusNotRegistered || version.Status == v1alpha1.VersionStatusCreated {
		return false
	}
	for _, queue := range expected {
		for _, kind := range queue.Kinds {
			found := false
			for _, registered := range version.RegisteredTaskQueues {
				if registered.Name == queue.Name && slices.Contains(registered.Kinds, kind) {
					found = true
					break
				}
			}
			if !found {
				return false
			}
		}
	}
	return true
}

// groupGateSucceeded requires success on every declared workflow queue when a
// gate is configured, including when a previous build is selected for rollback.
func groupGateSucceeded(saved *k8s.GroupRevision, workflows []v1alpha1.WorkflowExecution) bool {
	if saved.Spec.RolloutStrategy.Gate == nil {
		return true
	}
	for _, queue := range saved.Spec.ExpectedTaskQueues {
		if !slices.Contains(queue.Kinds, v1alpha1.TaskQueueWorkflow) {
			continue
		}
		completed := false
		for _, workflow := range workflows {
			if workflow.TaskQueue == queue.Name && workflow.Status == v1alpha1.WorkflowExecutionStatusCompleted {
				completed = true
				break
			}
		}
		if !completed {
			return false
		}
	}
	return true
}

// groupStopped requires Kubernetes to observe that every saved member has no
// remaining or terminating pods before its Deployments and saved record are deleted.
func groupStopped(actual, expected map[string]*appsv1.Deployment) bool {
	for name := range expected {
		deployment := actual[name]
		if deployment == nil || (deployment.Spec.Replicas == nil || *deployment.Spec.Replicas != 0 || deployment.Status.ObservedGeneration < deployment.Generation) {
			return false
		}
	}
	return groupHasNoPods(actual)
}

func groupHasNoPods(members map[string]*appsv1.Deployment) bool {
	for _, member := range members {
		if member.Status.Replicas != 0 || (member.Status.TerminatingReplicas != nil && *member.Status.TerminatingReplicas != 0) {
			return false
		}
	}
	return true
}
