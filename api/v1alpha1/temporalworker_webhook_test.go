// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package v1alpha1

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func TestTemporalWorkerDeployment_ValidateCreate(t *testing.T) {
	tests := map[string]struct {
		obj      runtime.Object
		errorMsg string
	}{
		"valid temporal worker deployment": {
			obj: MakeTWDWithName("valid-worker"),
		},
		"temporal worker deployment with name too long": {
			obj:      MakeTWDWithName("this-is-a-very-long-temporal-worker-deployment-name-that-exceeds-the-maximum-allowed-length-of-sixty-three-characters"),
			errorMsg: "cannot be more than 63 characters",
		},
		"invalid object type": {
			obj: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
			},
			errorMsg: "expected a TemporalWorkerDeployment",
		},
		"missing rollout steps": {
			obj: ModifyObj(MakeTWDWithName("prog-rollout-missing-steps"), func(obj *TemporalWorkerDeployment) *TemporalWorkerDeployment {
				obj.Spec.RolloutStrategy.Strategy = UpdateProgressive
				obj.Spec.RolloutStrategy.Steps = nil
				return obj
			}),
			errorMsg: "spec.cutover.steps: Invalid value: []v1alpha1.RolloutStep(nil): steps are required for Progressive cutover",
		},
		"ramp value for step <= previous step": {
			obj: ModifyObj(MakeTWDWithName("prog-rollout-decreasing-ramps"), func(obj *TemporalWorkerDeployment) *TemporalWorkerDeployment {
				obj.Spec.RolloutStrategy.Strategy = UpdateProgressive
				obj.Spec.RolloutStrategy.Steps = []RolloutStep{
					{5, metav1.Duration{Duration: time.Minute}},
					{10, metav1.Duration{Duration: time.Minute}},
					{9, metav1.Duration{Duration: time.Minute}},
					{50, metav1.Duration{Duration: time.Minute}},
					{50, metav1.Duration{Duration: time.Minute}},
					{75, metav1.Duration{Duration: time.Minute}},
				}
				return obj
			}),
			errorMsg: "[spec.cutover.steps[2].rampPercentage: Invalid value: 9: rampPercentage must increase between each step, spec.cutover.steps[4].rampPercentage: Invalid value: 50: rampPercentage must increase between each step]",
		},
		"pause duration < 30s": {
			obj: ModifyObj(MakeTWDWithName("prog-rollout-decreasing-ramps"), func(obj *TemporalWorkerDeployment) *TemporalWorkerDeployment {
				obj.Spec.RolloutStrategy.Strategy = UpdateProgressive
				obj.Spec.RolloutStrategy.Steps = []RolloutStep{
					{10, metav1.Duration{Duration: time.Minute}},
					{25, metav1.Duration{Duration: 10 * time.Second}},
					{50, metav1.Duration{Duration: time.Minute}},
				}
				return obj
			}),
			errorMsg: `spec.cutover.steps[1].pauseDuration: Invalid value: "10s": pause duration must be at least 30s`,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			webhook := &TemporalWorkerDeployment{}

			warnings, err := webhook.ValidateCreate(ctx, tc.obj)

			if tc.errorMsg != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.errorMsg)
			} else {
				require.NoError(t, err)
			}

			// Warnings should always be nil for this implementation
			assert.Nil(t, warnings)
		})
	}
}

func TestTemporalWorkerDeployment_ValidateUpdate(t *testing.T) {
	tests := []struct {
		name        string
		oldObj      runtime.Object
		newObj      runtime.Object
		expectError bool
		errorMsg    string
	}{
		{
			name:        "valid update",
			oldObj:      nil,
			newObj:      MakeTWDWithName("valid-worker"),
			expectError: false,
		},
		{
			name:        "update with name too long",
			oldObj:      nil,
			newObj:      MakeTWDWithName("this-is-a-very-long-temporal-worker-deployment-name-that-exceeds-the-maximum-allowed-length-of-sixty-three-characters"),
			expectError: true,
			errorMsg:    "cannot be more than 63 characters",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			webhook := &TemporalWorkerDeployment{}

			warnings, err := webhook.ValidateUpdate(ctx, tt.oldObj, tt.newObj)

			if tt.expectError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorMsg)
			} else {
				require.NoError(t, err)
			}

			// Warnings should always be nil for this implementation
			assert.Nil(t, warnings)
		})
	}
}

func TestTemporalWorkerDeployment_ValidateDelete(t *testing.T) {
	ctx := context.Background()
	webhook := &TemporalWorkerDeployment{}

	obj := &TemporalWorkerDeployment{
		ObjectMeta: metav1.ObjectMeta{
			Name: "worker",
		},
	}

	warnings, err := webhook.ValidateDelete(ctx, obj)

	// ValidateDelete should always return nil, nil
	assert.NoError(t, err)
	assert.Nil(t, warnings)
}
