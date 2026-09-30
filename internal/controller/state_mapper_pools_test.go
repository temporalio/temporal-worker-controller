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

func mapperPoolState(deployments ...*appsv1.Deployment) *k8s.DeploymentState {
	state := &k8s.DeploymentState{
		Deployments:     map[string]*appsv1.Deployment{},
		DeploymentRefs:  map[string]*corev1.ObjectReference{},
		PoolDeployments: map[string]map[string]*appsv1.Deployment{},
	}
	for _, d := range deployments {
		buildID := d.Labels[k8s.BuildIDLabel]
		pool := k8s.PoolName(d)
		if state.PoolDeployments[buildID] == nil {
			state.PoolDeployments[buildID] = map[string]*appsv1.Deployment{}
		}
		state.PoolDeployments[buildID][pool] = d
		if pool == temporaliov1alpha1.DefaultPoolName {
			state.Deployments[buildID] = d
			state.DeploymentRefs[buildID] = &corev1.ObjectReference{Name: d.Name}
		}
	}
	return state
}

func TestMapToStatus_PoolDeployments(t *testing.T) {
	earlier := metav1.NewTime(time.Now().Add(-2 * time.Hour))
	later := metav1.NewTime(time.Now().Add(-time.Hour))

	t.Run("version with only named pools left is still listed", func(t *testing.T) {
		state := mapperPoolState(mapperPoolDeployment("old", "activities", nil, 0))
		temporalState := &temporal.TemporalWorkerState{Versions: map[string]*temporal.VersionInfo{}}

		status := newStateMapper(state, temporalState, "ns/worker").mapToStatus("new")

		require.Len(t, status.DeprecatedVersions, 1)
		assert.Equal(t, "old", status.DeprecatedVersions[0].BuildID)
		assert.Nil(t, status.DeprecatedVersions[0].Deployment, "the default pool's reference stays empty")
	})

	t.Run("version is healthy once every pool is available", func(t *testing.T) {
		state := mapperPoolState(
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
		state := mapperPoolState(
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
		state := mapperPoolState(
			mapperPoolDeployment("old", "", nil, 0),
			mapperPoolDeployment("old", "activities", nil, 2),
		)
		temporalState := &temporal.TemporalWorkerState{Versions: map[string]*temporal.VersionInfo{
			"old": {BuildID: "old", Status: temporaliov1alpha1.VersionStatusDrained},
		}}

		assert.False(t, newStateMapper(state, temporalState, "ns/worker").mapDeprecatedWorkerDeploymentVersionByBuildID("old").EligibleForDeletion)
	})
}
