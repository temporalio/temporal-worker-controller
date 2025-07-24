package testhelpers

import (
	"fmt"
	"github.com/pborman/uuid"
	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	"github.com/temporalio/temporal-worker-controller/internal/k8s"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"time"
)

const (
	testTaskQueue = "hello_world"
)

func MakeTWD(
	name string,
	namespace string,
	replicas int32,
	podSpec corev1.PodTemplateSpec,
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
			Name:      name,
			Namespace: namespace,
			UID:       types.UID(fmt.Sprintf("test-owner-%v", uuid.New())),
			Labels:    map[string]string{"app": "test-worker"},
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
func MakePodSpec(containers []corev1.Container, labels map[string]string, taskQueue string) corev1.PodTemplateSpec {
	for i := range containers {
		containers[i].Env = append(containers[i].Env, corev1.EnvVar{Name: "TEMPORAL_TASK_QUEUE", Value: taskQueue})
	}

	return corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{
			Labels: labels,
		},
		Spec: corev1.PodSpec{
			Containers: containers,
		},
	}
}

// MakeHelloWorldPodSpec creates a pod spec with hello_world task queue and one container with the given image name.
func MakeHelloWorldPodSpec(imageName string) corev1.PodTemplateSpec {
	return MakePodSpec([]corev1.Container{{Name: "worker", Image: imageName}},
		map[string]string{"app": "test-worker"},
		testTaskQueue)
}

func MakeTWDWithImage(name, namespace, imageName string) *temporaliov1alpha1.TemporalWorkerDeployment {
	return MakeTWD(name, namespace, 1, MakePodSpec([]corev1.Container{{Image: imageName}}, nil, ""), nil, nil, nil)
}

// MakeVersionId computes a version id based on the image, HelloWorldPodSpec, and k8s namespace.
func MakeVersionId(k8sNamespace, twdName, imageName string) string {
	return k8s.ComputeVersionID(
		ModifyObj(
			MakeTWDWithName(twdName, k8sNamespace),
			func(obj *temporaliov1alpha1.TemporalWorkerDeployment) *temporaliov1alpha1.TemporalWorkerDeployment {
				obj.Spec.Template = MakeHelloWorldPodSpec(imageName)
				return obj
			},
		),
	)
}

// MakeBuildId computes a build id based on the image and
// If no podSpec is provided, defaults to HelloWorldPodSpec with the given image name.
// If you provide your own podSpec, make sure to give the first container your desired image name if success is expected.
func MakeBuildId(twdName, imageName string, podSpec *corev1.PodTemplateSpec) string {
	return k8s.ComputeBuildID(
		ModifyObj(
			MakeTWDWithName(twdName, ""),
			func(obj *temporaliov1alpha1.TemporalWorkerDeployment) *temporaliov1alpha1.TemporalWorkerDeployment {
				if podSpec != nil {
					obj.Spec.Template = *podSpec
				} else {
					obj.Spec.Template = MakeHelloWorldPodSpec(imageName)
				}
				return obj
			},
		),
	)
}

func MakeTWDWithName(name, namespace string) *temporaliov1alpha1.TemporalWorkerDeployment {
	twd := MakeTWD(name, namespace, 1, MakePodSpec(nil, nil, ""), nil, nil, nil)
	twd.ObjectMeta.Name = name
	twd.Name = name
	return twd
}

func MakeCurrentVersion(namespace, twdName, imageName string, healthy, createDeployment bool) *temporaliov1alpha1.CurrentWorkerDeploymentVersion {
	ret := &temporaliov1alpha1.CurrentWorkerDeploymentVersion{
		BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
			VersionID:    MakeVersionId(namespace, twdName, imageName),
			Status:       temporaliov1alpha1.VersionStatusCurrent,
			HealthySince: nil,
			Deployment: &corev1.ObjectReference{
				Namespace: namespace,
				Name: k8s.ComputeVersionedDeploymentName(
					twdName,
					MakeBuildId(twdName, imageName, nil),
				),
			},
			TaskQueues: []temporaliov1alpha1.TaskQueue{
				{Name: testTaskQueue},
			},
			ManagedBy: "",
		},
	}

	if healthy {
		h := metav1.NewTime(time.Now())
		ret.HealthySince = &h
	}

	if createDeployment {
		ret.Deployment.FieldPath = "create"
	}
	return ret
}

func MakeTargetVersion(namespace, twdName, imageName string, healthy, createDeployment bool) *temporaliov1alpha1.TargetWorkerDeploymentVersion {
	ret := &temporaliov1alpha1.TargetWorkerDeploymentVersion{
		BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
			VersionID:    MakeVersionId(namespace, twdName, imageName),
			Status:       temporaliov1alpha1.VersionStatusCurrent,
			HealthySince: nil,
			Deployment: &corev1.ObjectReference{
				Namespace: namespace,
				Name: k8s.ComputeVersionedDeploymentName(
					twdName,
					MakeBuildId(twdName, imageName, nil),
				),
			},
			TaskQueues: []temporaliov1alpha1.TaskQueue{
				{Name: testTaskQueue},
			},
			ManagedBy: "",
		},
	}

	if healthy {
		h := metav1.NewTime(time.Now())
		ret.HealthySince = &h
	}

	if createDeployment {
		ret.Deployment.FieldPath = "create"
	}
	return ret
}

func ModifyObj[T any](obj T, callback func(obj T) T) T {
	return callback(obj)
}
