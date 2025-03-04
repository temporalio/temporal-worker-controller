// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package controller

import (
	"context"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"testing"

	"github.com/stretchr/testify/assert"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	temporaliov1alpha1 "github.com/DataDog/temporal-worker-controller/api/v1alpha1"
)

var (
	testPodTemplate = v1.PodTemplateSpec{
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				{
					Name:  "main",
					Image: "foo/bar@sha256:deadbeef",
				},
			},
		},
	}
)

func newTestWorkerSpec(replicas int32) *temporaliov1alpha1.TemporalWorkerDeploymentSpec {
	return &temporaliov1alpha1.TemporalWorkerDeploymentSpec{
		Replicas: &replicas,
		Template: testPodTemplate,
		WorkerOptions: temporaliov1alpha1.WorkerOptions{
			TemporalNamespace: "baz",
			DeploymentName:    "qux",
		},
	}
}

func newTestDeployment(podSpec v1.PodTemplateSpec, desiredReplicas int32) *appsv1.Deployment {
	return &appsv1.Deployment{
		TypeMeta: metav1.TypeMeta{},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "foo",
			Name:      "bar-7476c6b88c",
			Annotations: map[string]string{
				"temporal.io/build-id": "7476c6b88c",
			},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &desiredReplicas,
			Template: podSpec,
		},
	}
}

func newTestWorkerDeploymentVersion(status temporaliov1alpha1.VersionStatus, deploymentName string) *temporaliov1alpha1.WorkerDeploymentVersion {
	result := temporaliov1alpha1.WorkerDeploymentVersion{
		HealthySince:   nil,
		VersionID:      deploymentName + "." + "test-id",
		Status:         status,
		RampPercentage: nil,
		Deployment:     nil,
	}

	if deploymentName != "" {
		result.Deployment = &v1.ObjectReference{
			Namespace: "foo",
			Name:      deploymentName,
		}
	} else {
		panic("deploymentName required")
	}

	return &result
}

// TODO(carlydf): Make these tests align with VersionStatus instead of reachability
func TestGeneratePlan(t *testing.T) {
	type testCase struct {
		observedState *temporaliov1alpha1.TemporalWorkerDeploymentStatus
		desiredState  *temporaliov1alpha1.TemporalWorkerDeploymentSpec
		expectedPlan  plan
	}

	testCases := map[string]testCase{
		"no action needed": {
			observedState: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
				DefaultVersion: newTestWorkerDeploymentVersion(temporaliov1alpha1.VersionStatusInactive, "foo-a"), // prev reachable
			},
			desiredState: newTestWorkerSpec(3),
			expectedPlan: plan{},
		},
		"create deployment version": {
			observedState: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
				DefaultVersion: &temporaliov1alpha1.WorkerDeploymentVersion{
					Status:    temporaliov1alpha1.VersionStatusInactive, // prev reachable
					VersionID: "foo-a.a",
				},
				DeprecatedVersions: nil,
			},
			desiredState: newTestWorkerSpec(3),
			expectedPlan: plan{
				DeleteDeployments:   nil,
				CreateDeployment:    newTestDeployment(testPodTemplate, 3),
				UpdateVersionConfig: nil,
			},
		},
		"delete unreachable deployment versions": {
			observedState: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
				DefaultVersion: newTestWorkerDeploymentVersion(temporaliov1alpha1.VersionStatusInactive, "foo-a"), // prev reachable
				DeprecatedVersions: []*temporaliov1alpha1.WorkerDeploymentVersion{
					newTestWorkerDeploymentVersion(temporaliov1alpha1.VersionStatusInactive, "foo-b"), // prev unreachable
					newTestWorkerDeploymentVersion(temporaliov1alpha1.VersionStatusInactive, "foo-c"), // prev reachable
					newTestWorkerDeploymentVersion(temporaliov1alpha1.VersionStatusInactive, "foo-d"), // prev unreachable
				},
			},
			desiredState: newTestWorkerSpec(3),
			expectedPlan: plan{
				DeleteDeployments: []*appsv1.Deployment{
					{
						ObjectMeta: metav1.ObjectMeta{
							Namespace: "foo",
							Name:      "foo-b",
						},
					},
					{
						ObjectMeta: metav1.ObjectMeta{
							Namespace: "foo",
							Name:      "foo-d",
						},
					},
				},
				CreateDeployment:    nil,
				UpdateVersionConfig: nil,
			},
		},
	}

	//env := envtest.Environment{}

	c := fake.NewFakeClient()

	//c, err := client.New(nil, client.Options{
	//	HTTPClient:     env.Config,
	//	Scheme:         nil,
	//	Mapper:         nil,
	//	Cache:          nil,
	//	WarningHandler: client.WarningHandlerOptions{},
	//	DryRun:         nil,
	//})
	//if err != nil {
	//	t.Fatal(err)
	//}

	r := &TemporalWorkerDeploymentReconciler{
		Client: c,
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			actualPlan, err := r.generatePlan(context.Background(), log.FromContext(context.Background()), &temporaliov1alpha1.TemporalWorkerDeployment{
				TypeMeta:   metav1.TypeMeta{},
				ObjectMeta: metav1.ObjectMeta{},
				Spec:       *tc.desiredState,
				Status:     *tc.observedState,
			}, temporaliov1alpha1.TemporalConnectionSpec{})
			assert.NoError(t, err)
			assert.Equal(t, &tc.expectedPlan, actualPlan)
		})
	}
}

func TestConvertFloatToUint(t *testing.T) {
	assert.Equal(t, uint8(1), convertFloatToUint(1.1))
}
