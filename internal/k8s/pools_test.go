// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package k8s_test

import (
	"context"
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
