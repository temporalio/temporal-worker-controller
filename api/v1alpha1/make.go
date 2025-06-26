package v1alpha1

import (
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func MakeTWD(
	replicas int32,
	podSpec v1.PodTemplateSpec,
	rolloutStrategy *RolloutStrategy,
	sunsetStrategy *SunsetStrategy,
	workerOpts *WorkerOptions,
) *TemporalWorkerDeployment {
	r := RolloutStrategy{}
	s := SunsetStrategy{}
	w := WorkerOptions{}
	if rolloutStrategy != nil {
		r = *rolloutStrategy
	}
	if sunsetStrategy != nil {
		s = *sunsetStrategy
	}
	if workerOpts != nil {
		w = *workerOpts
	}

	twd := &TemporalWorkerDeployment{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "temporal.io/v1alpha1",
			Kind:       "TemporalWorkerDeployment",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-worker",
			Namespace: "default",
			UID:       types.UID("test-owner-uid"),
		},
		Spec: TemporalWorkerDeploymentSpec{
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

func MakeTWDWithImage(imageName string) *TemporalWorkerDeployment {
	return MakeTWD(1, MakePodSpec([]v1.Container{{Image: imageName}}, nil), nil, nil, nil)
}

func MakeTWDWithName(name string) *TemporalWorkerDeployment {
	twd := MakeTWD(1, MakePodSpec(nil, nil), nil, nil, nil)
	twd.ObjectMeta.Name = name
	twd.Name = name
	return twd
}
