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

	temporaliov1alpha1 "github.com/DataDog/temporal-worker-controller/api/v1alpha1"
	"github.com/DataDog/temporal-worker-controller/internal/k8s"
)

func TestGeneratePlan(t *testing.T) {
	testCases := []struct {
		name           string
		k8sState       *k8s.DeploymentState
		config         *Config
		expectDelete   int
		expectScale    int
		expectCreate   bool
		expectWorkflow int
		expectConfig   bool
	}{
		{
			name: "empty state creates new deployment",
			k8sState: &k8s.DeploymentState{
				Deployments:       map[string]*appsv1.Deployment{},
				DeploymentsByTime: []*appsv1.Deployment{},
				DeploymentRefs:    map[string]*v1.ObjectReference{},
			},
			config: &Config{
				DesiredVersionID: "test/namespace.123",
				Status:           &temporaliov1alpha1.TemporalWorkerDeploymentStatus{},
				Spec:             &temporaliov1alpha1.TemporalWorkerDeploymentSpec{},
				RolloutStrategy:  temporaliov1alpha1.RolloutStrategy{},
				Replicas:         1,
				ConflictToken:    []byte{},
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
				DesiredVersionID: "test/namespace.456",
				Status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
					DeprecatedVersions: []*temporaliov1alpha1.WorkerDeploymentVersion{
						{
							VersionID: "test/namespace.123",
							Status:    temporaliov1alpha1.VersionStatusDrained,
							DrainedSince: &metav1.Time{
								Time: time.Now().Add(-24 * time.Hour),
							},
							Deployment: &v1.ObjectReference{Name: "test-123"},
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
				DesiredVersionID: "test/namespace.123",
				Status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
					DefaultVersion: &temporaliov1alpha1.WorkerDeploymentVersion{
						VersionID:  "test/namespace.123",
						Status:     temporaliov1alpha1.VersionStatusCurrent,
						Deployment: &v1.ObjectReference{Name: "test-123"},
					},
					TargetVersion: &temporaliov1alpha1.WorkerDeploymentVersion{
						VersionID:  "test/namespace.123",
						Status:     temporaliov1alpha1.VersionStatusCurrent,
						Deployment: &v1.ObjectReference{Name: "test-123"},
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
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := GeneratePlan(logr.Discard(), tc.k8sState, tc.config)
			require.NoError(t, err)

			assert.Equal(t, tc.expectDelete, len(plan.DeleteDeployments), "unexpected number of deletions")
			assert.Equal(t, tc.expectScale, len(plan.ScaleDeployments), "unexpected number of scales")
			assert.Equal(t, tc.expectCreate, plan.ShouldCreateDeployment, "unexpected create flag")
			assert.Equal(t, tc.expectWorkflow, len(plan.TestWorkflows), "unexpected number of test workflows")
			assert.Equal(t, tc.expectConfig != false, plan.VersionConfig != nil, "unexpected version config presence")
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
				DesiredVersionID: "test/namespace.456",
				Status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
					DeprecatedVersions: []*temporaliov1alpha1.WorkerDeploymentVersion{
						{
							VersionID: "test/namespace.123",
							Status:    temporaliov1alpha1.VersionStatusDrained,
							DrainedSince: &metav1.Time{
								Time: time.Now().Add(-24 * time.Hour),
							},
							Deployment: &v1.ObjectReference{Name: "test-123"},
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
			name: "not yet drained long enough",
			k8sState: &k8s.DeploymentState{
				Deployments: map[string]*appsv1.Deployment{
					"test/namespace.123": createDeploymentWithReplicas(0),
				},
			},
			config: &Config{
				DesiredVersionID: "test/namespace.456",
				Status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
					DeprecatedVersions: []*temporaliov1alpha1.WorkerDeploymentVersion{
						{
							VersionID: "test/namespace.123",
							Status:    temporaliov1alpha1.VersionStatusDrained,
							DrainedSince: &metav1.Time{
								Time: time.Now().Add(-1 * time.Hour),
							},
							Deployment: &v1.ObjectReference{Name: "test-123"},
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
				DesiredVersionID: "test/namespace.456",
				Status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
					DeprecatedVersions: []*temporaliov1alpha1.WorkerDeploymentVersion{
						{
							VersionID:  "test/namespace.123",
							Status:     temporaliov1alpha1.VersionStatusNotRegistered,
							Deployment: &v1.ObjectReference{Name: "test-123"},
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
				DesiredVersionID: "test/namespace.c", // Different desired version
				Status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
					TargetVersion: &temporaliov1alpha1.WorkerDeploymentVersion{
						VersionID:  "test/namespace.b",
						Status:     temporaliov1alpha1.VersionStatusInactive,
						Deployment: &v1.ObjectReference{Name: "test-b"},
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
				DesiredVersionID: "test/namespace.123",
				Status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
					DefaultVersion: &temporaliov1alpha1.WorkerDeploymentVersion{
						VersionID:  "test/namespace.123",
						Status:     temporaliov1alpha1.VersionStatusCurrent,
						Deployment: &v1.ObjectReference{Name: "test-123"},
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
				DesiredVersionID: "test/namespace.456",
				Status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
					DeprecatedVersions: []*temporaliov1alpha1.WorkerDeploymentVersion{
						{
							VersionID: "test/namespace.123",
							Status:    temporaliov1alpha1.VersionStatusDrained,
							DrainedSince: &metav1.Time{
								Time: time.Now().Add(-24 * time.Hour),
							},
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
				DesiredVersionID: "test/namespace.b",
				Status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
					DeprecatedVersions: []*temporaliov1alpha1.WorkerDeploymentVersion{
						{
							VersionID:  "test/namespace.a",
							Status:     temporaliov1alpha1.VersionStatusInactive,
							Deployment: &v1.ObjectReference{Name: "test-a"},
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
				DesiredVersionID: "test/namespace.b",
				Status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
					TargetVersion: &temporaliov1alpha1.WorkerDeploymentVersion{
						VersionID:  "test/namespace.b",
						Status:     temporaliov1alpha1.VersionStatusRamping,
						Deployment: &v1.ObjectReference{Name: "test-b"},
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
				DesiredVersionID: "test/namespace.b",
				Status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
					DeprecatedVersions: []*temporaliov1alpha1.WorkerDeploymentVersion{
						{
							VersionID:  "test/namespace.a",
							Status:     temporaliov1alpha1.VersionStatusCurrent,
							Deployment: &v1.ObjectReference{Name: "test-a"},
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
				DesiredVersionID: "test/namespace.a",
				Status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
					DefaultVersion: &temporaliov1alpha1.WorkerDeploymentVersion{
						VersionID:  "test/namespace.a",
						Status:     temporaliov1alpha1.VersionStatusCurrent,
						Deployment: &v1.ObjectReference{Name: "test-a"},
					},
					DeprecatedVersions: []*temporaliov1alpha1.WorkerDeploymentVersion{
						{
							VersionID: "test/namespace.b",
							Status:    temporaliov1alpha1.VersionStatusDrained,
							DrainedSince: &metav1.Time{
								Time: time.Now().Add(-1 * time.Hour), // Recently drained
							},
							Deployment: &v1.ObjectReference{Name: "test-b"},
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
				DesiredVersionID: "test/namespace.123",
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
				DesiredVersionID: "test/namespace.123",
				Status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
					TargetVersion: &temporaliov1alpha1.WorkerDeploymentVersion{
						VersionID:  "test/namespace.123",
						Status:     temporaliov1alpha1.VersionStatusInactive,
						Deployment: &v1.ObjectReference{Name: "test-123"},
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
				DesiredVersionID: "test/namespace.b",
				Status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
					TargetVersion: &temporaliov1alpha1.WorkerDeploymentVersion{
						VersionID:  "test/namespace.b",
						Status:     temporaliov1alpha1.VersionStatusInactive,
						Deployment: nil,
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
				DesiredVersionID: "test/namespace.123",
				Status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
					TargetVersion: &temporaliov1alpha1.WorkerDeploymentVersion{
						VersionID: "test/namespace.123",
						Status:    temporaliov1alpha1.VersionStatusInactive,
						TaskQueues: []temporaliov1alpha1.TaskQueue{
							{Name: "queue1"},
							{Name: "queue2"},
						},
						TestWorkflows: []temporaliov1alpha1.WorkflowExecution{
							{
								TaskQueue: "queue1",
								Status:    temporaliov1alpha1.WorkflowExecutionStatusRunning,
							},
						},
					},
					DefaultVersion: &temporaliov1alpha1.WorkerDeploymentVersion{
						VersionID: "test/namespace.456",
						Status:    temporaliov1alpha1.VersionStatusCurrent,
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
				DesiredVersionID: "test/namespace.123",
				Status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
					TargetVersion: &temporaliov1alpha1.WorkerDeploymentVersion{
						VersionID: "test/namespace.123",
						Status:    temporaliov1alpha1.VersionStatusInactive,
						TaskQueues: []temporaliov1alpha1.TaskQueue{
							{Name: "queue1"},
							{Name: "queue2"},
						},
					},
					DefaultVersion: &temporaliov1alpha1.WorkerDeploymentVersion{
						VersionID: "test/namespace.456",
						Status:    temporaliov1alpha1.VersionStatusCurrent,
					},
				},
				Spec:            &temporaliov1alpha1.TemporalWorkerDeploymentSpec{},
				RolloutStrategy: temporaliov1alpha1.RolloutStrategy{},
				Replicas:        1,
				ConflictToken:   []byte{},
			},
			expectWorkflows: 0,
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
		name             string
		strategy         temporaliov1alpha1.RolloutStrategy
		status           *temporaliov1alpha1.TemporalWorkerDeploymentStatus
		conflictToken    []byte
		expectConfig     bool
		expectSetDefault bool
	}{
		{
			name: "all at once strategy",
			strategy: temporaliov1alpha1.RolloutStrategy{
				Strategy: temporaliov1alpha1.UpdateAllAtOnce,
			},
			status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
				TargetVersion: &temporaliov1alpha1.WorkerDeploymentVersion{
					VersionID: "test/namespace.123",
					Status:    temporaliov1alpha1.VersionStatusInactive,
					HealthySince: &metav1.Time{
						Time: time.Now().Add(-1 * time.Hour),
					},
				},
				DefaultVersion: &temporaliov1alpha1.WorkerDeploymentVersion{
					VersionID: "test/namespace.456",
					Status:    temporaliov1alpha1.VersionStatusCurrent,
				},
			},
			conflictToken:    []byte("token"),
			expectConfig:     true,
			expectSetDefault: true,
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
				TargetVersion: &temporaliov1alpha1.WorkerDeploymentVersion{
					VersionID: "test/namespace.123",
					Status:    temporaliov1alpha1.VersionStatusInactive,
					HealthySince: &metav1.Time{
						Time: time.Now().Add(-30 * time.Minute),
					},
					RampPercentage: func() *float32 { f := float32(0); return &f }(),
				},
				DefaultVersion: &temporaliov1alpha1.WorkerDeploymentVersion{
					VersionID: "test/namespace.456",
					Status:    temporaliov1alpha1.VersionStatusCurrent,
				},
			},
			conflictToken:    []byte("token"),
			expectConfig:     true,
			expectSetDefault: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			config := getVersionConfigDiff(logr.Discard(), tc.strategy, tc.status, tc.conflictToken)
			if tc.expectConfig {
				assert.NotNil(t, config, "expected version config")
				assert.Equal(t, tc.expectSetDefault, config.SetDefault, "unexpected set default value")
			} else {
				assert.Nil(t, config, "expected no version config")
			}
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
