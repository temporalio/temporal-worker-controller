// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package planner

import (
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	"github.com/temporalio/temporal-worker-controller/internal/k8s"
	"github.com/temporalio/temporal-worker-controller/internal/temporal"
	"github.com/temporalio/temporal-worker-controller/internal/testhelpers/testlogr"
)

func TestGeneratePlan(t *testing.T) {
	testCases := []struct {
		name                    string
		k8sState                *k8s.DeploymentState
		status                  *temporaliov1alpha1.TemporalWorkerDeploymentStatus
		spec                    *temporaliov1alpha1.TemporalWorkerDeploymentSpec
		state                   *temporal.TemporalWorkerState
		config                  *Config
		expectDelete            int
		expectCreate            bool
		expectScale             int
		expectWorkflow          int
		expectConfig            bool
		expectConfigSetCurrent  *bool    // pointer so we can test nil
		expectConfigRampPercent *float32 // pointer so we can test nil
	}{
		{
			name:     "empty state creates new deployment",
			k8sState: &k8s.DeploymentState{},
			status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
				TargetVersion: &temporaliov1alpha1.TargetWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						VersionID: "test/namespace.123",
						Status:    temporaliov1alpha1.VersionStatusNotRegistered,
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
			expectCreate: true,
		},
		{
			name: "drained version gets deleted",
			k8sState: &k8s.DeploymentState{
				Deployments: map[string]*appsv1.Deployment{
					"test/namespace.123": createDeploymentWithReplicas(0),
					"test/namespace.456": createDeploymentWithReplicas(1),
				},
				DeploymentsByTime: []*appsv1.Deployment{
					createDeploymentWithReplicas(0),
					createDeploymentWithReplicas(1),
				},
			},
			status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
				TargetVersion: &temporaliov1alpha1.TargetWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						VersionID:  "test/namespace.456",
						Status:     temporaliov1alpha1.VersionStatusCurrent,
						Deployment: &v1.ObjectReference{Name: "test-456"},
					},
				},
				CurrentVersion: &temporaliov1alpha1.CurrentWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						VersionID:  "test/namespace.456",
						Status:     temporaliov1alpha1.VersionStatusCurrent,
						Deployment: &v1.ObjectReference{Name: "test-456"},
					},
				},
				DeprecatedVersions: []*temporaliov1alpha1.DeprecatedWorkerDeploymentVersion{
					{
						BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
							VersionID:  "test/namespace.123",
							Status:     temporaliov1alpha1.VersionStatusDrained,
							Deployment: &v1.ObjectReference{Name: "test-123"},
						},
						DrainedSince: &metav1.Time{
							Time: time.Now().Add(-24 * time.Hour),
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
			expectDelete: 1,
		},
		{
			name: "deployment needs to be scaled",
			k8sState: &k8s.DeploymentState{
				Deployments: map[string]*appsv1.Deployment{
					"test/namespace.123": createDeploymentWithReplicas(1),
				},
				DeploymentsByTime: []*appsv1.Deployment{
					createDeploymentWithReplicas(1),
				},
			},
			status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
				TargetVersion: &temporaliov1alpha1.TargetWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						VersionID:  "test/namespace.123",
						Status:     temporaliov1alpha1.VersionStatusCurrent,
						Deployment: &v1.ObjectReference{Name: "test-123"},
					},
				},
				CurrentVersion: &temporaliov1alpha1.CurrentWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						VersionID:  "test/namespace.123",
						Status:     temporaliov1alpha1.VersionStatusCurrent,
						Deployment: &v1.ObjectReference{Name: "test-123"},
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
					"test/namespace.123": createDeploymentWithReplicas(3),
					"test/namespace.456": createDeploymentWithReplicas(3),
				},
				DeploymentsByTime: []*appsv1.Deployment{
					createDeploymentWithReplicas(3),
					createDeploymentWithReplicas(3),
				},
				DeploymentRefs: map[string]*v1.ObjectReference{
					"test/namespace.123": {Name: "test-123"},
					"test/namespace.456": {Name: "test-456"},
				},
			},
			status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
				CurrentVersion: &temporaliov1alpha1.CurrentWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						VersionID:  "test/namespace.123",
						Status:     temporaliov1alpha1.VersionStatusCurrent,
						Deployment: &v1.ObjectReference{Name: "test-123"},
						HealthySince: &metav1.Time{
							Time: time.Now().Add(-2 * time.Hour),
						},
					},
				},
				TargetVersion: &temporaliov1alpha1.TargetWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						VersionID:  "test/namespace.123",
						Status:     temporaliov1alpha1.VersionStatusCurrent,
						Deployment: &v1.ObjectReference{Name: "test-123"},
						HealthySince: &metav1.Time{
							Time: time.Now().Add(-2 * time.Hour),
						},
					},
				},
				DeprecatedVersions: []*temporaliov1alpha1.DeprecatedWorkerDeploymentVersion{
					{
						BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
							VersionID:  "test/namespace.456",
							Status:     temporaliov1alpha1.VersionStatusRamping,
							Deployment: &v1.ObjectReference{Name: "test-456"},
						},
					},
				},
			},
			spec: &temporaliov1alpha1.TemporalWorkerDeploymentSpec{
				Replicas: func() *int32 { r := int32(3); return &r }(),
			},
			state: &temporal.TemporalWorkerState{
				RampingVersionID: "test/namespace.456", // This is what triggers the reset
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
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := GeneratePlan(logr.Discard(), tc.k8sState, tc.status, tc.spec, tc.state, tc.config)
			require.NoError(t, err)

			assert.Equal(t, tc.expectDelete, len(plan.DeleteDeployments), "unexpected number of deletions")
			assert.Equal(t, tc.expectScale, len(plan.ScaleDeployments), "unexpected number of scales")
			assert.Equal(t, tc.expectCreate, plan.ShouldCreateDeployment, "unexpected create flag")
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
					"test/namespace.123": createDeploymentWithReplicas(0),
				},
			},
			status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
				DeprecatedVersions: []*temporaliov1alpha1.DeprecatedWorkerDeploymentVersion{
					{
						BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
							VersionID:  "test/namespace.123",
							Status:     temporaliov1alpha1.VersionStatusDrained,
							Deployment: &v1.ObjectReference{Name: "test-123"},
						},
						DrainedSince: &metav1.Time{
							Time: time.Now().Add(-24 * time.Hour),
						},
					},
				},
			},
			spec: &temporaliov1alpha1.TemporalWorkerDeploymentSpec{
				SunsetStrategy: temporaliov1alpha1.SunsetStrategy{
					DeleteDelay: &metav1.Duration{
						Duration: 4 * time.Hour,
					},
				},
				Replicas: func() *int32 { r := int32(1); return &r }(),
			},
			expectDeletes: 1,
		},
		{
			name: "not yet drained long enough",
			k8sState: &k8s.DeploymentState{
				Deployments: map[string]*appsv1.Deployment{
					"test/namespace.123": createDeploymentWithReplicas(0),
				},
			},
			status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
				TargetVersion: &temporaliov1alpha1.TargetWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						VersionID:  "test/namespace.123",
						Status:     temporaliov1alpha1.VersionStatusCurrent,
						Deployment: &v1.ObjectReference{Name: "test-123"},
					},
				},
				DeprecatedVersions: []*temporaliov1alpha1.DeprecatedWorkerDeploymentVersion{
					{
						BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
							VersionID:  "test/namespace.456",
							Status:     temporaliov1alpha1.VersionStatusDrained,
							Deployment: &v1.ObjectReference{Name: "test-456"},
						},
						DrainedSince: &metav1.Time{
							Time: time.Now().Add(-1 * time.Hour),
						},
					},
				},
			},
			spec: &temporaliov1alpha1.TemporalWorkerDeploymentSpec{
				SunsetStrategy: temporaliov1alpha1.SunsetStrategy{
					DeleteDelay: &metav1.Duration{
						Duration: 4 * time.Hour,
					},
				},
				Replicas: func() *int32 { r := int32(1); return &r }(),
			},
			expectDeletes: 0,
		},
		{
			name: "not registered version should be deleted",
			k8sState: &k8s.DeploymentState{
				Deployments: map[string]*appsv1.Deployment{
					"test/namespace.123": createDeploymentWithReplicas(1),
					"test/namespace.456": createDeploymentWithReplicas(1),
				},
			},
			status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
				TargetVersion: &temporaliov1alpha1.TargetWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						VersionID:  "test/namespace.123",
						Status:     temporaliov1alpha1.VersionStatusCurrent,
						Deployment: &v1.ObjectReference{Name: "test-123"},
					},
				},
				DeprecatedVersions: []*temporaliov1alpha1.DeprecatedWorkerDeploymentVersion{
					{
						BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
							VersionID:  "test/namespace.456",
							Status:     temporaliov1alpha1.VersionStatusNotRegistered,
							Deployment: &v1.ObjectReference{Name: "test-456"},
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
			deletes := getDeleteDeployments(tc.k8sState, tc.status, tc.spec)
			assert.Equal(t, tc.expectDeletes, len(deletes), "unexpected number of deletes")
		})
	}
}

func TestGetScaleDeployments(t *testing.T) {
	testCases := []struct {
		name         string
		k8sState     *k8s.DeploymentState
		status       *temporaliov1alpha1.TemporalWorkerDeploymentStatus
		spec         *temporaliov1alpha1.TemporalWorkerDeploymentSpec
		state        *temporal.TemporalWorkerState
		expectScales int
	}{
		{
			name: "current version needs scaling",
			k8sState: &k8s.DeploymentState{
				Deployments: map[string]*appsv1.Deployment{
					"test/namespace.123": createDeploymentWithReplicas(1),
				},
			},
			status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
				TargetVersion: &temporaliov1alpha1.TargetWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						VersionID:  "test/namespace.123",
						Status:     temporaliov1alpha1.VersionStatusCurrent,
						Deployment: &v1.ObjectReference{Name: "test-123"},
					},
				},
				CurrentVersion: &temporaliov1alpha1.CurrentWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						VersionID:  "test/namespace.123",
						Status:     temporaliov1alpha1.VersionStatusCurrent,
						Deployment: &v1.ObjectReference{Name: "test-123"},
					},
				},
			},
			spec: &temporaliov1alpha1.TemporalWorkerDeploymentSpec{
				Replicas: func() *int32 { r := int32(2); return &r }(),
			},
			expectScales: 1,
		},
		{
			name: "drained version needs scaling down",
			k8sState: &k8s.DeploymentState{
				Deployments: map[string]*appsv1.Deployment{
					"test/namespace.123": createDeploymentWithReplicas(1),
					"test/namespace.456": createDeploymentWithReplicas(2),
				},
			},
			status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
				TargetVersion: &temporaliov1alpha1.TargetWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						VersionID:  "test/namespace.123",
						Status:     temporaliov1alpha1.VersionStatusCurrent,
						Deployment: &v1.ObjectReference{Name: "test-123"},
					},
				},
				DeprecatedVersions: []*temporaliov1alpha1.DeprecatedWorkerDeploymentVersion{
					{
						BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
							VersionID:  "test/namespace.456",
							Status:     temporaliov1alpha1.VersionStatusDrained,
							Deployment: &v1.ObjectReference{Name: "test-456"},
						},
						DrainedSince: &metav1.Time{
							Time: time.Now().Add(-24 * time.Hour),
						},
					},
				},
			},
			spec: &temporaliov1alpha1.TemporalWorkerDeploymentSpec{
				Replicas: func() *int32 { r := int32(1); return &r }(),
			},
			expectScales: 1,
		},
		{
			name: "inactive version needs scaling up",
			k8sState: &k8s.DeploymentState{
				Deployments: map[string]*appsv1.Deployment{
					"test/namespace.a": createDeploymentWithReplicas(0),
				},
				DeploymentRefs: map[string]*v1.ObjectReference{
					"test/namespace.a": {Name: "test-a"},
				},
			},
			status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
				TargetVersion: &temporaliov1alpha1.TargetWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						VersionID:  "test/namespace.123",
						Status:     temporaliov1alpha1.VersionStatusInactive,
						Deployment: &v1.ObjectReference{Name: "test-123"},
					},
				},
				DeprecatedVersions: []*temporaliov1alpha1.DeprecatedWorkerDeploymentVersion{
					{
						BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
							VersionID:  "test/namespace.a",
							Status:     temporaliov1alpha1.VersionStatusInactive,
							Deployment: &v1.ObjectReference{Name: "test-a"},
						},
					},
				},
			},
			spec: &temporaliov1alpha1.TemporalWorkerDeploymentSpec{
				Replicas: func() *int32 { r := int32(3); return &r }(),
			},
			expectScales: 1,
		},
		{
			name: "ramping version needs scaling up",
			k8sState: &k8s.DeploymentState{
				Deployments: map[string]*appsv1.Deployment{
					"test/namespace.b": createDeploymentWithReplicas(0),
				},
				DeploymentRefs: map[string]*v1.ObjectReference{
					"test/namespace.b": {Name: "test-b"},
				},
			},
			status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
				TargetVersion: &temporaliov1alpha1.TargetWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						VersionID:  "test/namespace.b",
						Status:     temporaliov1alpha1.VersionStatusRamping,
						Deployment: &v1.ObjectReference{Name: "test-b"},
					},
				},
			},
			spec: &temporaliov1alpha1.TemporalWorkerDeploymentSpec{
				Replicas: func() *int32 { r := int32(3); return &r }(),
			},
			state: &temporal.TemporalWorkerState{
				RampingVersionID: "test/namespace.b",
			},
			expectScales: 1,
		},
		{
			name: "current version needs scaling up",
			k8sState: &k8s.DeploymentState{
				Deployments: map[string]*appsv1.Deployment{
					"test/namespace.a": createDeploymentWithReplicas(0),
				},
				DeploymentRefs: map[string]*v1.ObjectReference{
					"test/namespace.a": {Name: "test-a"},
				},
			},
			status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
				TargetVersion: &temporaliov1alpha1.TargetWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						VersionID:  "test/namespace.a",
						Status:     temporaliov1alpha1.VersionStatusInactive,
						Deployment: &v1.ObjectReference{Name: "test-a"},
					},
				},
				DeprecatedVersions: []*temporaliov1alpha1.DeprecatedWorkerDeploymentVersion{
					{
						BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
							VersionID:  "test/namespace.b",
							Status:     temporaliov1alpha1.VersionStatusInactive,
							Deployment: &v1.ObjectReference{Name: "test-b"},
						},
					},
				},
			},
			spec: &temporaliov1alpha1.TemporalWorkerDeploymentSpec{
				Replicas: func() *int32 { r := int32(3); return &r }(),
			},
			expectScales: 1,
		},
		{
			name: "don't scale down drained deployment before delay",
			k8sState: &k8s.DeploymentState{
				Deployments: map[string]*appsv1.Deployment{
					"test/namespace.b": createDeploymentWithReplicas(3),
				},
				DeploymentRefs: map[string]*v1.ObjectReference{
					"test/namespace.b": {Name: "test-b"},
				},
			},
			status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
				TargetVersion: &temporaliov1alpha1.TargetWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						VersionID:  "test/namespace.a",
						Status:     temporaliov1alpha1.VersionStatusInactive,
						Deployment: &v1.ObjectReference{Name: "test-a"},
					},
				},
				DeprecatedVersions: []*temporaliov1alpha1.DeprecatedWorkerDeploymentVersion{
					{
						BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
							VersionID:  "test/namespace.b",
							Status:     temporaliov1alpha1.VersionStatusDrained,
							Deployment: &v1.ObjectReference{Name: "test-b"},
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
			expectScales: 0, // No scaling yet because not enough time passed
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			scales := getScaleDeployments(tc.k8sState, tc.status, tc.spec)
			assert.Equal(t, tc.expectScales, len(scales), "unexpected number of scales")
		})
	}
}

func TestShouldCreateDeployment(t *testing.T) {
	testCases := []struct {
		name          string
		status        *temporaliov1alpha1.TemporalWorkerDeploymentStatus
		expectCreates bool
	}{
		{
			name: "existing deployment should not create",
			status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
				TargetVersion: &temporaliov1alpha1.TargetWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						VersionID:  "test/namespace.123",
						Status:     temporaliov1alpha1.VersionStatusInactive,
						Deployment: &v1.ObjectReference{Name: "test-123"},
					},
				},
			},
			expectCreates: false,
		},
		{
			name: "target version without deployment should create",
			status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
				TargetVersion: &temporaliov1alpha1.TargetWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						VersionID:  "test/namespace.b",
						Status:     temporaliov1alpha1.VersionStatusInactive,
						Deployment: nil,
					},
				},
			},
			expectCreates: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			creates := shouldCreateDeployment(tc.status)
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
				TargetVersion: &temporaliov1alpha1.TargetWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						VersionID: "test/namespace.123",
						Status:    temporaliov1alpha1.VersionStatusInactive,
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
						VersionID: "test/namespace.456",
						Status:    temporaliov1alpha1.VersionStatusCurrent,
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
				TargetVersion: &temporaliov1alpha1.TargetWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						VersionID: "test/namespace.123",
						Status:    temporaliov1alpha1.VersionStatusInactive,
						TaskQueues: []temporaliov1alpha1.TaskQueue{
							{Name: "queue1"},
							{Name: "queue2"},
						},
					},
				},
				CurrentVersion: &temporaliov1alpha1.CurrentWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						VersionID: "test/namespace.456",
						Status:    temporaliov1alpha1.VersionStatusCurrent,
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
				TargetVersion: &temporaliov1alpha1.TargetWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						VersionID:  "test/namespace.123",
						Status:     temporaliov1alpha1.VersionStatusInactive,
						TaskQueues: []temporaliov1alpha1.TaskQueue{}, // Empty
					},
				},
				CurrentVersion: &temporaliov1alpha1.CurrentWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						VersionID: "test/namespace.456",
						Status:    temporaliov1alpha1.VersionStatusCurrent,
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
			expectWorkflows: 0, // No task queues, no workflows
		},
		{
			name: "all test workflows already running",
			status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
				TargetVersion: &temporaliov1alpha1.TargetWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						VersionID: "test/namespace.123",
						Status:    temporaliov1alpha1.VersionStatusInactive,
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
				CurrentVersion: &temporaliov1alpha1.CurrentWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						VersionID: "test/namespace.456",
						Status:    temporaliov1alpha1.VersionStatusCurrent,
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
			expectWorkflows: 0, // All queues have workflows
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			workflows := getTestWorkflows(tc.status, tc.config)
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
		expectRampPercent *float32 // Made pointer to handle nil case
	}{
		{
			name: "all at once strategy",
			strategy: temporaliov1alpha1.RolloutStrategy{
				Strategy: temporaliov1alpha1.UpdateAllAtOnce,
			},
			status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
				TargetVersion: &temporaliov1alpha1.TargetWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						VersionID:    "test/namespace.123",
						Status:       temporaliov1alpha1.VersionStatusInactive,
						HealthySince: &metav1.Time{Time: time.Now().Add(-1 * time.Hour)},
					},
				},
				CurrentVersion: &temporaliov1alpha1.CurrentWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						VersionID: "test/namespace.456",
						Status:    temporaliov1alpha1.VersionStatusCurrent,
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
				TargetVersion: &temporaliov1alpha1.TargetWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						VersionID: "test/namespace.123",
						Status:    temporaliov1alpha1.VersionStatusInactive,
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
						VersionID: "test/namespace.456",
						Status:    temporaliov1alpha1.VersionStatusCurrent,
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
				TargetVersion: &temporaliov1alpha1.TargetWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						VersionID: "test/namespace.123",
						Status:    temporaliov1alpha1.VersionStatusCurrent,
						HealthySince: &metav1.Time{
							Time: time.Now().Add(-1 * time.Hour),
						},
					},
				},
				CurrentVersion: &temporaliov1alpha1.CurrentWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						VersionID: "test/namespace.123",
						Status:    temporaliov1alpha1.VersionStatusCurrent,
					},
				},
				DeprecatedVersions: []*temporaliov1alpha1.DeprecatedWorkerDeploymentVersion{
					{
						BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
							VersionID: "test/namespace.456",
							Status:    temporaliov1alpha1.VersionStatusRamping,
							HealthySince: &metav1.Time{
								Time: time.Now().Add(-30 * time.Minute),
							},
						},
					},
				},
			},
			spec:             &temporaliov1alpha1.TemporalWorkerDeploymentSpec{},
			state:            &temporal.TemporalWorkerState{RampingVersionID: "test/namespace.456"},
			expectConfig:     true,
			expectSetCurrent: false,
		},
		{
			name: "roll-forward scenario - target differs from current but different version is ramping",
			strategy: temporaliov1alpha1.RolloutStrategy{
				Strategy: temporaliov1alpha1.UpdateAllAtOnce,
			},
			status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
				TargetVersion: &temporaliov1alpha1.TargetWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						VersionID: "test/namespace.789",
						Status:    temporaliov1alpha1.VersionStatusInactive,
						HealthySince: &metav1.Time{
							Time: time.Now().Add(-1 * time.Hour),
						},
					},
				},
				CurrentVersion: &temporaliov1alpha1.CurrentWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						VersionID: "test/namespace.123",
						Status:    temporaliov1alpha1.VersionStatusCurrent,
					},
				},
				DeprecatedVersions: []*temporaliov1alpha1.DeprecatedWorkerDeploymentVersion{
					{
						BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
							VersionID: "test/namespace.456",
							Status:    temporaliov1alpha1.VersionStatusRamping,
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
			versionConfig := getVersionConfigDiff(logr.Discard(), tc.status, tc.spec, tc.state, config)
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
						VersionID: "test/namespace.123",
						Status:    temporaliov1alpha1.VersionStatusCurrent,
					},
				},
				TargetVersion: &temporaliov1alpha1.TargetWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						VersionID: "test/namespace.456",
						Status:    temporaliov1alpha1.VersionStatusRamping,
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
			expectSetCurrent: true, // Should become current after all steps
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
						VersionID: "test/namespace.123",
						Status:    temporaliov1alpha1.VersionStatusCurrent,
					},
				},
				TargetVersion: &temporaliov1alpha1.TargetWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						VersionID:    "test/namespace.456",
						Status:       temporaliov1alpha1.VersionStatusInactive,
						HealthySince: &metav1.Time{Time: time.Now()},
					},
					RampingSince:       nil, // Not ramping yet
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
						VersionID: "test/namespace.123",
						Status:    temporaliov1alpha1.VersionStatusCurrent,
					},
				},
				TargetVersion: &temporaliov1alpha1.TargetWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						VersionID:    "test/namespace.456",
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
			expectRampPercent: 50,    // When set as current, ramp is 0
			expectSetCurrent:  false, // At exactly 2 hours, it sets as current
		},
		"progressive rollout with zero ramp percentage step": {
			strategy: temporaliov1alpha1.RolloutStrategy{
				Strategy: temporaliov1alpha1.UpdateProgressive,
				Steps: []temporaliov1alpha1.RolloutStep{
					{RampPercentage: 25, PauseDuration: metav1.Duration{Duration: 30 * time.Minute}},
					{RampPercentage: 0, PauseDuration: metav1.Duration{Duration: 30 * time.Minute}}, // Zero ramp
					{RampPercentage: 50, PauseDuration: metav1.Duration{Duration: 30 * time.Minute}},
				},
			},
			status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
				CurrentVersion: &temporaliov1alpha1.CurrentWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						VersionID: "test/namespace.123",
						Status:    temporaliov1alpha1.VersionStatusCurrent,
					},
				},
				TargetVersion: &temporaliov1alpha1.TargetWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						VersionID:    "test/namespace.456",
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
						VersionID: "test/namespace.123",
						Status:    temporaliov1alpha1.VersionStatusCurrent,
					},
				},
				TargetVersion: &temporaliov1alpha1.TargetWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						VersionID:    "test/namespace.456",
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
			expectSetCurrent:  true, // Past all steps, should be default
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			config := &Config{
				RolloutStrategy: tc.strategy,
			}
			spec := &temporaliov1alpha1.TemporalWorkerDeploymentSpec{}
			versionConfig := getVersionConfigDiff(testlogr.New(t), tc.status, spec, tc.state, config)
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
		spec         *temporaliov1alpha1.TemporalWorkerDeploymentSpec
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
						VersionID: "test/namespace.123",
						Status:    temporaliov1alpha1.VersionStatusCurrent,
					},
				},
				TargetVersion: &temporaliov1alpha1.TargetWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						VersionID:    "test/namespace.456",
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
			spec:         &temporaliov1alpha1.TemporalWorkerDeploymentSpec{},
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
						VersionID: "test/namespace.123",
						Status:    temporaliov1alpha1.VersionStatusCurrent,
					},
				},
				TargetVersion: &temporaliov1alpha1.TargetWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						VersionID:    "test/namespace.456",
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
			spec:         &temporaliov1alpha1.TemporalWorkerDeploymentSpec{},
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
						VersionID: "test/namespace.123",
						Status:    temporaliov1alpha1.VersionStatusCurrent,
					},
				},
				TargetVersion: &temporaliov1alpha1.TargetWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						VersionID:    "test/namespace.456",
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
			spec:         &temporaliov1alpha1.TemporalWorkerDeploymentSpec{},
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
						VersionID: "test/namespace.123",
						Status:    temporaliov1alpha1.VersionStatusCurrent,
					},
				},
				TargetVersion: &temporaliov1alpha1.TargetWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						VersionID:    "test/namespace.456",
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
			spec:         &temporaliov1alpha1.TemporalWorkerDeploymentSpec{},
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
						VersionID: "test/namespace.123",
						Status:    temporaliov1alpha1.VersionStatusCurrent,
					},
				},
				TargetVersion: &temporaliov1alpha1.TargetWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						VersionID:    "test/namespace.456",
						Status:       temporaliov1alpha1.VersionStatusInactive,
						HealthySince: &metav1.Time{Time: time.Now().Add(-1 * time.Hour)},
						TaskQueues:   []temporaliov1alpha1.TaskQueue{}, // Empty
					},
				},
			},
			spec:         &temporaliov1alpha1.TemporalWorkerDeploymentSpec{},
			expectConfig: false, // Should not proceed with no task queues
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			config := &Config{
				RolloutStrategy: tc.strategy,
			}
			versionConfig := getVersionConfigDiff(logr.Discard(), tc.status, tc.spec, tc.state, config)
			if tc.expectConfig {
				assert.NotNil(t, versionConfig, "expected version config")
			} else {
				assert.Nil(t, versionConfig, "expected no version config")
			}
		})
	}
}

func TestSunsetStrategyDefaults(t *testing.T) {
	testCases := []struct {
		name         string
		spec         *temporaliov1alpha1.TemporalWorkerDeploymentSpec
		expectScale  time.Duration
		expectDelete time.Duration
	}{
		{
			name: "nil delays return zero",
			spec: &temporaliov1alpha1.TemporalWorkerDeploymentSpec{
				SunsetStrategy: temporaliov1alpha1.SunsetStrategy{
					ScaledownDelay: nil,
					DeleteDelay:    nil,
				},
			},
			expectScale:  0,
			expectDelete: 0,
		},
		{
			name: "specified delays are returned",
			spec: &temporaliov1alpha1.TemporalWorkerDeploymentSpec{
				SunsetStrategy: temporaliov1alpha1.SunsetStrategy{
					ScaledownDelay: &metav1.Duration{Duration: 2 * time.Hour},
					DeleteDelay:    &metav1.Duration{Duration: 48 * time.Hour},
				},
			},
			expectScale:  2 * time.Hour,
			expectDelete: 48 * time.Hour,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expectScale, getScaledownDelay(tc.spec))
			assert.Equal(t, tc.expectDelete, getDeleteDelay(tc.spec))
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
		expectVersions []string // Expected version IDs for scaling
	}{
		{
			name: "multiple deprecated versions in different states",
			k8sState: &k8s.DeploymentState{
				Deployments: map[string]*appsv1.Deployment{
					"test/namespace.a": createDeploymentWithReplicas(5),
					"test/namespace.b": createDeploymentWithReplicas(3),
					"test/namespace.c": createDeploymentWithReplicas(3),
					"test/namespace.d": createDeploymentWithReplicas(1),
					"test/namespace.e": createDeploymentWithReplicas(0),
				},
			},
			config: &Config{
				RolloutStrategy: temporaliov1alpha1.RolloutStrategy{},
			},
			status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
				TargetVersion: &temporaliov1alpha1.TargetWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						VersionID:  "test/namespace.a",
						Status:     temporaliov1alpha1.VersionStatusInactive,
						Deployment: &v1.ObjectReference{Name: "test-a"},
					},
				},
				DeprecatedVersions: []*temporaliov1alpha1.DeprecatedWorkerDeploymentVersion{
					{
						BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
							VersionID:  "test/namespace.b",
							Status:     temporaliov1alpha1.VersionStatusInactive,
							Deployment: &v1.ObjectReference{Name: "test-b"},
						},
					},
					{
						BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
							VersionID:  "test/namespace.c",
							Status:     temporaliov1alpha1.VersionStatusDraining,
							Deployment: &v1.ObjectReference{Name: "test-c"},
						},
					},
					{
						BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
							VersionID:  "test/namespace.d",
							Status:     temporaliov1alpha1.VersionStatusDrained,
							Deployment: &v1.ObjectReference{Name: "test-d"},
						},
						DrainedSince: &metav1.Time{
							Time: time.Now().Add(-2 * time.Hour), // Recently drained
						},
					},
					{
						BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
							VersionID:  "test/namespace.e",
							Status:     temporaliov1alpha1.VersionStatusDrained,
							Deployment: &v1.ObjectReference{Name: "test-e"},
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
			expectVersions: []string{"test/namespace.b", "test/namespace.d"},
		},
		{
			name: "draining version not scaled down before delay",
			k8sState: &k8s.DeploymentState{
				Deployments: map[string]*appsv1.Deployment{
					"test/namespace.a": createDeploymentWithReplicas(3),
				},
			},
			config: &Config{
				RolloutStrategy: temporaliov1alpha1.RolloutStrategy{},
			},
			status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
				TargetVersion: &temporaliov1alpha1.TargetWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						VersionID:  "test/namespace.a",
						Status:     temporaliov1alpha1.VersionStatusInactive,
						Deployment: &v1.ObjectReference{Name: "test-a"},
					},
				},
				DeprecatedVersions: []*temporaliov1alpha1.DeprecatedWorkerDeploymentVersion{
					{
						BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
							VersionID:  "test/namespace.a",
							Status:     temporaliov1alpha1.VersionStatusDrained,
							Deployment: &v1.ObjectReference{Name: "test-a"},
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
				},
				Replicas: func() *int32 { r := int32(3); return &r }(),
			},
			expectDeletes: 0,
			expectScales:  0, // Should not scale down yet
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := GeneratePlan(logr.Discard(), tc.k8sState, tc.status, tc.spec, tc.state, tc.config)
			require.NoError(t, err)

			assert.Equal(t, tc.expectDeletes, len(plan.DeleteDeployments), "unexpected number of deletes")
			assert.Equal(t, tc.expectScales, len(plan.ScaleDeployments), "unexpected number of scales")

			if tc.expectVersions != nil {
				scaledVersions := make([]string, 0, len(plan.ScaleDeployments))
				for ref := range plan.ScaleDeployments {
					// Extract version ID by looking up in status
					for _, v := range tc.status.DeprecatedVersions {
						if v.Deployment != nil && v.Deployment.Name == ref.Name {
							scaledVersions = append(scaledVersions, v.VersionID)
							break
						}
					}
					if tc.status.CurrentVersion != nil &&
						tc.status.CurrentVersion.Deployment != nil &&
						tc.status.CurrentVersion.Deployment.Name == ref.Name {
						scaledVersions = append(scaledVersions, tc.status.CurrentVersion.VersionID)
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
		name      string
		taskQueue string
		versionID string
		expectID  string
	}{
		{
			name:      "basic workflow ID generation",
			taskQueue: "my-queue",
			versionID: "test/namespace.123",
			expectID:  "test-test/namespace.123-my-queue",
		},
		{
			name:      "workflow ID with special characters in queue name",
			taskQueue: "queue-with-dashes-and_underscores",
			versionID: "test/namespace.456",
			expectID:  "test-test/namespace.456-queue-with-dashes-and_underscores",
		},
		{
			name:      "workflow ID with dots in version",
			taskQueue: "queue",
			versionID: "test/namespace.1.2.3",
			expectID:  "test-test/namespace.1.2.3-queue",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			id := getTestWorkflowID(tc.taskQueue, tc.versionID)
			assert.Equal(t, tc.expectID, id, "unexpected workflow ID")
		})
	}
}

// Helper function to create a deployment with specified replicas
func createDeploymentWithReplicas(replicas int32) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-deployment",
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
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
