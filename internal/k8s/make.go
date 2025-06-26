package k8s

import (
	temporaliov1alpha1 "github.com/DataDog/temporal-worker-controller/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
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

	return &temporaliov1alpha1.TemporalWorkerDeployment{
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
}

// MakePodSpec creates a pod spec. Feel free to add parameters as needed.
func MakePodSpec(containers []v1.Container, labels map[string]string) v1.PodTemplateSpec {
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
	return MakeTWD(1, MakePodSpec([]v1.Container{{Image: imageName}}, nil), nil, nil, nil)
}
