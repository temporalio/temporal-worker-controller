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
	"github.com/temporalio/temporal-worker-controller/internal/defaults"
	"github.com/temporalio/temporal-worker-controller/internal/testhelpers"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

func TestWorkerDeployment_ValidateCreate(t *testing.T) {
	tests := map[string]struct {
		obj      runtime.Object
		errorMsg string
	}{
		"valid temporal worker deployment": {
			obj: testhelpers.MakeWDWithName("valid-worker", ""),
		},
		"invalid object type": {
			obj: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
			},
			errorMsg: "expected a WorkerDeployment",
		},
		"ramp value for step <= previous step": {
			obj: testhelpers.ModifyObj(testhelpers.MakeWDWithName("prog-rollout-decreasing-ramps", ""), func(obj *temporaliov1alpha1.WorkerDeployment) *temporaliov1alpha1.WorkerDeployment {
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
		"invalid deployment strategy type": {
			obj: testhelpers.ModifyObj(testhelpers.MakeWDWithName("bad-deployment-strategy-type", ""), func(obj *temporaliov1alpha1.WorkerDeployment) *temporaliov1alpha1.WorkerDeployment {
				obj.Spec.DeploymentStrategy = &appsv1.DeploymentStrategy{Type: "Bogus"}
				return obj
			}),
			errorMsg: `spec.deploymentStrategy.type: Unsupported value: "Bogus"`,
		},
		"recreate with rollingUpdate forbidden": {
			obj: testhelpers.ModifyObj(testhelpers.MakeWDWithName("recreate-with-rolling-update", ""), func(obj *temporaliov1alpha1.WorkerDeployment) *temporaliov1alpha1.WorkerDeployment {
				zero := intstr.FromInt32(0)
				obj.Spec.DeploymentStrategy = &appsv1.DeploymentStrategy{
					Type: appsv1.RecreateDeploymentStrategyType,
					RollingUpdate: &appsv1.RollingUpdateDeployment{
						MaxUnavailable: &zero,
					},
				}
				return obj
			}),
			errorMsg: "spec.deploymentStrategy.rollingUpdate: Forbidden",
		},
		"maxUnavailable and maxSurge both zero": {
			obj: testhelpers.ModifyObj(testhelpers.MakeWDWithName("both-zero-surge", ""), func(obj *temporaliov1alpha1.WorkerDeployment) *temporaliov1alpha1.WorkerDeployment {
				zero := intstr.FromInt32(0)
				obj.Spec.DeploymentStrategy = &appsv1.DeploymentStrategy{
					Type: appsv1.RollingUpdateDeploymentStrategyType,
					RollingUpdate: &appsv1.RollingUpdateDeployment{
						MaxUnavailable: &zero,
						MaxSurge:       &zero,
					},
				}
				return obj
			}),
			errorMsg: "maxUnavailable and maxSurge cannot both be 0",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			webhook := &temporaliov1alpha1.WorkerDeployment{}

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

func TestWorkerDeployment_ValidateUpdate(t *testing.T) {
	tests := map[string]struct {
		oldObj   runtime.Object
		newObj   runtime.Object
		errorMsg string
	}{
		"valid update": {
			oldObj: nil,
			newObj: testhelpers.MakeWDWithName("valid-worker", ""),
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			webhook := &temporaliov1alpha1.WorkerDeployment{}

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

func TestWorkerDeployment_ValidateDelete(t *testing.T) {
	ctx := context.Background()
	webhook := &temporaliov1alpha1.WorkerDeployment{}

	obj := &temporaliov1alpha1.WorkerDeployment{
		ObjectMeta: metav1.ObjectMeta{
			Name: "worker",
		},
	}

	warnings, err := webhook.ValidateDelete(ctx, obj)

	// ValidateDelete should always return nil, nil
	assert.NoError(t, err)
	assert.Nil(t, warnings)
}

func TestWorkerDeployment_Default(t *testing.T) {
	tests := map[string]struct {
		obj      runtime.Object
		expected func(t *testing.T, obj *temporaliov1alpha1.WorkerDeployment)
	}{
		"sets default sunset strategy delays": {
			obj: testhelpers.MakeWDWithName("default-sunset-delays", ""),
			expected: func(t *testing.T, obj *temporaliov1alpha1.WorkerDeployment) {
				require.NotNil(t, obj.Spec.SunsetStrategy.ScaledownDelay)
				assert.Equal(t, time.Hour, obj.Spec.SunsetStrategy.ScaledownDelay.Duration)
				require.NotNil(t, obj.Spec.SunsetStrategy.DeleteDelay)
				assert.Equal(t, 24*time.Hour, obj.Spec.SunsetStrategy.DeleteDelay.Duration)
			},
		},
		"leaves nil deploymentStrategy unset": {
			obj: testhelpers.MakeWDWithName("nil-deployment-strategy", ""),
			expected: func(t *testing.T, obj *temporaliov1alpha1.WorkerDeployment) {
				assert.Nil(t, obj.Spec.DeploymentStrategy)
			},
		},
		"defaults partial deploymentStrategy": {
			obj: testhelpers.ModifyObj(testhelpers.MakeWDWithName("partial-deployment-strategy", ""), func(obj *temporaliov1alpha1.WorkerDeployment) *temporaliov1alpha1.WorkerDeployment {
				maxUnavailable := intstr.FromString("5%")
				obj.Spec.DeploymentStrategy = &appsv1.DeploymentStrategy{
					RollingUpdate: &appsv1.RollingUpdateDeployment{
						MaxUnavailable: &maxUnavailable,
					},
				}
				return obj
			}),
			expected: func(t *testing.T, obj *temporaliov1alpha1.WorkerDeployment) {
				require.NotNil(t, obj.Spec.DeploymentStrategy)
				assert.Equal(t, appsv1.RollingUpdateDeploymentStrategyType, obj.Spec.DeploymentStrategy.Type)
				require.NotNil(t, obj.Spec.DeploymentStrategy.RollingUpdate)
				assert.Equal(t, intstr.FromString("5%"), *obj.Spec.DeploymentStrategy.RollingUpdate.MaxUnavailable)
				require.NotNil(t, obj.Spec.DeploymentStrategy.RollingUpdate.MaxSurge)
				assert.Equal(t, intstr.FromString(defaults.DeploymentMaxSurge), *obj.Spec.DeploymentStrategy.RollingUpdate.MaxSurge)
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			webhook := &temporaliov1alpha1.WorkerDeployment{}

			err := webhook.Default(ctx, tc.obj)
			require.NoError(t, err)

			obj, ok := tc.obj.(*temporaliov1alpha1.WorkerDeployment)
			require.True(t, ok)

			tc.expected(t, obj)
		})
	}
}
