// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package k8s

import (
	"testing"

	"github.com/stretchr/testify/assert"
	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// These tests pin the EXACT identity values the controller derives from a
// WorkerDeployment spec: the build ID, the Temporal worker deployment name,
// the versioned k8s Deployment name, and the selector labels.
//
// They are deliberately golden (byte-for-byte literals, not shapes): these
// values are load-bearing identity. Build IDs name live Worker Deployment
// Versions on the Temporal server; versioned Deployment names and selector
// labels identify running children, and Deployment selectors are immutable.
// If any of these change for an unchanged spec, a controller upgrade
// re-versions or orphans every existing deployment in the field.
//
// If a change makes one of these tests fail, that is a release-noteworthy
// compatibility break, not a test to update in passing. (Context: the
// multi-pod-template proposal in issue #330 extends these functions and
// relies on the legacy single-template outputs staying byte-identical.)

// goldenWD is constructed inline (not via testhelpers) so that helper
// changes can never silently alter the fixture the goldens are pinned to.
func goldenWD() *temporaliov1alpha1.WorkerDeployment {
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

func TestGolden_ComputeBuildID_ImageTag(t *testing.T) {
	assert.Equal(t, "v1.2.3-c8cb", ComputeBuildID(goldenWD()))
}

func TestGolden_ComputeBuildID_NoImage(t *testing.T) {
	wd := goldenWD()
	wd.Spec.Template.Spec.Containers[0].Image = ""
	assert.Equal(t, "4444447645", ComputeBuildID(wd))
}

func TestGolden_ComputeBuildID_UnsafeCustomBuildID(t *testing.T) {
	wd := goldenWD()
	wd.Spec.WorkerOptions.UnsafeCustomBuildID = "release/2024_01!"
	// Custom IDs pass through cleanBuildID: disallowed characters collapse to
	// "-" and leading/trailing separators are trimmed.
	assert.Equal(t, "release-2024_01", ComputeBuildID(wd))
}

func TestGolden_ComputeWorkerDeploymentName(t *testing.T) {
	assert.Equal(t, "staging/payment-processor", ComputeWorkerDeploymentName(goldenWD()))
}

func TestGolden_ComputeVersionedDeploymentName(t *testing.T) {
	buildID := ComputeBuildID(goldenWD())
	assert.Equal(t, "payment-processor-v1-2-3-c8cb", ComputeVersionedDeploymentName(goldenWD().Name, buildID))
}

func TestGolden_ComputeVersionedDeploymentName_TruncatesOver47(t *testing.T) {
	// 34-char base + separator + 15-char build ID = 50 > 47, forcing the
	// trunc10-trunc10-hash10 fallback. Both inputs are fixed literals so the
	// embedded sha256 prefix is stable.
	assert.Equal(t,
		"a-very-lon-v1-2-3-abc-865506c044",
		ComputeVersionedDeploymentName("a-very-long-worker-deployment-name", "v1.2.3-abcd4567"),
	)
}

func TestGolden_ComputeSelectorLabels(t *testing.T) {
	buildID := ComputeBuildID(goldenWD())
	assert.Equal(t,
		map[string]string{
			"temporal.io/deployment-name": "payment-processor",
			"temporal.io/build-id":        "v1.2.3-c8cb",
		},
		ComputeSelectorLabels(goldenWD().Name, buildID),
	)
}
