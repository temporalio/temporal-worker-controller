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

// computePollerHealthCondition derives the ConditionWorkersHealthy status/reason from
// a version's poller health data. It is a pure function so the decision logic can be
// unit tested without an envtest environment.
//
//   - pollerHealth == nil: poller status was never checked for this version (e.g. not
//     yet registered with Temporal) -> Unknown.
//   - any task queue with a false value -> False, naming the affected queues. A known
//     problem takes precedence over an unrelated unknown elsewhere.
//   - no false values, but unknown == true (some task queues errored) -> Unknown.
//   - all task queues true, unknown == false -> True.
func computePollerHealthCondition(
	pollerHealth map[string]bool,
	unknown bool,
) (status metav1.ConditionStatus, reason string, affectedQueues []string) {
	if pollerHealth == nil {
		return metav1.ConditionUnknown, temporaliov1alpha1.ReasonPollerStatusUnknown, nil
	}

	for tq, healthy := range pollerHealth {
		if !healthy {
			affectedQueues = append(affectedQueues, tq)
		}
	}

	if len(affectedQueues) > 0 {
		sort.Strings(affectedQueues)
		return metav1.ConditionFalse, temporaliov1alpha1.ReasonNoActivePollers, affectedQueues
	}
	if unknown {
		return metav1.ConditionUnknown, temporaliov1alpha1.ReasonPollerStatusUnknown, nil
	}
	return metav1.ConditionTrue, temporaliov1alpha1.ReasonPollersHealthy, nil
}

// syncWorkersHealthyCondition sets ConditionWorkersHealthy from poller health observed
// for the version currently serving production traffic (CurrentVersion), falling back
// to TargetVersion before the first rollout has ever completed (CurrentVersion nil).
// It emits a Normal/Warning event only when the condition actually transitions, to
// avoid spamming an Event on every reconcile loop.
func (r *WorkerDeploymentReconciler) syncWorkersHealthyCondition(
	workerDeploy *temporaliov1alpha1.WorkerDeployment,
	temporalState *temporal.TemporalWorkerState,
) {
	buildID := workerDeploy.Status.TargetVersion.BuildID
	if workerDeploy.Status.CurrentVersion != nil {
		buildID = workerDeploy.Status.CurrentVersion.BuildID
	}
	if buildID == "" {
		return
	}

	var pollerHealth map[string]bool
	var unknown bool
	if versionInfo, exists := temporalState.Versions[buildID]; exists {
		pollerHealth = versionInfo.PollerHealth
		unknown = versionInfo.PollerHealthUnknown
	}

	status, reason, affectedQueues := computePollerHealthCondition(pollerHealth, unknown)

	var message string
	switch status {
	case metav1.ConditionFalse:
		message = fmt.Sprintf("Version %s has no active pollers on task queue(s): %s", buildID, strings.Join(affectedQueues, ", "))
	case metav1.ConditionTrue:
		message = fmt.Sprintf("Version %s pollers are healthy", buildID)
	default:
		message = fmt.Sprintf("Poller status for version %s could not be determined", buildID)
	}

	changed := r.setCondition(workerDeploy, temporaliov1alpha1.ConditionWorkersHealthy, status, reason, message)
	if !changed {
		return
	}

	switch status {
	case metav1.ConditionFalse:
		r.Recorder.Eventf(workerDeploy, corev1.EventTypeWarning, temporaliov1alpha1.ReasonNoActivePollers,
			"Version %s has no active pollers on task queue(s): %s", buildID, strings.Join(affectedQueues, ", "))
	case metav1.ConditionTrue:
		r.Recorder.Eventf(workerDeploy, corev1.EventTypeNormal, temporaliov1alpha1.ReasonPollersHealthy,
			"Version %s pollers are healthy", buildID)
	}
}
