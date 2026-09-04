// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package controller

import (
	"fmt"
	"sort"
	"strings"

	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	"github.com/temporalio/temporal-worker-controller/internal/temporal"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// setConditionProgressingForCurrent sets ConditionProgressing from task-queue poller
// presence for the version serving current traffic (buildID: CurrentVersion if set,
// else TargetVersion), and emits an event when the condition's reason changes. Called
// only when the target version's rollout has completed (VersionStatusCurrent) -- see
// syncConditions. Named around "task queues without pollers" rather than "poller
// health", since that's genuinely all this observes -- not overall worker health.
func (r *WorkerDeploymentReconciler) setConditionProgressingForCurrent(
	twd *temporaliov1alpha1.WorkerDeployment,
	temporalState *temporal.TemporalWorkerState,
) {
	buildID := twd.Status.TargetVersion.BuildID
	if twd.Status.CurrentVersion != nil {
		buildID = twd.Status.CurrentVersion.BuildID
	}

	// If buildID isn't in temporalState.Versions (temporalState is nil, or this
	// version wasn't described this reconcile), taskQueuesWithoutPollers and
	// describeErr stay at their zero values (nil, nil). progressingConditionFromTaskQueues
	// treats that the same as "never checked" -> ReasonPollerStatusUnknown, not as
	// healthy or unhealthy.
	var taskQueuesWithoutPollers []string
	var describeErr error
	if temporalState != nil {
		if versionInfo, exists := temporalState.Versions[buildID]; exists {
			taskQueuesWithoutPollers = versionInfo.TaskQueuesWithoutPollers
			describeErr = versionInfo.TaskQueueDescribeError
		}
	}

	status, reason, affectedQueues := progressingConditionFromTaskQueues(taskQueuesWithoutPollers, describeErr)

	var message string
	switch reason {
	case temporaliov1alpha1.ReasonWaitingForPollers:
		message = fmt.Sprintf("Version %s has no active pollers on task queue(s): %s", buildID, strings.Join(affectedQueues, ", "))
	case temporaliov1alpha1.ReasonActivePollers:
		message = fmt.Sprintf("Version %s has active pollers on all known task queues", buildID)
	default:
		message = fmt.Sprintf("Poller status for version %s could not be determined", buildID)
	}

	changed := r.setCondition(twd, temporaliov1alpha1.ConditionProgressing, status, reason, message)
	if !changed {
		return
	}
	switch reason {
	case temporaliov1alpha1.ReasonWaitingForPollers:
		r.Recorder.Eventf(twd, corev1.EventTypeWarning, temporaliov1alpha1.ReasonWaitingForPollers,
			"Version %s has no active pollers on task queue(s): %s", buildID, strings.Join(affectedQueues, ", "))
	case temporaliov1alpha1.ReasonActivePollers:
		r.Recorder.Eventf(twd, corev1.EventTypeNormal, temporaliov1alpha1.ReasonActivePollers,
			"Version %s has active pollers on all known task queues", buildID)
	case temporaliov1alpha1.ReasonPollerStatusUnknown:
		// Don't emit an event for Unknown -- it just means "don't know yet", not a
		// state transition worth alerting on.
	}
}

// progressingConditionFromTaskQueues derives a ConditionProgressing status/reason from
// a version's task-queue poller presence data, for use by
// setConditionProgressingForCurrent. It is a pure function so the decision logic can
// be unit tested without an envtest environment.
//
//   - any task queue named in taskQueuesWithoutPollers -> Progressing=True,
//     ReasonWaitingForPollers, naming the affected queues. A known missing-poller
//     problem takes precedence over an unrelated describe error.
//   - taskQueuesWithoutPollers is empty (nil or not) and describeErr != nil (some
//     task queue's poller status couldn't be determined) -> Progressing=False,
//     ReasonPollerStatusUnknown.
//   - taskQueuesWithoutPollers == nil and describeErr == nil: poller status was
//     never checked for this version (e.g. not yet registered with Temporal) ->
//     Progressing=False, ReasonPollerStatusUnknown.
//   - taskQueuesWithoutPollers is a non-nil empty slice and describeErr == nil:
//     checked, every task queue has a poller -> Progressing=False, ReasonActivePollers.
func progressingConditionFromTaskQueues(
	taskQueuesWithoutPollers []string,
	describeErr error,
) (status metav1.ConditionStatus, reason string, affectedQueues []string) {
	if len(taskQueuesWithoutPollers) > 0 {
		affectedQueues = append([]string(nil), taskQueuesWithoutPollers...)
		sort.Strings(affectedQueues)
		return metav1.ConditionTrue, temporaliov1alpha1.ReasonWaitingForPollers, affectedQueues
	}
	if describeErr != nil {
		return metav1.ConditionFalse, temporaliov1alpha1.ReasonPollerStatusUnknown, nil
	}
	if taskQueuesWithoutPollers == nil {
		return metav1.ConditionFalse, temporaliov1alpha1.ReasonPollerStatusUnknown, nil
	}
	return metav1.ConditionFalse, temporaliov1alpha1.ReasonActivePollers, nil
}
