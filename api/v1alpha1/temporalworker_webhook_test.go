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
		"valid temporal worker deployment": {
			obj: testhelpers.MakeTWDWithName("valid-worker"),
		},
		"temporal worker deployment with name too long": {
			obj:      testhelpers.MakeTWDWithName("this-is-a-very-long-temporal-worker-deployment-name-that-exceeds-the-maximum-allowed-length-of-sixty-three-characters"),
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
		"missing rollout steps": {
			obj: testhelpers.ModifyObj(testhelpers.MakeTWDWithName("prog-rollout-missing-steps"), func(obj *temporaliov1alpha1.TemporalWorkerDeployment) *temporaliov1alpha1.TemporalWorkerDeployment {
				obj.Spec.RolloutStrategy.Strategy = temporaliov1alpha1.UpdateProgressive
				obj.Spec.RolloutStrategy.Steps = nil
				return obj
			}),
			errorMsg: "spec.rollout.steps: Invalid value: []v1alpha1.RolloutStep(nil): steps are required for Progressive rollout",
		},
		"ramp value for step <= previous step": {
			obj: testhelpers.ModifyObj(testhelpers.MakeTWDWithName("prog-rollout-decreasing-ramps"), func(obj *temporaliov1alpha1.TemporalWorkerDeployment) *temporaliov1alpha1.TemporalWorkerDeployment {
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
		"pause duration < 30s": {
			obj: testhelpers.ModifyObj(testhelpers.MakeTWDWithName("prog-rollout-decreasing-ramps"), func(obj *temporaliov1alpha1.TemporalWorkerDeployment) *temporaliov1alpha1.TemporalWorkerDeployment {
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
			newObj: testhelpers.MakeTWDWithName("valid-worker"),
		},
		"update with name too long": {
			oldObj:   nil,
			newObj:   testhelpers.MakeTWDWithName("this-is-a-very-long-temporal-worker-deployment-name-that-exceeds-the-maximum-allowed-length-of-sixty-three-characters"),
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
		"sets default maxVersions": {
			obj: testhelpers.MakeTWDWithName("default-max-versions"),
			expected: func(t *testing.T, obj *temporaliov1alpha1.TemporalWorkerDeployment) {
				require.NotNil(t, obj.Spec.MaxVersions)
				assert.Equal(t, int32(75), *obj.Spec.MaxVersions)
			},
		},
		"preserves existing maxVersions": {
			obj: testhelpers.ModifyObj(testhelpers.MakeTWDWithName("preserve-max-versions"), func(obj *temporaliov1alpha1.TemporalWorkerDeployment) *temporaliov1alpha1.TemporalWorkerDeployment {
				maxVersions := int32(100)
				obj.Spec.MaxVersions = &maxVersions
				return obj
			}),
			expected: func(t *testing.T, obj *temporaliov1alpha1.TemporalWorkerDeployment) {
				require.NotNil(t, obj.Spec.MaxVersions)
				assert.Equal(t, int32(100), *obj.Spec.MaxVersions)
			},
		},
		"sets default sunset strategy delays": {
			obj: testhelpers.MakeTWDWithName("default-sunset-delays"),
			expected: func(t *testing.T, obj *temporaliov1alpha1.TemporalWorkerDeployment) {
				require.NotNil(t, obj.Spec.SunsetStrategy.ScaledownDelay)
				assert.Equal(t, time.Hour, obj.Spec.SunsetStrategy.ScaledownDelay.Duration)
				require.NotNil(t, obj.Spec.SunsetStrategy.DeleteDelay)
				assert.Equal(t, 24*time.Hour, obj.Spec.SunsetStrategy.DeleteDelay.Duration)
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
