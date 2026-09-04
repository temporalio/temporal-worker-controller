// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package controller

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestComputePollerHealthCondition(t *testing.T) {
	tests := []struct {
		name                     string
		taskQueuesWithoutPollers []string
		describeErr              error
		wantStatus               metav1.ConditionStatus
		wantReason               string
		wantAffected             []string
	}{
		{
			name:                     "nil slice means never checked",
			taskQueuesWithoutPollers: nil,
			describeErr:              nil,
			wantStatus:               metav1.ConditionFalse,
			wantReason:               temporaliov1alpha1.ReasonPollerStatusUnknown,
		},
		{
			name:                     "all queues have active pollers",
			taskQueuesWithoutPollers: []string{},
			describeErr:              nil,
			wantStatus:               metav1.ConditionFalse,
			wantReason:               temporaliov1alpha1.ReasonActivePollers,
		},
		{
			name:                     "one queue has no pollers",
			taskQueuesWithoutPollers: []string{"tq-2"},
			describeErr:              nil,
			wantStatus:               metav1.ConditionTrue,
			wantReason:               temporaliov1alpha1.ReasonWaitingForPollers,
			wantAffected:             []string{"tq-2"},
		},
		{
			name:                     "empty slice with no fetch errors is active (no task queues to check)",
			taskQueuesWithoutPollers: []string{},
			describeErr:              nil,
			wantStatus:               metav1.ConditionFalse,
			wantReason:               temporaliov1alpha1.ReasonActivePollers,
		},
		{
			name:                     "fetch error with no confirmed-missing poller is unknown, not waiting",
			taskQueuesWithoutPollers: []string{},
			describeErr:              errors.New("describe task queue failed"),
			wantStatus:               metav1.ConditionFalse,
			wantReason:               temporaliov1alpha1.ReasonPollerStatusUnknown,
		},
		{
			name:                     "a confirmed no-poller queue wins over an unrelated fetch error",
			taskQueuesWithoutPollers: []string{"tq-1"},
			describeErr:              errors.New("describe task queue failed"),
			wantStatus:               metav1.ConditionTrue,
			wantReason:               temporaliov1alpha1.ReasonWaitingForPollers,
			wantAffected:             []string{"tq-1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, reason, affected := computePollerHealthCondition(tt.taskQueuesWithoutPollers, tt.describeErr)
			assert.Equal(t, tt.wantStatus, status)
			assert.Equal(t, tt.wantReason, reason)
			assert.Equal(t, tt.wantAffected, affected)
		})
	}
}
