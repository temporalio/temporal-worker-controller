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

func poolTemplate(image string) corev1.PodTemplateSpec {
	return corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "worker", Image: image}}}}
}

// pooledSpec returns a spec with a default pool and the named pools, each running image.
func pooledSpec(t *testing.T, image string, replicas map[string]*int32, pools ...string) *temporaliov1alpha1.WorkerDeploymentSpec {
	t.Helper()
	spec := sunsetSpec(t)
	spec.Deployment = &appsv1.DeploymentSpec{Replicas: replicas[temporaliov1alpha1.DefaultPoolName], Template: poolTemplate(image)}
	for _, name := range pools {
		spec.Pools = append(spec.Pools, temporaliov1alpha1.WorkerPool{
			Name:       name,
			Deployment: appsv1.DeploymentSpec{Replicas: replicas[name], Template: poolTemplate(image)},
		})
	}
	return spec
}

func labelledPoolDeployment(buildID, pool string, replicas int32) *appsv1.Deployment {
	d := poolDeployment(buildID, pool, replicas)
	if pool == "" {
		d.Labels[k8s.PoolLabel] = temporaliov1alpha1.DefaultPoolName
	}
	return d
}

func targetStatus(buildID string) *temporaliov1alpha1.WorkerDeploymentStatus {
	return &temporaliov1alpha1.WorkerDeploymentStatus{
		TargetVersion: temporaliov1alpha1.TargetWorkerDeploymentVersion{
			BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{BuildID: buildID},
		},
	}
}

func TestGetCreateDeploymentPools(t *testing.T) {
	cappedStatus := func() *temporaliov1alpha1.WorkerDeploymentStatus {
		s := targetStatus("new")
		s.DeprecatedVersions = []*temporaliov1alpha1.DeprecatedWorkerDeploymentVersion{deprecatedVersion("old", temporaliov1alpha1.VersionStatusDraining, true)}
		return s
	}

	tests := []struct {
		name         string
		state        *k8s.DeploymentState
		status       *temporaliov1alpha1.WorkerDeploymentStatus
		spec         *temporaliov1alpha1.WorkerDeploymentSpec
		maxVersions  int32
		wantPools    []string
		wantLabelled bool
		wantBlocked  bool
	}{
		{
			name:        "new single-pool version",
			state:       poolState(),
			status:      targetStatus("new"),
			spec:        pooledSpec(t, "worker:v1", nil),
			maxVersions: 75,
			wantPools:   []string{temporaliov1alpha1.DefaultPoolName},
		},
		{
			name:         "new multi-pool version creates every pool, labelled",
			state:        poolState(),
			status:       targetStatus("new"),
			spec:         pooledSpec(t, "worker:v1", nil, "batch", "activities"),
			maxVersions:  75,
			wantPools:    []string{temporaliov1alpha1.DefaultPoolName, "activities", "batch"},
			wantLabelled: true,
		},
		{
			name:        "new version is held back at the version cap",
			state:       poolState(),
			status:      cappedStatus(),
			spec:        pooledSpec(t, "worker:v1", nil, "activities"),
			maxVersions: 1,
		},
		{
			name:         "pool added to an existing multi-pool version ignores the version cap",
			state:        poolState(labelledPoolDeployment("new", "", 1), labelledPoolDeployment("new", "activities", 1)),
			status:       cappedStatus(),
			spec:         pooledSpec(t, "worker:v1", nil, "activities", "batch"),
			maxVersions:  1,
			wantPools:    []string{"batch"},
			wantLabelled: true,
		},
		{
			name:         "missing default pool is recreated labelled",
			state:        poolState(labelledPoolDeployment("new", "activities", 1)),
			status:       targetStatus("new"),
			spec:         pooledSpec(t, "worker:v1", nil, "activities"),
			maxVersions:  75,
			wantPools:    []string{temporaliov1alpha1.DefaultPoolName},
			wantLabelled: true,
		},
		{
			name:        "complete version creates nothing",
			state:       poolState(labelledPoolDeployment("new", "", 1), labelledPoolDeployment("new", "activities", 1)),
			status:      targetStatus("new"),
			spec:        pooledSpec(t, "worker:v1", nil, "activities"),
			maxVersions: 75,
			wantPools:   nil,
		},
		{
			name:        "pools added to an unlabelled version are blocked",
			state:       poolState(poolDeployment("new", "", 1)),
			status:      targetStatus("new"),
			spec:        pooledSpec(t, "worker:v1", nil, "activities"),
			maxVersions: 75,
			wantBlocked: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := getCreateDeploymentPools(tt.state, tt.status, tt.spec, tt.maxVersions)
			assert.Equal(t, tt.wantPools, got.pools)
			assert.Equal(t, tt.wantLabelled, got.labelDefault)
			assert.Equal(t, tt.wantBlocked, got.blockedReason != "")
		})
	}
}

func TestGetDeletePoolDeployments(t *testing.T) {
	t.Run("pools removed from the spec are deleted from the target version", func(t *testing.T) {
		state := poolState(
			labelledPoolDeployment("v1", "", 1),
			labelledPoolDeployment("v1", "activities", 1),
			labelledPoolDeployment("v1", "batch", 1),
			labelledPoolDeployment("old", "batch", 1),
		)
		assert.Equal(t, []string{"wd-v1-batch"}, names(getDeletePoolDeployments(state, targetStatus("v1"), pooledSpec(t, "worker:v1", nil, "activities"))))
	})

	t.Run("removing every pool keeps the default pool", func(t *testing.T) {
		state := poolState(labelledPoolDeployment("v1", "", 1), labelledPoolDeployment("v1", "activities", 1))
		assert.Equal(t, []string{"wd-v1-activities"}, names(getDeletePoolDeployments(state, targetStatus("v1"), pooledSpec(t, "worker:v1", nil))))
	})

	t.Run("single-pool version is left alone", func(t *testing.T) {
		state := poolState(poolDeployment("v1", "", 1))
		assert.Empty(t, getDeletePoolDeployments(state, targetStatus("v1"), pooledSpec(t, "worker:v1", nil)))
	})
}

func TestGetScaleDeployments_CurrentAndTargetPools(t *testing.T) {
	replicas := map[string]*int32{temporaliov1alpha1.DefaultPoolName: int32Ptr(2), "activities": int32Ptr(4)}

	t.Run("current version scales each pool to its own replicas", func(t *testing.T) {
		state := poolState(
			labelledPoolDeployment("v1", "", 1),
			labelledPoolDeployment("v1", "activities", 1),
			labelledPoolDeployment("v1", "retired", 1),
		)
		status := targetStatus("v1")
		status.TargetVersion.Deployment = &corev1.ObjectReference{Name: "wd-v1"}
		status.CurrentVersion = &temporaliov1alpha1.CurrentWorkerDeploymentVersion{BaseWorkerDeploymentVersion: status.TargetVersion.BaseWorkerDeploymentVersion}

		assert.Equal(t, map[string]uint32{"wd-v1": 2, "wd-v1-activities": 4},
			scaleNames(getScaleDeployments(logr.Discard(), state, status, pooledSpec(t, "worker:v1", replicas, "activities"))),
			"a pool no longer in the spec is left alone")
	})

	t.Run("target pool managed by a scaler is scaled up from zero", func(t *testing.T) {
		state := poolState(labelledPoolDeployment("v2", "", 2), labelledPoolDeployment("v2", "autoscaled", 0))
		status := targetStatus("v2")
		status.TargetVersion.Deployment = &corev1.ObjectReference{Name: "wd-v2"}

		assert.Equal(t, map[string]uint32{"wd-v2-autoscaled": 1},
			scaleNames(getScaleDeployments(logr.Discard(), state, status, pooledSpec(t, "worker:v1", replicas, "autoscaled"))))
	})

	t.Run("draining pool at zero is scaled up to its own replicas", func(t *testing.T) {
		state := poolState(labelledPoolDeployment("old", "", 0), labelledPoolDeployment("old", "activities", 0))
		status := targetStatus("new")
		status.DeprecatedVersions = []*temporaliov1alpha1.DeprecatedWorkerDeploymentVersion{deprecatedVersion("old", temporaliov1alpha1.VersionStatusDraining, true)}

		assert.Equal(t, map[string]uint32{"wd-old": 2, "wd-old-activities": 4},
			scaleNames(getScaleDeployments(logr.Discard(), state, status, pooledSpec(t, "worker:v1", replicas, "activities"))))
	})
}

func driftPoolDeployment(buildID, pool, image string) *appsv1.Deployment {
	d := createDeploymentForDriftTest(1, buildID, image)
	d.Name = "wd-" + buildID + "-" + pool
	d.Labels[k8s.PoolLabel] = pool
	d.Spec.Selector.MatchLabels[k8s.PoolLabel] = pool
	return d
}

func TestGetUpdateDeployments_PodTemplateDriftPerPool(t *testing.T) {
	state := poolState(
		driftPoolDeployment("custom", temporaliov1alpha1.DefaultPoolName, "worker:v1"),
		driftPoolDeployment("custom", "activities", "worker:v1"),
	)
	spec := pooledSpec(t, "worker:v1", nil, "activities")
	spec.WorkerOptions.UnsafeCustomBuildID = "custom"
	spec.Pools[0].Deployment.Template = poolTemplate("activities:v2")

	updates := getUpdateDeployments(state, targetStatus("custom"), spec, createDefaultConnectionSpec())

	require.Equal(t, []string{"wd-custom-activities"}, names(updates))
	updated := updates[0]
	assert.Equal(t, "activities:v2", updated.Spec.Template.Spec.Containers[0].Image, "rebuilt from the pool's own template")
	assert.Equal(t, "activities", updated.Spec.Selector.MatchLabels[k8s.PoolLabel])
	assert.Equal(t, "activities", updated.Spec.Template.Labels[k8s.PoolLabel])
}

func TestGetUpdateDeployments_StrategyPerPool(t *testing.T) {
	state := poolState(labelledPoolDeployment("v1", "", 1), labelledPoolDeployment("v1", "activities", 1), labelledPoolDeployment("v1", "retired", 1))
	spec := pooledSpec(t, "worker:v1", nil, "activities")
	spec.Pools[0].Deployment.Strategy = appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType}

	updates := getUpdateDeployments(state, targetStatus("v1"), spec, createDefaultConnectionSpec())

	require.Equal(t, []string{"wd-v1-activities"}, names(updates), "the default pool already matches and a retired pool is left alone")
	assert.Equal(t, appsv1.RecreateDeploymentStrategyType, updates[0].Spec.Strategy.Type)
}

func TestGetTestWorkflows_MultiPoolTargetWaitsForHealth(t *testing.T) {
	status := targetStatus("v1")
	status.TargetVersion.Status = temporaliov1alpha1.VersionStatusInactive
	status.TargetVersion.TaskQueues = []temporaliov1alpha1.TaskQueue{{Name: "orders"}}
	status.TargetVersion.Pools = []temporaliov1alpha1.WorkerPoolStatus{{Name: temporaliov1alpha1.DefaultPoolName}, {Name: "activities"}}
	config := &Config{RolloutStrategy: temporaliov1alpha1.RolloutStrategy{Gate: &temporaliov1alpha1.GateWorkflowConfig{WorkflowType: "Gate"}}}

	assert.Empty(t, getTestWorkflows(status, config, "ns/wd", nil, false), "a pool may not be polling yet")

	status.TargetVersion.HealthySince = &metav1.Time{Time: time.Now()}
	assert.Len(t, getTestWorkflows(status, config, "ns/wd", nil, false), 1)
}
