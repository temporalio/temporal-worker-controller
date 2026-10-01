// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package controller

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	"github.com/temporalio/temporal-worker-controller/internal/k8s"
	"github.com/temporalio/temporal-worker-controller/internal/temporal"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func mapperPoolDeployment(buildID, pool string, availableSince *metav1.Time, replicas int32) *appsv1.Deployment {
	d := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "worker-" + buildID, Labels: map[string]string{k8s.BuildIDLabel: buildID}},
		Status:     appsv1.DeploymentStatus{Replicas: replicas},
	}
	if pool != "" {
		d.Name += "-" + pool
		d.Labels[k8s.PoolLabel] = pool
	}
	if availableSince != nil {
		d.Status.Conditions = []appsv1.DeploymentCondition{{
			Type: appsv1.DeploymentAvailable, Status: corev1.ConditionTrue, LastTransitionTime: *availableSince,
		}}
	}
	return d
}

func TestMapToStatus_PoolDeployments(t *testing.T) {
	earlier := metav1.NewTime(time.Now().Add(-2 * time.Hour))
	later := metav1.NewTime(time.Now().Add(-time.Hour))

	t.Run("version with only named pools left is still listed", func(t *testing.T) {
		state := k8s.NewDeploymentState(mapperPoolDeployment("old", "activities", nil, 0))
		temporalState := &temporal.TemporalWorkerState{Versions: map[string]*temporal.VersionInfo{}}

		status := newStateMapper(state, temporalState, "ns/worker").mapToStatus("new")

		require.Len(t, status.DeprecatedVersions, 1)
		assert.Equal(t, "old", status.DeprecatedVersions[0].BuildID)
		assert.Nil(t, status.DeprecatedVersions[0].Deployment, "the default pool's reference stays empty")
	})

	t.Run("version is healthy once every pool is available", func(t *testing.T) {
		state := k8s.NewDeploymentState(
			mapperPoolDeployment("v1", "", &earlier, 1),
			mapperPoolDeployment("v1", "activities", &later, 1),
		)
		temporalState := &temporal.TemporalWorkerState{Versions: map[string]*temporal.VersionInfo{}}

		target := newStateMapper(state, temporalState, "ns/worker").mapTargetWorkerDeploymentVersionByBuildID("v1")

		require.NotNil(t, target.HealthySince)
		assert.Equal(t, later.Unix(), target.HealthySince.Unix(), "healthy since the last pool became available")
		assert.Equal(t, "worker-v1", target.Deployment.Name)
	})

	t.Run("version is not healthy while any pool is unavailable", func(t *testing.T) {
		state := k8s.NewDeploymentState(
			mapperPoolDeployment("v1", "", &earlier, 1),
			mapperPoolDeployment("v1", "activities", nil, 1),
		)
		temporalState := &temporal.TemporalWorkerState{Versions: map[string]*temporal.VersionInfo{}}
		mapper := newStateMapper(state, temporalState, "ns/worker")

		assert.Nil(t, mapper.mapTargetWorkerDeploymentVersionByBuildID("v1").HealthySince)
		assert.Nil(t, mapper.mapCurrentWorkerDeploymentVersionByBuildID("v1").HealthySince)
		assert.Nil(t, mapper.mapDeprecatedWorkerDeploymentVersionByBuildID("v1").HealthySince)
	})

	t.Run("drained version is not eligible for deletion while any pool has pods", func(t *testing.T) {
		state := k8s.NewDeploymentState(
			mapperPoolDeployment("old", "", nil, 0),
			mapperPoolDeployment("old", "activities", nil, 2),
		)
		temporalState := &temporal.TemporalWorkerState{Versions: map[string]*temporal.VersionInfo{
			"old": {BuildID: "old", Status: temporaliov1alpha1.VersionStatusDrained},
		}}

		assert.False(t, newStateMapper(state, temporalState, "ns/worker").mapDeprecatedWorkerDeploymentVersionByBuildID("old").EligibleForDeletion)
	})
}

func TestMapToStatus_PoolStatus(t *testing.T) {
	earlier := metav1.NewTime(time.Now().Add(-2 * time.Hour))
	defaultPool := mapperPoolDeployment("v1", "", &earlier, 1)
	defaultPool.Labels[k8s.PoolLabel] = temporaliov1alpha1.DefaultPoolName
	state := k8s.NewDeploymentState(
		mapperPoolDeployment("v1", "zeta", nil, 1),
		defaultPool,
		mapperPoolDeployment("v1", "alpha", &earlier, 1),
		mapperPoolDeployment("single", "", &earlier, 1),
	)
	temporalState := &temporal.TemporalWorkerState{Versions: map[string]*temporal.VersionInfo{}}
	mapper := newStateMapper(state, temporalState, "ns/worker")

	target := mapper.mapTargetWorkerDeploymentVersionByBuildID("v1")
	require.Len(t, target.Pools, 3)
	assert.Equal(t, []string{"alpha", temporaliov1alpha1.DefaultPoolName, "zeta"},
		[]string{target.Pools[0].Name, target.Pools[1].Name, target.Pools[2].Name}, "sorted so status doesn't churn")
	assert.Equal(t, "worker-v1-alpha", target.Pools[0].Deployment.Name)
	assert.Equal(t, earlier.Unix(), target.Pools[0].HealthySince.Unix())
	assert.Nil(t, target.Pools[2].HealthySince)

	assert.Empty(t, mapper.mapTargetWorkerDeploymentVersionByBuildID("single").Pools, "single-pool versions keep their status shape")
}

func TestMapTargetVersion_MultiPoolHealth(t *testing.T) {
	available := metav1.NewTime(time.Now().Add(-time.Hour))
	pooled := func(pool string, availableReplicas int32) *appsv1.Deployment {
		d := mapperPoolDeployment("v1", pool, &available, availableReplicas)
		d.Status.AvailableReplicas = availableReplicas
		return d
	}
	spec := func(activitiesReplicas *int32) *temporaliov1alpha1.WorkerDeploymentSpec {
		return &temporaliov1alpha1.WorkerDeploymentSpec{
			Pools: []temporaliov1alpha1.WorkerPool{
				{Name: "workflows"},
				{Name: "activities", Deployment: appsv1.DeploymentSpec{Replicas: activitiesReplicas}},
			},
		}
	}
	temporalState := &temporal.TemporalWorkerState{Versions: map[string]*temporal.VersionInfo{}}

	tests := []struct {
		name        string
		deployments []*appsv1.Deployment
		spec        *temporaliov1alpha1.WorkerDeploymentSpec
		wantHealthy bool
	}{
		{name: "every pool available with ready replicas", deployments: []*appsv1.Deployment{pooled("workflows", 1), pooled("activities", 1)}, spec: spec(nil), wantHealthy: true},
		{name: "a pool is not created yet", deployments: []*appsv1.Deployment{pooled("workflows", 1)}, spec: spec(nil)},
		{name: "a pool is available with no replicas", deployments: []*appsv1.Deployment{pooled("workflows", 1), pooled("activities", 0)}, spec: spec(nil)},
		{name: "a pool scaled to zero on purpose", deployments: []*appsv1.Deployment{pooled("workflows", 1), pooled("activities", 0)}, spec: spec(ptr(int32(0))), wantHealthy: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mapper := newStateMapper(k8s.NewDeploymentState(tt.deployments...), temporalState, "ns/worker")
			mapper.targetSpec = tt.spec

			assert.Equal(t, tt.wantHealthy, mapper.mapTargetWorkerDeploymentVersionByBuildID("v1").HealthySince != nil)
		})
	}
}

func TestMapTargetVersion_PoolHealthMatchesVersionHealth(t *testing.T) {
	available := metav1.NewTime(time.Now().Add(-time.Hour))
	def := mapperPoolDeployment("v1", "workflows", &available, 1)
	def.Status.AvailableReplicas = 1
	idle := mapperPoolDeployment("v1", "activities", &available, 0)
	temporalState := &temporal.TemporalWorkerState{Versions: map[string]*temporal.VersionInfo{}}

	for name, tc := range map[string]struct {
		replicas    *int32
		wantHealthy bool
	}{
		"available with no replicas": {},
		"scaled to zero on purpose":  {replicas: ptr(int32(0)), wantHealthy: true},
	} {
		t.Run(name, func(t *testing.T) {
			mapper := newStateMapper(k8s.NewDeploymentState(def, idle), temporalState, "ns/worker")
			mapper.targetSpec = &temporaliov1alpha1.WorkerDeploymentSpec{
				Pools: []temporaliov1alpha1.WorkerPool{{Name: "workflows"}, {Name: "activities", Deployment: appsv1.DeploymentSpec{Replicas: tc.replicas}}},
			}

			target := mapper.mapTargetWorkerDeploymentVersionByBuildID("v1")
			require.Len(t, target.Pools, 2)
			assert.Equal(t, "activities", target.Pools[0].Name)
			assert.Equal(t, tc.wantHealthy, target.Pools[0].HealthySince != nil)
			assert.Equal(t, tc.wantHealthy, target.HealthySince != nil)
		})
	}
}

func TestMapTargetVersion_CurrentTargetIgnoresPoolReplicaRule(t *testing.T) {
	available := metav1.NewTime(time.Now().Add(-time.Hour))
	def := mapperPoolDeployment("v1", "workflows", &available, 1)
	def.Status.AvailableReplicas = 1
	idle := mapperPoolDeployment("v1", "activities", &available, 0)
	temporalState := &temporal.TemporalWorkerState{CurrentBuildID: "v1", Versions: map[string]*temporal.VersionInfo{}}
	mapper := newStateMapper(k8s.NewDeploymentState(def, idle), temporalState, "ns/worker")
	mapper.targetSpec = &temporaliov1alpha1.WorkerDeploymentSpec{
		Pools: []temporaliov1alpha1.WorkerPool{{Name: "workflows"}, {Name: "activities"}},
	}

	assert.NotNil(t, mapper.mapTargetWorkerDeploymentVersionByBuildID("v1").HealthySince,
		"an autoscaler may scale a current pool to zero; that must not block clearing a stale ramp")
}
