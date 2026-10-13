package v1alpha1

import (
	"testing"

	"github.com/stretchr/testify/assert"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
)

func groupSpecTemplate(image string) corev1.PodTemplateSpec {
	return corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "worker", Image: image}}}}
}

func TestWorkerDeploymentSpec_Groups(t *testing.T) {
	spec := WorkerDeploymentSpec{
		WorkerGroups: []WorkerGroup{
			{Name: "zeta", Deployment: appsv1.DeploymentSpec{Template: groupSpecTemplate("zeta:v1")}},
			{Name: "alpha", Deployment: appsv1.DeploymentSpec{Replicas: ptr(int32(4)), Template: groupSpecTemplate("alpha:v1")}},
		},
	}

	assert.True(t, spec.HasWorkerGroups())
	assert.Equal(t, []string{"alpha", "zeta"}, spec.WorkerGroupNames())
	assert.True(t, spec.HasWorkerGroup("alpha"))
	assert.False(t, spec.HasWorkerGroup(DefaultWorkerGroupName), "a grouped spec has no default group")
	_, ok := spec.WorkerGroupDeploymentSpec(DefaultWorkerGroupName)
	assert.False(t, ok)

	alpha, ok := spec.WorkerGroupDeploymentSpec("alpha")
	assert.True(t, ok)
	assert.Equal(t, ptr(int32(4)), alpha.Replicas)
	assert.Equal(t, "alpha:v1", alpha.Template.Spec.Containers[0].Image)
	assert.Equal(t, DefaultDeploymentStrategy(), alpha.Strategy, "groups get the default rolling update strategy")

	_, ok = spec.WorkerGroupDeploymentSpec("missing")
	assert.False(t, ok)
}

func TestWorkerDeploymentSpec_SingleDeploymentIsTheDefaultGroup(t *testing.T) {
	spec := WorkerDeploymentSpec{Deployment: &appsv1.DeploymentSpec{Replicas: ptr(int32(2)), Template: groupSpecTemplate("default:v1")}}

	assert.False(t, spec.HasWorkerGroups())
	assert.Equal(t, []string{DefaultWorkerGroupName}, spec.WorkerGroupNames())
	assert.True(t, spec.HasWorkerGroup(DefaultWorkerGroupName))
	def, ok := spec.WorkerGroupDeploymentSpec(DefaultWorkerGroupName)
	assert.True(t, ok)
	assert.Equal(t, spec.DeploymentSpec(), def)
}

func TestWorkerDeploymentSpec_WorkerGroupDeploymentSpecDoesNotAlias(t *testing.T) {
	spec := WorkerDeploymentSpec{
		WorkerGroups: []WorkerGroup{{Name: "alpha", Deployment: appsv1.DeploymentSpec{Template: groupSpecTemplate("alpha:v1")}}},
	}

	alpha, _ := spec.WorkerGroupDeploymentSpec("alpha")
	alpha.Template.Spec.Containers[0].Image = "mutated"

	assert.Equal(t, "alpha:v1", spec.WorkerGroups[0].Deployment.Template.Spec.Containers[0].Image)
}
