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
		name      string
		inputSpec *TemporalWorkerDeploymentSpec
	}{
		"basic default behavior": {
			inputSpec: &TemporalWorkerDeploymentSpec{
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{
							"app":     "my-worker",
							"version": "v1",
						},
					},
				},
			},
		},
	}

	for testName, tc := range tests {
		t.Run(testName, func(t *testing.T) {
			// Apply defaults
			err := tc.inputSpec.Default(context.Background())
			assert.NoError(t, err, "Default() should not return an error")
		})
	}
}
