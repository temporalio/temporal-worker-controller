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
		{
			name: "gate workflow with empty task queues",
			config: &Config{
				DesiredVersionID: "test/namespace.123",
				Status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
					TargetVersion: &temporaliov1alpha1.WorkerDeploymentVersion{
						VersionID:  "test/namespace.123",
						Status:     temporaliov1alpha1.VersionStatusInactive,
						TaskQueues: []temporaliov1alpha1.TaskQueue{}, // Empty
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
			expectWorkflows: 0, // No task queues, no workflows
		},
		{
			name: "all test workflows already running",
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
							{
								TaskQueue: "queue2",
								Status:    temporaliov1alpha1.WorkflowExecutionStatusCompleted,
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

func TestGetVersionConfig_ProgressiveRolloutEdgeCases(t *testing.T) {
	testCases := []struct {
		name              string
		strategy          temporaliov1alpha1.RolloutStrategy
		status            *temporaliov1alpha1.TemporalWorkerDeploymentStatus
		expectConfig      bool
		expectSetDefault  bool
		expectRampPercent float32
	}{
		{
			name: "progressive rollout completes all steps",
			strategy: temporaliov1alpha1.RolloutStrategy{
				Strategy: temporaliov1alpha1.UpdateProgressive,
				Steps: []temporaliov1alpha1.RolloutStep{
					{RampPercentage: 25, PauseDuration: metav1.Duration{Duration: 1 * time.Hour}},
					{RampPercentage: 50, PauseDuration: metav1.Duration{Duration: 1 * time.Hour}},
					{RampPercentage: 75, PauseDuration: metav1.Duration{Duration: 1 * time.Hour}},
				},
			},
			status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
				TargetVersion: &temporaliov1alpha1.WorkerDeploymentVersion{
					VersionID: "test/namespace.123",
					Status:    temporaliov1alpha1.VersionStatusRamping,
					HealthySince: &metav1.Time{
						Time: time.Now().Add(-1 * time.Hour),
					},
					RampingSince: &metav1.Time{
						Time: time.Now().Add(-4 * time.Hour), // Past all steps
					},
				},
			},
			expectConfig:     true,
			expectSetDefault: true, // Should become default after all steps
		},
		{
			name: "progressive rollout with nil RampingSince",
			strategy: temporaliov1alpha1.RolloutStrategy{
				Strategy: temporaliov1alpha1.UpdateProgressive,
				Steps: []temporaliov1alpha1.RolloutStep{
					{RampPercentage: 25, PauseDuration: metav1.Duration{Duration: 1 * time.Hour}},
				},
			},
			status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
				TargetVersion: &temporaliov1alpha1.WorkerDeploymentVersion{
					VersionID:    "test/namespace.123",
					Status:       temporaliov1alpha1.VersionStatusInactive,
					HealthySince: &metav1.Time{Time: time.Now()},
					RampingSince: nil, // Not ramping yet
				},
			},
			expectConfig:      true,
			expectRampPercent: 25, // First step
			expectSetDefault:  false,
		},
		{
			name: "progressive rollout at exact step boundary",
			strategy: temporaliov1alpha1.RolloutStrategy{
				Strategy: temporaliov1alpha1.UpdateProgressive,
				Steps: []temporaliov1alpha1.RolloutStep{
					{RampPercentage: 25, PauseDuration: metav1.Duration{Duration: 1 * time.Hour}},
					{RampPercentage: 50, PauseDuration: metav1.Duration{Duration: 1 * time.Hour}},
				},
			},
			status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
				TargetVersion: &temporaliov1alpha1.WorkerDeploymentVersion{
					VersionID:    "test/namespace.123",
					Status:       temporaliov1alpha1.VersionStatusRamping,
					HealthySince: &metav1.Time{Time: time.Now()},
					RampingSince: &metav1.Time{
						Time: time.Now().Add(-2 * time.Hour), // Exactly at step boundary
					},
				},
			},
			expectConfig:      true,
			expectRampPercent: 0,    // When set as default, ramp is 0
			expectSetDefault:  true, // At exactly 2 hours, it sets as default
		},
		{
			name: "progressive rollout with zero ramp percentage step",
			strategy: temporaliov1alpha1.RolloutStrategy{
				Strategy: temporaliov1alpha1.UpdateProgressive,
				Steps: []temporaliov1alpha1.RolloutStep{
					{RampPercentage: 25, PauseDuration: metav1.Duration{Duration: 30 * time.Minute}},
					{RampPercentage: 0, PauseDuration: metav1.Duration{Duration: 30 * time.Minute}}, // Zero ramp
					{RampPercentage: 50, PauseDuration: metav1.Duration{Duration: 30 * time.Minute}},
				},
			},
			status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
				TargetVersion: &temporaliov1alpha1.WorkerDeploymentVersion{
					VersionID:    "test/namespace.123",
					Status:       temporaliov1alpha1.VersionStatusRamping,
					HealthySince: &metav1.Time{Time: time.Now()},
					RampingSince: &metav1.Time{
						Time: time.Now().Add(-45 * time.Minute), // In second step
					},
				},
			},
			expectConfig:      true,
			expectRampPercent: 25, // Should maintain previous ramp value
			expectSetDefault:  false,
		},
		{
			name: "progressive rollout just past exact boundary",
			strategy: temporaliov1alpha1.RolloutStrategy{
				Strategy: temporaliov1alpha1.UpdateProgressive,
				Steps: []temporaliov1alpha1.RolloutStep{
					{RampPercentage: 25, PauseDuration: metav1.Duration{Duration: 1 * time.Hour}},
					{RampPercentage: 50, PauseDuration: metav1.Duration{Duration: 1 * time.Hour}},
				},
			},
			status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
				TargetVersion: &temporaliov1alpha1.WorkerDeploymentVersion{
					VersionID:    "test/namespace.123",
					Status:       temporaliov1alpha1.VersionStatusRamping,
					HealthySince: &metav1.Time{Time: time.Now()},
					RampingSince: &metav1.Time{
						Time: time.Now().Add(-2*time.Hour - 1*time.Second), // Just past boundary
					},
				},
			},
			expectConfig:      true,
			expectRampPercent: 0,
			expectSetDefault:  true, // Past all steps, should be default
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			config := getVersionConfig(logr.Discard(), tc.strategy, tc.status)
			if tc.expectConfig {
				require.NotNil(t, config, "expected version config")
				assert.Equal(t, tc.expectSetDefault, config.SetDefault, "unexpected set default value")
				if !tc.expectSetDefault {
					assert.Equal(t, tc.expectRampPercent, config.RampPercentage, "unexpected ramp percentage")
				}
			} else {
				assert.Nil(t, config, "expected no version config")
			}
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
				TargetVersion: &temporaliov1alpha1.WorkerDeploymentVersion{
					VersionID:    "test/namespace.123",
					Status:       temporaliov1alpha1.VersionStatusInactive,
					HealthySince: &metav1.Time{Time: time.Now().Add(-1 * time.Hour)},
					TaskQueues: []temporaliov1alpha1.TaskQueue{
						{Name: "queue1"},
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
				TargetVersion: &temporaliov1alpha1.WorkerDeploymentVersion{
					VersionID:    "test/namespace.123",
					Status:       temporaliov1alpha1.VersionStatusInactive,
					HealthySince: &metav1.Time{Time: time.Now().Add(-1 * time.Hour)},
					TaskQueues: []temporaliov1alpha1.TaskQueue{
						{Name: "queue1"},
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
				TargetVersion: &temporaliov1alpha1.WorkerDeploymentVersion{
					VersionID:    "test/namespace.123",
					Status:       temporaliov1alpha1.VersionStatusInactive,
					HealthySince: &metav1.Time{Time: time.Now().Add(-1 * time.Hour)},
					TaskQueues: []temporaliov1alpha1.TaskQueue{
						{Name: "queue1"},
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
				TargetVersion: &temporaliov1alpha1.WorkerDeploymentVersion{
					VersionID:    "test/namespace.123",
					Status:       temporaliov1alpha1.VersionStatusInactive,
					HealthySince: &metav1.Time{Time: time.Now().Add(-1 * time.Hour)},
					TaskQueues: []temporaliov1alpha1.TaskQueue{
						{Name: "queue1"},
						{Name: "queue2"},
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
				TargetVersion: &temporaliov1alpha1.WorkerDeploymentVersion{
					VersionID:    "test/namespace.123",
					Status:       temporaliov1alpha1.VersionStatusInactive,
					HealthySince: &metav1.Time{Time: time.Now().Add(-1 * time.Hour)},
					TaskQueues:   []temporaliov1alpha1.TaskQueue{}, // Empty
				},
			},
			expectConfig: false, // Should not proceed with no task queues
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			config := getVersionConfig(logr.Discard(), tc.strategy, tc.status)
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
				DesiredVersionID: "test/namespace.e",
				Status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
					DeprecatedVersions: []*temporaliov1alpha1.WorkerDeploymentVersion{
						{
							VersionID:  "test/namespace.a",
							Status:     temporaliov1alpha1.VersionStatusCurrent,
							Deployment: &v1.ObjectReference{Name: "test-a"},
						},
						{
							VersionID:  "test/namespace.b",
							Status:     temporaliov1alpha1.VersionStatusDraining,
							Deployment: &v1.ObjectReference{Name: "test-b"},
						},
						{
							VersionID: "test/namespace.c",
							Status:    temporaliov1alpha1.VersionStatusDrained,
							DrainedSince: &metav1.Time{
								Time: time.Now().Add(-2 * time.Hour), // Recently drained
							},
							Deployment: &v1.ObjectReference{Name: "test-c"},
						},
						{
							VersionID: "test/namespace.d",
							Status:    temporaliov1alpha1.VersionStatusDrained,
							DrainedSince: &metav1.Time{
								Time: time.Now().Add(-48 * time.Hour), // Long time drained
							},
							Deployment: &v1.ObjectReference{Name: "test-d"},
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
				DesiredVersionID: "test/namespace.b",
				Status: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
					DeprecatedVersions: []*temporaliov1alpha1.WorkerDeploymentVersion{
						{
							VersionID: "test/namespace.a",
							Status:    temporaliov1alpha1.VersionStatusDrained,
							DrainedSince: &metav1.Time{
								Time: time.Now().Add(-30 * time.Minute), // Not long enough
							},
							Deployment: &v1.ObjectReference{Name: "test-a"},
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
					if tc.config.Status.DefaultVersion != nil &&
						tc.config.Status.DefaultVersion.Deployment != nil &&
						tc.config.Status.DefaultVersion.Deployment.Name == ref.Name {
						scaledVersions = append(scaledVersions, tc.config.Status.DefaultVersion.VersionID)
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
				DesiredVersionID: "test/namespace.123",
			},
			taskQueue: "my-queue",
			versionID: "test/namespace.123",
			expectID:  "test-test/namespace.123-my-queue",
		},
		{
			name: "workflow ID with special characters in queue name",
			config: &Config{
				DesiredVersionID: "test/namespace.456",
			},
			taskQueue: "queue-with-dashes-and_underscores",
			versionID: "test/namespace.456",
			expectID:  "test-test/namespace.456-queue-with-dashes-and_underscores",
		},
		{
			name: "workflow ID with dots in version",
			config: &Config{
				DesiredVersionID: "test/namespace.1.2.3",
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
