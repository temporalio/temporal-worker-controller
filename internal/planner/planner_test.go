// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package planner

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	"github.com/temporalio/temporal-worker-controller/internal/defaults"
	"github.com/temporalio/temporal-worker-controller/internal/k8s"
	"github.com/temporalio/temporal-worker-controller/internal/temporal"
	"github.com/temporalio/temporal-worker-controller/internal/testhelpers/testlogr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestGeneratePlan(t *testing.T) {
	testCases := []struct {
		name                             string
		k8sState                         *k8s.DeploymentState
		status                           *temporaliov1alpha1.TemporalWorkerDeploymentStatus
		spec                             *temporaliov1alpha1.TemporalWorkerDeploymentSpec
		state                            *temporal.TemporalWorkerState
		config                           *Config
		expectDelete                     int
		expectCreate                     bool
		expectScale                      int
		expectUpdate                     int
		expectWorkflow                   int
		expectConfig                     bool
		expectConfigSetCurrent           *bool    // pointer so we can test nil
		expectConfigRampPercent          *float32 // pointer so we can test nil
		maxVersionsIneligibleForDeletion *int32   // set by env if non-nil, else default 75
	}{
		{
			name: "empty state creates new deployment",
			k8sState: &k8s.DeploymentState{
				Deployments:       map[string]*appsv1.Deployment{},
				DeploymentsByTime: []*appsv1.Deployment{},
				DeploymentRefs:    map[string]*corev1.ObjectReference{},
			},
			spec: &temporaliov1alpha1.TemporalWorkerDeploymentSpec{
				Replicas: func() *int32 { r := int32(1); return &r }(),
			},
			state: &temporal.TemporalWorkerState{},
			config: &Config{
				RolloutStrategy: temporaliov1alpha1.RolloutStrategy{},
			},
			expectCreate: true,
		},
		{
			name: "drained version gets deleted",
			k8sState: &k8s.DeploymentState{
				Deployments: map[string]*appsv1.Deployment{
					"123": createDeploymentWithDefaultConnectionSpecHash(0),
					"456": createDeploymentWithDefaultConnectionSpecHash(1),
				},
				DeploymentsByTime: []*appsv1.Deployment{
					createDeploymentWithDefaultConnectionSpecHash(0),
					createDeploymentWithDefaultConnectionSpecHash(1),
				},
			},
			status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
				TargetVersion: temporaliov1alpha1.TargetWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						BuildID:    "456",
						Status:     temporaliov1alpha1.VersionStatusCurrent,
						Deployment: &corev1.ObjectReference{Name: "test-456"},
					},
				},
				CurrentVersion: &temporaliov1alpha1.CurrentWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						BuildID:    "456",
						Status:     temporaliov1alpha1.VersionStatusCurrent,
						Deployment: &corev1.ObjectReference{Name: "test-456"},
					},
				},
				DeprecatedVersions: []*temporaliov1alpha1.DeprecatedWorkerDeploymentVersion{
					{
						BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
							BuildID:    "123",
							Status:     temporaliov1alpha1.VersionStatusDrained,
							Deployment: &corev1.ObjectReference{Name: "test-123"},
						},
						DrainedSince: &metav1.Time{
							Time: time.Now().Add(-24 * time.Hour),
						},
					},
				},
			},
			spec: &temporaliov1alpha1.TemporalWorkerDeploymentSpec{
				SunsetStrategy: temporaliov1alpha1.SunsetStrategy{
					ScaledownDelay: &metav1.Duration{Duration: 0},
					DeleteDelay:    &metav1.Duration{Duration: 0},
				},
				Replicas: func() *int32 { r := int32(1); return &r }(),
			},
			state: &temporal.TemporalWorkerState{},
			config: &Config{
				RolloutStrategy: temporaliov1alpha1.RolloutStrategy{},
			},
			expectDelete: 1,
		},
		{
			name: "deployment needs to be scaled",
			k8sState: &k8s.DeploymentState{
				Deployments: map[string]*appsv1.Deployment{
					"123": createDeploymentWithDefaultConnectionSpecHash(1),
				},
				DeploymentsByTime: []*appsv1.Deployment{
					createDeploymentWithDefaultConnectionSpecHash(1),
				},
				DeploymentRefs: map[string]*corev1.ObjectReference{
					"123": {Name: "test-123"},
				},
			},
			status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
				TargetVersion: temporaliov1alpha1.TargetWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						BuildID:    "123",
						Status:     temporaliov1alpha1.VersionStatusCurrent,
						Deployment: &corev1.ObjectReference{Name: "test-123"},
					},
				},
				CurrentVersion: &temporaliov1alpha1.CurrentWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						BuildID:    "123",
						Status:     temporaliov1alpha1.VersionStatusCurrent,
						Deployment: &corev1.ObjectReference{Name: "test-123"},
					},
				},
			},
			spec: &temporaliov1alpha1.TemporalWorkerDeploymentSpec{
				Replicas: func() *int32 { r := int32(2); return &r }(),
			},
			state: &temporal.TemporalWorkerState{},
			config: &Config{
				RolloutStrategy: temporaliov1alpha1.RolloutStrategy{},
			},
			expectScale: 1,
		},
		{
			name: "rollback scenario - target equals current but deprecated version is ramping",
			k8sState: &k8s.DeploymentState{
				Deployments: map[string]*appsv1.Deployment{
					"123": createDeploymentWithDefaultConnectionSpecHash(3),
					"456": createDeploymentWithDefaultConnectionSpecHash(3),
				},
				DeploymentsByTime: []*appsv1.Deployment{
					createDeploymentWithDefaultConnectionSpecHash(3),
					createDeploymentWithDefaultConnectionSpecHash(3),
				},
				DeploymentRefs: map[string]*corev1.ObjectReference{
					"123": {Name: "test-123"},
					"456": {Name: "test-456"},
				},
			},
			status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
				CurrentVersion: &temporaliov1alpha1.CurrentWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						BuildID:    "123",
						Status:     temporaliov1alpha1.VersionStatusCurrent,
						Deployment: &corev1.ObjectReference{Name: "test-123"},
						HealthySince: &metav1.Time{
							Time: time.Now().Add(-2 * time.Hour),
						},
					},
				},
				TargetVersion: temporaliov1alpha1.TargetWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						BuildID:    "123",
						Status:     temporaliov1alpha1.VersionStatusCurrent,
						Deployment: &corev1.ObjectReference{Name: "test-123"},
						HealthySince: &metav1.Time{
							Time: time.Now().Add(-2 * time.Hour),
						},
					},
				},
				DeprecatedVersions: []*temporaliov1alpha1.DeprecatedWorkerDeploymentVersion{
					{
						BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
							BuildID:    "456",
							Status:     temporaliov1alpha1.VersionStatusRamping,
							Deployment: &corev1.ObjectReference{Name: "test-456"},
						},
					},
				},
			},
			spec: &temporaliov1alpha1.TemporalWorkerDeploymentSpec{
				Replicas: func() *int32 { r := int32(3); return &r }(),
			},
			state: &temporal.TemporalWorkerState{
				RampingBuildID: "456", // This is what triggers the reset
			},
			config: &Config{
				RolloutStrategy: temporaliov1alpha1.RolloutStrategy{
					Strategy: temporaliov1alpha1.UpdateAllAtOnce,
				},
			},
			expectCreate:            false,
			expectScale:             0,
			expectConfig:            true,                                             // Should generate config to reset ramp
			expectConfigSetCurrent:  func() *bool { b := false; return &b }(),         // Should NOT set current (already current)
			expectConfigRampPercent: func() *float32 { f := float32(0); return &f }(), // Should reset ramp to 0
		},
		{
			name: "should not create deployment when version limit (ineligible for deletion) is reached",
			k8sState: &k8s.DeploymentState{
				Deployments:       map[string]*appsv1.Deployment{},
				DeploymentsByTime: []*appsv1.Deployment{},
				DeploymentRefs:    map[string]*corev1.ObjectReference{},
			},
			status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
				TargetVersion: temporaliov1alpha1.TargetWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						BuildID:    "new",
						Status:     temporaliov1alpha1.VersionStatusNotRegistered,
						Deployment: nil,
					},
				},
				DeprecatedVersions: func() []*temporaliov1alpha1.DeprecatedWorkerDeploymentVersion {
					// default is NOT eligible for deletion, so 5 empty Deprecated Versions should block rollout
					r := make([]*temporaliov1alpha1.DeprecatedWorkerDeploymentVersion, 5)
					for i := range r {
						r[i] = &temporaliov1alpha1.DeprecatedWorkerDeploymentVersion{}
					}
					return r
				}(),
			},
			spec: &temporaliov1alpha1.TemporalWorkerDeploymentSpec{
				Replicas: func() *int32 { r := int32(1); return &r }(),
			},
			state: &temporal.TemporalWorkerState{},
			config: &Config{
				RolloutStrategy: temporaliov1alpha1.RolloutStrategy{},
			},
			expectCreate:                     false,
			maxVersionsIneligibleForDeletion: func() *int32 { i := int32(5); return &i }(),
		},
		{
			name: "should create deployment when version limit (ineligible for deletion) is not reached",
			k8sState: &k8s.DeploymentState{
				Deployments:       map[string]*appsv1.Deployment{},
				DeploymentsByTime: []*appsv1.Deployment{},
				DeploymentRefs:    map[string]*corev1.ObjectReference{},
			},
			status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
				VersionCount: 5,
				TargetVersion: temporaliov1alpha1.TargetWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						BuildID:    "new",
						Status:     temporaliov1alpha1.VersionStatusNotRegistered,
						Deployment: nil,
					},
				},
				DeprecatedVersions: func() []*temporaliov1alpha1.DeprecatedWorkerDeploymentVersion {
					r := make([]*temporaliov1alpha1.DeprecatedWorkerDeploymentVersion, 4)
					for i := range r {
						r[i] = &temporaliov1alpha1.DeprecatedWorkerDeploymentVersion{}
					}
					return r
				}(),
			},
			spec: &temporaliov1alpha1.TemporalWorkerDeploymentSpec{
				Replicas: func() *int32 { r := int32(1); return &r }(),
			},
			state: &temporal.TemporalWorkerState{},
			config: &Config{
				RolloutStrategy: temporaliov1alpha1.RolloutStrategy{},
			},
			expectCreate:                     true,
			maxVersionsIneligibleForDeletion: func() *int32 { i := int32(5); return &i }(),
		},
		{
			name: "update deployment when target version, with an existing deployment, has an expired connection spec hash",
			k8sState: &k8s.DeploymentState{
				Deployments: map[string]*appsv1.Deployment{
					"123": createDeploymentWithExpiredConnectionSpecHash(1),
					"456": createDeploymentWithDefaultConnectionSpecHash(1),
				},
			},
			status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
				TargetVersion: temporaliov1alpha1.TargetWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						BuildID:    "123",
						Status:     temporaliov1alpha1.VersionStatusRamping,
						Deployment: &corev1.ObjectReference{Name: "test-123"},
					},
				},
				CurrentVersion: &temporaliov1alpha1.CurrentWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						BuildID:    "456",
						Status:     temporaliov1alpha1.VersionStatusCurrent,
						Deployment: &corev1.ObjectReference{Name: "test-456"},
					},
				},
			},
			spec: &temporaliov1alpha1.TemporalWorkerDeploymentSpec{
				Replicas: func() *int32 { r := int32(1); return &r }(),
			},
			state: &temporal.TemporalWorkerState{},
			config: &Config{
				RolloutStrategy: temporaliov1alpha1.RolloutStrategy{},
			},
			expectUpdate: 1,
		},
		{
			name: "update deployment when a deprecated version has an expired connection spec hash",
			k8sState: &k8s.DeploymentState{
				Deployments: map[string]*appsv1.Deployment{
					"123": createDeploymentWithDefaultConnectionSpecHash(1),
					"456": createDeploymentWithDefaultConnectionSpecHash(1),
					"789": createDeploymentWithExpiredConnectionSpecHash(1),
				},
			},
			status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
				TargetVersion: temporaliov1alpha1.TargetWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						BuildID:    "123",
						Status:     temporaliov1alpha1.VersionStatusRamping,
						Deployment: &corev1.ObjectReference{Name: "test-123"},
					},
				},
				CurrentVersion: &temporaliov1alpha1.CurrentWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						BuildID:    "456",
						Status:     temporaliov1alpha1.VersionStatusCurrent,
						Deployment: &corev1.ObjectReference{Name: "test-456"},
					},
				},
				DeprecatedVersions: []*temporaliov1alpha1.DeprecatedWorkerDeploymentVersion{
					{
						BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
							BuildID:    "789",
							Status:     temporaliov1alpha1.VersionStatusDraining,
							Deployment: &corev1.ObjectReference{Name: "test-789"},
						},
					},
				},
			},
			spec: &temporaliov1alpha1.TemporalWorkerDeploymentSpec{
				Replicas: func() *int32 { r := int32(1); return &r }(),
			},
			state: &temporal.TemporalWorkerState{},
			config: &Config{
				RolloutStrategy: temporaliov1alpha1.RolloutStrategy{},
			},
			expectUpdate: 1,
		},
		{
			name: "update deployment when current version has an expired connection spec hash",
			k8sState: &k8s.DeploymentState{
				Deployments: map[string]*appsv1.Deployment{
					"123": createDeploymentWithDefaultConnectionSpecHash(1),
					"456": createDeploymentWithExpiredConnectionSpecHash(1),
				},
			},
			status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
				TargetVersion: temporaliov1alpha1.TargetWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						BuildID:    "123",
						Status:     temporaliov1alpha1.VersionStatusRamping,
						Deployment: &corev1.ObjectReference{Name: "test-123"},
					},
				},
				CurrentVersion: &temporaliov1alpha1.CurrentWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						BuildID:    "456",
						Status:     temporaliov1alpha1.VersionStatusCurrent,
						Deployment: &corev1.ObjectReference{Name: "test-456"},
					},
				},
			},
			spec: &temporaliov1alpha1.TemporalWorkerDeploymentSpec{
				Replicas: func() *int32 { r := int32(1); return &r }(),
			},
			state: &temporal.TemporalWorkerState{},
			config: &Config{
				RolloutStrategy: temporaliov1alpha1.RolloutStrategy{},
			},
			expectUpdate: 1,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.status == nil {
				tc.status = &temporaliov1alpha1.TemporalWorkerDeploymentStatus{}
			}
			maxV := defaults.MaxVersionsIneligibleForDeletion
			if tc.maxVersionsIneligibleForDeletion != nil {
				maxV = *tc.maxVersionsIneligibleForDeletion
			}

			plan, err := GeneratePlan(logr.Discard(), tc.k8sState, tc.status, tc.spec, tc.state, createDefaultConnectionSpec(), tc.config, "test/namespace", maxV)
			require.NoError(t, err)

			assert.Equal(t, tc.expectDelete, len(plan.DeleteDeployments), "unexpected number of deletions")
			assert.Equal(t, tc.expectScale, len(plan.ScaleDeployments), "unexpected number of scales")
			assert.Equal(t, tc.expectCreate, plan.ShouldCreateDeployment, "unexpected create flag")
			assert.Equal(t, tc.expectUpdate, len(plan.UpdateDeployments), "unexpected number of updates")
			assert.Equal(t, tc.expectWorkflow, len(plan.TestWorkflows), "unexpected number of test workflows")
			assert.Equal(t, tc.expectConfig, plan.VersionConfig != nil, "unexpected version config presence")

			if tc.expectConfig {
				assert.NotNil(t, plan.VersionConfig, "expected version config")
				if tc.expectConfigSetCurrent != nil {
					assert.Equal(t, *tc.expectConfigSetCurrent, plan.VersionConfig.SetCurrent, "unexpected SetCurrent value")
				}
				if tc.expectConfigRampPercent != nil {
					assert.Equal(t, *tc.expectConfigRampPercent, plan.VersionConfig.RampPercentage, "unexpected RampPercentage value")
				}
			}
		})
	}
}

func TestGetDeleteDeployments(t *testing.T) {
	testCases := []struct {
		name          string
		k8sState      *k8s.DeploymentState
		status        *temporaliov1alpha1.TemporalWorkerDeploymentStatus
		spec          *temporaliov1alpha1.TemporalWorkerDeploymentSpec
		config        *Config
		expectDeletes int
	}{
		{
			name: "drained version should be deleted",
			k8sState: &k8s.DeploymentState{
				Deployments: map[string]*appsv1.Deployment{
					"123": createDeploymentWithDefaultConnectionSpecHash(0),
				},
			},
			status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
				DeprecatedVersions: []*temporaliov1alpha1.DeprecatedWorkerDeploymentVersion{
					{
						BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
							BuildID:    "123",
							Status:     temporaliov1alpha1.VersionStatusDrained,
							Deployment: &corev1.ObjectReference{Name: "test-123"},
						},
						DrainedSince: &metav1.Time{
							Time: time.Now().Add(-24 * time.Hour),
						},
					},
				},
			},
			spec: &temporaliov1alpha1.TemporalWorkerDeploymentSpec{
				SunsetStrategy: temporaliov1alpha1.SunsetStrategy{
					DeleteDelay: &metav1.Duration{Duration: 4 * time.Hour},
				},
				Replicas: func() *int32 { r := int32(1); return &r }(),
			},
			config: &Config{
				RolloutStrategy: temporaliov1alpha1.RolloutStrategy{},
			},
			expectDeletes: 1,
		},
		{
			name: "not yet drained long enough",
			k8sState: &k8s.DeploymentState{
				Deployments: map[string]*appsv1.Deployment{
					"456": createDeploymentWithDefaultConnectionSpecHash(0),
				},
			},
			status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
				DeprecatedVersions: []*temporaliov1alpha1.DeprecatedWorkerDeploymentVersion{
					{
						BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
							BuildID:    "456",
							Status:     temporaliov1alpha1.VersionStatusDrained,
							Deployment: &corev1.ObjectReference{Name: "test-456"},
						},
						DrainedSince: &metav1.Time{
							Time: time.Now().Add(-1 * time.Hour),
						},
					},
				},
			},
			spec: &temporaliov1alpha1.TemporalWorkerDeploymentSpec{
				SunsetStrategy: temporaliov1alpha1.SunsetStrategy{
					DeleteDelay: &metav1.Duration{Duration: 4 * time.Hour},
				},
				Replicas: func() *int32 { r := int32(1); return &r }(),
			},
			expectDeletes: 0,
		},
		{
			name: "not registered version should be deleted",
			k8sState: &k8s.DeploymentState{
				Deployments: map[string]*appsv1.Deployment{
					"123": createDeploymentWithDefaultConnectionSpecHash(1),
					"456": createDeploymentWithDefaultConnectionSpecHash(1),
				},
			},
			status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
				TargetVersion: temporaliov1alpha1.TargetWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						BuildID:    "123",
						Status:     temporaliov1alpha1.VersionStatusCurrent,
						Deployment: &corev1.ObjectReference{Name: "test-123"},
					},
				},
				DeprecatedVersions: []*temporaliov1alpha1.DeprecatedWorkerDeploymentVersion{
					{
						BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
							BuildID:    "456",
							Status:     temporaliov1alpha1.VersionStatusNotRegistered,
							Deployment: &corev1.ObjectReference{Name: "test-456"},
						},
					},
				},
			},
			spec: &temporaliov1alpha1.TemporalWorkerDeploymentSpec{
				Replicas: func() *int32 { r := int32(1); return &r }(),
			},
			expectDeletes: 1,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.spec.Default(context.Background())
			require.NoError(t, err)
			deletes := getDeleteDeployments(tc.k8sState, tc.status, tc.spec)
			assert.Equal(t, tc.expectDeletes, len(deletes), "unexpected number of deletes")
		})
	}
}

func TestGetScaleDeployments(t *testing.T) {
	testCases := []struct {
		name     string
		k8sState *k8s.DeploymentState
		status   *temporaliov1alpha1.TemporalWorkerDeploymentStatus
		spec     *temporaliov1alpha1.TemporalWorkerDeploymentSpec
		state    *temporal.TemporalWorkerState
		// map of build id to scaled replicas
		expectScales map[string]uint32
	}{
		{
			name: "current version needs scaling",
			k8sState: &k8s.DeploymentState{
				Deployments: map[string]*appsv1.Deployment{
					"123": createDeploymentWithDefaultConnectionSpecHash(1),
				},
			},
			status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
				TargetVersion: temporaliov1alpha1.TargetWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						BuildID:    "123",
						Status:     temporaliov1alpha1.VersionStatusCurrent,
						Deployment: &corev1.ObjectReference{Name: "test-123"},
					},
				},
				CurrentVersion: &temporaliov1alpha1.CurrentWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						BuildID:    "123",
						Status:     temporaliov1alpha1.VersionStatusCurrent,
						Deployment: &corev1.ObjectReference{Name: "test-123"},
					},
				},
			},
			spec: &temporaliov1alpha1.TemporalWorkerDeploymentSpec{
				Replicas: func() *int32 { r := int32(2); return &r }(),
			},
			expectScales: map[string]uint32{"test-123": 2},
		},
		{
			name: "drained version needs scaling down",
			k8sState: &k8s.DeploymentState{
				Deployments: map[string]*appsv1.Deployment{
					"123": createDeploymentWithDefaultConnectionSpecHash(1),
					"456": createDeploymentWithDefaultConnectionSpecHash(2),
				},
			},
			status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
				TargetVersion: temporaliov1alpha1.TargetWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						BuildID:    "123",
						Status:     temporaliov1alpha1.VersionStatusCurrent,
						Deployment: &corev1.ObjectReference{Name: "test-123"},
					},
				},
				DeprecatedVersions: []*temporaliov1alpha1.DeprecatedWorkerDeploymentVersion{
					{
						BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
							BuildID:    "456",
							Status:     temporaliov1alpha1.VersionStatusDrained,
							Deployment: &corev1.ObjectReference{Name: "test-456"},
						},
						DrainedSince: &metav1.Time{
							Time: time.Now().Add(-24 * time.Hour),
						},
					},
				},
			},
			spec: &temporaliov1alpha1.TemporalWorkerDeploymentSpec{
				SunsetStrategy: temporaliov1alpha1.SunsetStrategy{
					ScaledownDelay: &metav1.Duration{Duration: 0},
				},
				Replicas: func() *int32 { r := int32(1); return &r }(),
			},
			expectScales: map[string]uint32{"test-456": 0},
		},
		{
			name: "inactive non-target version needs scaling down",
			k8sState: &k8s.DeploymentState{
				Deployments: map[string]*appsv1.Deployment{
					"a": createDeploymentWithDefaultConnectionSpecHash(1),
				},
				DeploymentRefs: map[string]*corev1.ObjectReference{
					"a": {Name: "test-a"},
				},
			},
			status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
				TargetVersion: temporaliov1alpha1.TargetWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						BuildID:    "123",
						Status:     temporaliov1alpha1.VersionStatusInactive,
						Deployment: &corev1.ObjectReference{Name: "test-123"},
					},
				},
				DeprecatedVersions: []*temporaliov1alpha1.DeprecatedWorkerDeploymentVersion{
					{
						BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
							BuildID:    "a",
							Status:     temporaliov1alpha1.VersionStatusInactive,
							Deployment: &corev1.ObjectReference{Name: "test-a"},
						},
					},
				},
			},
			spec: &temporaliov1alpha1.TemporalWorkerDeploymentSpec{
				Replicas: func() *int32 { r := int32(3); return &r }(),
			},
			expectScales: map[string]uint32{"test-a": 0},
		},
		{
			name: "ramping version needs scaling up",
			k8sState: &k8s.DeploymentState{
				Deployments: map[string]*appsv1.Deployment{
					"b": createDeploymentWithDefaultConnectionSpecHash(0),
				},
				DeploymentRefs: map[string]*corev1.ObjectReference{
					"b": {Name: "test-b"},
				},
			},
			status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
				TargetVersion: temporaliov1alpha1.TargetWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						BuildID:    "b",
						Status:     temporaliov1alpha1.VersionStatusRamping,
						Deployment: &corev1.ObjectReference{Name: "test-b"},
					},
				},
			},
			spec: &temporaliov1alpha1.TemporalWorkerDeploymentSpec{
				Replicas: func() *int32 { r := int32(3); return &r }(),
			},
			state: &temporal.TemporalWorkerState{
				RampingBuildID: "b",
			},
			expectScales: map[string]uint32{"test-b": 3},
		},
		{
			name: "inactive target version needs scaling up",
			k8sState: &k8s.DeploymentState{
				Deployments: map[string]*appsv1.Deployment{
					"a": createDeploymentWithDefaultConnectionSpecHash(0),
				},
				DeploymentRefs: map[string]*corev1.ObjectReference{
					"a": {Name: "test-a"},
				},
			},
			status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
				TargetVersion: temporaliov1alpha1.TargetWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						BuildID:    "a",
						Status:     temporaliov1alpha1.VersionStatusInactive,
						Deployment: &corev1.ObjectReference{Name: "test-a"},
					},
				},
				DeprecatedVersions: []*temporaliov1alpha1.DeprecatedWorkerDeploymentVersion{
					{
						BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
							BuildID:    "b",
							Status:     temporaliov1alpha1.VersionStatusInactive,
							Deployment: &corev1.ObjectReference{Name: "test-b"},
						},
					},
				},
			},
			spec: &temporaliov1alpha1.TemporalWorkerDeploymentSpec{
				Replicas: func() *int32 { r := int32(3); return &r }(),
			},
			expectScales: map[string]uint32{"test-a": 3},
		},
		{
			name: "don't scale down drained deployment before delay",
			k8sState: &k8s.DeploymentState{
				Deployments: map[string]*appsv1.Deployment{
					"b": createDeploymentWithDefaultConnectionSpecHash(3),
				},
				DeploymentRefs: map[string]*corev1.ObjectReference{
					"b": {Name: "test-b"},
				},
			},
			status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
				TargetVersion: temporaliov1alpha1.TargetWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						BuildID:    "a",
						Status:     temporaliov1alpha1.VersionStatusInactive,
						Deployment: &corev1.ObjectReference{Name: "test-a"},
					},
				},
				DeprecatedVersions: []*temporaliov1alpha1.DeprecatedWorkerDeploymentVersion{
					{
						BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
							BuildID:    "b",
							Status:     temporaliov1alpha1.VersionStatusDrained,
							Deployment: &corev1.ObjectReference{Name: "test-b"},
						},
						DrainedSince: &metav1.Time{
							Time: time.Now().Add(-1 * time.Hour), // Recently drained
						},
					},
				},
			},
			spec: &temporaliov1alpha1.TemporalWorkerDeploymentSpec{
				SunsetStrategy: temporaliov1alpha1.SunsetStrategy{
					ScaledownDelay: &metav1.Duration{
						Duration: 4 * time.Hour, // Longer than 1 hour
					},
				},
				Replicas: func() *int32 { r := int32(3); return &r }(),
			},
			expectScales: map[string]uint32{}, // No scaling yet because not enough time passed
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			scales := getScaleDeployments(tc.k8sState, tc.status, tc.spec)
			assert.Equal(t, len(tc.expectScales), len(scales), "unexpected number of scales")
			actualScaleDeploymentNames := make([]string, 0)
			for deploymentRef, actualReplicas := range scales {
				expectedReplicas, ok := tc.expectScales[deploymentRef.Name]
				actualScaleDeploymentNames = append(actualScaleDeploymentNames, deploymentRef.Name)
				assert.True(t, ok, "did not expect to find Deployment %s in ScaleDeployments, but found it", deploymentRef.Name)
				assert.Equal(t, expectedReplicas, actualReplicas, "unexpected scale replicas")
			}
			for expectedName := range tc.expectScales {
				if !slices.Contains(actualScaleDeploymentNames, expectedName) {
					t.Errorf("expected to find Deployment %s in ScaleDeployments, but did not find it", expectedName)
				}
			}
		})
	}
}

func TestShouldCreateDeployment(t *testing.T) {
	testCases := []struct {
		name                             string
		status                           *temporaliov1alpha1.TemporalWorkerDeploymentStatus
		spec                             *temporaliov1alpha1.TemporalWorkerDeploymentSpec
		expectCreates                    bool
		maxVersionsIneligibleForDeletion *int32 // set by env if non-nil, else default 75
	}{
		{
			name: "existing deployment should not create",
			status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
				TargetVersion: temporaliov1alpha1.TargetWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						BuildID:    "123",
						Status:     temporaliov1alpha1.VersionStatusInactive,
						Deployment: &corev1.ObjectReference{Name: "test-123"},
					},
				},
			},
			spec: &temporaliov1alpha1.TemporalWorkerDeploymentSpec{
				Replicas: func() *int32 { r := int32(1); return &r }(),
			},
			expectCreates: false,
		},
		{
			name: "target version without deployment should create",
			status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
				TargetVersion: temporaliov1alpha1.TargetWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						BuildID:    "b",
						Status:     temporaliov1alpha1.VersionStatusInactive,
						Deployment: nil,
					},
				},
			},
			spec: &temporaliov1alpha1.TemporalWorkerDeploymentSpec{
				Replicas: func() *int32 { r := int32(1); return &r }(),
			},
			expectCreates: true,
		},
		{
			name: "should not create when version limit ineligible for deletion is reached (default limit)",
			status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
				TargetVersion: temporaliov1alpha1.TargetWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						BuildID:    "new",
						Status:     temporaliov1alpha1.VersionStatusNotRegistered,
						Deployment: nil,
					},
				},
				DeprecatedVersions: func() []*temporaliov1alpha1.DeprecatedWorkerDeploymentVersion {
					// default is NOT eligible for deletion, so 75 empty Deprecated Versions should block rollout
					r := make([]*temporaliov1alpha1.DeprecatedWorkerDeploymentVersion, 75)
					for i := range r {
						r[i] = &temporaliov1alpha1.DeprecatedWorkerDeploymentVersion{}
					}
					return r
				}(),
			},
			spec: &temporaliov1alpha1.TemporalWorkerDeploymentSpec{
				Replicas: func() *int32 { r := int32(1); return &r }(),
			},
			expectCreates:                    false,
			maxVersionsIneligibleForDeletion: nil, // MaxVersions is nil, so uses default of 75
		},
		{
			name: "should not create when version limit ineligible for deletion is reached (custom limit)",
			status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
				TargetVersion: temporaliov1alpha1.TargetWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						BuildID:    "new",
						Status:     temporaliov1alpha1.VersionStatusNotRegistered,
						Deployment: nil,
					},
				},
				DeprecatedVersions: func() []*temporaliov1alpha1.DeprecatedWorkerDeploymentVersion {
					// default is NOT eligible for deletion, so 5 empty Deprecated Versions should block rollout
					r := make([]*temporaliov1alpha1.DeprecatedWorkerDeploymentVersion, 5)
					for i := range r {
						r[i] = &temporaliov1alpha1.DeprecatedWorkerDeploymentVersion{}
					}
					return r
				}(),
			},
			spec: &temporaliov1alpha1.TemporalWorkerDeploymentSpec{
				Replicas: func() *int32 { r := int32(1); return &r }(),
			},
			expectCreates:                    false,
			maxVersionsIneligibleForDeletion: func() *int32 { i := int32(5); return &i }(),
		},
		{
			name: "should create when below version limit",
			status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
				VersionCount: 4,
				TargetVersion: temporaliov1alpha1.TargetWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						BuildID:    "new",
						Status:     temporaliov1alpha1.VersionStatusNotRegistered,
						Deployment: nil,
					},
				},
				DeprecatedVersions: func() []*temporaliov1alpha1.DeprecatedWorkerDeploymentVersion {
					r := make([]*temporaliov1alpha1.DeprecatedWorkerDeploymentVersion, 4)
					for i := range r {
						r[i] = &temporaliov1alpha1.DeprecatedWorkerDeploymentVersion{}
					}
					return r
				}(),
			},
			spec: &temporaliov1alpha1.TemporalWorkerDeploymentSpec{
				Replicas: func() *int32 { r := int32(1); return &r }(),
			},
			expectCreates:                    true,
			maxVersionsIneligibleForDeletion: func() *int32 { i := int32(5); return &i }(),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			maxV := defaults.MaxVersionsIneligibleForDeletion
			if tc.maxVersionsIneligibleForDeletion != nil {
				maxV = *tc.maxVersionsIneligibleForDeletion
			}
			creates := shouldCreateDeployment(tc.status, maxV)
			assert.Equal(t, tc.expectCreates, creates, "unexpected create decision")
		})
	}
}

func TestGetTestWorkflows(t *testing.T) {
	testCases := []struct {
		name            string
		status          *temporaliov1alpha1.TemporalWorkerDeploymentStatus
		config          *Config
		expectWorkflows int
	}{
		{
			name: "gate workflow needed",
			status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
				TargetVersion: temporaliov1alpha1.TargetWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						BuildID: "123",
						Status:  temporaliov1alpha1.VersionStatusInactive,
						TaskQueues: []temporaliov1alpha1.TaskQueue{
							{Name: "queue1"},
							{Name: "queue2"},
						},
					},
					TestWorkflows: []temporaliov1alpha1.WorkflowExecution{
						{
							TaskQueue: "queue1",
							Status:    temporaliov1alpha1.WorkflowExecutionStatusRunning,
						},
					},
				},
				CurrentVersion: &temporaliov1alpha1.CurrentWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						BuildID: "456",
						Status:  temporaliov1alpha1.VersionStatusCurrent,
					},
				},
			},
			config: &Config{
				RolloutStrategy: temporaliov1alpha1.RolloutStrategy{
					Gate: &temporaliov1alpha1.GateWorkflowConfig{
						WorkflowType: "TestWorkflow",
					},
				},
			},
			expectWorkflows: 1, // Only queue2 needs a workflow
		},
		{
			name: "no gate workflow",
			status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
				TargetVersion: temporaliov1alpha1.TargetWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						BuildID: "123",
						Status:  temporaliov1alpha1.VersionStatusInactive,
						TaskQueues: []temporaliov1alpha1.TaskQueue{
							{Name: "queue1"},
							{Name: "queue2"},
						},
					},
				},
			},
			config: &Config{
				RolloutStrategy: temporaliov1alpha1.RolloutStrategy{},
			},
			expectWorkflows: 0,
		},
		{
			name: "gate workflow with empty task queues",
			status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
				TargetVersion: temporaliov1alpha1.TargetWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						BuildID:    "123",
						Status:     temporaliov1alpha1.VersionStatusInactive,
						TaskQueues: []temporaliov1alpha1.TaskQueue{},
					},
				},
			},
			config: &Config{
				RolloutStrategy: temporaliov1alpha1.RolloutStrategy{
					Gate: &temporaliov1alpha1.GateWorkflowConfig{
						WorkflowType: "TestWorkflow",
					},
				},
			},
			expectWorkflows: 0,
		},
		{
			name: "all test workflows already running",
			status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
				TargetVersion: temporaliov1alpha1.TargetWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						BuildID: "123",
						Status:  temporaliov1alpha1.VersionStatusInactive,
						TaskQueues: []temporaliov1alpha1.TaskQueue{
							{Name: "queue1"},
							{Name: "queue2"},
						},
					},
					TestWorkflows: []temporaliov1alpha1.WorkflowExecution{
						{
							TaskQueue: "queue1",
							Status:    temporaliov1alpha1.WorkflowExecutionStatusRunning,
						},
						{
							TaskQueue: "queue2",
							Status:    temporaliov1alpha1.WorkflowExecutionStatusCompleted,
						},
					},
				},
			},
			config: &Config{
				RolloutStrategy: temporaliov1alpha1.RolloutStrategy{
					Gate: &temporaliov1alpha1.GateWorkflowConfig{
						WorkflowType: "TestWorkflow",
					},
				},
			},
			expectWorkflows: 0,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			workflows := getTestWorkflows(tc.status, tc.config, "test/namespace")
			assert.Equal(t, tc.expectWorkflows, len(workflows), "unexpected number of test workflows")
		})
	}
}

func TestGetVersionConfigDiff(t *testing.T) {
	testCases := []struct {
		name              string
		strategy          temporaliov1alpha1.RolloutStrategy
		status            *temporaliov1alpha1.TemporalWorkerDeploymentStatus
		spec              *temporaliov1alpha1.TemporalWorkerDeploymentSpec
		state             *temporal.TemporalWorkerState
		expectConfig      bool
		expectSetCurrent  bool
		expectRampPercent *float32
	}{
		{
			name: "all at once strategy",
			strategy: temporaliov1alpha1.RolloutStrategy{
				Strategy: temporaliov1alpha1.UpdateAllAtOnce,
			},
			status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
				TargetVersion: temporaliov1alpha1.TargetWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						BuildID:      "test/namespace.123",
						Status:       temporaliov1alpha1.VersionStatusInactive,
						HealthySince: &metav1.Time{Time: time.Now().Add(-1 * time.Hour)},
					},
				},
				CurrentVersion: &temporaliov1alpha1.CurrentWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						BuildID: "456",
						Status:  temporaliov1alpha1.VersionStatusCurrent,
					},
				},
			},
			spec:             &temporaliov1alpha1.TemporalWorkerDeploymentSpec{},
			expectConfig:     true,
			expectSetCurrent: true,
		},
		{
			name: "progressive strategy",
			strategy: temporaliov1alpha1.RolloutStrategy{
				Strategy: temporaliov1alpha1.UpdateProgressive,
				Steps: []temporaliov1alpha1.RolloutStep{
					{
						RampPercentage: 25,
						PauseDuration: metav1.Duration{
							Duration: 1 * time.Hour,
						},
					},
				},
			},
			status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
				TargetVersion: temporaliov1alpha1.TargetWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						BuildID: "123",
						Status:  temporaliov1alpha1.VersionStatusInactive,
						HealthySince: &metav1.Time{
							Time: time.Now().Add(-30 * time.Minute),
						},
					},
					RampPercentage: nil,
					RampLastModifiedAt: &metav1.Time{
						Time: time.Now().Add(-30 * time.Minute),
					},
				},
				CurrentVersion: &temporaliov1alpha1.CurrentWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						BuildID: "456",
						Status:  temporaliov1alpha1.VersionStatusCurrent,
					},
				},
			},
			spec:              &temporaliov1alpha1.TemporalWorkerDeploymentSpec{},
			expectConfig:      true,
			expectSetCurrent:  false,
			expectRampPercent: float32Ptr(25),
		},
		{
			name: "rollback scenario - target equals current but different version is ramping",
			strategy: temporaliov1alpha1.RolloutStrategy{
				Strategy: temporaliov1alpha1.UpdateAllAtOnce,
			},
			status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
				TargetVersion: temporaliov1alpha1.TargetWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						BuildID: "123",
						Status:  temporaliov1alpha1.VersionStatusCurrent,
						HealthySince: &metav1.Time{
							Time: time.Now().Add(-1 * time.Hour),
						},
					},
				},
				CurrentVersion: &temporaliov1alpha1.CurrentWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						BuildID: "123",
						Status:  temporaliov1alpha1.VersionStatusCurrent,
					},
				},
				DeprecatedVersions: []*temporaliov1alpha1.DeprecatedWorkerDeploymentVersion{
					{
						BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
							BuildID: "456",
							Status:  temporaliov1alpha1.VersionStatusRamping,
							HealthySince: &metav1.Time{
								Time: time.Now().Add(-30 * time.Minute),
							},
						},
					},
				},
			},
			spec:             &temporaliov1alpha1.TemporalWorkerDeploymentSpec{},
			state:            &temporal.TemporalWorkerState{RampingBuildID: "456"},
			expectConfig:     true,
			expectSetCurrent: false,
		},
		{
			name: "roll-forward scenario - target differs from current but different version is ramping",
			strategy: temporaliov1alpha1.RolloutStrategy{
				Strategy: temporaliov1alpha1.UpdateAllAtOnce,
			},
			status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
				TargetVersion: temporaliov1alpha1.TargetWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						BuildID: "789",
						Status:  temporaliov1alpha1.VersionStatusInactive,
						HealthySince: &metav1.Time{
							Time: time.Now().Add(-1 * time.Hour),
						},
					},
				},
				CurrentVersion: &temporaliov1alpha1.CurrentWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						BuildID: "123",
						Status:  temporaliov1alpha1.VersionStatusCurrent,
					},
				},
				DeprecatedVersions: []*temporaliov1alpha1.DeprecatedWorkerDeploymentVersion{
					{
						BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
							BuildID: "456",
							Status:  temporaliov1alpha1.VersionStatusRamping,
						},
					},
				},
			},
			spec:              &temporaliov1alpha1.TemporalWorkerDeploymentSpec{},
			expectConfig:      true,
			expectSetCurrent:  true,
			expectRampPercent: func() *float32 { f := float32(0); return &f }(),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			config := &Config{
				RolloutStrategy: tc.strategy,
			}
			versionConfig := getVersionConfigDiff(logr.Discard(), tc.status, tc.state, config, "test/namespace")
			assert.Equal(t, tc.expectConfig, versionConfig != nil, "unexpected version config presence")
			if tc.expectConfig {
				assert.NotNil(t, versionConfig, "expected version config")
				assert.Equal(t, tc.expectSetCurrent, versionConfig.SetCurrent, "unexpected set current value")
				if tc.expectRampPercent != nil {
					assert.Equal(t, *tc.expectRampPercent, versionConfig.RampPercentage, "unexpected ramp percentage")
				}
			}
		})
	}
}

func TestGetVersionConfig_ProgressiveRolloutEdgeCases(t *testing.T) {
	testCases := map[string]struct {
		strategy          temporaliov1alpha1.RolloutStrategy
		status            *temporaliov1alpha1.TemporalWorkerDeploymentStatus
		state             *temporal.TemporalWorkerState
		expectConfig      bool
		expectSetCurrent  bool
		expectRampPercent float32
	}{
		"progressive rollout completes last step": {
			strategy: temporaliov1alpha1.RolloutStrategy{
				Strategy: temporaliov1alpha1.UpdateProgressive,
				Steps: []temporaliov1alpha1.RolloutStep{
					{RampPercentage: 25, PauseDuration: metav1.Duration{Duration: 1 * time.Hour}},
					{RampPercentage: 50, PauseDuration: metav1.Duration{Duration: 1 * time.Hour}},
					{RampPercentage: 75, PauseDuration: metav1.Duration{Duration: 1 * time.Hour}},
				},
			},
			status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
				CurrentVersion: &temporaliov1alpha1.CurrentWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						BuildID: "123",
						Status:  temporaliov1alpha1.VersionStatusCurrent,
					},
				},
				TargetVersion: temporaliov1alpha1.TargetWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						BuildID: "456",
						Status:  temporaliov1alpha1.VersionStatusRamping,
						HealthySince: &metav1.Time{
							Time: time.Now().Add(-1 * time.Hour),
						},
					},
					RampingSince: &metav1.Time{
						Time: time.Now().Add(-4 * time.Hour), // Past all steps
					},
					RampPercentage: float32Ptr(75),
					RampLastModifiedAt: &metav1.Time{
						Time: time.Now().Add(-4 * time.Hour), // Past all steps
					},
				},
			},
			expectConfig:     true,
			expectSetCurrent: true,
		},
		"progressive rollout with nil RampingSince": {
			strategy: temporaliov1alpha1.RolloutStrategy{
				Strategy: temporaliov1alpha1.UpdateProgressive,
				Steps: []temporaliov1alpha1.RolloutStep{
					{RampPercentage: 25, PauseDuration: metav1.Duration{Duration: 1 * time.Hour}},
				},
			},
			status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
				CurrentVersion: &temporaliov1alpha1.CurrentWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						BuildID: "123",
						Status:  temporaliov1alpha1.VersionStatusCurrent,
					},
				},
				TargetVersion: temporaliov1alpha1.TargetWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						BuildID:      "test/namespace.456",
						Status:       temporaliov1alpha1.VersionStatusInactive,
						HealthySince: &metav1.Time{Time: time.Now()},
					},
					RampingSince:       nil,
					RampLastModifiedAt: nil,
				},
			},
			expectConfig:      true,
			expectRampPercent: 25, // First step
			expectSetCurrent:  false,
		},
		"progressive rollout at exact step boundary": {
			strategy: temporaliov1alpha1.RolloutStrategy{
				Strategy: temporaliov1alpha1.UpdateProgressive,
				Steps: []temporaliov1alpha1.RolloutStep{
					{RampPercentage: 25, PauseDuration: metav1.Duration{Duration: 1 * time.Hour}},
					{RampPercentage: 50, PauseDuration: metav1.Duration{Duration: 1 * time.Hour}},
				},
			},
			status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
				CurrentVersion: &temporaliov1alpha1.CurrentWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						BuildID: "123",
						Status:  temporaliov1alpha1.VersionStatusCurrent,
					},
				},
				TargetVersion: temporaliov1alpha1.TargetWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						BuildID:      "test/namespace.456",
						Status:       temporaliov1alpha1.VersionStatusRamping,
						HealthySince: &metav1.Time{Time: time.Now()},
					},
					RampingSince: &metav1.Time{
						Time: time.Now().Add(-4 * time.Hour),
					},
					RampPercentage: float32Ptr(25),
					RampLastModifiedAt: &metav1.Time{
						Time: time.Now().Add(-1 * time.Hour), // Exactly at step boundary
					},
				},
			},
			expectConfig:      true,
			expectRampPercent: 50, // When set as current, ramp is 0
			expectSetCurrent:  false,
		},
		"progressive rollout with zero ramp percentage step": {
			strategy: temporaliov1alpha1.RolloutStrategy{
				Strategy: temporaliov1alpha1.UpdateProgressive,
				Steps: []temporaliov1alpha1.RolloutStep{
					{RampPercentage: 25, PauseDuration: metav1.Duration{Duration: 30 * time.Minute}},
					{RampPercentage: 0, PauseDuration: metav1.Duration{Duration: 30 * time.Minute}},
					{RampPercentage: 50, PauseDuration: metav1.Duration{Duration: 30 * time.Minute}},
				},
			},
			status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
				CurrentVersion: &temporaliov1alpha1.CurrentWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						BuildID: "123",
						Status:  temporaliov1alpha1.VersionStatusCurrent,
					},
				},
				TargetVersion: temporaliov1alpha1.TargetWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						BuildID:      "test/namespace.456",
						Status:       temporaliov1alpha1.VersionStatusRamping,
						HealthySince: &metav1.Time{Time: time.Now()},
					},
					RampingSince: &metav1.Time{
						Time: time.Now().Add(-45 * time.Minute), // In second step
					},
					RampLastModifiedAt: &metav1.Time{
						Time: time.Now().Add(-45 * time.Minute), // In second step
					},
				},
			},
			expectConfig:      true,
			expectRampPercent: 25, // Should maintain previous ramp value
			expectSetCurrent:  false,
		},
		"progressive rollout just past exact boundary": {
			strategy: temporaliov1alpha1.RolloutStrategy{
				Strategy: temporaliov1alpha1.UpdateProgressive,
				Steps: []temporaliov1alpha1.RolloutStep{
					{RampPercentage: 25, PauseDuration: metav1.Duration{Duration: 1 * time.Hour}},
					{RampPercentage: 50, PauseDuration: metav1.Duration{Duration: 1 * time.Hour}},
				},
			},
			status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
				CurrentVersion: &temporaliov1alpha1.CurrentWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						BuildID: "123",
						Status:  temporaliov1alpha1.VersionStatusCurrent,
					},
				},
				TargetVersion: temporaliov1alpha1.TargetWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						BuildID:      "test/namespace.456",
						Status:       temporaliov1alpha1.VersionStatusRamping,
						HealthySince: &metav1.Time{Time: time.Now()},
					},
					RampingSince: &metav1.Time{
						Time: time.Now().Add(-2*time.Hour - 1*time.Second), // Just past boundary
					},
					RampPercentage: float32Ptr(50),
					RampLastModifiedAt: &metav1.Time{
						Time: time.Now().Add(-2*time.Hour - 1*time.Second), // Just past boundary
					},
				},
			},
			expectConfig:      true,
			expectRampPercent: 0,
			expectSetCurrent:  true,
		},
		"nil rampLastModifiedAt should not cause a panic": {
			strategy: temporaliov1alpha1.RolloutStrategy{
				Strategy: temporaliov1alpha1.UpdateProgressive,
				Steps: []temporaliov1alpha1.RolloutStep{
					{
						RampPercentage: 10,
						PauseDuration:  metav1.Duration{Duration: 30 * time.Second},
					},
				},
			},
			status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
				CurrentVersion: &temporaliov1alpha1.CurrentWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						BuildID: "123",
						Status:  temporaliov1alpha1.VersionStatusCurrent,
					},
				},
				TargetVersion: temporaliov1alpha1.TargetWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						BuildID:      "test/namespace.456",
						Status:       temporaliov1alpha1.VersionStatusRamping,
						HealthySince: &metav1.Time{Time: time.Now()},
					},
					RampingSince: &metav1.Time{
						Time: time.Now().Add(-2*time.Hour - 1*time.Second),
					},
					RampPercentage:     float32Ptr(10),
					RampLastModifiedAt: nil, // nil rampLastModifiedAt should not cause a panic!
				},
			},
			expectConfig:      false,
			expectRampPercent: 0,
			expectSetCurrent:  false,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			config := &Config{
				RolloutStrategy: tc.strategy,
			}
			versionConfig := getVersionConfigDiff(testlogr.New(t), tc.status, tc.state, config, "test/namespace")
			assert.Equal(t, tc.expectConfig, versionConfig != nil, "unexpected version config presence")
			if tc.expectConfig {
				assert.Equal(t, tc.expectSetCurrent, versionConfig.SetCurrent, "unexpected set default value")
				if !tc.expectSetCurrent {
					assert.Equal(t, tc.expectRampPercent, versionConfig.RampPercentage, "unexpected ramp percentage")
				}
			}
		})
	}
}

func TestGetVersionConfig_ProgressiveRolloutOverTime(t *testing.T) {
	testCases := map[string]struct {
		steps                 []temporaliov1alpha1.RolloutStep
		reconcileFreq         time.Duration
		initialRamp           *float32
		expectRamps           []float32
		expectRolloutDuration time.Duration
	}{
		"no steps, no ramps": {
			steps:                 []temporaliov1alpha1.RolloutStep{},
			reconcileFreq:         time.Second,
			expectRamps:           nil,
			expectRolloutDuration: 5*time.Minute - time.Second, // should reach test timeout with zero ramps
		},
		"controller keeping up": {
			steps: []temporaliov1alpha1.RolloutStep{
				rolloutStep(25, 5*time.Second),
				rolloutStep(50, 10*time.Second),
				rolloutStep(75, 30*time.Second),
			},
			reconcileFreq:         time.Second,
			expectRamps:           []float32{25, 50, 75, 100},
			expectRolloutDuration: 5*time.Second + 10*time.Second + 30*time.Second + 3*time.Second,
		},
		"controller reconciles slower than step durations": {
			steps: []temporaliov1alpha1.RolloutStep{
				rolloutStep(25, 5*time.Second),
				rolloutStep(50, 10*time.Second),
				rolloutStep(75, 30*time.Second),
			},
			reconcileFreq:         time.Minute,
			expectRamps:           []float32{25, 50, 75, 100},
			expectRolloutDuration: 3 * time.Minute,
		},
		"pick up ramping from last step that is <= current ramp": {
			steps: []temporaliov1alpha1.RolloutStep{
				rolloutStep(5, 10*time.Second),
				rolloutStep(10, 10*time.Second),
				rolloutStep(25, 10*time.Second),
				rolloutStep(50, 10*time.Second),
			},
			reconcileFreq: time.Second,
			// Simulate a ramp value set manually via Temporal CLI
			initialRamp:           float32Ptr(20),
			expectRamps:           []float32{10, 25, 50, 100},
			expectRolloutDuration: 30*time.Second + 3*time.Second,
		},
		"pick up ramping from first step if current ramp less than all steps": {
			steps: []temporaliov1alpha1.RolloutStep{
				rolloutStep(10, 10*time.Second),
				rolloutStep(25, 10*time.Second),
				rolloutStep(50, 10*time.Second),
			},
			reconcileFreq: time.Second,
			// Simulate a ramp value set manually via Temporal CLI
			initialRamp:           float32Ptr(1),
			expectRamps:           []float32{10, 25, 50, 100},
			expectRolloutDuration: 30*time.Second + 3*time.Second,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			var (
				diffs                 []float32
				testStart             = time.Now()
				testTimeout           = testStart.Add(5 * time.Minute)
				currentRampPercentage *float32
				now                   time.Time
				rampLastModified      *metav1.Time
			)

			// Simulate an initial ramping value that is different from existing steps in the rollout.
			// This could happen if the progressive rollout spec was updated, or if the user modified
			// the current ramp value externally, e.g. via the Temporal CLI/UI.
			if tc.initialRamp != nil {
				currentRampPercentage = tc.initialRamp
				// Imitate the "worst case" of ramp having just been updated.
				rampLastModified = &metav1.Time{Time: now}
			}

			for ct := testStart; ct.Before(testTimeout); ct = ct.Add(tc.reconcileFreq) {
				now = ct
				currentTime := metav1.NewTime(ct)

				config := handleProgressiveRollout(
					tc.steps,
					currentTime.Time,
					rampLastModified,
					currentRampPercentage,
					&VersionConfig{},
				)
				if config == nil {
					continue
				}

				// Keep track of the diff
				if config.SetCurrent {
					diffs = append(diffs, 100)
				} else {
					diffs = append(diffs, config.RampPercentage)
				}

				// Set ramp value and last modified time if it was updated (simulates what Temporal server would return on next reconcile loop)
				if config.RampPercentage != 0 {
					rampLastModified = &currentTime
					currentRampPercentage = &config.RampPercentage
				}

				// Exit early if ramping is complete
				if config.SetCurrent {
					break
				}
			}

			assert.Equal(t, tc.expectRamps, diffs)
			assert.Equal(t, tc.expectRolloutDuration, now.Sub(testStart))
		})
	}
}

func TestGetVersionConfig_GateWorkflowValidation(t *testing.T) {
	testCases := []struct {
		name         string
		strategy     temporaliov1alpha1.RolloutStrategy
		status       *temporaliov1alpha1.TemporalWorkerDeploymentStatus
		state        *temporal.TemporalWorkerState
		expectConfig bool
	}{
		{
			name: "gate workflow with failed test",
			strategy: temporaliov1alpha1.RolloutStrategy{
				Strategy: temporaliov1alpha1.UpdateAllAtOnce,
				Gate: &temporaliov1alpha1.GateWorkflowConfig{
					WorkflowType: "TestWorkflow",
				},
			},
			status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
				CurrentVersion: &temporaliov1alpha1.CurrentWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						BuildID: "123",
						Status:  temporaliov1alpha1.VersionStatusCurrent,
					},
				},
				TargetVersion: temporaliov1alpha1.TargetWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						BuildID:      "test/namespace.456",
						Status:       temporaliov1alpha1.VersionStatusInactive,
						HealthySince: &metav1.Time{Time: time.Now().Add(-1 * time.Hour)},
						TaskQueues: []temporaliov1alpha1.TaskQueue{
							{Name: "queue1"},
						},
					},
					TestWorkflows: []temporaliov1alpha1.WorkflowExecution{
						{
							TaskQueue: "queue1",
							Status:    temporaliov1alpha1.WorkflowExecutionStatusFailed,
						},
					},
				},
			},
			expectConfig: false, // Should not proceed with failed test
		},
		{
			name: "gate workflow with cancelled test",
			strategy: temporaliov1alpha1.RolloutStrategy{
				Strategy: temporaliov1alpha1.UpdateAllAtOnce,
				Gate: &temporaliov1alpha1.GateWorkflowConfig{
					WorkflowType: "TestWorkflow",
				},
			},
			status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
				CurrentVersion: &temporaliov1alpha1.CurrentWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						BuildID: "123",
						Status:  temporaliov1alpha1.VersionStatusCurrent,
					},
				},
				TargetVersion: temporaliov1alpha1.TargetWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						BuildID:      "test/namespace.456",
						Status:       temporaliov1alpha1.VersionStatusInactive,
						HealthySince: &metav1.Time{Time: time.Now().Add(-1 * time.Hour)},
						TaskQueues: []temporaliov1alpha1.TaskQueue{
							{Name: "queue1"},
						},
					},
					TestWorkflows: []temporaliov1alpha1.WorkflowExecution{
						{
							TaskQueue: "queue1",
							Status:    temporaliov1alpha1.WorkflowExecutionStatusCanceled,
						},
					},
				},
			},
			expectConfig: false, // Should not proceed with cancelled test
		},
		{
			name: "gate workflow with terminated test",
			strategy: temporaliov1alpha1.RolloutStrategy{
				Strategy: temporaliov1alpha1.UpdateAllAtOnce,
				Gate: &temporaliov1alpha1.GateWorkflowConfig{
					WorkflowType: "TestWorkflow",
				},
			},
			status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
				CurrentVersion: &temporaliov1alpha1.CurrentWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						BuildID: "123",
						Status:  temporaliov1alpha1.VersionStatusCurrent,
					},
				},
				TargetVersion: temporaliov1alpha1.TargetWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						BuildID:      "test/namespace.456",
						Status:       temporaliov1alpha1.VersionStatusInactive,
						HealthySince: &metav1.Time{Time: time.Now().Add(-1 * time.Hour)},
						TaskQueues: []temporaliov1alpha1.TaskQueue{
							{Name: "queue1"},
						},
					},
					TestWorkflows: []temporaliov1alpha1.WorkflowExecution{
						{
							TaskQueue: "queue1",
							Status:    temporaliov1alpha1.WorkflowExecutionStatusTerminated,
						},
					},
				},
			},
			expectConfig: false, // Should not proceed with terminated test
		},
		{
			name: "gate workflow with incomplete tests",
			strategy: temporaliov1alpha1.RolloutStrategy{
				Strategy: temporaliov1alpha1.UpdateAllAtOnce,
				Gate: &temporaliov1alpha1.GateWorkflowConfig{
					WorkflowType: "TestWorkflow",
				},
			},
			status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
				CurrentVersion: &temporaliov1alpha1.CurrentWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						BuildID: "123",
						Status:  temporaliov1alpha1.VersionStatusCurrent,
					},
				},
				TargetVersion: temporaliov1alpha1.TargetWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						BuildID:      "test/namespace.456",
						Status:       temporaliov1alpha1.VersionStatusInactive,
						HealthySince: &metav1.Time{Time: time.Now().Add(-1 * time.Hour)},
						TaskQueues: []temporaliov1alpha1.TaskQueue{
							{Name: "queue1"},
							{Name: "queue2"},
						},
					},
					TestWorkflows: []temporaliov1alpha1.WorkflowExecution{
						{
							TaskQueue: "queue1",
							Status:    temporaliov1alpha1.WorkflowExecutionStatusCompleted,
						},
						// Missing workflow for queue2
					},
				},
			},
			expectConfig: false, // Should not proceed with incomplete tests
		},
		{
			name: "gate workflow with empty task queues",
			strategy: temporaliov1alpha1.RolloutStrategy{
				Strategy: temporaliov1alpha1.UpdateAllAtOnce,
				Gate: &temporaliov1alpha1.GateWorkflowConfig{
					WorkflowType: "TestWorkflow",
				},
			},
			status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
				CurrentVersion: &temporaliov1alpha1.CurrentWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						BuildID: "123",
						Status:  temporaliov1alpha1.VersionStatusCurrent,
					},
				},
				TargetVersion: temporaliov1alpha1.TargetWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						BuildID:      "test/namespace.456",
						Status:       temporaliov1alpha1.VersionStatusInactive,
						HealthySince: &metav1.Time{Time: time.Now().Add(-1 * time.Hour)},
						TaskQueues:   []temporaliov1alpha1.TaskQueue{}, // Empty
					},
				},
			},
			expectConfig: false, // Should not proceed with no task queues
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			config := &Config{
				RolloutStrategy: tc.strategy,
			}
			versionConfig := getVersionConfigDiff(logr.Discard(), tc.status, tc.state, config, "test/namespace")
			if tc.expectConfig {
				assert.NotNil(t, versionConfig, "expected version config")
			} else {
				assert.Nil(t, versionConfig, "expected no version config")
			}
		})
	}
}

func TestComplexVersionStateScenarios(t *testing.T) {
	testCases := []struct {
		name           string
		k8sState       *k8s.DeploymentState
		config         *Config
		status         *temporaliov1alpha1.TemporalWorkerDeploymentStatus
		spec           *temporaliov1alpha1.TemporalWorkerDeploymentSpec
		state          *temporal.TemporalWorkerState
		expectDeletes  int
		expectScales   int
		expectVersions []string
	}{
		{
			name: "multiple deprecated versions in different states",
			k8sState: &k8s.DeploymentState{
				Deployments: map[string]*appsv1.Deployment{
					"a": createDeploymentWithDefaultConnectionSpecHash(5),
					"b": createDeploymentWithDefaultConnectionSpecHash(3),
					"c": createDeploymentWithDefaultConnectionSpecHash(3),
					"d": createDeploymentWithDefaultConnectionSpecHash(1),
					"e": createDeploymentWithDefaultConnectionSpecHash(0),
				},
			},
			config: &Config{
				RolloutStrategy: temporaliov1alpha1.RolloutStrategy{},
			},
			status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
				TargetVersion: temporaliov1alpha1.TargetWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						BuildID:    "a",
						Status:     temporaliov1alpha1.VersionStatusInactive,
						Deployment: &corev1.ObjectReference{Name: "test-a"},
					},
				},
				DeprecatedVersions: []*temporaliov1alpha1.DeprecatedWorkerDeploymentVersion{
					{
						BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
							BuildID:    "b",
							Status:     temporaliov1alpha1.VersionStatusInactive,
							Deployment: &corev1.ObjectReference{Name: "test-b"},
						},
					},
					{
						BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
							BuildID:    "c",
							Status:     temporaliov1alpha1.VersionStatusDraining,
							Deployment: &corev1.ObjectReference{Name: "test-c"},
						},
					},
					{
						BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
							BuildID:    "d",
							Status:     temporaliov1alpha1.VersionStatusDrained,
							Deployment: &corev1.ObjectReference{Name: "test-d"},
						},
						DrainedSince: &metav1.Time{
							Time: time.Now().Add(-2 * time.Hour), // Recently drained
						},
					},
					{
						BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
							BuildID:    "e",
							Status:     temporaliov1alpha1.VersionStatusDrained,
							Deployment: &corev1.ObjectReference{Name: "test-e"},
						},
						DrainedSince: &metav1.Time{
							Time: time.Now().Add(-48 * time.Hour), // Long time drained
						},
					},
				},
			},
			spec: &temporaliov1alpha1.TemporalWorkerDeploymentSpec{
				SunsetStrategy: temporaliov1alpha1.SunsetStrategy{
					ScaledownDelay: &metav1.Duration{Duration: 1 * time.Hour},
					DeleteDelay:    &metav1.Duration{Duration: 24 * time.Hour},
				},
				Replicas: func() *int32 { r := int32(5); return &r }(),
			},
			expectDeletes:  1, // Only e should be deleted (drained long enough and scaled to 0)
			expectScales:   2, // b needs scaling up, d needs scaling down
			expectVersions: []string{"b", "d"},
		},
		{
			name: "draining version not scaled down before delay",
			k8sState: &k8s.DeploymentState{
				Deployments: map[string]*appsv1.Deployment{
					"a": createDeploymentWithDefaultConnectionSpecHash(3),
				},
			},
			config: &Config{
				RolloutStrategy: temporaliov1alpha1.RolloutStrategy{},
			},
			status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
				TargetVersion: temporaliov1alpha1.TargetWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						BuildID:    "a",
						Status:     temporaliov1alpha1.VersionStatusInactive,
						Deployment: &corev1.ObjectReference{Name: "test-a"},
					},
				},
				DeprecatedVersions: []*temporaliov1alpha1.DeprecatedWorkerDeploymentVersion{
					{
						BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
							BuildID:    "a",
							Status:     temporaliov1alpha1.VersionStatusDrained,
							Deployment: &corev1.ObjectReference{Name: "test-a"},
						},
						DrainedSince: &metav1.Time{
							Time: time.Now().Add(-30 * time.Minute), // Not long enough
						},
					},
				},
			},
			spec: &temporaliov1alpha1.TemporalWorkerDeploymentSpec{
				SunsetStrategy: temporaliov1alpha1.SunsetStrategy{
					ScaledownDelay: &metav1.Duration{Duration: 2 * time.Hour},
					DeleteDelay:    &metav1.Duration{Duration: 24 * time.Hour},
				},
				Replicas: func() *int32 { r := int32(3); return &r }(),
			},
			expectDeletes: 0,
			expectScales:  0,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := GeneratePlan(logr.Discard(), tc.k8sState, tc.status, tc.spec, tc.state, createDefaultConnectionSpec(), tc.config, "test/namespace", defaults.MaxVersionsIneligibleForDeletion)
			require.NoError(t, err)

			assert.Equal(t, tc.expectDeletes, len(plan.DeleteDeployments), "unexpected number of deletes")
			assert.Equal(t, tc.expectScales, len(plan.ScaleDeployments), "unexpected number of scales")

			if tc.expectVersions != nil {
				scaledVersions := make([]string, 0, len(plan.ScaleDeployments))
				for ref := range plan.ScaleDeployments {
					// Extract build ID by looking up in status
					for _, v := range tc.status.DeprecatedVersions {
						if v.Deployment != nil && v.Deployment.Name == ref.Name {
							scaledVersions = append(scaledVersions, v.BuildID)
							break
						}
					}
					if tc.status.CurrentVersion != nil &&
						tc.status.CurrentVersion.Deployment != nil &&
						tc.status.CurrentVersion.Deployment.Name == ref.Name {
						scaledVersions = append(scaledVersions, tc.status.CurrentVersion.BuildID)
					}
				}
				// Sort for consistent comparison
				assert.ElementsMatch(t, tc.expectVersions, scaledVersions, "unexpected versions being scaled")
			}
		})
	}
}

func TestGetTestWorkflowID(t *testing.T) {
	testCases := []struct {
		name           string
		deploymentName string
		buildID        string
		taskQueue      string
		expectID       string
	}{
		{
			name:           "basic workflow ID generation",
			deploymentName: "test/namespace",
			buildID:        "123",
			taskQueue:      "my-queue",
			expectID:       "test-test/namespace:123-my-queue",
		},
		{
			name:           "workflow ID with special characters in queue name",
			deploymentName: "test/namespace",
			buildID:        "456",
			taskQueue:      "queue-with-dashes-and_underscores",
			expectID:       "test-test/namespace:456-queue-with-dashes-and_underscores",
		},
		{
			name:           "workflow ID with dots in version",
			deploymentName: "test/namespace",
			buildID:        "1.2.3",
			taskQueue:      "queue",
			expectID:       "test-test/namespace:1.2.3-queue",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			id := temporal.GetTestWorkflowID(tc.deploymentName, tc.buildID, tc.taskQueue)
			assert.Equal(t, tc.expectID, id, "unexpected workflow ID")
		})
	}
}

func TestCheckAndUpdateDeploymentConnectionSpec(t *testing.T) {
	tests := []struct {
		name                 string
		buildID              string
		existingDeployment   *appsv1.Deployment
		newConnection        temporaliov1alpha1.TemporalConnectionSpec
		expectUpdate         bool
		expectSecretName     string
		expectHostPortEnv    string
		expectConnectionHash string
	}{
		{
			name:               "non-existing deployment does not result in an update",
			buildID:            "non-existent",
			existingDeployment: nil,
			newConnection: temporaliov1alpha1.TemporalConnectionSpec{
				HostPort:        "new-host:7233",
				MutualTLSSecret: "new-secret",
			},
			expectUpdate: false,
		},
		{
			name:    "same connection spec hash does not update the existing deployment",
			buildID: "v1",
			existingDeployment: createTestDeploymentWithConnection(
				"test-worker", "v1",
				temporaliov1alpha1.TemporalConnectionSpec{
					HostPort:        defaultHostPort(),
					MutualTLSSecret: defaultMutualTLSSecret(),
				},
			),
			newConnection: temporaliov1alpha1.TemporalConnectionSpec{
				HostPort:        defaultHostPort(),
				MutualTLSSecret: defaultMutualTLSSecret(),
			},
			expectUpdate: false,
		},
		{
			name:    "different secret name triggers update",
			buildID: "v2",
			existingDeployment: createTestDeploymentWithConnection(
				"test-worker", "v2",
				temporaliov1alpha1.TemporalConnectionSpec{
					HostPort:        defaultHostPort(),
					MutualTLSSecret: defaultMutualTLSSecret(),
				},
			),
			newConnection: temporaliov1alpha1.TemporalConnectionSpec{
				HostPort:        defaultHostPort(),
				MutualTLSSecret: "new-secret",
			},
			expectUpdate:      true,
			expectSecretName:  "new-secret",
			expectHostPortEnv: defaultHostPort(),
			expectConnectionHash: k8s.ComputeConnectionSpecHash(temporaliov1alpha1.TemporalConnectionSpec{
				HostPort:        defaultHostPort(),
				MutualTLSSecret: "new-secret",
			}),
		},
		{
			name:    "different host port triggers update",
			buildID: "v3",
			existingDeployment: createTestDeploymentWithConnection(
				"test-worker", "v3",
				temporaliov1alpha1.TemporalConnectionSpec{
					HostPort:        defaultHostPort(),
					MutualTLSSecret: defaultMutualTLSSecret(),
				},
			),
			newConnection: temporaliov1alpha1.TemporalConnectionSpec{
				HostPort:        "new-host:7233",
				MutualTLSSecret: defaultMutualTLSSecret(),
			},
			expectUpdate:      true,
			expectSecretName:  defaultMutualTLSSecret(),
			expectHostPortEnv: "new-host:7233",
			expectConnectionHash: k8s.ComputeConnectionSpecHash(temporaliov1alpha1.TemporalConnectionSpec{
				HostPort:        "new-host:7233",
				MutualTLSSecret: defaultMutualTLSSecret(),
			}),
		},
		{
			name:    "both hostport and secret change triggers update",
			buildID: "v4",
			existingDeployment: createTestDeploymentWithConnection(
				"test-worker", "v4",
				temporaliov1alpha1.TemporalConnectionSpec{
					HostPort:        defaultHostPort(),
					MutualTLSSecret: defaultMutualTLSSecret(),
				},
			),
			newConnection: temporaliov1alpha1.TemporalConnectionSpec{
				HostPort:        "new-host:7233",
				MutualTLSSecret: "new-secret",
			},
			expectUpdate:      true,
			expectSecretName:  "new-secret",
			expectHostPortEnv: "new-host:7233",
			expectConnectionHash: k8s.ComputeConnectionSpecHash(temporaliov1alpha1.TemporalConnectionSpec{
				HostPort:        "new-host:7233",
				MutualTLSSecret: "new-secret",
			}),
		},
		{
			name:    "empty mutual tls secret updates correctly",
			buildID: "v5",
			existingDeployment: createTestDeploymentWithConnection(
				"test-worker", "v5",
				temporaliov1alpha1.TemporalConnectionSpec{
					HostPort:        defaultHostPort(),
					MutualTLSSecret: defaultMutualTLSSecret(),
				},
			),
			newConnection: temporaliov1alpha1.TemporalConnectionSpec{
				HostPort:        defaultHostPort(),
				MutualTLSSecret: "",
			},
			expectUpdate:      true,
			expectSecretName:  "", // Should not update secret volume when empty
			expectHostPortEnv: defaultHostPort(),
			expectConnectionHash: k8s.ComputeConnectionSpecHash(temporaliov1alpha1.TemporalConnectionSpec{
				HostPort:        defaultHostPort(),
				MutualTLSSecret: "",
			}),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			k8sState := &k8s.DeploymentState{
				Deployments: map[string]*appsv1.Deployment{},
			}

			buildID := tt.buildID
			if tt.existingDeployment != nil {
				k8sState.Deployments[buildID] = tt.existingDeployment
			}

			result := checkAndUpdateDeploymentConnectionSpec(buildID, k8sState, tt.newConnection)

			if !tt.expectUpdate {
				assert.Nil(t, result, "Expected no update, but got deployment")
				return
			}

			require.NotNil(t, result, "Expected deployment update, but got nil")

			// Check that the connection hash annotation was updated
			actualHash := result.Spec.Template.Annotations[k8s.ConnectionSpecHashAnnotation]
			assert.Equal(t, tt.expectConnectionHash, actualHash, "Connection spec hash should be updated")

			// Check secret volume update (only if mTLS secret is not empty)
			if tt.newConnection.MutualTLSSecret != "" {
				found := false
				for _, volume := range result.Spec.Template.Spec.Volumes {
					if volume.Name == "temporal-tls" && volume.Secret != nil {
						assert.Equal(t, tt.expectSecretName, volume.Secret.SecretName, "Secret name should be updated")
						found = true
						break
					}
				}
				assert.True(t, found, "Should find temporal-tls volume with updated secret")
			}

			// Check environment variable update
			found := false
			for _, container := range result.Spec.Template.Spec.Containers {
				for _, env := range container.Env {
					if env.Name == "TEMPORAL_ADDRESS" {
						assert.Equal(t, tt.expectHostPortEnv, env.Value, "TEMPORAL_ADDRESS should be updated")
						found = true
						break
					}
				}
			}
			assert.True(t, found, "Should find TEMPORAL_ADDRESS environment variable")
		})
	}
}

// Helper function to create a deployment with the specified replicas and the default connection spec hash
func createDeploymentWithDefaultConnectionSpecHash(replicas int32) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-deployment",
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						k8s.ConnectionSpecHashAnnotation: k8s.ComputeConnectionSpecHash(createDefaultConnectionSpec()),
					},
				},
			},
		},
	}
}

// Helper function to create a deployment with the specified replicas and with a non-default connection spec hash
func createDeploymentWithExpiredConnectionSpecHash(replicas int32) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-deployment",
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						k8s.ConnectionSpecHashAnnotation: k8s.ComputeConnectionSpecHash(createOutdatedConnectionSpec()),
					},
				},
			},
		},
	}
}

func float32Ptr(v float32) *float32 {
	return &v
}

func metav1Duration(d time.Duration) metav1.Duration {
	return metav1.Duration{Duration: d}
}

func rolloutStep(ramp float32, d time.Duration) temporaliov1alpha1.RolloutStep {
	return temporaliov1alpha1.RolloutStep{
		RampPercentage: ramp,
		PauseDuration:  metav1Duration(d),
	}
}

// Helper function to create a default TemporalConnectionSpec used for testing purposes
func createDefaultConnectionSpec() temporaliov1alpha1.TemporalConnectionSpec {
	return temporaliov1alpha1.TemporalConnectionSpec{
		HostPort:        defaultHostPort(),
		MutualTLSSecret: defaultMutualTLSSecret(),
	}
}

// Helper function to create a TemporalConnectionSpec with an outdated secret
func createOutdatedConnectionSpec() temporaliov1alpha1.TemporalConnectionSpec {
	return temporaliov1alpha1.TemporalConnectionSpec{
		HostPort:        defaultHostPort(),
		MutualTLSSecret: "outdated-secret",
	}
}

func defaultHostPort() string {
	return "default-host:7233"
}

func defaultMutualTLSSecret() string {
	return "default-secret"
}

// createDefaultWorkerSpec creates a default TemporalWorkerDeploymentSpec for testing
func createDefaultWorkerSpec() *temporaliov1alpha1.TemporalWorkerDeploymentSpec {
	return &temporaliov1alpha1.TemporalWorkerDeploymentSpec{
		Template: corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name:  "worker",
						Image: "test-image:latest",
					},
				},
			},
		},
		WorkerOptions: temporaliov1alpha1.WorkerOptions{
			TemporalNamespace: "test-namespace",
		},
	}
}

// createTestDeploymentWithConnection creates a test deployment with the specified connection spec
func createTestDeploymentWithConnection(deploymentName, buildID string, connection temporaliov1alpha1.TemporalConnectionSpec) *appsv1.Deployment {
	return k8s.NewDeploymentWithOwnerRef(
		&metav1.TypeMeta{},
		&metav1.ObjectMeta{Name: "test-worker", Namespace: "default"},
		createDefaultWorkerSpec(),
		deploymentName,
		buildID,
		connection,
	)
}
