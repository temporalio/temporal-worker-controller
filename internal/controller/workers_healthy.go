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
//   - pollerHealth == nil: poller status was never checked for this version (e.g. not
//     yet registered with Temporal) -> Progressing=False, ReasonPollerStatusUnknown.
//   - any task queue with a false value -> Progressing=True, ReasonWaitingForPollers,
//     naming the affected queues. A known missing-poller problem takes precedence
//     over an unrelated unknown elsewhere.
//   - no false values, but unknown == true (some task queues errored) ->
//     Progressing=False, ReasonPollerStatusUnknown.
//   - all task queues true, unknown == false -> Progressing=False, ReasonActivePollers.
func computePollerHealthCondition(
	pollerHealth map[string]bool,
	unknown bool,
) (status metav1.ConditionStatus, reason string, affectedQueues []string) {
	if pollerHealth == nil {
		return metav1.ConditionFalse, temporaliov1alpha1.ReasonPollerStatusUnknown, nil
	}

	for tq, hasPoller := range pollerHealth {
		if !hasPoller {
			affectedQueues = append(affectedQueues, tq)
		}
	}

	if len(affectedQueues) > 0 {
		sort.Strings(affectedQueues)
		return metav1.ConditionTrue, temporaliov1alpha1.ReasonWaitingForPollers, affectedQueues
	}
	if unknown {
		return metav1.ConditionFalse, temporaliov1alpha1.ReasonPollerStatusUnknown, nil
	}
	return metav1.ConditionFalse, temporaliov1alpha1.ReasonActivePollers, nil
}
