package v1alpha1

import (
	"testing"

	"github.com/stretchr/testify/assert"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
)

func poolSpecTemplate(image string) corev1.PodTemplateSpec {
	return corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "worker", Image: image}}}}
}

func TestWorkerDeploymentSpec_Pools(t *testing.T) {
	spec := WorkerDeploymentSpec{
		Pools: []WorkerPool{
			{Name: "zeta", Deployment: appsv1.DeploymentSpec{Template: poolSpecTemplate("zeta:v1")}},
			{Name: "alpha", Deployment: appsv1.DeploymentSpec{Replicas: ptr(int32(4)), Template: poolSpecTemplate("alpha:v1")}},
		},
	}

	assert.True(t, spec.HasPools())
	assert.Equal(t, []string{"alpha", "zeta"}, spec.PoolNames())
	assert.True(t, spec.HasPool("alpha"))
	assert.False(t, spec.HasPool(DefaultPoolName), "a pooled spec has no default pool")
	_, ok := spec.PoolDeploymentSpec(DefaultPoolName)
	assert.False(t, ok)

	alpha, ok := spec.PoolDeploymentSpec("alpha")
	assert.True(t, ok)
	assert.Equal(t, ptr(int32(4)), alpha.Replicas)
	assert.Equal(t, "alpha:v1", alpha.Template.Spec.Containers[0].Image)
	assert.Equal(t, DefaultDeploymentStrategy(), alpha.Strategy, "pools get the default rolling update strategy")

	_, ok = spec.PoolDeploymentSpec("missing")
	assert.False(t, ok)
}

func TestWorkerDeploymentSpec_SingleDeploymentIsTheDefaultPool(t *testing.T) {
	spec := WorkerDeploymentSpec{Deployment: &appsv1.DeploymentSpec{Replicas: ptr(int32(2)), Template: poolSpecTemplate("default:v1")}}

	assert.False(t, spec.HasPools())
	assert.Equal(t, []string{DefaultPoolName}, spec.PoolNames())
	assert.True(t, spec.HasPool(DefaultPoolName))
	def, ok := spec.PoolDeploymentSpec(DefaultPoolName)
	assert.True(t, ok)
	assert.Equal(t, spec.DeploymentSpec(), def)
}

func TestWorkerDeploymentSpec_PoolDeploymentSpecDoesNotAlias(t *testing.T) {
	spec := WorkerDeploymentSpec{
		Pools: []WorkerPool{{Name: "alpha", Deployment: appsv1.DeploymentSpec{Template: poolSpecTemplate("alpha:v1")}}},
	}

	alpha, _ := spec.PoolDeploymentSpec("alpha")
	alpha.Template.Spec.Containers[0].Image = "mutated"

	assert.Equal(t, "alpha:v1", spec.Pools[0].Deployment.Template.Spec.Containers[0].Image)
}
