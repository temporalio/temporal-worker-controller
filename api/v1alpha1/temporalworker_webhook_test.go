// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package v1alpha1_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	"github.com/temporalio/temporal-worker-controller/internal/testhelpers"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

func TestTemporalWorkerDeployment_ValidateCreate(t *testing.T) {
	tests := map[string]struct {
		obj      runtime.Object
		errorMsg string
	}{
		"valid default temporal worker deployment": {
			obj: testhelpers.MakeTWDWithName("valid-worker", ""),
		},
		"temporal worker deployment with name too long": {
			obj:      testhelpers.MakeTWDWithName("this-is-a-very-long-temporal-worker-deployment-name-that-exceeds-the-maximum-allowed-length-of-sixty-three-characters", ""),
			errorMsg: "cannot be more than 63 characters",
		},
		"invalid object type": {
			obj: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
			},
			errorMsg: "expected a TemporalWorkerDeployment",
		},
		"rollout strategy - invalid Progressive without steps": {
			obj: testhelpers.ModifyObj(testhelpers.MakeTWDWithName("prog-rollout-missing-steps", ""), func(obj *temporaliov1alpha1.TemporalWorkerDeployment) *temporaliov1alpha1.TemporalWorkerDeployment {
				obj.Spec.RolloutStrategy.Strategy = temporaliov1alpha1.UpdateProgressive
				obj.Spec.RolloutStrategy.Steps = nil
				return obj
			}),
			errorMsg: "spec.rollout.steps: Invalid value: null: steps are required for Progressive strategy",
		},
		"rollout strategy - invalid Progressive with non-increasing ramp": {
			obj: testhelpers.ModifyObj(testhelpers.MakeTWDWithName("prog-rollout-decreasing-ramps", ""), func(obj *temporaliov1alpha1.TemporalWorkerDeployment) *temporaliov1alpha1.TemporalWorkerDeployment {
				obj.Spec.RolloutStrategy.Strategy = temporaliov1alpha1.UpdateProgressive
				obj.Spec.RolloutStrategy.Steps = []temporaliov1alpha1.RolloutStep{
					{5, metav1.Duration{Duration: time.Minute}},
					{10, metav1.Duration{Duration: time.Minute}},
					{9, metav1.Duration{Duration: time.Minute}},
					{50, metav1.Duration{Duration: time.Minute}},
					{50, metav1.Duration{Duration: time.Minute}},
					{75, metav1.Duration{Duration: time.Minute}},
				}
				return obj
			}),
			errorMsg: "[spec.rollout.steps[2].rampPercentage: Invalid value: 9: rampPercentage must increase between each step, spec.rollout.steps[4].rampPercentage: Invalid value: 50: rampPercentage must increase between each step]",
		},
		"rollout strategy - invalid Progressive pause duration < 30s": {
			obj: testhelpers.ModifyObj(testhelpers.MakeTWDWithName("prog-rollout-decreasing-ramps", ""), func(obj *temporaliov1alpha1.TemporalWorkerDeployment) *temporaliov1alpha1.TemporalWorkerDeployment {
				obj.Spec.RolloutStrategy.Strategy = temporaliov1alpha1.UpdateProgressive
				obj.Spec.RolloutStrategy.Steps = []temporaliov1alpha1.RolloutStep{
					{10, metav1.Duration{Duration: time.Minute}},
					{25, metav1.Duration{Duration: 10 * time.Second}},
					{50, metav1.Duration{Duration: time.Minute}},
				}
				return obj
			}),
			errorMsg: `spec.rollout.steps[1].pauseDuration: Invalid value: "10s": pause duration must be at least 30s`,
		},
		"rollback strategy - valid Progressive with steps": {
			obj: testhelpers.ModifyObj(testhelpers.MakeTWDWithName("rollback-progressive", ""), func(obj *temporaliov1alpha1.TemporalWorkerDeployment) *temporaliov1alpha1.TemporalWorkerDeployment {
				obj.Spec.RollbackStrategy = &temporaliov1alpha1.RollbackStrategy{
					Strategy: temporaliov1alpha1.RollbackProgressive,
					Steps: []temporaliov1alpha1.RolloutStep{
						{50, metav1.Duration{Duration: 30 * time.Second}},
					},
				}
				return obj
			}),
		},
		"rollback strategy - invalid Progressive without steps": {
			obj: testhelpers.ModifyObj(testhelpers.MakeTWDWithName("rollback-progressive-no-steps", ""), func(obj *temporaliov1alpha1.TemporalWorkerDeployment) *temporaliov1alpha1.TemporalWorkerDeployment {
				obj.Spec.RollbackStrategy = &temporaliov1alpha1.RollbackStrategy{
					Strategy: temporaliov1alpha1.RollbackProgressive,
					Steps:    nil,
				}
				return obj
			}),
			errorMsg: "steps are required for Progressive strategy",
		},
		"rollback strategy - invalid Progressive pause duration < 30s": {
			obj: testhelpers.ModifyObj(testhelpers.MakeTWDWithName("rollback-progressive-invalid", ""), func(obj *temporaliov1alpha1.TemporalWorkerDeployment) *temporaliov1alpha1.TemporalWorkerDeployment {
				obj.Spec.RollbackStrategy = &temporaliov1alpha1.RollbackStrategy{
					Strategy: temporaliov1alpha1.RollbackProgressive,
					Steps: []temporaliov1alpha1.RolloutStep{
						{50, metav1.Duration{Duration: 10 * time.Second}},
					},
				}
				return obj
			}),
			errorMsg: "pause duration must be at least 30s",
		},
		"rollback strategy - invalid Progressive with non-increasing ramp": {
			obj: testhelpers.ModifyObj(testhelpers.MakeTWDWithName("rollback-progressive-decreasing", ""), func(obj *temporaliov1alpha1.TemporalWorkerDeployment) *temporaliov1alpha1.TemporalWorkerDeployment {
				obj.Spec.RollbackStrategy = &temporaliov1alpha1.RollbackStrategy{
					Strategy: temporaliov1alpha1.RollbackProgressive,
					Steps: []temporaliov1alpha1.RolloutStep{
						{50, metav1.Duration{Duration: time.Minute}},
						{25, metav1.Duration{Duration: time.Minute}},
					},
				}
				return obj
			}),
			errorMsg: "rampPercentage must increase between each step",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			webhook := &temporaliov1alpha1.TemporalWorkerDeployment{}

			assertAdmission := func(warnings admission.Warnings, err error) {
				if tc.errorMsg != "" {
					require.Error(t, err)
					assert.Contains(t, err.Error(), tc.errorMsg)
				} else {
					require.NoError(t, err)
				}

				// Warnings should always be nil for this implementation
				assert.Nil(t, warnings)
			}

			// Verify that create and update enforce the same rules
			assertAdmission(webhook.ValidateCreate(ctx, tc.obj))
			assertAdmission(webhook.ValidateUpdate(ctx, nil, tc.obj))
		})
	}
}

func TestTemporalWorkerDeployment_ValidateUpdate(t *testing.T) {
	tests := map[string]struct {
		oldObj   runtime.Object
		newObj   runtime.Object
		errorMsg string
	}{
		"valid update": {
			oldObj: nil,
			newObj: testhelpers.MakeTWDWithName("valid-worker", ""),
		},
		"update with name too long": {
			oldObj:   nil,
			newObj:   testhelpers.MakeTWDWithName("this-is-a-very-long-temporal-worker-deployment-name-that-exceeds-the-maximum-allowed-length-of-sixty-three-characters", ""),
			errorMsg: "cannot be more than 63 characters",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			webhook := &temporaliov1alpha1.TemporalWorkerDeployment{}

			warnings, err := webhook.ValidateUpdate(ctx, tc.oldObj, tc.newObj)

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

func TestTemporalWorkerDeployment_ValidateDelete(t *testing.T) {
	ctx := context.Background()
	webhook := &temporaliov1alpha1.TemporalWorkerDeployment{}

	obj := &temporaliov1alpha1.TemporalWorkerDeployment{
		ObjectMeta: metav1.ObjectMeta{
			Name: "worker",
		},
	}

	warnings, err := webhook.ValidateDelete(ctx, obj)

	// ValidateDelete should always return nil, nil
	assert.NoError(t, err)
	assert.Nil(t, warnings)
}

func TestTemporalWorkerDeployment_Default(t *testing.T) {
	tests := map[string]struct {
		obj      runtime.Object
		expected func(t *testing.T, obj *temporaliov1alpha1.TemporalWorkerDeployment)
	}{
		"sets default sunset strategy delays": {
			obj: testhelpers.MakeTWDWithName("default-sunset-delays", ""),
			expected: func(t *testing.T, obj *temporaliov1alpha1.TemporalWorkerDeployment) {
				require.NotNil(t, obj.Spec.SunsetStrategy.ScaledownDelay)
				assert.Equal(t, time.Hour, obj.Spec.SunsetStrategy.ScaledownDelay.Duration)
				require.NotNil(t, obj.Spec.SunsetStrategy.DeleteDelay)
				assert.Equal(t, 24*time.Hour, obj.Spec.SunsetStrategy.DeleteDelay.Duration)
			},
		},
		"rollback strategy initialized when nil": {
			obj: testhelpers.ModifyObj(testhelpers.MakeTWDWithName("default-rollback-nil", ""), func(obj *temporaliov1alpha1.TemporalWorkerDeployment) *temporaliov1alpha1.TemporalWorkerDeployment {
				obj.Spec.RollbackStrategy = nil
				return obj
			}),
			expected: func(t *testing.T, obj *temporaliov1alpha1.TemporalWorkerDeployment) {
				require.NotNil(t, obj.Spec.RollbackStrategy, "expected RollbackStrategy to be initialized by webhook")
				assert.Equal(t, temporaliov1alpha1.RollbackAllAtOnce, obj.Spec.RollbackStrategy.Strategy, "expected RollbackStrategy.Strategy to default to AllAtOnce")
			},
		},
		"rollback strategy defaults empty strategy field to AllAtOnce": {
			obj: testhelpers.ModifyObj(testhelpers.MakeTWDWithName("default-rollback-empty", ""), func(obj *temporaliov1alpha1.TemporalWorkerDeployment) *temporaliov1alpha1.TemporalWorkerDeployment {
				obj.Spec.RollbackStrategy = &temporaliov1alpha1.RollbackStrategy{
					Strategy: "",
				}
				return obj
			}),
			expected: func(t *testing.T, obj *temporaliov1alpha1.TemporalWorkerDeployment) {
				require.NotNil(t, obj.Spec.RollbackStrategy)
				assert.Equal(t, temporaliov1alpha1.RollbackAllAtOnce, obj.Spec.RollbackStrategy.Strategy, "expected RollbackStrategy.Strategy to default to AllAtOnce")
			},
		},
		"rollback strategy preserves explicit strategy": {
			obj: testhelpers.ModifyObj(testhelpers.MakeTWDWithName("explicit-rollback-progressive", ""), func(obj *temporaliov1alpha1.TemporalWorkerDeployment) *temporaliov1alpha1.TemporalWorkerDeployment {
				obj.Spec.RollbackStrategy = &temporaliov1alpha1.RollbackStrategy{
					Strategy: temporaliov1alpha1.RollbackProgressive,
					Steps: []temporaliov1alpha1.RolloutStep{
						{50, metav1.Duration{Duration: 30 * time.Second}},
					},
				}
				return obj
			}),
			expected: func(t *testing.T, obj *temporaliov1alpha1.TemporalWorkerDeployment) {
				require.NotNil(t, obj.Spec.RollbackStrategy)
				assert.Equal(t, temporaliov1alpha1.RollbackProgressive, obj.Spec.RollbackStrategy.Strategy, "expected RollbackStrategy.Strategy to remain Progressive")
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			webhook := &temporaliov1alpha1.TemporalWorkerDeployment{}

			err := webhook.Default(ctx, tc.obj)
			require.NoError(t, err)

			obj, ok := tc.obj.(*temporaliov1alpha1.TemporalWorkerDeployment)
			require.True(t, ok)

			tc.expected(t, obj)
		})
	}
}
