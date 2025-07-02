package testhelpers

import (
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

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

func MakeTWDWithName(name string) *temporaliov1alpha1.TemporalWorkerDeployment {
	twd := MakeTWD(1, MakePodSpec(nil, nil), nil, nil, nil)
	twd.ObjectMeta.Name = name
	twd.Name = name
	return twd
}

func ModifyObj[T any](obj T, callback func(obj T) T) T {
	return callback(obj)
}

// Ptr returns a pointer to the given value of any type
//
// Examples:
//
//	testhelpers.Ptr[int32](42)     // *int32
//	testhelpers.Ptr[string]("hi")  // *string
//	testhelpers.Ptr[bool](true)    // *bool
func Ptr[T any](v T) *T {
	return &v
}

// SetupTestScheme creates a runtime.Scheme with common types registered
func SetupTestScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = scheme.AddToScheme(s)
	_ = temporaliov1alpha1.AddToScheme(s)
	return s
}

// SetupFakeClient creates a fake client with the test scheme and optional objects
func SetupFakeClient(objects ...client.Object) client.Client {
	s := SetupTestScheme()
	builder := fake.NewClientBuilder().WithScheme(s)
	if len(objects) > 0 {
		builder = builder.WithObjects(objects...)
	}
	return builder.Build()
}
