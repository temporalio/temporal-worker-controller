// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package planner

import (
	"context"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	"github.com/temporalio/temporal-worker-controller/internal/k8s"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func poolDeployment(buildID, pool string, replicas int32) *appsv1.Deployment {
	d := createDeploymentWithDefaultConnectionSpecHash(replicas)
	d.Name = "wd-" + buildID
	d.Labels = map[string]string{k8s.BuildIDLabel: buildID}
	if pool != "" {
		d.Name += "-" + pool
		d.Labels[k8s.PoolLabel] = pool
	}
	return d
}

func poolState(deployments ...*appsv1.Deployment) *k8s.DeploymentState {
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

func names(ds []*appsv1.Deployment) []string {
	out := make([]string, 0, len(ds))
	for _, d := range ds {
		out = append(out, d.Name)
	}
	return out
}

func scaleNames(scales map[*corev1.ObjectReference]uint32) map[string]uint32 {
	out := make(map[string]uint32, len(scales))
	for ref, replicas := range scales {
		out[ref.Name] = replicas
	}
	return out
}

func deprecatedVersion(buildID string, status temporaliov1alpha1.VersionStatus, withDefault bool) *temporaliov1alpha1.DeprecatedWorkerDeploymentVersion {
	v := &temporaliov1alpha1.DeprecatedWorkerDeploymentVersion{
		BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{BuildID: buildID, Status: status},
	}
	if withDefault {
		v.Deployment = &corev1.ObjectReference{Name: "wd-" + buildID}
	}
	return v
}

func sunsetSpec(t *testing.T) *temporaliov1alpha1.WorkerDeploymentSpec {
	t.Helper()
	spec := &temporaliov1alpha1.WorkerDeploymentSpec{}
	require.NoError(t, spec.Default(context.Background()))
	spec.SunsetStrategy.ScaledownDelay = &metav1.Duration{}
	spec.SunsetStrategy.DeleteDelay = &metav1.Duration{}
	return spec
}

func TestGetDeleteDeployments_Pools(t *testing.T) {
	drainedAt := &metav1.Time{Time: time.Now().Add(-time.Hour)}

	t.Run("drained version deletes every pool with the default last", func(t *testing.T) {
		state := poolState(poolDeployment("old", "", 0), poolDeployment("old", "activities", 0))
		v := deprecatedVersion("old", temporaliov1alpha1.VersionStatusDrained, true)
		v.DrainedSince = drainedAt
		v.EligibleForDeletion = true
		status := &temporaliov1alpha1.WorkerDeploymentStatus{DeprecatedVersions: []*temporaliov1alpha1.DeprecatedWorkerDeploymentVersion{v}}

		assert.Equal(t, []string{"wd-old-activities", "wd-old"}, names(getDeleteDeployments(state, status, sunsetSpec(t), true)))
	})

	t.Run("drained version waits until every pool is scaled to zero", func(t *testing.T) {
		state := poolState(poolDeployment("old", "", 0), poolDeployment("old", "activities", 1))
		v := deprecatedVersion("old", temporaliov1alpha1.VersionStatusDrained, true)
		v.DrainedSince = drainedAt
		v.EligibleForDeletion = true
		status := &temporaliov1alpha1.WorkerDeploymentStatus{DeprecatedVersions: []*temporaliov1alpha1.DeprecatedWorkerDeploymentVersion{v}}

		assert.Empty(t, getDeleteDeployments(state, status, sunsetSpec(t), true))
	})

	t.Run("inactive version waits until every pool has no pods", func(t *testing.T) {
		busy := poolDeployment("old", "activities", 0)
		busy.Status.Replicas = 1
		state := poolState(poolDeployment("old", "", 0), busy)
		status := &temporaliov1alpha1.WorkerDeploymentStatus{DeprecatedVersions: []*temporaliov1alpha1.DeprecatedWorkerDeploymentVersion{
			deprecatedVersion("old", temporaliov1alpha1.VersionStatusInactive, true),
		}}

		assert.Empty(t, getDeleteDeployments(state, status, sunsetSpec(t), true))
	})

	t.Run("named pools are deleted after the default pool is already gone", func(t *testing.T) {
		state := poolState(poolDeployment("old", "activities", 0))
		status := &temporaliov1alpha1.WorkerDeploymentStatus{DeprecatedVersions: []*temporaliov1alpha1.DeprecatedWorkerDeploymentVersion{
			deprecatedVersion("old", temporaliov1alpha1.VersionStatusNotRegistered, false),
		}}

		assert.Equal(t, []string{"wd-old-activities"}, names(getDeleteDeployments(state, status, sunsetSpec(t), true)))
	})
}

func TestGetScaleDeployments_DeprecatedPools(t *testing.T) {
	status := func(v *temporaliov1alpha1.DeprecatedWorkerDeploymentVersion) *temporaliov1alpha1.WorkerDeploymentStatus {
		return &temporaliov1alpha1.WorkerDeploymentStatus{
			TargetVersion: temporaliov1alpha1.TargetWorkerDeploymentVersion{
				BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{BuildID: "new"},
			},
			DeprecatedVersions: []*temporaliov1alpha1.DeprecatedWorkerDeploymentVersion{v},
		}
	}

	t.Run("drained version scales every pool to zero", func(t *testing.T) {
		v := deprecatedVersion("old", temporaliov1alpha1.VersionStatusDrained, true)
		v.DrainedSince = &metav1.Time{Time: time.Now().Add(-time.Hour)}
		state := poolState(poolDeployment("old", "", 2), poolDeployment("old", "activities", 3))

		assert.Equal(t, map[string]uint32{"wd-old": 0, "wd-old-activities": 0},
			scaleNames(getScaleDeployments(logr.Discard(), state, status(v), sunsetSpec(t))))
	})

	t.Run("inactive non-target version scales every pool to zero", func(t *testing.T) {
		state := poolState(poolDeployment("old", "", 2), poolDeployment("old", "activities", 3))

		assert.Equal(t, map[string]uint32{"wd-old": 0, "wd-old-activities": 0},
			scaleNames(getScaleDeployments(logr.Discard(), state, status(deprecatedVersion("old", temporaliov1alpha1.VersionStatusInactive, true)), sunsetSpec(t))))
	})

	t.Run("draining version scales every pool at zero back up", func(t *testing.T) {
		state := poolState(poolDeployment("old", "", 0), poolDeployment("old", "activities", 0))

		assert.Equal(t, map[string]uint32{"wd-old": 1, "wd-old-activities": 1},
			scaleNames(getScaleDeployments(logr.Discard(), state, status(deprecatedVersion("old", temporaliov1alpha1.VersionStatusDraining, true)), sunsetSpec(t))))
	})

	t.Run("named pools are scaled after the default pool is already gone", func(t *testing.T) {
		state := poolState(poolDeployment("old", "activities", 3))

		assert.Equal(t, map[string]uint32{"wd-old-activities": 0},
			scaleNames(getScaleDeployments(logr.Discard(), state, status(deprecatedVersion("old", temporaliov1alpha1.VersionStatusInactive, false)), sunsetSpec(t))))
	})
}

func TestGetUpdateDeployments_ConnectionDriftUpdatesEveryPool(t *testing.T) {
	state := poolState(poolDeployment("v1", "", 1), poolDeployment("v1", "activities", 1))
	status := &temporaliov1alpha1.WorkerDeploymentStatus{
		TargetVersion: temporaliov1alpha1.TargetWorkerDeploymentVersion{
			BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{BuildID: "v1"},
		},
	}
	spec := &temporaliov1alpha1.WorkerDeploymentSpec{}

	updates := getUpdateDeployments(state, status, spec, createOutdatedConnectionSpec())

	assert.ElementsMatch(t, []string{"wd-v1", "wd-v1-activities"}, names(updates))
}
