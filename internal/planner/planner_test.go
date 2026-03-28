// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package planner

import (
	"context"
	"encoding/json"
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
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
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
		expectConfigSetCurrent           *bool   // pointer so we can test nil
		expectConfigRampPercent          *int32  // pointer so we can test nil, in percentage (0-100)
		expectManagerIdentity            *string // pointer so nil means "don't assert"
		maxVersionsIneligibleForDeletion *int32  // set by env if non-nil, else default 75
		wrts                             []temporaliov1alpha1.WorkerResourceTemplate
		expectWorkerResourceApplies      int
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
			expectConfig:            true,                                         // Should generate config to reset ramp
			expectConfigSetCurrent:  func() *bool { b := false; return &b }(),     // Should NOT set current (already current)
			expectConfigRampPercent: func() *int32 { f := int32(0); return &f }(), // Should reset ramp to 0
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
		{
			name: "one WRT with two deployments produces two worker resource applies",
			k8sState: &k8s.DeploymentState{
				Deployments: map[string]*appsv1.Deployment{
					"build-a": createDeploymentWithUID("worker-build-a", "uid-a"),
					"build-b": createDeploymentWithUID("worker-build-b", "uid-b"),
				},
				DeploymentsByTime: []*appsv1.Deployment{},
				DeploymentRefs:    map[string]*corev1.ObjectReference{},
			},
			status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
				TargetVersion: temporaliov1alpha1.TargetWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						BuildID:    "build-a",
						Status:     temporaliov1alpha1.VersionStatusCurrent,
						Deployment: &corev1.ObjectReference{Name: "worker-build-a"},
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
			wrts: []temporaliov1alpha1.WorkerResourceTemplate{
				createTestWRT("my-hpa", "my-worker"),
			},
			expectScale:                 1,
			expectWorkerResourceApplies: 2,
		},
		{
			name: "no WRTs produces no worker resource applies",
			k8sState: &k8s.DeploymentState{
				Deployments: map[string]*appsv1.Deployment{
					"build-a": createDeploymentWithUID("worker-build-a", "uid-a"),
				},
				DeploymentsByTime: []*appsv1.Deployment{},
				DeploymentRefs:    map[string]*corev1.ObjectReference{},
			},
			status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
				TargetVersion: temporaliov1alpha1.TargetWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						BuildID:    "build-a",
						Status:     temporaliov1alpha1.VersionStatusCurrent,
						Deployment: &corev1.ObjectReference{Name: "worker-build-a"},
					},
				},
			},
			spec: &temporaliov1alpha1.TemporalWorkerDeploymentSpec{
				Replicas: func() *int32 { r := int32(1); return &r }(),
			},
			state:                       &temporal.TemporalWorkerState{},
			config:                      &Config{RolloutStrategy: temporaliov1alpha1.RolloutStrategy{}},
			wrts:                        nil,
			expectScale:                 1,
			expectWorkerResourceApplies: 0,
		},
		{
			// VersionConfig is non-nil because a new healthy target needs to be promoted.
			// With empty ManagerIdentity the controller should claim before promoting.
			name: "claims manager identity when ManagerIdentity is empty and a version config change is pending",
			k8sState: &k8s.DeploymentState{
				Deployments: map[string]*appsv1.Deployment{
					"oldbuild": createDeploymentWithDefaultConnectionSpecHash(1),
					"newbuild": createDeploymentWithDefaultConnectionSpecHash(1),
				},
				DeploymentRefs: map[string]*corev1.ObjectReference{
					"oldbuild": {Name: "test-oldbuild"},
					"newbuild": {Name: "test-newbuild"},
				},
			},
			status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
				TargetVersion: temporaliov1alpha1.TargetWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						BuildID:    "newbuild",
						Status:     temporaliov1alpha1.VersionStatusInactive,
						Deployment: &corev1.ObjectReference{Name: "test-newbuild"},
						HealthySince: &metav1.Time{
							Time: time.Now().Add(-1 * time.Hour),
						},
					},
				},
				CurrentVersion: &temporaliov1alpha1.CurrentWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						BuildID:    "oldbuild",
						Status:     temporaliov1alpha1.VersionStatusCurrent,
						Deployment: &corev1.ObjectReference{Name: "test-oldbuild"},
					},
				},
			},
			spec: &temporaliov1alpha1.TemporalWorkerDeploymentSpec{
				Replicas: func() *int32 { r := int32(1); return &r }(),
			},
			state: &temporal.TemporalWorkerState{
				ManagerIdentity: "", // empty → controller should claim
			},
			config: &Config{
				RolloutStrategy: temporaliov1alpha1.RolloutStrategy{
					Strategy: temporaliov1alpha1.UpdateAllAtOnce,
				},
			},
			expectConfig:           true,
			expectConfigSetCurrent: func() *bool { b := true; return &b }(),
			expectManagerIdentity:  func() *string { s := ""; return &s }(),
		},
		{
			// Same routing change scenario but ManagerIdentity is already set — no claim.
			name: "does not claim manager identity when ManagerIdentity is already set",
			k8sState: &k8s.DeploymentState{
				Deployments: map[string]*appsv1.Deployment{
					"oldbuild": createDeploymentWithDefaultConnectionSpecHash(1),
					"newbuild": createDeploymentWithDefaultConnectionSpecHash(1),
				},
				DeploymentRefs: map[string]*corev1.ObjectReference{
					"oldbuild": {Name: "test-oldbuild"},
					"newbuild": {Name: "test-newbuild"},
				},
			},
			status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
				TargetVersion: temporaliov1alpha1.TargetWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						BuildID:    "newbuild",
						Status:     temporaliov1alpha1.VersionStatusInactive,
						Deployment: &corev1.ObjectReference{Name: "test-newbuild"},
						HealthySince: &metav1.Time{
							Time: time.Now().Add(-1 * time.Hour),
						},
					},
				},
				CurrentVersion: &temporaliov1alpha1.CurrentWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						BuildID:    "oldbuild",
						Status:     temporaliov1alpha1.VersionStatusCurrent,
						Deployment: &corev1.ObjectReference{Name: "test-oldbuild"},
					},
				},
			},
			spec: &temporaliov1alpha1.TemporalWorkerDeploymentSpec{
				Replicas: func() *int32 { r := int32(1); return &r }(),
			},
			state: &temporal.TemporalWorkerState{
				ManagerIdentity: "some-other-client",
			},
			config: &Config{
				RolloutStrategy: temporaliov1alpha1.RolloutStrategy{
					Strategy: temporaliov1alpha1.UpdateAllAtOnce,
				},
			},
			expectConfig:           true,
			expectConfigSetCurrent: func() *bool { b := true; return &b }(),
			expectManagerIdentity:  func() *string { s := "some-other-client"; return &s }(),
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

			plan, err := GeneratePlan(logr.Discard(), tc.k8sState, tc.status, tc.spec, tc.state, createDefaultConnectionSpec(), tc.config, "test/namespace", maxV, nil, false, tc.wrts, "test-twd", types.UID("test-twd-uid"))
			require.NoError(t, err)

			assert.Equal(t, tc.expectDelete, len(plan.DeleteDeployments), "unexpected number of deletions")
			assert.Equal(t, tc.expectScale, len(plan.ScaleDeployments), "unexpected number of scales")
			assert.Equal(t, tc.expectCreate, plan.ShouldCreateDeployment, "unexpected create flag")
			assert.Equal(t, tc.expectUpdate, len(plan.UpdateDeployments), "unexpected number of updates")
			assert.Equal(t, tc.expectWorkflow, len(plan.TestWorkflows), "unexpected number of test workflows")
			assert.Equal(t, tc.expectConfig, plan.VersionConfig != nil, "unexpected version config presence")
			assert.Equal(t, tc.expectWorkerResourceApplies, len(plan.ApplyWorkerResources), "unexpected number of worker resource applies")
			if tc.expectManagerIdentity != nil {
				require.NotNil(t, plan.VersionConfig, "expected VersionConfig to be non-nil when asserting ManagerIdentity")
				assert.Equal(t, *tc.expectManagerIdentity, plan.VersionConfig.ManagerIdentity, "unexpected ManagerIdentity")
			}

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
		name                      string
		k8sState                  *k8s.DeploymentState
		status                    *temporaliov1alpha1.TemporalWorkerDeploymentStatus
		spec                      *temporaliov1alpha1.TemporalWorkerDeploymentSpec
		config                    *Config
		expectDeletes             int
		foundDeploymentInTemporal bool
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
			expectDeletes:             1,
			foundDeploymentInTemporal: true,
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
			expectDeletes:             0,
			foundDeploymentInTemporal: true,
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
			expectDeletes:             1,
			foundDeploymentInTemporal: true,
		},
		{
			name: "not registered version should NOT be deleted, deployment not found in temporal",
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
			expectDeletes:             0,
			foundDeploymentInTemporal: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.spec.Default(context.Background())
			require.NoError(t, err)
			deletes := getDeleteDeployments(tc.k8sState, tc.status, tc.spec, tc.foundDeploymentInTemporal)
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
			name: "HPA mode: rolled-back target was drained to zero, scale it back to 1",
			k8sState: &k8s.DeploymentState{
				Deployments: map[string]*appsv1.Deployment{
					// The rolled-back target: exists, Spec.Replicas was explicitly set to 0 by drain
					"old": createDeploymentWithDefaultConnectionSpecHash(0),
				},
				DeploymentRefs: map[string]*corev1.ObjectReference{
					"old": {Name: "test-old"},
				},
			},
			status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
				TargetVersion: temporaliov1alpha1.TargetWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						BuildID:    "old",
						Status:     temporaliov1alpha1.VersionStatusInactive,
						Deployment: &corev1.ObjectReference{Name: "test-old"},
					},
				},
				CurrentVersion: &temporaliov1alpha1.CurrentWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						BuildID:    "new",
						Status:     temporaliov1alpha1.VersionStatusCurrent,
						Deployment: &corev1.ObjectReference{Name: "test-new"},
					},
				},
			},
			// spec.Replicas == nil means HPA controls replicas
			spec:         &temporaliov1alpha1.TemporalWorkerDeploymentSpec{},
			expectScales: map[string]uint32{"test-old": 1},
		},
		{
			name: "HPA mode: target deployment is brand-new (Spec.Replicas nil), no explicit scale needed",
			k8sState: &k8s.DeploymentState{
				Deployments: map[string]*appsv1.Deployment{
					// New deployment created with nil replicas — Deployment controller defaults to 1.
					// Spec.Replicas is nil, so the condition d.Spec.Replicas != nil && *d.Spec.Replicas == 0 is false.
					"new": {
						ObjectMeta: metav1.ObjectMeta{Name: "test-new"},
						Spec:       appsv1.DeploymentSpec{Replicas: nil},
					},
				},
				DeploymentRefs: map[string]*corev1.ObjectReference{
					"new": {Name: "test-new"},
				},
			},
			status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
				TargetVersion: temporaliov1alpha1.TargetWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						BuildID:    "new",
						Status:     temporaliov1alpha1.VersionStatusInactive,
						Deployment: &corev1.ObjectReference{Name: "test-new"},
					},
				},
			},
			spec:         &temporaliov1alpha1.TemporalWorkerDeploymentSpec{},
			expectScales: map[string]uint32{},
		},
		{
			name: "HPA mode: target has positive Spec.Replicas already (HPA managing), no explicit scale",
			k8sState: &k8s.DeploymentState{
				Deployments: map[string]*appsv1.Deployment{
					"old": createDeploymentWithDefaultConnectionSpecHash(3),
				},
				DeploymentRefs: map[string]*corev1.ObjectReference{
					"old": {Name: "test-old"},
				},
			},
			status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
				TargetVersion: temporaliov1alpha1.TargetWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						BuildID:    "old",
						Status:     temporaliov1alpha1.VersionStatusInactive,
						Deployment: &corev1.ObjectReference{Name: "test-old"},
					},
				},
				CurrentVersion: &temporaliov1alpha1.CurrentWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						BuildID:    "new",
						Status:     temporaliov1alpha1.VersionStatusCurrent,
						Deployment: &corev1.ObjectReference{Name: "test-new"},
					},
				},
			},
			spec:         &temporaliov1alpha1.TemporalWorkerDeploymentSpec{},
			expectScales: map[string]uint32{},
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

// TestUpdateDeploymentInPlace_ReplicasNil verifies that updateDeploymentInPlace does not
// overwrite spec.replicas when spec.Replicas is nil (external autoscaler mode).
// Patching spec.replicas to nil would be a no-op anyway (Deployment defaulter resets nil→1),
// so the controller must simply stop writing the field when it doesn't own replicas.
func TestUpdateDeploymentWithPodTemplateSpec_ReplicasNilPreserved(t *testing.T) {
	five := int32(5)
	dep := &appsv1.Deployment{
		Spec: appsv1.DeploymentSpec{
			Replicas: &five, // HPA previously set to 5
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{}},
		},
	}
	spec := &temporaliov1alpha1.TemporalWorkerDeploymentSpec{} // spec.Replicas == nil
	updateDeploymentWithPodTemplateSpec(dep, spec, temporaliov1alpha1.TemporalConnectionSpec{})
	require.NotNil(t, dep.Spec.Replicas)
	assert.Equal(t, int32(5), *dep.Spec.Replicas, "replicas must be preserved when spec.Replicas is nil")
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
			workflows := getTestWorkflows(tc.status, tc.config, "test/namespace", nil, false)
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
		expectRampPercent *int32
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
			expectRampPercent: int32Ptr(25),
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
			expectRampPercent: func() *int32 { f := int32(0); return &f }(),
		},
		{
			name: "gate configured with no current version should set current immediately",
			strategy: temporaliov1alpha1.RolloutStrategy{
				Strategy: temporaliov1alpha1.UpdateProgressive,
				Gate:     &temporaliov1alpha1.GateWorkflowConfig{WorkflowType: "TestWorkflow"},
				Steps: []temporaliov1alpha1.RolloutStep{
					{RampPercentage: 1, PauseDuration: metav1Duration(30 * time.Second)},
				},
			},
			status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
				TargetVersion: temporaliov1alpha1.TargetWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						BuildID:      "123",
						Status:       temporaliov1alpha1.VersionStatusInactive,
						HealthySince: &metav1.Time{Time: time.Now()},
						TaskQueues: []temporaliov1alpha1.TaskQueue{
							{Name: "queue1"},
						},
					},
					TestWorkflows: []temporaliov1alpha1.WorkflowExecution{
						{
							TaskQueue: "queue1",
							Status:    temporaliov1alpha1.WorkflowExecutionStatusCompleted,
						},
					},
				},
				CurrentVersion: nil,
			},
			state: &temporal.TemporalWorkerState{
				Versions: map[string]*temporal.VersionInfo{
					"123": {BuildID: "123", AllTaskQueuesHaveUnversionedPoller: false},
				},
			},
			expectConfig:     true,
			expectSetCurrent: true,
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
		expectRampPercent int32
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
					RampPercentage: float32Ptr(75.0),
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
					RampPercentage: float32Ptr(25.0),
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
					RampPercentage: float32Ptr(50.0),
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
					RampPercentage:     float32Ptr(10.0),
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
		initialRamp           *int32
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
			initialRamp:           int32Ptr(20),
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
			initialRamp:           int32Ptr(1),
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
				ramp := float32(*tc.initialRamp) // Already in percentage
				currentRampPercentage = &ramp
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
					diffs = append(diffs, float32(config.RampPercentage))
				}

				// Set ramp value and last modified time if it was updated (simulates what Temporal server would return on next reconcile loop)
				if float32(config.RampPercentage) != 0 {
					rampLastModified = &currentTime
					rampPercentage := float32(config.RampPercentage)
					currentRampPercentage = &rampPercentage
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
			plan, err := GeneratePlan(logr.Discard(), tc.k8sState, tc.status, tc.spec, tc.state, createDefaultConnectionSpec(), tc.config, "test/namespace", defaults.MaxVersionsIneligibleForDeletion, nil, false, nil, "test-twd", types.UID("test-twd-uid"))
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
				HostPort:           "new-host:7233",
				MutualTLSSecretRef: &temporaliov1alpha1.SecretReference{Name: "new-secret"},
			},
			expectUpdate: false,
		},
		{
			name:    "same connection spec hash does not update the existing deployment",
			buildID: "v1",
			existingDeployment: createTestDeploymentWithConnection(
				"test-worker", "v1",
				temporaliov1alpha1.TemporalConnectionSpec{
					HostPort:           defaultHostPort(),
					MutualTLSSecretRef: &temporaliov1alpha1.SecretReference{Name: defaultMutualTLSSecret()},
				},
			),
			newConnection: temporaliov1alpha1.TemporalConnectionSpec{
				HostPort:           defaultHostPort(),
				MutualTLSSecretRef: &temporaliov1alpha1.SecretReference{Name: defaultMutualTLSSecret()},
			},
			expectUpdate: false,
		},
		{
			name:    "different secret name triggers update",
			buildID: "v2",
			existingDeployment: createTestDeploymentWithConnection(
				"test-worker", "v2",
				temporaliov1alpha1.TemporalConnectionSpec{
					HostPort:           defaultHostPort(),
					MutualTLSSecretRef: &temporaliov1alpha1.SecretReference{Name: defaultMutualTLSSecret()},
				},
			),
			newConnection: temporaliov1alpha1.TemporalConnectionSpec{
				HostPort:           defaultHostPort(),
				MutualTLSSecretRef: &temporaliov1alpha1.SecretReference{Name: "new-secret"},
			},
			expectUpdate:      true,
			expectSecretName:  "new-secret",
			expectHostPortEnv: defaultHostPort(),
			expectConnectionHash: k8s.ComputeConnectionSpecHash(temporaliov1alpha1.TemporalConnectionSpec{
				HostPort:           defaultHostPort(),
				MutualTLSSecretRef: &temporaliov1alpha1.SecretReference{Name: "new-secret"},
			}),
		},
		{
			name:    "different host port triggers update",
			buildID: "v3",
			existingDeployment: createTestDeploymentWithConnection(
				"test-worker", "v3",
				temporaliov1alpha1.TemporalConnectionSpec{
					HostPort:           defaultHostPort(),
					MutualTLSSecretRef: &temporaliov1alpha1.SecretReference{Name: defaultMutualTLSSecret()},
				},
			),
			newConnection: temporaliov1alpha1.TemporalConnectionSpec{
				HostPort:           "new-host:7233",
				MutualTLSSecretRef: &temporaliov1alpha1.SecretReference{Name: defaultMutualTLSSecret()},
			},
			expectUpdate:      true,
			expectSecretName:  defaultMutualTLSSecret(),
			expectHostPortEnv: "new-host:7233",
			expectConnectionHash: k8s.ComputeConnectionSpecHash(temporaliov1alpha1.TemporalConnectionSpec{
				HostPort:           "new-host:7233",
				MutualTLSSecretRef: &temporaliov1alpha1.SecretReference{Name: defaultMutualTLSSecret()},
			}),
		},
		{
			name:    "both hostport and secret change triggers update",
			buildID: "v4",
			existingDeployment: createTestDeploymentWithConnection(
				"test-worker", "v4",
				temporaliov1alpha1.TemporalConnectionSpec{
					HostPort:           defaultHostPort(),
					MutualTLSSecretRef: &temporaliov1alpha1.SecretReference{Name: defaultMutualTLSSecret()},
				},
			),
			newConnection: temporaliov1alpha1.TemporalConnectionSpec{
				HostPort:           "new-host:7233",
				MutualTLSSecretRef: &temporaliov1alpha1.SecretReference{Name: "new-secret"},
			},
			expectUpdate:      true,
			expectSecretName:  "new-secret",
			expectHostPortEnv: "new-host:7233",
			expectConnectionHash: k8s.ComputeConnectionSpecHash(temporaliov1alpha1.TemporalConnectionSpec{
				HostPort:           "new-host:7233",
				MutualTLSSecretRef: &temporaliov1alpha1.SecretReference{Name: "new-secret"},
			}),
		},
		{
			name:    "empty mutual tls secret updates correctly",
			buildID: "v5",
			existingDeployment: createTestDeploymentWithConnection(
				"test-worker", "v5",
				temporaliov1alpha1.TemporalConnectionSpec{
					HostPort:           defaultHostPort(),
					MutualTLSSecretRef: &temporaliov1alpha1.SecretReference{Name: defaultMutualTLSSecret()},
				},
			),
			newConnection: temporaliov1alpha1.TemporalConnectionSpec{
				HostPort:           defaultHostPort(),
				MutualTLSSecretRef: &temporaliov1alpha1.SecretReference{Name: ""},
			},
			expectUpdate:      true,
			expectSecretName:  "", // Should not update secret volume when empty
			expectHostPortEnv: defaultHostPort(),
			expectConnectionHash: k8s.ComputeConnectionSpecHash(temporaliov1alpha1.TemporalConnectionSpec{
				HostPort:           defaultHostPort(),
				MutualTLSSecretRef: &temporaliov1alpha1.SecretReference{Name: ""},
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
			if tt.newConnection.MutualTLSSecretRef.Name != "" {
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

func TestCheckAndUpdateDeploymentPodTemplateSpec(t *testing.T) {
	tests := []struct {
		name               string
		buildID            string
		existingDeployment *appsv1.Deployment
		newSpec            *temporaliov1alpha1.TemporalWorkerDeploymentSpec
		connection         temporaliov1alpha1.TemporalConnectionSpec
		expectUpdate       bool
		expectImage        string
	}{
		{
			name:               "non-existing deployment does not result in an update",
			buildID:            "non-existent",
			existingDeployment: nil,
			newSpec:            createWorkerSpecWithBuildID("stable-build-id"),
			connection:         createDefaultConnectionSpec(),
			expectUpdate:       false,
		},
		{
			name:               "no update when buildID is not explicitly set (auto-generated buildID)",
			buildID:            "v1",
			existingDeployment: createDeploymentForDriftTest(1, "v1", "old-image:v1"),
			newSpec: &temporaliov1alpha1.TemporalWorkerDeploymentSpec{
				Replicas: int32Ptr(1),
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{Name: "worker", Image: "new-image:v2"},
						},
					},
				},
				WorkerOptions: temporaliov1alpha1.WorkerOptions{
					TemporalNamespace: "test-namespace",
					// BuildID not set - means auto-generated
				},
			},
			connection:   createDefaultConnectionSpec(),
			expectUpdate: false, // No update because BuildID is not explicitly set
		},
		{
			name:               "same image does not trigger update when buildID is set",
			buildID:            "stable-build-id",
			existingDeployment: createDeploymentForDriftTest(1, "stable-build-id", "my-image:v1"),
			newSpec: &temporaliov1alpha1.TemporalWorkerDeploymentSpec{
				Replicas: int32Ptr(1),
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{Name: "worker", Image: "my-image:v1"},
						},
					},
				},
				WorkerOptions: temporaliov1alpha1.WorkerOptions{
					TemporalNamespace:   "test-namespace",
					UnsafeCustomBuildID: "stable-build-id",
				},
			},
			connection:   createDefaultConnectionSpec(),
			expectUpdate: false, // No update because image is the same
		},
		{
			name:               "different image triggers update when buildID is set",
			buildID:            "stable-build-id",
			existingDeployment: createDeploymentForDriftTest(1, "stable-build-id", "old-image:v1"),
			newSpec: &temporaliov1alpha1.TemporalWorkerDeploymentSpec{
				Replicas: int32Ptr(1),
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{Name: "worker", Image: "new-image:v2"},
						},
					},
				},
				WorkerOptions: temporaliov1alpha1.WorkerOptions{
					TemporalNamespace:   "test-namespace",
					UnsafeCustomBuildID: "stable-build-id",
				},
			},
			connection:   createDefaultConnectionSpec(),
			expectUpdate: true,
			expectImage:  "new-image:v2",
		},
		{
			name:               "replicas-only change does not trigger update (handled by scaling logic)",
			buildID:            "stable-build-id",
			existingDeployment: createDeploymentForDriftTest(1, "stable-build-id", "my-image:v1"),
			newSpec: &temporaliov1alpha1.TemporalWorkerDeploymentSpec{
				Replicas: int32Ptr(3), // Changed from 1 to 3
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							// Same image as stored - hash will match
							{Name: "worker", Image: "my-image:v1"},
						},
					},
				},
				WorkerOptions: temporaliov1alpha1.WorkerOptions{
					TemporalNamespace:   "test-namespace",
					UnsafeCustomBuildID: "stable-build-id",
				},
			},
			connection: createDefaultConnectionSpec(),
			// Replicas are not part of PodTemplateSpec, so hash won't change.
			// Replicas changes are handled by getScaleDeployments() instead.
			expectUpdate: false,
		},
		{
			name:    "env var change triggers update when buildID is set",
			buildID: "stable-build-id",
			existingDeployment: createDeploymentForDriftTestWithEnv(1, "stable-build-id", "my-image:v1",
				[]corev1.EnvVar{{Name: "MY_VAR", Value: "old-value"}}),
			newSpec: &temporaliov1alpha1.TemporalWorkerDeploymentSpec{
				Replicas: int32Ptr(1),
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:  "worker",
								Image: "my-image:v1",
								Env:   []corev1.EnvVar{{Name: "MY_VAR", Value: "new-value"}},
							},
						},
					},
				},
				WorkerOptions: temporaliov1alpha1.WorkerOptions{
					TemporalNamespace:   "test-namespace",
					UnsafeCustomBuildID: "stable-build-id",
				},
			},
			connection:   createDefaultConnectionSpec(),
			expectUpdate: true,
			expectImage:  "my-image:v1",
		},
		{
			name:               "backwards compat: no hash annotation means no update",
			buildID:            "stable-build-id",
			existingDeployment: createDeploymentWithoutHashAnnotation(1, "stable-build-id", "old-image:v1"),
			newSpec: &temporaliov1alpha1.TemporalWorkerDeploymentSpec{
				Replicas: int32Ptr(1),
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{Name: "worker", Image: "new-image:v2"},
						},
					},
				},
				WorkerOptions: temporaliov1alpha1.WorkerOptions{
					TemporalNamespace:   "test-namespace",
					UnsafeCustomBuildID: "stable-build-id",
				},
			},
			connection: createDefaultConnectionSpec(),
			// Legacy deployment without hash annotation should not trigger update
			// to maintain backwards compatibility
			expectUpdate: false,
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

			result := checkAndUpdateDeploymentPodTemplateSpec(buildID, k8sState, tt.newSpec, tt.connection)

			if !tt.expectUpdate {
				assert.Nil(t, result, "Expected no update, but got deployment")
				return
			}

			require.NotNil(t, result, "Expected deployment update, but got nil")

			// Check container image was updated
			if tt.expectImage != "" {
				require.Len(t, result.Spec.Template.Spec.Containers, 1, "Should have one container")
				assert.Equal(t, tt.expectImage, result.Spec.Template.Spec.Containers[0].Image, "Container image should be updated")
			}

			// Check that controller-injected env vars are present
			found := false
			for _, container := range result.Spec.Template.Spec.Containers {
				for _, env := range container.Env {
					if env.Name == "TEMPORAL_WORKER_BUILD_ID" {
						assert.Equal(t, buildID, env.Value, "TEMPORAL_WORKER_BUILD_ID should be set")
						found = true
						break
					}
				}
			}
			assert.True(t, found, "Should find TEMPORAL_WORKER_BUILD_ID environment variable")
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

// Helper function to create a deployment for drift testing
func createDeploymentForDriftTest(replicas int32, buildID string, image string) *appsv1.Deployment {
	r := replicas
	// Create the user-provided pod template spec (without controller modifications)
	userTemplate := corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:  "worker",
					Image: image,
				},
			},
		},
	}
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-deployment",
			Labels: map[string]string{
				k8s.BuildIDLabel: buildID,
			},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &r,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					k8s.BuildIDLabel: buildID,
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						k8s.ConnectionSpecHashAnnotation:  k8s.ComputeConnectionSpecHash(createDefaultConnectionSpec()),
						k8s.PodTemplateSpecHashAnnotation: k8s.ComputePodTemplateSpecHash(userTemplate),
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "worker",
							Image: image,
							Env: []corev1.EnvVar{
								{Name: "TEMPORAL_DEPLOYMENT_NAME", Value: "test/my-worker"},
							},
						},
					},
				},
			},
		},
	}
}

// createDeploymentForDriftTestWithEnv creates a deployment for drift testing with custom env vars
func createDeploymentForDriftTestWithEnv(replicas int32, buildID string, image string, envVars []corev1.EnvVar) *appsv1.Deployment {
	r := replicas
	// Create the user-provided pod template spec (without controller modifications)
	userTemplate := corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:  "worker",
					Image: image,
					Env:   envVars,
				},
			},
		},
	}
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-deployment",
			Labels: map[string]string{
				k8s.BuildIDLabel: buildID,
			},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &r,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					k8s.BuildIDLabel: buildID,
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						k8s.ConnectionSpecHashAnnotation:  k8s.ComputeConnectionSpecHash(createDefaultConnectionSpec()),
						k8s.PodTemplateSpecHashAnnotation: k8s.ComputePodTemplateSpecHash(userTemplate),
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "worker",
							Image: image,
							Env: append(envVars, corev1.EnvVar{
								Name: "TEMPORAL_DEPLOYMENT_NAME", Value: "test/my-worker",
							}),
						},
					},
				},
			},
		},
	}
}

// createDeploymentWithoutHashAnnotation creates a deployment without the pod template spec hash (for backwards compat testing)
func createDeploymentWithoutHashAnnotation(replicas int32, buildID string, image string) *appsv1.Deployment {
	r := replicas
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-deployment",
			Labels: map[string]string{
				k8s.BuildIDLabel: buildID,
			},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &r,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					k8s.BuildIDLabel: buildID,
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						// Only connection spec hash, no pod template spec hash
						k8s.ConnectionSpecHashAnnotation: k8s.ComputeConnectionSpecHash(createDefaultConnectionSpec()),
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "worker",
							Image: image,
							Env: []corev1.EnvVar{
								{Name: "TEMPORAL_DEPLOYMENT_NAME", Value: "test/my-worker"},
							},
						},
					},
				},
			},
		},
	}
}

func int32Ptr(v int32) *int32 {
	return &v
}

func float32Ptr(v float32) *float32 {
	return &v
}

func metav1Duration(d time.Duration) metav1.Duration {
	return metav1.Duration{Duration: d}
}

func rolloutStep(ramp int32, d time.Duration) temporaliov1alpha1.RolloutStep {
	return temporaliov1alpha1.RolloutStep{
		RampPercentage: int(ramp),
		PauseDuration:  metav1Duration(d),
	}
}

// Helper function to create a default TemporalConnectionSpec used for testing purposes
func createDefaultConnectionSpec() temporaliov1alpha1.TemporalConnectionSpec {
	return temporaliov1alpha1.TemporalConnectionSpec{
		HostPort:           defaultHostPort(),
		MutualTLSSecretRef: &temporaliov1alpha1.SecretReference{Name: defaultMutualTLSSecret()},
	}
}

// Helper function to create a TemporalConnectionSpec with an outdated secret
func createOutdatedConnectionSpec() temporaliov1alpha1.TemporalConnectionSpec {
	return temporaliov1alpha1.TemporalConnectionSpec{
		HostPort:           defaultHostPort(),
		MutualTLSSecretRef: &temporaliov1alpha1.SecretReference{Name: "outdated-secret"},
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

// createWorkerSpecWithBuildID creates a worker spec with an explicit UnsafeCustomBuildID set
func createWorkerSpecWithBuildID(buildID string) *temporaliov1alpha1.TemporalWorkerDeploymentSpec {
	return &temporaliov1alpha1.TemporalWorkerDeploymentSpec{
		Replicas: int32Ptr(1),
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
			TemporalNamespace:   "test-namespace",
			UnsafeCustomBuildID: buildID,
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

func TestResolveGateInput(t *testing.T) {
	testCases := []struct {
		name                string
		gate                *temporaliov1alpha1.GateWorkflowConfig
		namespace           string
		configMapData       map[string]string
		configMapBinaryData map[string][]byte
		secretData          map[string][]byte
		expected            []byte
		expectedIsSecret    bool
		expectedError       string
	}{
		{
			name:      "nil gate returns nil",
			gate:      nil,
			namespace: "test-ns",
			expected:  nil,
		},
		{
			name: "inline input returns raw bytes",
			gate: &temporaliov1alpha1.GateWorkflowConfig{
				Input: &apiextensionsv1.JSON{
					Raw: []byte(`{"key": "value"}`),
				},
			},
			namespace: "test-ns",
			expected:  []byte(`{"key": "value"}`),
		},
		{
			name: "both input and inputFrom set returns error",
			gate: &temporaliov1alpha1.GateWorkflowConfig{
				Input: &apiextensionsv1.JSON{
					Raw: []byte(`{"key": "value"}`),
				},
				InputFrom: &temporaliov1alpha1.GateInputSource{
					ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: "test-cm"},
						Key:                  "test-key",
					},
				},
			},
			namespace:     "test-ns",
			expectedError: "both spec.rollout.gate.input and spec.rollout.gate.inputFrom are set",
		},
		{
			name:      "nil inputFrom returns nil",
			gate:      &temporaliov1alpha1.GateWorkflowConfig{},
			namespace: "test-ns",
			expected:  nil,
		},
		{
			name: "both configMapKeyRef and secretKeyRef set returns error",
			gate: &temporaliov1alpha1.GateWorkflowConfig{
				InputFrom: &temporaliov1alpha1.GateInputSource{
					ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: "test-cm"},
						Key:                  "test-key",
					},
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: "test-secret"},
						Key:                  "test-key",
					},
				},
			},
			namespace:     "test-ns",
			expectedError: "spec.rollout.gate.inputFrom must set exactly one of configMapKeyRef or secretKeyRef",
		},
		{
			name: "neither configMapKeyRef nor secretKeyRef set returns error",
			gate: &temporaliov1alpha1.GateWorkflowConfig{
				InputFrom: &temporaliov1alpha1.GateInputSource{},
			},
			namespace:     "test-ns",
			expectedError: "spec.rollout.gate.inputFrom must set exactly one of configMapKeyRef or secretKeyRef",
		},
		{
			name: "configMapKeyRef with data returns value",
			gate: &temporaliov1alpha1.GateWorkflowConfig{
				InputFrom: &temporaliov1alpha1.GateInputSource{
					ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: "test-cm"},
						Key:                  "test-key",
					},
				},
			},
			namespace: "test-ns",
			configMapData: map[string]string{
				"test-key": `{"config": "from-cm"}`,
			},
			expected: []byte(`{"config": "from-cm"}`),
		},
		{
			name: "configMapKeyRef with binary data returns value",
			gate: &temporaliov1alpha1.GateWorkflowConfig{
				InputFrom: &temporaliov1alpha1.GateInputSource{
					ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: "test-cm"},
						Key:                  "test-key",
					},
				},
			},
			namespace: "test-ns",
			configMapBinaryData: map[string][]byte{
				"test-key": []byte(`{"config": "from-cm-binary"}`),
			},
			expected: []byte(`{"config": "from-cm-binary"}`),
		},
		{
			name: "configMapKeyRef with missing key returns error",
			gate: &temporaliov1alpha1.GateWorkflowConfig{
				InputFrom: &temporaliov1alpha1.GateInputSource{
					ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: "test-cm"},
						Key:                  "missing-key",
					},
				},
			},
			namespace: "test-ns",
			configMapData: map[string]string{
				"other-key": `{"config": "value"}`,
			},
			expectedError: `key "missing-key" not found in ConfigMap test-ns/test-cm`,
		},
		{
			name: "secretKeyRef with data returns value",
			gate: &temporaliov1alpha1.GateWorkflowConfig{
				InputFrom: &temporaliov1alpha1.GateInputSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: "test-secret"},
						Key:                  "test-key",
					},
				},
			},
			namespace: "test-ns",
			secretData: map[string][]byte{
				"test-key": []byte(`{"secret": "data"}`),
			},
			expected:         []byte(`{"secret": "data"}`),
			expectedIsSecret: true,
		},
		{
			name: "secretKeyRef with missing key returns error",
			gate: &temporaliov1alpha1.GateWorkflowConfig{
				InputFrom: &temporaliov1alpha1.GateInputSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: "test-secret"},
						Key:                  "missing-key",
					},
				},
			},
			namespace: "test-ns",
			secretData: map[string][]byte{
				"other-key": []byte(`{"secret": "value"}`),
			},
			expectedError: `key "missing-key" not found in Secret test-ns/test-secret`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result, isSecret, err := ResolveGateInput(tc.gate, tc.namespace, tc.configMapData, tc.configMapBinaryData, tc.secretData)

			if tc.expectedError != "" {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tc.expectedError)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tc.expected, result)
				assert.Equal(t, tc.expectedIsSecret, isSecret)
			}
		})
	}
}

func TestGetWorkerResourceApplies(t *testing.T) {
	testCases := []struct {
		name              string
		wrts              []temporaliov1alpha1.WorkerResourceTemplate
		k8sState          *k8s.DeploymentState
		deleteDeployments []*appsv1.Deployment
		expectCount       int
	}{
		{
			name: "no WRTs produces no applies",
			wrts: nil,
			k8sState: &k8s.DeploymentState{
				Deployments: map[string]*appsv1.Deployment{
					"build-a": createDeploymentWithUID("worker-build-a", "uid-a"),
				},
			},
			expectCount: 0,
		},
		{
			name: "no deployments produces no applies",
			wrts: []temporaliov1alpha1.WorkerResourceTemplate{
				createTestWRT("my-hpa", "my-worker"),
			},
			k8sState: &k8s.DeploymentState{
				Deployments: map[string]*appsv1.Deployment{},
			},
			expectCount: 0,
		},
		{
			name: "1 WRT × 1 deployment produces 1 apply",
			wrts: []temporaliov1alpha1.WorkerResourceTemplate{
				createTestWRT("my-hpa", "my-worker"),
			},
			k8sState: &k8s.DeploymentState{
				Deployments: map[string]*appsv1.Deployment{
					"build-a": createDeploymentWithUID("worker-build-a", "uid-a"),
				},
			},
			expectCount: 1,
		},
		{
			name: "1 WRT × 2 deployments produces 2 applies",
			wrts: []temporaliov1alpha1.WorkerResourceTemplate{
				createTestWRT("my-hpa", "my-worker"),
			},
			k8sState: &k8s.DeploymentState{
				Deployments: map[string]*appsv1.Deployment{
					"build-a": createDeploymentWithUID("worker-build-a", "uid-a"),
					"build-b": createDeploymentWithUID("worker-build-b", "uid-b"),
				},
			},
			expectCount: 2,
		},
		{
			name: "2 WRTs × 1 deployment produces 2 applies",
			wrts: []temporaliov1alpha1.WorkerResourceTemplate{
				createTestWRT("my-hpa", "my-worker"),
				createTestWRT("my-pdb", "my-worker"),
			},
			k8sState: &k8s.DeploymentState{
				Deployments: map[string]*appsv1.Deployment{
					"build-a": createDeploymentWithUID("worker-build-a", "uid-a"),
				},
			},
			expectCount: 2,
		},
		{
			name: "2 WRTs × 2 deployments produces 4 applies",
			wrts: []temporaliov1alpha1.WorkerResourceTemplate{
				createTestWRT("my-hpa", "my-worker"),
				createTestWRT("my-pdb", "my-worker"),
			},
			k8sState: &k8s.DeploymentState{
				Deployments: map[string]*appsv1.Deployment{
					"build-a": createDeploymentWithUID("worker-build-a", "uid-a"),
					"build-b": createDeploymentWithUID("worker-build-b", "uid-b"),
				},
			},
			expectCount: 4,
		},
		{
			name: "WRT with nil Raw is skipped without blocking others",
			wrts: []temporaliov1alpha1.WorkerResourceTemplate{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "nil-raw", Namespace: "default"},
					Spec: temporaliov1alpha1.WorkerResourceTemplateSpec{
						TemporalWorkerDeploymentRef: temporaliov1alpha1.TemporalWorkerDeploymentReference{Name: "my-worker"},
						Template:                    runtime.RawExtension{Raw: nil},
					},
				},
				createTestWRT("my-hpa", "my-worker"),
			},
			k8sState: &k8s.DeploymentState{
				Deployments: map[string]*appsv1.Deployment{
					"build-a": createDeploymentWithUID("worker-build-a", "uid-a"),
				},
			},
			expectCount: 1, // only the valid WRT
		},
		{
			name: "WRT with invalid template produces a RenderError entry without blocking others",
			wrts: []temporaliov1alpha1.WorkerResourceTemplate{
				createTestWRTWithInvalidTemplate("bad-template", "my-worker"),
				createTestWRT("my-hpa", "my-worker"),
			},
			k8sState: &k8s.DeploymentState{
				Deployments: map[string]*appsv1.Deployment{
					"build-a": createDeploymentWithUID("worker-build-a", "uid-a"),
				},
			},
			expectCount: 2, // 1 render-error entry + 1 valid apply
		},
		{
			name: "deployments in delete list are skipped",
			wrts: []temporaliov1alpha1.WorkerResourceTemplate{
				createTestWRT("my-hpa", "my-worker"),
			},
			k8sState: &k8s.DeploymentState{
				Deployments: map[string]*appsv1.Deployment{
					"build-a": createDeploymentWithUID("worker-build-a", "uid-a"),
					"build-b": createDeploymentWithUID("worker-build-b", "uid-b"),
				},
			},
			deleteDeployments: []*appsv1.Deployment{
				createDeploymentWithUID("worker-build-b", "uid-b"),
			},
			expectCount: 1, // only build-a; build-b is being deleted
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			applies := getWorkerResourceApplies(logr.Discard(), tc.wrts, tc.k8sState, "test-temporal-ns", tc.deleteDeployments)
			assert.Equal(t, tc.expectCount, len(applies), "unexpected number of worker resource applies")
		})
	}
}

func TestGetWorkerResourceApplies_RenderError(t *testing.T) {
	k8sState := &k8s.DeploymentState{
		Deployments: map[string]*appsv1.Deployment{
			"build-a": createDeploymentWithUID("worker-build-a", "uid-a"),
		},
	}
	wrts := []temporaliov1alpha1.WorkerResourceTemplate{
		createTestWRTWithInvalidTemplate("bad-template", "my-worker"),
		createTestWRT("my-hpa", "my-worker"),
	}

	applies := getWorkerResourceApplies(logr.Discard(), wrts, k8sState, "test-temporal-ns", nil)
	require.Len(t, applies, 2)

	var errEntry, okEntry *WorkerResourceApply
	for i := range applies {
		if applies[i].RenderError != nil {
			errEntry = &applies[i]
		} else {
			okEntry = &applies[i]
		}
	}
	require.NotNil(t, errEntry, "expected an entry with RenderError set")
	assert.Equal(t, "build-a", errEntry.BuildID)
	assert.Equal(t, "bad-template", errEntry.WRTName)
	assert.Nil(t, errEntry.Resource)

	require.NotNil(t, okEntry, "expected a valid apply entry")
	assert.Equal(t, "build-a", okEntry.BuildID)
	assert.Equal(t, "my-hpa", okEntry.WRTName)
	assert.NotNil(t, okEntry.Resource)
	assert.Nil(t, okEntry.RenderError)
}

func TestGetWorkerResourceApplies_ApplyContents(t *testing.T) {
	wrt := createTestWRT("my-hpa", "my-worker")
	deployment := createDeploymentWithUID("my-worker-build-abc", "uid-abc")
	k8sState := &k8s.DeploymentState{
		Deployments: map[string]*appsv1.Deployment{
			"build-abc": deployment,
		},
	}

	applies := getWorkerResourceApplies(logr.Discard(), []temporaliov1alpha1.WorkerResourceTemplate{wrt}, k8sState, "test-temporal-ns", nil)
	require.Len(t, applies, 1)

	apply := applies[0]

	// Resource kind and apiVersion must come from the template
	assert.Equal(t, "HorizontalPodAutoscaler", apply.Resource.GetKind())
	assert.Equal(t, "autoscaling/v2", apply.Resource.GetAPIVersion())

	// Resource must be owned by the WRT (not the Deployment)
	ownerRefs := apply.Resource.GetOwnerReferences()
	require.Len(t, ownerRefs, 1)
	assert.Equal(t, wrt.Name, ownerRefs[0].Name)
	assert.Equal(t, "WorkerResourceTemplate", ownerRefs[0].Kind)
	assert.Equal(t, wrt.UID, ownerRefs[0].UID)

	// Resource name must be deterministic
	assert.Equal(t, k8s.ComputeWorkerResourceTemplateName("my-worker", "my-hpa", "build-abc"), apply.Resource.GetName())
}

// createTestWRT builds a minimal valid WorkerResourceTemplate for use in tests.
// The embedded object is a stub HPA with scaleTargetRef opted in for auto-injection.
func createTestWRT(name, workerDeploymentRefName string) temporaliov1alpha1.WorkerResourceTemplate {
	hpaSpec := map[string]interface{}{
		"apiVersion": "autoscaling/v2",
		"kind":       "HorizontalPodAutoscaler",
		"spec": map[string]interface{}{
			"scaleTargetRef": map[string]interface{}{}, // opt in to auto-injection
			"minReplicas":    float64(1),
			"maxReplicas":    float64(5),
		},
	}
	raw, _ := json.Marshal(hpaSpec)
	return temporaliov1alpha1.WorkerResourceTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
		},
		Spec: temporaliov1alpha1.WorkerResourceTemplateSpec{
			TemporalWorkerDeploymentRef: temporaliov1alpha1.TemporalWorkerDeploymentReference{Name: workerDeploymentRefName},
			Template:                    runtime.RawExtension{Raw: raw},
		},
	}
}

// createDeploymentWithUID builds a Deployment with the given name and UID, with the default
// connection spec hash annotation pre-set so it does not trigger an update during plan generation.
func createDeploymentWithUID(name, uid string) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
			UID:       types.UID(uid),
		},
		Spec: appsv1.DeploymentSpec{
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

func TestGetWorkerResourceApplies_MatchLabelsInjection(t *testing.T) {
	// PDB with matchLabels opted in for auto-injection via empty-object sentinel.
	pdbSpec := map[string]interface{}{
		"apiVersion": "policy/v1",
		"kind":       "PodDisruptionBudget",
		"spec": map[string]interface{}{
			"selector": map[string]interface{}{
				"matchLabels": map[string]interface{}{}, // {} = opt in to auto-injection
			},
			"minAvailable": float64(1),
		},
	}
	raw, _ := json.Marshal(pdbSpec)
	wrt := temporaliov1alpha1.WorkerResourceTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "my-pdb", Namespace: "default"},
		Spec: temporaliov1alpha1.WorkerResourceTemplateSpec{
			TemporalWorkerDeploymentRef: temporaliov1alpha1.TemporalWorkerDeploymentReference{Name: "my-worker"},
			Template:                    runtime.RawExtension{Raw: raw},
		},
	}

	deployment := createDeploymentWithUID("my-worker-build-abc", "uid-abc")
	k8sState := &k8s.DeploymentState{
		Deployments: map[string]*appsv1.Deployment{"build-abc": deployment},
	}

	applies := getWorkerResourceApplies(logr.Discard(), []temporaliov1alpha1.WorkerResourceTemplate{wrt}, k8sState, "test-temporal-ns", nil)
	require.Len(t, applies, 1)

	spec, ok := applies[0].Resource.Object["spec"].(map[string]interface{})
	require.True(t, ok)
	selector, ok := spec["selector"].(map[string]interface{})
	require.True(t, ok)
	matchLabels, ok := selector["matchLabels"].(map[string]interface{})
	require.True(t, ok, "matchLabels should have been auto-injected")

	// The injected labels must equal ComputeSelectorLabels(temporalWorkerDeploymentRef, buildID).
	expected := k8s.ComputeSelectorLabels("my-worker", "build-abc")
	for k, v := range expected {
		assert.Equal(t, v, matchLabels[k], "injected matchLabels[%q]", k)
	}
	assert.Len(t, matchLabels, len(expected), "no extra keys should be injected")
}

// createTestWRTWithInvalidTemplate builds a WRT whose spec.template contains invalid json
// template, causing RenderWorkerResourceTemplate to return an error.
func createTestWRTWithInvalidTemplate(name, workerDeploymentRefName string) temporaliov1alpha1.WorkerResourceTemplate {
	raw, _ := json.Marshal([]byte("invalid json"))
	return temporaliov1alpha1.WorkerResourceTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
		},
		Spec: temporaliov1alpha1.WorkerResourceTemplateSpec{
			TemporalWorkerDeploymentRef: temporaliov1alpha1.TemporalWorkerDeploymentReference{Name: workerDeploymentRefName},
			Template:                    runtime.RawExtension{Raw: raw},
		},
	}
}

func TestGetDeleteWorkerResources(t *testing.T) {
	hpaRaw, _ := json.Marshal(map[string]interface{}{
		"apiVersion": "autoscaling/v2",
		"kind":       "HorizontalPodAutoscaler",
		"spec":       map[string]interface{}{"minReplicas": 1},
	})
	wrt := temporaliov1alpha1.WorkerResourceTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "my-hpa", Namespace: "default"},
		Spec: temporaliov1alpha1.WorkerResourceTemplateSpec{
			TemporalWorkerDeploymentRef: temporaliov1alpha1.TemporalWorkerDeploymentReference{Name: "my-worker"},
			Template:                    runtime.RawExtension{Raw: hpaRaw},
		},
	}

	makeDeployment := func(name, buildID string) *appsv1.Deployment {
		d := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:   name,
				Labels: map[string]string{k8s.BuildIDLabel: buildID},
			},
		}
		return d
	}

	t.Run("no delete deployments produces no refs", func(t *testing.T) {
		refs := getDeleteWorkerResources([]temporaliov1alpha1.WorkerResourceTemplate{wrt}, nil)
		assert.Nil(t, refs)
	})

	t.Run("no wrts produces no refs", func(t *testing.T) {
		refs := getDeleteWorkerResources(nil, []*appsv1.Deployment{makeDeployment("worker-abc", "abc")})
		assert.Nil(t, refs)
	})

	t.Run("resource name computed deterministically without status entry", func(t *testing.T) {
		// The resource name must be computable even when wrt.Status.Versions is empty —
		// i.e. the WRT was never applied for this buildID before the Deployment was deleted.
		dep := makeDeployment("worker-build-abc", "build-abc")
		refs := getDeleteWorkerResources([]temporaliov1alpha1.WorkerResourceTemplate{wrt}, []*appsv1.Deployment{dep})
		require.Len(t, refs, 1)
		expectedName := k8s.ComputeWorkerResourceTemplateName("my-worker", "my-hpa", "build-abc")
		assert.Equal(t, expectedName, refs[0].Name)
		assert.Equal(t, "default", refs[0].Namespace)
		assert.Equal(t, "autoscaling/v2", refs[0].APIVersion)
		assert.Equal(t, "HorizontalPodAutoscaler", refs[0].Kind)
	})

	t.Run("multiple WRTs × single deployment produces one ref per WRT", func(t *testing.T) {
		pdbRaw, _ := json.Marshal(map[string]interface{}{
			"apiVersion": "policy/v1",
			"kind":       "PodDisruptionBudget",
			"spec":       map[string]interface{}{"minAvailable": 1},
		})
		wrt2 := temporaliov1alpha1.WorkerResourceTemplate{
			ObjectMeta: metav1.ObjectMeta{Name: "my-pdb", Namespace: "default"},
			Spec: temporaliov1alpha1.WorkerResourceTemplateSpec{
				TemporalWorkerDeploymentRef: temporaliov1alpha1.TemporalWorkerDeploymentReference{Name: "my-worker"},
				Template:                    runtime.RawExtension{Raw: pdbRaw},
			},
		}
		dep := makeDeployment("worker-build-abc", "build-abc")
		refs := getDeleteWorkerResources([]temporaliov1alpha1.WorkerResourceTemplate{wrt, wrt2}, []*appsv1.Deployment{dep})
		require.Len(t, refs, 2)
		kinds := map[string]bool{refs[0].Kind: true, refs[1].Kind: true}
		assert.True(t, kinds["HorizontalPodAutoscaler"], "expected HPA ref")
		assert.True(t, kinds["PodDisruptionBudget"], "expected PDB ref")
	})

	t.Run("single WRT × multiple deployments produces one ref per deployment", func(t *testing.T) {
		dep1 := makeDeployment("worker-abc", "build-abc")
		dep2 := makeDeployment("worker-def", "build-def")
		refs := getDeleteWorkerResources([]temporaliov1alpha1.WorkerResourceTemplate{wrt}, []*appsv1.Deployment{dep1, dep2})
		require.Len(t, refs, 2)
		names := map[string]bool{refs[0].Name: true, refs[1].Name: true}
		assert.True(t, names[k8s.ComputeWorkerResourceTemplateName("my-worker", "my-hpa", "build-abc")])
		assert.True(t, names[k8s.ComputeWorkerResourceTemplateName("my-worker", "my-hpa", "build-def")])
	})

	t.Run("deployment with missing build-id label is skipped", func(t *testing.T) {
		dep := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "worker-no-label"},
		}
		refs := getDeleteWorkerResources([]temporaliov1alpha1.WorkerResourceTemplate{wrt}, []*appsv1.Deployment{dep})
		assert.Nil(t, refs)
	})

	t.Run("wrt with unparseable template is skipped", func(t *testing.T) {
		badWRT := temporaliov1alpha1.WorkerResourceTemplate{
			ObjectMeta: metav1.ObjectMeta{Name: "bad-wrt", Namespace: "default"},
			Spec: temporaliov1alpha1.WorkerResourceTemplateSpec{
				TemporalWorkerDeploymentRef: temporaliov1alpha1.TemporalWorkerDeploymentReference{Name: "my-worker"},
				Template:                    runtime.RawExtension{Raw: []byte(`not json`)},
			},
		}
		dep := makeDeployment("worker-abc", "build-abc")
		refs := getDeleteWorkerResources([]temporaliov1alpha1.WorkerResourceTemplate{badWRT, wrt}, []*appsv1.Deployment{dep})
		require.Len(t, refs, 1) // only the valid WRT
		assert.Equal(t, "HorizontalPodAutoscaler", refs[0].Kind)
	})
}

func TestGetWRTOwnerRefPatches(t *testing.T) {
	const twdName = "my-worker"
	const twdUID = types.UID("twd-uid-123")

	newWRT := func(name string, ownerRefs ...metav1.OwnerReference) temporaliov1alpha1.WorkerResourceTemplate {
		return temporaliov1alpha1.WorkerResourceTemplate{
			ObjectMeta: metav1.ObjectMeta{
				Name:            name,
				Namespace:       "default",
				OwnerReferences: ownerRefs,
			},
		}
	}

	t.Run("all WRTs need owner ref", func(t *testing.T) {
		wrts := []temporaliov1alpha1.WorkerResourceTemplate{
			newWRT("wrt-a"),
			newWRT("wrt-b"),
		}
		patches := getWRTOwnerRefPatches(wrts, twdName, twdUID)
		require.Len(t, patches, 2)

		// Base should be unchanged
		assert.Empty(t, patches[0].Base.OwnerReferences)
		assert.Empty(t, patches[1].Base.OwnerReferences)

		// Patched should have the owner ref appended
		require.Len(t, patches[0].Patched.OwnerReferences, 1)
		assert.Equal(t, twdUID, patches[0].Patched.OwnerReferences[0].UID)
		assert.Equal(t, true, *patches[0].Patched.OwnerReferences[0].Controller)
	})

	t.Run("WRT already owned by this TWD is skipped", func(t *testing.T) {
		wrts := []temporaliov1alpha1.WorkerResourceTemplate{
			newWRT("already-owned", metav1.OwnerReference{
				APIVersion: "temporal.io/v1alpha1", Kind: "TemporalWorkerDeployment",
				Name: twdName, UID: twdUID, Controller: func() *bool { b := true; return &b }(),
			}),
			newWRT("needs-ref"),
		}
		patches := getWRTOwnerRefPatches(wrts, twdName, twdUID)
		require.Len(t, patches, 1)
		assert.Equal(t, "needs-ref", patches[0].Patched.Name)
	})

	t.Run("WRT with different controller owner still gets patched", func(t *testing.T) {
		otherUID := types.UID("other-uid")
		otherController := true
		otherRef := metav1.OwnerReference{UID: otherUID, Controller: &otherController}
		wrts := []temporaliov1alpha1.WorkerResourceTemplate{
			newWRT("other-owner", otherRef),
		}
		patches := getWRTOwnerRefPatches(wrts, twdName, twdUID)
		// The other controller has a different UID so we still add our ref
		require.Len(t, patches, 1)
		require.Len(t, patches[0].Patched.OwnerReferences, 2)
	})

	t.Run("empty WRT list returns nil", func(t *testing.T) {
		patches := getWRTOwnerRefPatches(nil, twdName, twdUID)
		assert.Nil(t, patches)
	})

	t.Run("non-controller owner ref with same UID does not skip", func(t *testing.T) {
		notController := false
		nonControllerRef := metav1.OwnerReference{
			UID:        twdUID,
			Controller: &notController,
		}
		wrts := []temporaliov1alpha1.WorkerResourceTemplate{
			newWRT("non-controller-ref", nonControllerRef),
		}
		patches := getWRTOwnerRefPatches(wrts, twdName, twdUID)
		// controller=false with matching UID should still get the controller ref added
		require.Len(t, patches, 1)
	})
}
