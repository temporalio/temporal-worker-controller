// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package controller

import (
	"context"
	"strings"
	"testing"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/stretchr/testify/assert"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	temporaliov1alpha1 "github.com/DataDog/temporal-worker-controller/api/v1alpha1"
	"github.com/DataDog/temporal-worker-controller/internal/controller/k8s.io/utils"
)

var (
	testPodTemplate = v1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{
				"temporal.io/build-id": "123456789",
			},
		},
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				{
					Name:  "main",
					Image: "foo/bar@sha256:deadbeef",
				},
			},
		},
	}

	testNamespace = "ns"
)

// Hash is always taken of this for generating the buildID.
func newTestWorkerSpec(replicas int32) *temporaliov1alpha1.TemporalWorkerDeploymentSpec {
	return &temporaliov1alpha1.TemporalWorkerDeploymentSpec{
		Replicas: &replicas,
		Template: testPodTemplate,
		WorkerOptions: temporaliov1alpha1.WorkerOptions{
			TemporalNamespace: testNamespace,
		},
	}
}

// This is the new Deployment object that will be created by the controller based on the hash of the TemporalWorkerDeploymentSpec
func newTestDeploymentWithHashedBuildID(podSpec v1.PodTemplateSpec, desiredState *temporaliov1alpha1.TemporalWorkerDeploymentSpec, deploymentName string) *appsv1.Deployment {
	buildID := utils.ComputeHash(&desiredState.Template, nil)

	labels := map[string]string{
		"temporal.io/build-id": buildID,
	}

	blockOwnerDeletion := true
	isController := true

	return &appsv1.Deployment{
		TypeMeta: metav1.TypeMeta{},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: testNamespace,
			Name:      deploymentName + k8sResourceNameSeparator + buildID,
			Labels:    labels,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion:         "temporal.io/v1alpha1",
				Kind:               "TemporalWorkerDeployment",
				Name:               deploymentName,
				UID:                "",
				Controller:         &isController,
				BlockOwnerDeletion: &blockOwnerDeletion,
			}},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: desiredState.Replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: podSpec,
		},
	}
}

func newTestDeploymentWithBuildID(podSpec v1.PodTemplateSpec, desiredState *temporaliov1alpha1.TemporalWorkerDeploymentSpec, deploymentName string, buildID string) *appsv1.Deployment {

	labels := map[string]string{
		"temporal.io/build-id": buildID,
	}

	blockOwnerDeletion := true
	isController := true

	return &appsv1.Deployment{
		TypeMeta: metav1.TypeMeta{},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: testNamespace,
			Name:      deploymentName + k8sResourceNameSeparator + buildID,
			Labels:    labels,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion:         "temporal.io/v1alpha1",
				Kind:               "TemporalWorkerDeployment",
				Name:               deploymentName,
				UID:                "",
				Controller:         &isController,
				BlockOwnerDeletion: &blockOwnerDeletion,
			}},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: desiredState.Replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: podSpec,
		},
	}
}

func newTestWorkerDeploymentVersion(status temporaliov1alpha1.VersionStatus, deploymentName string, buildID string, managedK8Deployment bool) *temporaliov1alpha1.WorkerDeploymentVersion {
	result := temporaliov1alpha1.WorkerDeploymentVersion{
		HealthySince:   nil,
		VersionID:      deploymentName + versionIDSeparator + buildID,
		Status:         status,
		RampPercentage: nil,
		Deployment:     nil,
		DrainedSince:   &metav1.Time{Time: time.Now().Add(-30 * time.Hour)}, // useful when testing versions that have been drained but may/may not be eligible to be deleted.
	}

	if managedK8Deployment {
		result.Deployment = &v1.ObjectReference{
			Namespace: testNamespace,
			Name:      deploymentName + k8sResourceNameSeparator + buildID,
		}
	}

	return &result
}

func clearResourceVersion(obj metav1.Object) {
	obj.SetResourceVersion("")
}

func TestGeneratePlan(t *testing.T) {
	type testCase struct {
		workerDeploymentName string
		observedReplicas     int32
		desiredReplicas      int32
		observedState        *temporaliov1alpha1.TemporalWorkerDeploymentStatus
		desiredState         *temporaliov1alpha1.TemporalWorkerDeploymentSpec
		expectedPlan         plan
	}

	testCases := map[string]testCase{
		"no action needed": {
			workerDeploymentName: "foo",
			observedReplicas:     3,
			desiredReplicas:      3,
			observedState: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
				DefaultVersion: newTestWorkerDeploymentVersion(temporaliov1alpha1.VersionStatusInactive, "foo", "a", true),
			},
			desiredState: newTestWorkerSpec(3),
			expectedPlan: plan{
				TemporalNamespace:    testNamespace,
				WorkerDeploymentName: "foo" + deploymentNameSeparator + testNamespace,
				DeleteDeployments:    nil,
				CreateDeployment:     nil,
				ScaleDeployments:     make(map[*v1.ObjectReference]uint32),
				UpdateVersionConfig:  nil,
			},
		},
		"scale up inactive deployments if the number of replicas don't match": {
			workerDeploymentName: "fia",
			observedReplicas:     0,
			desiredReplicas:      3,
			observedState: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
				DeprecatedVersions: []*temporaliov1alpha1.WorkerDeploymentVersion{
					newTestWorkerDeploymentVersion(temporaliov1alpha1.VersionStatusInactive, "fia", "a", true),
				},
			},
			desiredState: newTestWorkerSpec(3),
			expectedPlan: plan{
				TemporalNamespace:    testNamespace,
				WorkerDeploymentName: "fia" + deploymentNameSeparator + testNamespace,
				DeleteDeployments:    nil,
				CreateDeployment:     nil,
				ScaleDeployments: map[*v1.ObjectReference]uint32{
					&v1.ObjectReference{
						Namespace: testNamespace,
						Name:      "fia-a",
					}: 3,
				},
				UpdateVersionConfig: nil,
			},
		},
		"scale up ramping deployment if the number of replicas don't match": {
			workerDeploymentName: "fii",
			observedReplicas:     0,
			desiredReplicas:      3,
			observedState: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
				DeprecatedVersions: []*temporaliov1alpha1.WorkerDeploymentVersion{
					newTestWorkerDeploymentVersion(temporaliov1alpha1.VersionStatusRamping, "fii", "b", true),
				},
			},
			desiredState: newTestWorkerSpec(3),
			expectedPlan: plan{
				TemporalNamespace:    testNamespace,
				WorkerDeploymentName: "fii" + deploymentNameSeparator + testNamespace,
				DeleteDeployments:    nil,
				CreateDeployment:     nil,
				ScaleDeployments: map[*v1.ObjectReference]uint32{
					&v1.ObjectReference{
						Namespace: testNamespace,
						Name:      "fii-b",
					}: 3,
				},
				UpdateVersionConfig: nil,
			},
		},
		"scale up current deployment if the number of replicas don't match": {
			workerDeploymentName: "aii",
			observedReplicas:     0,
			desiredReplicas:      3,
			observedState: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
				DeprecatedVersions: []*temporaliov1alpha1.WorkerDeploymentVersion{
					newTestWorkerDeploymentVersion(temporaliov1alpha1.VersionStatusCurrent, "aii", "a", true),
				},
			},
			desiredState: newTestWorkerSpec(3),
			expectedPlan: plan{
				TemporalNamespace:    testNamespace,
				WorkerDeploymentName: "aii" + deploymentNameSeparator + testNamespace,
				DeleteDeployments:    nil,
				CreateDeployment:     nil,
				ScaleDeployments: map[*v1.ObjectReference]uint32{
					&v1.ObjectReference{
						Namespace: testNamespace,
						Name:      "aii-a",
					}: 3,
				},
				UpdateVersionConfig: nil,
			},
		},
		"create new k8s deployment from the pod template ": {
			workerDeploymentName: "baz",
			observedReplicas:     3,
			desiredReplicas:      3,
			observedState: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
				DefaultVersion: newTestWorkerDeploymentVersion(temporaliov1alpha1.VersionStatusInactive, "baz", "a", true),
				// k8 deployment does not exist and will be created from the pod template
				TargetVersion:      newTestWorkerDeploymentVersion(temporaliov1alpha1.VersionStatusInactive, "baz", "b", false),
				DeprecatedVersions: nil,
			},
			desiredState: newTestWorkerSpec(3),
			expectedPlan: plan{
				TemporalNamespace:    testNamespace,
				WorkerDeploymentName: "baz" + deploymentNameSeparator + testNamespace,
				DeleteDeployments:    nil,
				CreateDeployment:     newTestDeploymentWithHashedBuildID(testPodTemplate, newTestWorkerSpec(3), "baz"),
				ScaleDeployments:     make(map[*v1.ObjectReference]uint32),
				UpdateVersionConfig:  nil,
			},
		},
		"delete unreachable deployment versions": {
			workerDeploymentName: "bar",
			observedReplicas:     3,
			desiredReplicas:      3,
			observedState: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
				DefaultVersion: newTestWorkerDeploymentVersion(temporaliov1alpha1.VersionStatusInactive, "bar", "a", true),
				DeprecatedVersions: []*temporaliov1alpha1.WorkerDeploymentVersion{
					newTestWorkerDeploymentVersion(temporaliov1alpha1.VersionStatusNotRegistered, "bar", "b", true),
				},
			},
			desiredState: newTestWorkerSpec(3),
			expectedPlan: plan{
				TemporalNamespace:    testNamespace,
				WorkerDeploymentName: "bar" + deploymentNameSeparator + testNamespace,
				DeleteDeployments: []*appsv1.Deployment{
					newTestDeploymentWithBuildID(testPodTemplate, newTestWorkerSpec(3), "bar", "b"),
				},
				ScaleDeployments:    map[*v1.ObjectReference]uint32{},
				CreateDeployment:    nil,
				UpdateVersionConfig: nil,
			},
		},
		"delete deployment when scaled down to 0 replicas": {
			workerDeploymentName: "def",
			observedReplicas:     0,
			desiredReplicas:      0,
			observedState: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
				DefaultVersion: newTestWorkerDeploymentVersion(temporaliov1alpha1.VersionStatusDrained, "def", "a", true),
				DeprecatedVersions: []*temporaliov1alpha1.WorkerDeploymentVersion{
					newTestWorkerDeploymentVersion(temporaliov1alpha1.VersionStatusDrained, "def", "b", true),
				},
			},
			desiredState: newTestWorkerSpec(0),
			expectedPlan: plan{
				TemporalNamespace:    testNamespace,
				WorkerDeploymentName: "def" + deploymentNameSeparator + testNamespace,
				DeleteDeployments: []*appsv1.Deployment{
					newTestDeploymentWithBuildID(testPodTemplate, newTestWorkerSpec(0), "def", "b"),
				},
				CreateDeployment:    nil,
				ScaleDeployments:    make(map[*v1.ObjectReference]uint32),
				UpdateVersionConfig: nil,
			},
		},
		"don't delete deployment if not scaled down to 0 replicas even though it's past scaledown + delete delay": {
			workerDeploymentName: "abc",
			observedReplicas:     3,
			desiredReplicas:      3,
			observedState: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
				DefaultVersion: newTestWorkerDeploymentVersion(temporaliov1alpha1.VersionStatusDrained, "abc", "a", true),
				DeprecatedVersions: []*temporaliov1alpha1.WorkerDeploymentVersion{
					newTestWorkerDeploymentVersion(temporaliov1alpha1.VersionStatusDrained, "abc", "b", true),
				},
			},
			desiredState: newTestWorkerSpec(3),
			expectedPlan: plan{
				TemporalNamespace:    testNamespace,
				WorkerDeploymentName: "abc" + deploymentNameSeparator + testNamespace,
				DeleteDeployments:    nil,
				CreateDeployment:     nil,
				ScaleDeployments: map[*v1.ObjectReference]uint32{
					&v1.ObjectReference{
						Namespace: testNamespace,
						Name:      "abc-b",
					}: 0,
				},
				UpdateVersionConfig: nil,
			},
		},
	}

	// Create a new scheme and client for each test case to avoid state bleeding
	scheme := runtime.NewScheme()
	_ = temporaliov1alpha1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme) // Add this to register Deployment type

	// Create the fake client
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		Build()

	r := &TemporalWorkerDeploymentReconciler{
		Client: c,
		Scheme: scheme,
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {

			// Create the TemporalWorkerDeployment resource.
			worker := &temporaliov1alpha1.TemporalWorkerDeployment{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "temporal.io/v1alpha1",
					Kind:       "TemporalWorkerDeployment",
				},
				ObjectMeta: metav1.ObjectMeta{
					Namespace: testNamespace,
					Name:      tc.workerDeploymentName,
				},
				Spec:   *tc.desiredState,
				Status: *tc.observedState,
			}
			err := c.Create(context.Background(), worker)
			if err != nil {
				t.Fatal(err)
			}

			// Create the Deployments referenced in the status
			if tc.observedState.DefaultVersion != nil && tc.observedState.DefaultVersion.Deployment != nil {
				deployment := newTestDeploymentWithHashedBuildID(testPodTemplate, newTestWorkerSpec(tc.observedReplicas), tc.workerDeploymentName)
				deployment.Name = tc.observedState.DefaultVersion.Deployment.Name
				deployment.Namespace = tc.observedState.DefaultVersion.Deployment.Namespace
				err = c.Create(context.Background(), deployment)
				if err != nil {
					t.Fatal(err)
				}
			}

			// Create the Deployments referenced in the deprecated versions
			for _, version := range tc.observedState.DeprecatedVersions {
				if version.Deployment != nil {
					buildID := strings.Split(version.VersionID, versionIDSeparator)[1]
					deployment := newTestDeploymentWithBuildID(testPodTemplate, newTestWorkerSpec(tc.observedReplicas), tc.workerDeploymentName, buildID)

					deployment.Name = version.Deployment.Name
					deployment.Namespace = version.Deployment.Namespace
					err = c.Create(context.Background(), deployment)
					if err != nil {
						t.Fatal(err)
					}
				}
			}

			actualPlan, err := r.generatePlan(
				context.Background(),
				log.FromContext(context.Background()),
				worker,
				temporaliov1alpha1.TemporalConnectionSpec{},
			)
			assert.NoError(t, err)

			// Clear ResourceVersion before comparison
			if actualPlan.CreateDeployment != nil {
				clearResourceVersion(actualPlan.CreateDeployment)
			}
			for _, d := range actualPlan.DeleteDeployments {
				clearResourceVersion(d)
			}

			assertEqualPlans(t, &tc.expectedPlan, actualPlan)
		})

	}
}

// assertEqualPlans checks that the two plans are equal by conducting a deep comparsion of all the fields.
// Unlike assert.Equal, however, this function does not deep-compare the keys in the scale deployments map since
// the values are pointers to the deployment objects.
func assertEqualPlans(t *testing.T, expectedPlan *plan, actualPlan *plan) {

	assert.Equal(t, expectedPlan.TemporalNamespace, actualPlan.TemporalNamespace)
	assert.Equal(t, expectedPlan.WorkerDeploymentName, actualPlan.WorkerDeploymentName)
	assert.Equal(t, expectedPlan.DeleteDeployments, actualPlan.DeleteDeployments)
	assert.Equal(t, expectedPlan.CreateDeployment, actualPlan.CreateDeployment)
	assert.Equal(t, expectedPlan.UpdateVersionConfig, actualPlan.UpdateVersionConfig)

	// ignore key values in the scale deployments map since they are pointers to the deployment objects.
	assert.Equal(t, len(expectedPlan.ScaleDeployments), len(actualPlan.ScaleDeployments))

	// Verify whether the right values are present in the scale deployments map
	for k, v := range expectedPlan.ScaleDeployments {
		found := false
		for k2, v2 := range actualPlan.ScaleDeployments {
			if k.Name == k2.Name {
				found = true
				assert.Equal(t, v, v2)
			}
		}
		assert.True(t, found)
	}
}
