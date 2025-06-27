package testhelpers

import (
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
)

func MakeTWD(
	replicas int32,
	podSpec v1.PodTemplateSpec,
	rolloutStrategy *temporaliov1alpha1.RolloutStrategy,
	sunsetStrategy *temporaliov1alpha1.SunsetStrategy,
	workerOpts *temporaliov1alpha1.WorkerOptions,
) *temporaliov1alpha1.TemporalWorkerDeployment {
	r := temporaliov1alpha1.RolloutStrategy{}
	s := temporaliov1alpha1.SunsetStrategy{}
	w := temporaliov1alpha1.WorkerOptions{}
	if rolloutStrategy != nil {
		r = *rolloutStrategy
	}
	if sunsetStrategy != nil {
		s = *sunsetStrategy
	}
	if workerOpts != nil {
		w = *workerOpts
	}

	twd := &temporaliov1alpha1.TemporalWorkerDeployment{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "temporal.io/v1alpha1",
			Kind:       "TemporalWorkerDeployment",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-worker",
			Namespace: "default",
			UID:       types.UID("test-owner-uid"),
		},
		Spec: temporaliov1alpha1.TemporalWorkerDeploymentSpec{
			Replicas:        &replicas,
			Template:        podSpec,
			RolloutStrategy: r,
			SunsetStrategy:  s,
			WorkerOptions:   w,
		},
	}
	twd.Name = twd.ObjectMeta.Name
	return twd
}

// MakePodSpec creates a pod spec. Feel free to add parameters as needed.
func MakePodSpec(containers []v1.Container, labels map[string]string, taskQueue string) v1.PodTemplateSpec {
	for i := range containers {
		containers[i].Env = append(containers[i].Env, v1.EnvVar{Name: "TEMPORAL_TASK_QUEUE", Value: taskQueue})
	}

	return v1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{
			Labels: labels,
		},
		Spec: v1.PodSpec{
			Containers: containers,
		},
	}
}

func MakeTWDWithImage(imageName string) *temporaliov1alpha1.TemporalWorkerDeployment {
	return MakeTWD(1, MakePodSpec([]v1.Container{{Image: imageName}}, nil, ""), nil, nil, nil)
}

func MakeTWDWithName(name string) *temporaliov1alpha1.TemporalWorkerDeployment {
	twd := MakeTWD(1, MakePodSpec(nil, nil, ""), nil, nil, nil)
	twd.ObjectMeta.Name = name
	twd.Name = name
	return twd
}

func ModifyObj[T any](obj T, callback func(obj T) T) T {
	return callback(obj)
}
