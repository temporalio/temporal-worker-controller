package v1alpha1

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestTemporalWorkerDeploymentSpec_Default(t *testing.T) {
	tests := map[string]struct {
		name           string
		inputSpec      *TemporalWorkerDeploymentSpec
		expectedResult *TemporalWorkerDeploymentSpec
	}{
		"selector inferred from pod template labels": {
			inputSpec: &TemporalWorkerDeploymentSpec{
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{
							"app":     "my-worker",
							"version": "v1",
						},
					},
				},
				Selector: nil, // Not specified
			},
			expectedResult: &TemporalWorkerDeploymentSpec{
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{
							"app":     "my-worker",
							"version": "v1",
						},
					},
				},
				Selector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"app":     "my-worker",
						"version": "v1",
					},
				},
			},
		},
		"existing selector preserved": {
			inputSpec: &TemporalWorkerDeploymentSpec{
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{
							"app":     "my-worker",
							"version": "v1",
						},
					},
				},
				Selector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"app": "my-worker",
					},
				},
			},
			expectedResult: &TemporalWorkerDeploymentSpec{
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{
							"app":     "my-worker",
							"version": "v1",
						},
					},
				},
				Selector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"app": "my-worker", // Original selector preserved
					},
				},
			},
		},
		"no selector when template has no labels": {
			inputSpec: &TemporalWorkerDeploymentSpec{
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Labels: nil,
					},
				},
				Selector: nil,
			},
			expectedResult: &TemporalWorkerDeploymentSpec{
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Labels: nil,
					},
				},
				Selector: nil, // Still nil since no template labels
			},
		},
	}

	for testName, tc := range tests {
		t.Run(testName, func(t *testing.T) {
			// Apply defaults
			err := tc.inputSpec.Default(context.Background())
			assert.NoError(t, err, "Default() should not return an error")

			// Check selector
			assert.Equal(t, tc.expectedResult.Selector, tc.inputSpec.Selector, "Selector should match expected")
		})
	}
}
