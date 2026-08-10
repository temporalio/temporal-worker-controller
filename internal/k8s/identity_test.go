// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package k8s_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	"github.com/temporalio/temporal-worker-controller/internal/k8s"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Keep this fixture local so changes to shared test helpers do not alter the
// identity values under test.
func legacyWorkerDeployment() *temporaliov1alpha1.WorkerDeployment {
	return &temporaliov1alpha1.WorkerDeployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "payment-processor",
			Namespace: "staging",
		},
		Spec: temporaliov1alpha1.WorkerDeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "worker",
							Image: "docker.io/temporalio/worker:v1.2.3",
						},
					},
				},
			},
		},
	}
}

func TestWorkerDeploymentIdentityCompatibility(t *testing.T) {
	worker := legacyWorkerDeployment()
	buildID := k8s.ComputeBuildID(worker)

	assert.Equal(t, "v1.2.3-c8cb", buildID)
	assert.Equal(t, "staging/payment-processor", k8s.ComputeWorkerDeploymentName(worker))
	assert.Equal(t,
		"payment-processor-v1-2-3-c8cb",
		k8s.ComputeVersionedDeploymentName(worker.Name, buildID),
	)
	assert.Equal(t,
		map[string]string{
			"temporal.io/deployment-name": "payment-processor",
			"temporal.io/build-id":        "v1.2.3-c8cb",
		},
		k8s.ComputeSelectorLabels(worker.Name, buildID),
	)
}

func TestComputeBuildIDCompatibility(t *testing.T) {
	t.Run("empty image", func(t *testing.T) {
		worker := legacyWorkerDeployment()
		worker.Spec.Template.Spec.Containers[0].Image = ""
		assert.Equal(t, "4444447645", k8s.ComputeBuildID(worker))
	})

	t.Run("custom build ID", func(t *testing.T) {
		worker := legacyWorkerDeployment()
		worker.Spec.WorkerOptions.UnsafeCustomBuildID = "release/2024_01!"
		assert.Equal(t, "release-2024_01", k8s.ComputeBuildID(worker))
	})
}
