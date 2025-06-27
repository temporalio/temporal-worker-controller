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
	"github.com/temporalio/temporal-worker-controller/internal/testlogr"
)

func TestGeneratePlan(t *testing.T) {
	testCases := []struct {
		name                    string
		k8sState                *k8s.DeploymentState
		config                  *Config
		expectDelete            int
		expectScale             int
		expectCreate            bool
		expectWorkflow          int
		expectConfig            bool
		expectConfigSetCurrent  *bool // pointer to distinguish between false and not set
		expectConfigRampPercent *float32
	}{
		{
			name: "empty state creates new deployment",
			k8sState: &k8s.DeploymentState{
				Deployments:       map[string]*appsv1.Deployment{},
				DeploymentsByTime: []*appsv1.Deployment{},
				DeploymentRefs:    map[string]*v1.ObjectReference{},
			},
			config: &Config{
				TargetVersionID: "test/namespace.123",
				Status:          &temporaliov1alpha1.TemporalWorkerDeploymentStatus{},
				Spec:            &temporaliov1alpha1.TemporalWorkerDeploymentSpec{},
				RolloutStrategy: temporaliov1alpha1.RolloutStrategy{},
				Replicas:        1,
				ConflictToken:   []byte{},
			},
			expectCreate: true,
		},
		{
			name: "drained version gets deleted",
			k8sState: &k8s.DeploymentState{
				Deployments: map[string]*appsv1.Deployment{
					"test/namespace.123": createDeploymentWithReplicas(0),
				},
				DeploymentsByTime: []*appsv1.Deployment{
					createDeploymentWithReplicas(0),
				},
				DeploymentRefs: map[string]*v1.ObjectReference{
					"test/namespace.123": {Name: "test-123"},
				},
			},
			config: &Config{
				TargetVersionID: "test/namespace.456",
				Status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
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
				Spec:            &temporaliov1alpha1.TemporalWorkerDeploymentSpec{},
				RolloutStrategy: temporaliov1alpha1.RolloutStrategy{},
				Replicas:        1,
				ConflictToken:   []byte{},
			},
			expectDelete: 1,
			expectCreate: true,
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
				DeploymentRefs: map[string]*v1.ObjectReference{
					"test/namespace.123": {Name: "test-123"},
				},
			},
			config: &Config{
				TargetVersionID: "test/namespace.123",
				Status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
					CurrentVersion: &temporaliov1alpha1.CurrentWorkerDeploymentVersion{
						BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
							VersionID:  "test/namespace.123",
							Status:     temporaliov1alpha1.VersionStatusCurrent,
							Deployment: &v1.ObjectReference{Name: "test-123"},
						},
					},
					TargetVersion: &temporaliov1alpha1.TargetWorkerDeploymentVersion{
						BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
							VersionID:  "test/namespace.123",
							Status:     temporaliov1alpha1.VersionStatusCurrent,
							Deployment: &v1.ObjectReference{Name: "test-123"},
						},
					},
				},
				Spec:            &temporaliov1alpha1.TemporalWorkerDeploymentSpec{},
				RolloutStrategy: temporaliov1alpha1.RolloutStrategy{},
				Replicas:        2,
				ConflictToken:   []byte{},
			},
			expectScale:  2,
			expectCreate: false,
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
			config: &Config{
				TargetVersionID: "test/namespace.123", // Rolling back to current version
				Status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
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
					RampingVersion: &temporaliov1alpha1.TargetWorkerDeploymentVersion{
						BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
							VersionID:  "test/namespace.456",
							Status:     temporaliov1alpha1.VersionStatusRamping,
							Deployment: &v1.ObjectReference{Name: "test-456"},
							HealthySince: &metav1.Time{
								Time: time.Now().Add(-30 * time.Minute),
							},
						},
						RampPercentage: func() *float32 { f := float32(25); return &f }(),
					},
				},
				Spec: &temporaliov1alpha1.TemporalWorkerDeploymentSpec{},
				RolloutStrategy: temporaliov1alpha1.RolloutStrategy{
					Strategy: temporaliov1alpha1.UpdateAllAtOnce,
				},
				Replicas:      3,
				ConflictToken: []byte("token"),
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
			plan, err := GeneratePlan(logr.Discard(), tc.k8sState, tc.config)
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
			config: &Config{
				TargetVersionID: "test/namespace.456",
				Status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
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
				Spec:            &temporaliov1alpha1.TemporalWorkerDeploymentSpec{}, // Uses default sunset strategy: ScaledownDelay=0, DeleteDelay=0
				RolloutStrategy: temporaliov1alpha1.RolloutStrategy{},
				Replicas:        1,
				ConflictToken:   []byte{},
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
			config: &Config{
				TargetVersionID: "test/namespace.456",
				Status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
					DeprecatedVersions: []*temporaliov1alpha1.DeprecatedWorkerDeploymentVersion{
						{
							BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
								VersionID:  "test/namespace.123",
								Status:     temporaliov1alpha1.VersionStatusDrained,
								Deployment: &v1.ObjectReference{Name: "test-123"},
							},
							DrainedSince: &metav1.Time{
								Time: time.Now().Add(-1 * time.Hour),
							},
						},
					},
				},
				Spec: &temporaliov1alpha1.TemporalWorkerDeploymentSpec{
					SunsetStrategy: temporaliov1alpha1.SunsetStrategy{
						DeleteDelay: &metav1.Duration{
							Duration: 4 * time.Hour,
						},
					},
				},
				RolloutStrategy: temporaliov1alpha1.RolloutStrategy{},
				Replicas:        1,
				ConflictToken:   []byte{},
			},
			expectDeletes: 0,
		},
		{
			name: "not registered version should be deleted",
			k8sState: &k8s.DeploymentState{
				Deployments: map[string]*appsv1.Deployment{
					"test/namespace.123": createDeploymentWithReplicas(1),
				},
			},
			config: &Config{
				TargetVersionID: "test/namespace.456",
				Status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
					DeprecatedVersions: []*temporaliov1alpha1.DeprecatedWorkerDeploymentVersion{
						{
							BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
								VersionID:  "test/namespace.123",
								Status:     temporaliov1alpha1.VersionStatusNotRegistered,
								Deployment: &v1.ObjectReference{Name: "test-123"},
							},
						},
					},
				},
				Spec:            &temporaliov1alpha1.TemporalWorkerDeploymentSpec{},
				RolloutStrategy: temporaliov1alpha1.RolloutStrategy{},
				Replicas:        1,
				ConflictToken:   []byte{},
			},
			expectDeletes: 1,
		},
		{
			name: "delete target deployment when version ID has changed",
			k8sState: &k8s.DeploymentState{
				Deployments: map[string]*appsv1.Deployment{
					"test/namespace.b": createDeploymentWithReplicas(3),
				},
			},
			config: &Config{
				TargetVersionID: "test/namespace.c", // Different desired version
				Status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
					TargetVersion: &temporaliov1alpha1.TargetWorkerDeploymentVersion{
						BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
							VersionID:  "test/namespace.b",
							Status:     temporaliov1alpha1.VersionStatusInactive,
							Deployment: &v1.ObjectReference{Name: "test-b"},
						},
					},
				},
				Spec:            &temporaliov1alpha1.TemporalWorkerDeploymentSpec{},
				RolloutStrategy: temporaliov1alpha1.RolloutStrategy{},
				Replicas:        3,
				ConflictToken:   []byte{},
			},
			expectDeletes: 1,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			deletes := getDeleteDeployments(tc.k8sState, tc.config)
			assert.Equal(t, tc.expectDeletes, len(deletes), "unexpected number of deletes")
		})
	}
}

func TestGetScaleDeployments(t *testing.T) {
	testCases := []struct {
		name         string
		k8sState     *k8s.DeploymentState
		config       *Config
		expectScales int
	}{
		{
			name: "default version needs scaling",
			k8sState: &k8s.DeploymentState{
				Deployments: map[string]*appsv1.Deployment{
					"test/namespace.123": createDeploymentWithReplicas(1),
				},
			},
			config: &Config{
				TargetVersionID: "test/namespace.123",
				Status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
					CurrentVersion: &temporaliov1alpha1.CurrentWorkerDeploymentVersion{
						BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
							VersionID:  "test/namespace.123",
							Status:     temporaliov1alpha1.VersionStatusCurrent,
							Deployment: &v1.ObjectReference{Name: "test-123"},
						},
					},
				},
				Spec:            &temporaliov1alpha1.TemporalWorkerDeploymentSpec{},
				RolloutStrategy: temporaliov1alpha1.RolloutStrategy{},
				Replicas:        2,
				ConflictToken:   []byte{},
			},
			expectScales: 1,
		},
		{
			name: "drained version needs scaling down",
			k8sState: &k8s.DeploymentState{
				Deployments: map[string]*appsv1.Deployment{
					"test/namespace.123": createDeploymentWithReplicas(1),
				},
			},
			config: &Config{
				TargetVersionID: "test/namespace.456",
				Status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
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
				Spec:            &temporaliov1alpha1.TemporalWorkerDeploymentSpec{},
				RolloutStrategy: temporaliov1alpha1.RolloutStrategy{},
				Replicas:        2,
				ConflictToken:   []byte{},
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
			config: &Config{
				TargetVersionID: "test/namespace.b",
				Status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
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
				Spec:            &temporaliov1alpha1.TemporalWorkerDeploymentSpec{},
				RolloutStrategy: temporaliov1alpha1.RolloutStrategy{},
				Replicas:        3,
				ConflictToken:   []byte{},
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
			config: &Config{
				TargetVersionID: "test/namespace.b",
				Status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
					TargetVersion: &temporaliov1alpha1.TargetWorkerDeploymentVersion{
						BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
							VersionID:  "test/namespace.b",
							Status:     temporaliov1alpha1.VersionStatusRamping,
							Deployment: &v1.ObjectReference{Name: "test-b"},
						},
					},
				},
				Spec:            &temporaliov1alpha1.TemporalWorkerDeploymentSpec{},
				RolloutStrategy: temporaliov1alpha1.RolloutStrategy{},
				Replicas:        3,
				ConflictToken:   []byte{},
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
			config: &Config{
				TargetVersionID: "test/namespace.b",
				Status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
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
				Spec:            &temporaliov1alpha1.TemporalWorkerDeploymentSpec{},
				RolloutStrategy: temporaliov1alpha1.RolloutStrategy{},
				Replicas:        3,
				ConflictToken:   []byte{},
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
			config: &Config{
				TargetVersionID: "test/namespace.a",
				Status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
					CurrentVersion: &temporaliov1alpha1.CurrentWorkerDeploymentVersion{
						BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
							VersionID:  "test/namespace.a",
							Status:     temporaliov1alpha1.VersionStatusCurrent,
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
				Spec: &temporaliov1alpha1.TemporalWorkerDeploymentSpec{
					SunsetStrategy: temporaliov1alpha1.SunsetStrategy{
						ScaledownDelay: &metav1.Duration{
							Duration: 4 * time.Hour, // Longer than 1 hour
						},
					},
				},
				RolloutStrategy: temporaliov1alpha1.RolloutStrategy{},
				Replicas:        3,
				ConflictToken:   []byte{},
			},
			expectScales: 0, // No scaling yet because not enough time passed
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			scales := getScaleDeployments(tc.k8sState, tc.config)
			assert.Equal(t, tc.expectScales, len(scales), "unexpected number of scales")
		})
	}
}

func TestShouldCreateDeployment(t *testing.T) {
	testCases := []struct {
		name          string
		k8sState      *k8s.DeploymentState
		config        *Config
		expectCreates bool
	}{
		{
			name: "no target version should create",
			k8sState: &k8s.DeploymentState{
				Deployments: map[string]*appsv1.Deployment{},
			},
			config: &Config{
				TargetVersionID: "test/namespace.123",
				Status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
					TargetVersion: nil,
				},
				Spec:            &temporaliov1alpha1.TemporalWorkerDeploymentSpec{},
				RolloutStrategy: temporaliov1alpha1.RolloutStrategy{},
				Replicas:        1,
				ConflictToken:   []byte{},
			},
			expectCreates: true,
		},
		{
			name: "existing deployment should not create",
			k8sState: &k8s.DeploymentState{
				Deployments: map[string]*appsv1.Deployment{
					"test/namespace.123": createDeploymentWithReplicas(1),
				},
			},
			config: &Config{
				TargetVersionID: "test/namespace.123",
				Status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
					TargetVersion: &temporaliov1alpha1.TargetWorkerDeploymentVersion{
						BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
							VersionID:  "test/namespace.123",
							Status:     temporaliov1alpha1.VersionStatusInactive,
							Deployment: &v1.ObjectReference{Name: "test-123"},
						},
					},
				},
				Spec:            &temporaliov1alpha1.TemporalWorkerDeploymentSpec{},
				RolloutStrategy: temporaliov1alpha1.RolloutStrategy{},
				Replicas:        1,
				ConflictToken:   []byte{},
			},
			expectCreates: false,
		},
		{
			name: "target version without deployment should create",
			k8sState: &k8s.DeploymentState{
				Deployments: map[string]*appsv1.Deployment{},
			},
			config: &Config{
				TargetVersionID: "test/namespace.b",
				Status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
					TargetVersion: &temporaliov1alpha1.TargetWorkerDeploymentVersion{
						BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
							VersionID:  "test/namespace.b",
							Status:     temporaliov1alpha1.VersionStatusInactive,
							Deployment: nil,
						},
					},
				},
				Spec:            &temporaliov1alpha1.TemporalWorkerDeploymentSpec{},
				RolloutStrategy: temporaliov1alpha1.RolloutStrategy{},
				Replicas:        1,
				ConflictToken:   []byte{},
			},
			expectCreates: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			creates := shouldCreateDeployment(tc.k8sState, tc.config)
			assert.Equal(t, tc.expectCreates, creates, "unexpected create decision")
		})
	}
}

func TestGetTestWorkflows(t *testing.T) {
	testCases := []struct {
		name            string
		config          *Config
		expectWorkflows int
	}{
		{
			name: "gate workflow needed",
			config: &Config{
				TargetVersionID: "test/namespace.123",
				Status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
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
				Spec: &temporaliov1alpha1.TemporalWorkerDeploymentSpec{},
				RolloutStrategy: temporaliov1alpha1.RolloutStrategy{
					Gate: &temporaliov1alpha1.GateWorkflowConfig{
						WorkflowType: "TestWorkflow",
					},
				},
				Replicas:      1,
				ConflictToken: []byte{},
			},
			expectWorkflows: 1, // Only queue2 needs a workflow
		},
		{
			name: "no gate workflow",
			config: &Config{
				TargetVersionID: "test/namespace.123",
				Status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
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
				Spec:            &temporaliov1alpha1.TemporalWorkerDeploymentSpec{},
				RolloutStrategy: temporaliov1alpha1.RolloutStrategy{},
				Replicas:        1,
				ConflictToken:   []byte{},
			},
			expectWorkflows: 0,
		},
		{
			name: "gate workflow with empty task queues",
			config: &Config{
				TargetVersionID: "test/namespace.123",
				Status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
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
				Spec: &temporaliov1alpha1.TemporalWorkerDeploymentSpec{},
				RolloutStrategy: temporaliov1alpha1.RolloutStrategy{
					Gate: &temporaliov1alpha1.GateWorkflowConfig{
						WorkflowType: "TestWorkflow",
					},
				},
				Replicas:      1,
				ConflictToken: []byte{},
			},
			expectWorkflows: 0, // No task queues, no workflows
		},
		{
			name: "all test workflows already running",
			config: &Config{
				TargetVersionID: "test/namespace.123",
				Status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
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
				Spec: &temporaliov1alpha1.TemporalWorkerDeploymentSpec{},
				RolloutStrategy: temporaliov1alpha1.RolloutStrategy{
					Gate: &temporaliov1alpha1.GateWorkflowConfig{
						WorkflowType: "TestWorkflow",
					},
				},
				Replicas:      1,
				ConflictToken: []byte{},
			},
			expectWorkflows: 0, // All queues have workflows
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			workflows := getTestWorkflows(tc.config)
			assert.Equal(t, tc.expectWorkflows, len(workflows), "unexpected number of test workflows")
		})
	}
}

func TestGetVersionConfigDiff(t *testing.T) {
	testCases := []struct {
		name              string
		strategy          temporaliov1alpha1.RolloutStrategy
		status            *temporaliov1alpha1.TemporalWorkerDeploymentStatus
		conflictToken     []byte
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
			conflictToken:    []byte("token"),
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
				RampLastModifiedAt: &metav1.Time{
					Time: time.Now().Add(-30 * time.Minute),
				},
				TargetVersion: &temporaliov1alpha1.TargetWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						VersionID: "test/namespace.123",
						Status:    temporaliov1alpha1.VersionStatusInactive,
						HealthySince: &metav1.Time{
							Time: time.Now().Add(-30 * time.Minute),
						},
					},
					RampPercentage: func() *float32 { f := float32(0); return &f }(),
				},
				CurrentVersion: &temporaliov1alpha1.CurrentWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						VersionID: "test/namespace.456",
						Status:    temporaliov1alpha1.VersionStatusCurrent,
					},
				},
			},
			conflictToken:    []byte("token"),
			expectConfig:     true,
			expectSetCurrent: false,
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
				RampingVersion: &temporaliov1alpha1.TargetWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						VersionID: "test/namespace.456",
						Status:    temporaliov1alpha1.VersionStatusRamping,
						HealthySince: &metav1.Time{
							Time: time.Now().Add(-30 * time.Minute),
						},
					},
					RampPercentage: func() *float32 { f := float32(25); return &f }(),
				},
			},
			conflictToken:     []byte("token"),
			expectConfig:      true,
			expectSetCurrent:  false,
			expectRampPercent: func() *float32 { f := float32(0); return &f }(),
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
				RampingVersion: &temporaliov1alpha1.TargetWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						VersionID: "test/namespace.456",
						Status:    temporaliov1alpha1.VersionStatusRamping,
					},
				},
			},
			conflictToken:     []byte("token"),
			expectConfig:      true,
			expectSetCurrent:  true,
			expectRampPercent: func() *float32 { f := float32(0); return &f }(),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			config := getVersionConfigDiff(logr.Discard(), tc.strategy, tc.status, tc.conflictToken)
			assert.Equal(t, tc.expectConfig, config != nil, "unexpected version config presence")
			if tc.expectConfig {
				assert.NotNil(t, config, "expected version config")
				assert.Equal(t, tc.expectSetCurrent, config.SetCurrent, "unexpected set current value")
				if tc.expectRampPercent != nil {
					assert.Equal(t, *tc.expectRampPercent, config.RampPercentage, "unexpected ramp percentage")
				}
			}
		})
	}
}

func TestGetVersionConfig_ProgressiveRolloutEdgeCases(t *testing.T) {
	testCases := map[string]struct {
		strategy          temporaliov1alpha1.RolloutStrategy
		status            *temporaliov1alpha1.TemporalWorkerDeploymentStatus
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
				RampLastModifiedAt: &metav1.Time{
					Time: time.Now().Add(-4 * time.Hour), // Past all steps
				},
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
				RampLastModifiedAt: nil,
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
					RampingSince: nil, // Not ramping yet
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
				RampLastModifiedAt: &metav1.Time{
					Time: time.Now().Add(-1 * time.Hour), // Exactly at step boundary
				},
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
				RampLastModifiedAt: &metav1.Time{
					Time: time.Now().Add(-45 * time.Minute), // In second step
				},
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
				RampLastModifiedAt: &metav1.Time{
					Time: time.Now().Add(-2*time.Hour - 1*time.Second), // Just past boundary
				},
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
				},
			},
			expectConfig:      true,
			expectRampPercent: 0,
			expectSetCurrent:  true, // Past all steps, should be default
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			config := getVersionConfigDiff(testlogr.New(t), tc.strategy, tc.status, []byte("token"))
			assert.Equal(t, tc.expectConfig, config != nil, "unexpected version config presence")
			if tc.expectConfig {
				assert.Equal(t, tc.expectSetCurrent, config.SetCurrent, "unexpected set default value")
				if !tc.expectSetCurrent {
					assert.Equal(t, tc.expectRampPercent, config.RampPercentage, "unexpected ramp percentage")
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
			expectConfig: false, // Should not proceed with no task queues
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			config := getVersionConfigDiff(logr.Discard(), tc.strategy, tc.status, []byte("token"))
			if tc.expectConfig {
				assert.NotNil(t, config, "expected version config")
			} else {
				assert.Nil(t, config, "expected no version config")
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
		expectDeletes  int
		expectScales   int
		expectVersions []string // Expected version IDs for scaling
	}{
		{
			name: "multiple deprecated versions in different states",
			k8sState: &k8s.DeploymentState{
				Deployments: map[string]*appsv1.Deployment{
					"test/namespace.a": createDeploymentWithReplicas(3),
					"test/namespace.b": createDeploymentWithReplicas(3),
					"test/namespace.c": createDeploymentWithReplicas(1),
					"test/namespace.d": createDeploymentWithReplicas(0),
				},
			},
			config: &Config{
				TargetVersionID: "test/namespace.e",
				Status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
					DeprecatedVersions: []*temporaliov1alpha1.DeprecatedWorkerDeploymentVersion{
						{
							BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
								VersionID:  "test/namespace.a",
								Status:     temporaliov1alpha1.VersionStatusInactive,
								Deployment: &v1.ObjectReference{Name: "test-a"},
							},
						},
						{
							BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
								VersionID:  "test/namespace.b",
								Status:     temporaliov1alpha1.VersionStatusDraining,
								Deployment: &v1.ObjectReference{Name: "test-b"},
							},
						},
						{
							BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
								VersionID:  "test/namespace.c",
								Status:     temporaliov1alpha1.VersionStatusDrained,
								Deployment: &v1.ObjectReference{Name: "test-c"},
							},
							DrainedSince: &metav1.Time{
								Time: time.Now().Add(-2 * time.Hour), // Recently drained
							},
						},
						{
							BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
								VersionID:  "test/namespace.d",
								Status:     temporaliov1alpha1.VersionStatusDrained,
								Deployment: &v1.ObjectReference{Name: "test-d"},
							},
							DrainedSince: &metav1.Time{
								Time: time.Now().Add(-48 * time.Hour), // Long time drained
							},
						},
					},
				},
				Spec: &temporaliov1alpha1.TemporalWorkerDeploymentSpec{
					SunsetStrategy: temporaliov1alpha1.SunsetStrategy{
						ScaledownDelay: &metav1.Duration{Duration: 1 * time.Hour},
						DeleteDelay:    &metav1.Duration{Duration: 24 * time.Hour},
					},
				},
				RolloutStrategy: temporaliov1alpha1.RolloutStrategy{},
				Replicas:        5,
				ConflictToken:   []byte{},
			},
			expectDeletes:  1, // Only d should be deleted (drained long enough and scaled to 0)
			expectScales:   2, // a needs scaling up, c needs scaling down
			expectVersions: []string{"test/namespace.a", "test/namespace.c"},
		},
		{
			name: "draining version not scaled down before delay",
			k8sState: &k8s.DeploymentState{
				Deployments: map[string]*appsv1.Deployment{
					"test/namespace.a": createDeploymentWithReplicas(3),
				},
			},
			config: &Config{
				TargetVersionID: "test/namespace.b",
				Status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
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
				Spec: &temporaliov1alpha1.TemporalWorkerDeploymentSpec{
					SunsetStrategy: temporaliov1alpha1.SunsetStrategy{
						ScaledownDelay: &metav1.Duration{Duration: 2 * time.Hour},
					},
				},
				RolloutStrategy: temporaliov1alpha1.RolloutStrategy{},
				Replicas:        3,
				ConflictToken:   []byte{},
			},
			expectDeletes: 0,
			expectScales:  0, // Should not scale down yet
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := GeneratePlan(logr.Discard(), tc.k8sState, tc.config)
			require.NoError(t, err)

			assert.Equal(t, tc.expectDeletes, len(plan.DeleteDeployments), "unexpected number of deletes")
			assert.Equal(t, tc.expectScales, len(plan.ScaleDeployments), "unexpected number of scales")

			if tc.expectVersions != nil {
				scaledVersions := make([]string, 0, len(plan.ScaleDeployments))
				for ref := range plan.ScaleDeployments {
					// Extract version ID by looking up in config
					for _, v := range tc.config.Status.DeprecatedVersions {
						if v.Deployment != nil && v.Deployment.Name == ref.Name {
							scaledVersions = append(scaledVersions, v.VersionID)
							break
						}
					}
					if tc.config.Status.CurrentVersion != nil &&
						tc.config.Status.CurrentVersion.Deployment != nil &&
						tc.config.Status.CurrentVersion.Deployment.Name == ref.Name {
						scaledVersions = append(scaledVersions, tc.config.Status.CurrentVersion.VersionID)
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
		config    *Config
		taskQueue string
		versionID string
		expectID  string
	}{
		{
			name: "basic workflow ID generation",
			config: &Config{
				TargetVersionID: "test/namespace.123",
			},
			taskQueue: "my-queue",
			versionID: "test/namespace.123",
			expectID:  "test-test/namespace.123-my-queue",
		},
		{
			name: "workflow ID with special characters in queue name",
			config: &Config{
				TargetVersionID: "test/namespace.456",
			},
			taskQueue: "queue-with-dashes-and_underscores",
			versionID: "test/namespace.456",
			expectID:  "test-test/namespace.456-queue-with-dashes-and_underscores",
		},
		{
			name: "workflow ID with dots in version",
			config: &Config{
				TargetVersionID: "test/namespace.1.2.3",
			},
			taskQueue: "queue",
			versionID: "test/namespace.1.2.3",
			expectID:  "test-test/namespace.1.2.3-queue",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			id := getTestWorkflowID(tc.config, tc.taskQueue, tc.versionID)
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
