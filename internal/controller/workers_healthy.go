// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package controller

import (
	"sort"

	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// computePollerHealthCondition derives a ConditionProgressing status/reason from a
// version's poller presence data, for use as part of the ConditionProgressing
// calculation when the target version is Current (see syncConditions). It is a
// pure function so the decision logic can be unit tested without an envtest
// environment.
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
func computePollerHealthCondition(
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
