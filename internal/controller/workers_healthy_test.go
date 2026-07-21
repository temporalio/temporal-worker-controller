// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package controller

import (
	"testing"

	"github.com/stretchr/testify/assert"
	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestComputePollerHealthCondition(t *testing.T) {
	tests := []struct {
		name         string
		pollerHealth map[string]bool
		unknown      bool
		wantStatus   metav1.ConditionStatus
		wantReason   string
		wantAffected []string
	}{
		{
			name:         "nil map means never checked",
			pollerHealth: nil,
			unknown:      false,
			wantStatus:   metav1.ConditionUnknown,
			wantReason:   temporaliov1alpha1.ReasonPollerStatusUnknown,
		},
		{
			name:         "all queues healthy",
			pollerHealth: map[string]bool{"tq-1": true, "tq-2": true},
			unknown:      false,
			wantStatus:   metav1.ConditionTrue,
			wantReason:   temporaliov1alpha1.ReasonPollersHealthy,
		},
		{
			name:         "one queue has no pollers",
			pollerHealth: map[string]bool{"tq-1": true, "tq-2": false},
			unknown:      false,
			wantStatus:   metav1.ConditionFalse,
			wantReason:   temporaliov1alpha1.ReasonNoActivePollers,
			wantAffected: []string{"tq-2"},
		},
		{
			name:         "empty map with no fetch errors is healthy (no task queues to check)",
			pollerHealth: map[string]bool{},
			unknown:      false,
			wantStatus:   metav1.ConditionTrue,
			wantReason:   temporaliov1alpha1.ReasonPollersHealthy,
		},
		{
			name:         "fetch error with no confirmed-unhealthy queue is unknown, not unhealthy",
			pollerHealth: map[string]bool{"tq-1": true},
			unknown:      true,
			wantStatus:   metav1.ConditionUnknown,
			wantReason:   temporaliov1alpha1.ReasonPollerStatusUnknown,
		},
		{
			name:         "a confirmed no-poller queue wins over an unrelated fetch error",
			pollerHealth: map[string]bool{"tq-1": false},
			unknown:      true,
			wantStatus:   metav1.ConditionFalse,
			wantReason:   temporaliov1alpha1.ReasonNoActivePollers,
			wantAffected: []string{"tq-1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, reason, affected := computePollerHealthCondition(tt.pollerHealth, tt.unknown)
			assert.Equal(t, tt.wantStatus, status)
			assert.Equal(t, tt.wantReason, reason)
			assert.Equal(t, tt.wantAffected, affected)
		})
	}
}
