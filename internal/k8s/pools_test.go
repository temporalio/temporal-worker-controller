// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package k8s_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	"github.com/temporalio/temporal-worker-controller/internal/k8s"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func ownedDeployment(name, buildID, pool string, age time.Duration) *appsv1.Deployment {
	labels := map[string]string{k8s.BuildIDLabel: buildID}
	if pool != "" {
		labels[k8s.PoolLabel] = pool
	}
	controller := true
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Namespace:         "default",
			Labels:            labels,
			CreationTimestamp: metav1.NewTime(time.Now().Add(-age)),
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "temporal.io/v1alpha1",
				Kind:       "WorkerDeployment",
				Name:       "test-worker",
				UID:        "test-owner-uid",
				Controller: &controller,
			}},
		},
	}
}

func deploymentStateFor(t *testing.T, objs ...client.Object) *k8s.DeploymentState {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, appsv1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		WithIndex(&appsv1.Deployment{}, k8s.DeployOwnerKey, func(o client.Object) []string {
			owner := metav1.GetControllerOf(o)
			if owner == nil {
				return nil
			}
			return []string{owner.Name}
		}).
		Build()
	state, err := k8s.GetDeploymentState(context.Background(), c, "default", "test-worker", "test-worker")
	require.NoError(t, err)
	return state
}

func deploymentNames(ds []*appsv1.Deployment) []string {
	names := make([]string, 0, len(ds))
	for _, d := range ds {
		names = append(names, d.Name)
	}
	return names
}

func TestGetDeploymentState_Pools(t *testing.T) {
	state := deploymentStateFor(t,
		ownedDeployment("w-v1", "v1", "", 3*time.Hour),
		ownedDeployment("w-v2", "v2", temporaliov1alpha1.DefaultPoolName, 2*time.Hour),
		ownedDeployment("w-v2-activities", "v2", "activities", 2*time.Hour),
		ownedDeployment("w-v2-batch", "v2", "batch", 2*time.Hour),
		ownedDeployment("w-v3-activities", "v3", "activities", time.Hour),
	)

	assert.Equal(t, "w-v1", state.Deployments["v1"].Name)
	assert.Equal(t, "w-v2", state.Deployments["v2"].Name, "named pools must not replace the default pool")
	assert.NotContains(t, state.Deployments, "v3", "a build with only named pools has no default Deployment")
	assert.Equal(t, "w-v2", state.DeploymentRefs["v2"].Name)
	assert.Len(t, state.DeploymentsByTime, 5)

	assert.Equal(t, []string{"v1", "v2", "v3"}, state.BuildIDs())
	assert.Equal(t, []string{"w-v1"}, deploymentNames(state.VersionDeploymentList("v1")))
	assert.Equal(t, []string{"w-v2-activities", "w-v2-batch", "w-v2"}, deploymentNames(state.VersionDeploymentList("v2")),
		"named pools come first, sorted, and the default pool last")
	assert.Equal(t, []string{"w-v3-activities"}, deploymentNames(state.VersionDeploymentList("v3")))
	assert.Empty(t, state.VersionDeploymentList("missing"))

	assert.Equal(t, temporaliov1alpha1.DefaultPoolName, k8s.PoolName(state.Deployments["v1"]))
	assert.Equal(t, "activities", k8s.PoolName(state.VersionDeployments("v2")["activities"]))
}

func TestDeploymentState_VersionDeploymentsFallsBackToDefaultMap(t *testing.T) {
	d := ownedDeployment("w-v1", "v1", "", time.Hour)
	state := &k8s.DeploymentState{Deployments: map[string]*appsv1.Deployment{"v1": d}}

	assert.Equal(t, map[string]*appsv1.Deployment{temporaliov1alpha1.DefaultPoolName: d}, state.VersionDeployments("v1"))
	assert.Equal(t, []string{"v1"}, state.BuildIDs())
}

func poolsFixture() *temporaliov1alpha1.WorkerDeployment {
	w := identityFixture("")
	w.Spec.Deployment = nil
	w.Spec.Pools = []temporaliov1alpha1.WorkerPool{
		{Name: "workflows", Deployment: appsv1.DeploymentSpec{
			Replicas: ptr.To[int32](2),
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "worker", Image: "registry.example.com/payments/worker:v1.2.3"}},
			}},
		}},
		{Name: "activities", Deployment: appsv1.DeploymentSpec{
			Replicas: ptr.To[int32](4),
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				ServiceAccountName: "activities",
				Containers:         []corev1.Container{{Name: "worker", Image: "registry.example.com/payments/activities:v1.2.4"}},
			}},
		}},
	}
	return w
}

func TestComputeBuildID_Pools(t *testing.T) {
	base := k8s.ComputeBuildID(poolsFixture())
	assert.Regexp(t, `^v1\.2\.4-[0-9a-f]{10}$`, base, "the image prefix comes from the first pool by name")

	tests := []struct {
		name   string
		mutate func(*temporaliov1alpha1.WorkerDeployment)
		same   bool
	}{
		{name: "reordered pools", same: true, mutate: func(w *temporaliov1alpha1.WorkerDeployment) {
			w.Spec.Pools[0], w.Spec.Pools[1] = w.Spec.Pools[1], w.Spec.Pools[0]
		}},
		{name: "pool replicas changed", same: true, mutate: func(w *temporaliov1alpha1.WorkerDeployment) {
			w.Spec.Pools[0].Deployment.Replicas = ptr.To[int32](9)
		}},
		{name: "pool template changed", mutate: func(w *temporaliov1alpha1.WorkerDeployment) {
			w.Spec.Pools[0].Deployment.Template.Spec.Containers[0].Image = "registry.example.com/payments/worker:v1.2.5"
		}},
		{name: "pool renamed", mutate: func(w *temporaliov1alpha1.WorkerDeployment) {
			w.Spec.Pools[1].Name = "jobs"
		}},
		{name: "pool removed", mutate: func(w *temporaliov1alpha1.WorkerDeployment) {
			w.Spec.Pools = w.Spec.Pools[:1]
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := poolsFixture()
			tt.mutate(w)
			if tt.same {
				assert.Equal(t, base, k8s.ComputeBuildID(w))
			} else {
				assert.NotEqual(t, base, k8s.ComputeBuildID(w))
			}
		})
	}

	t.Run("one pool differs from the same template without pools", func(t *testing.T) {
		single := identityFixture("registry.example.com/payments/worker:v1.2.3")
		pooled := identityFixture("registry.example.com/payments/worker:v1.2.3")
		pooled.Spec.Pools = []temporaliov1alpha1.WorkerPool{{Name: "workflows", Deployment: *pooled.Spec.Deployment}}
		pooled.Spec.Deployment = nil
		assert.NotEqual(t, k8s.ComputeBuildID(single), k8s.ComputeBuildID(pooled))
	})

	t.Run("custom build ID wins", func(t *testing.T) {
		w := poolsFixture()
		w.Spec.WorkerOptions.UnsafeCustomBuildID = "release-7"
		assert.Equal(t, "release-7", k8s.ComputeBuildID(w))
	})

	t.Run("long image tag fits in a label", func(t *testing.T) {
		w := poolsFixture()
		w.Spec.Pools[1].Deployment.Template.Spec.Containers[0].Image = "worker:" + strings.Repeat("t", 100)
		id := k8s.ComputeBuildID(w)
		assert.LessOrEqual(t, len(id), k8s.MaxBuildIDLen)
		assert.Regexp(t, `-[0-9a-f]{10}$`, id)
	})
}

func TestComputePoolDeploymentName(t *testing.T) {
	name := k8s.ComputePoolDeploymentName("payments", "activities", "v1.2.3-abcd")
	assert.Regexp(t, `^payments-activities-v1-2-3-abcd-[0-9a-f]{8}$`, name)

	long := k8s.ComputePoolDeploymentName("a-very-long-worker-deployment-name", "document-extraction", "release-2024-01-15-abcdef")
	assert.LessOrEqual(t, len(long), k8s.MaxDeploymentNameLen)
	assert.Regexp(t, `^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`, long)

	assert.NotEqual(t, k8s.ComputeVersionedDeploymentName("wd", "activities-v2"), k8s.ComputePoolDeploymentName("wd", "activities", "v2"),
		"a named pool never takes a default pool's name")
	assert.NotEqual(t, k8s.ComputePoolDeploymentName("wd", "a", "b-c"), k8s.ComputePoolDeploymentName("wd", "a-b", "c"))
}

func TestNewPoolDeploymentWithOwnerRef(t *testing.T) {
	w := poolsFixture()
	buildID := k8s.ComputeBuildID(w)
	wdName := k8s.ComputeWorkerDeploymentName(w)

	activities, err := k8s.NewPoolDeploymentWithOwnerRef(&w.TypeMeta, &w.ObjectMeta, &w.Spec, wdName, buildID, "activities", identityConnection)
	require.NoError(t, err)
	workflows, err := k8s.NewPoolDeploymentWithOwnerRef(&w.TypeMeta, &w.ObjectMeta, &w.Spec, wdName, buildID, "workflows", identityConnection)
	require.NoError(t, err)

	assert.Equal(t, k8s.ComputePoolDeploymentName(w.Name, "activities", buildID), activities.Name)
	assert.Equal(t, k8s.ComputePoolDeploymentName(w.Name, "workflows", buildID), workflows.Name)

	wantSelector := map[string]string{
		k8s.WorkerDeploymentNameLabel: "payment-processor",
		k8s.BuildIDLabel:              buildID,
		k8s.PoolLabel:                 "activities",
	}
	assert.Equal(t, wantSelector, activities.Spec.Selector.MatchLabels)
	assert.Equal(t, wantSelector, activities.Labels)
	assert.Equal(t, "workflows", workflows.Spec.Selector.MatchLabels[k8s.PoolLabel], "every pool selects only its own pods")
	for k, v := range activities.Spec.Selector.MatchLabels {
		assert.Equal(t, v, activities.Spec.Template.Labels[k])
	}

	assert.Equal(t, ptr.To[int32](4), activities.Spec.Replicas)
	assert.Equal(t, "activities", activities.Spec.Template.Spec.ServiceAccountName)
	assert.Equal(t, "registry.example.com/payments/activities:v1.2.4", activities.Spec.Template.Spec.Containers[0].Image)
	assert.Equal(t, temporaliov1alpha1.DefaultDeploymentStrategy(), activities.Spec.Strategy)
	assert.Equal(t, workflows.Spec.Template.Spec.Containers[0].Env, activities.Spec.Template.Spec.Containers[0].Env,
		"every pool gets the same controller-injected env")

	for _, missing := range []string{"missing", temporaliov1alpha1.DefaultPoolName} {
		_, err = k8s.NewPoolDeploymentWithOwnerRef(&w.TypeMeta, &w.ObjectMeta, &w.Spec, wdName, buildID, missing, identityConnection)
		assert.Error(t, err, missing)
	}
}
