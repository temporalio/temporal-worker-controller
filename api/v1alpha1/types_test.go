package v1alpha1

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestSecretReference_Validation(t *testing.T) {
	tests := map[string]struct {
		name      string
		wantValid bool
	}{
		"valid lowercase name": {
			name:      "my-secret",
			wantValid: true,
		},
		"valid single character": {
			name:      "a",
			wantValid: true,
		},
		"valid alphanumeric": {
			name:      "secret123",
			wantValid: true,
		},
		"valid with hyphens": {
			name:      "my-secret-123",
			wantValid: true,
		},
		"invalid uppercase": {
			name:      "My-Secret",
			wantValid: false,
		},
		"invalid leading hyphen": {
			name:      "-secret",
			wantValid: false,
		},
		"invalid trailing hyphen": {
			name:      "secret-",
			wantValid: false,
		},
		"invalid underscore": {
			name:      "my_secret",
			wantValid: false,
		},
		"invalid dot": {
			name:      "my.secret",
			wantValid: false,
		},
		"empty name": {
			name:      "",
			wantValid: false,
		},
	}

	for testName, tc := range tests {
		t.Run(testName, func(t *testing.T) {
			ref := SecretReference{Name: tc.name}

			// Test that the struct can be created
			assert.Equal(t, tc.name, ref.Name)

			// The actual validation happens at the kubebuilder level,
			// but we can test the pattern would match/not match
			// Pattern: ^[a-z0-9]([-a-z0-9]*[a-z0-9])?$
			if tc.wantValid {
				assert.Regexp(t, `^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`, ref.Name,
					"Expected name %q to match validation pattern", ref.Name)
			} else if tc.name != "" { // empty string is handled by Required validation
				assert.NotRegexp(t, `^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`, ref.Name,
					"Expected name %q to NOT match validation pattern", ref.Name)
			}
		})
	}
}

func TestTemporalConnectionReference_Validation(t *testing.T) {
	tests := map[string]struct {
		name      string
		wantValid bool
	}{
		"valid lowercase name": {
			name:      "my-temporal-connection",
			wantValid: true,
		},
		"valid single character": {
			name:      "t",
			wantValid: true,
		},
		"valid alphanumeric": {
			name:      "temporal123",
			wantValid: true,
		},
		"valid with hyphens": {
			name:      "temporal-conn-123",
			wantValid: true,
		},
		"invalid uppercase": {
			name:      "My-Connection",
			wantValid: false,
		},
		"invalid leading hyphen": {
			name:      "-connection",
			wantValid: false,
		},
		"invalid trailing hyphen": {
			name:      "connection-",
			wantValid: false,
		},
		"invalid underscore": {
			name:      "my_connection",
			wantValid: false,
		},
		"invalid dot": {
			name:      "my.connection",
			wantValid: false,
		},
		"empty name": {
			name:      "",
			wantValid: false,
		},
	}

	for testName, tc := range tests {
		t.Run(testName, func(t *testing.T) {
			ref := TemporalConnectionReference{Name: tc.name}

			// Test that the struct can be created
			assert.Equal(t, tc.name, ref.Name)

			// The actual validation happens at the kubebuilder level,
			// but we can test the pattern would match/not match
			// Pattern: ^[a-z0-9]([-a-z0-9]*[a-z0-9])?$
			if tc.wantValid {
				assert.Regexp(t, `^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`, ref.Name,
					"Expected name %q to match validation pattern", ref.Name)
			} else if tc.name != "" { // empty string is handled by Required validation
				assert.NotRegexp(t, `^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`, ref.Name,
					"Expected name %q to NOT match validation pattern", ref.Name)
			}
		})
	}
}

func TestRampPercentageBasisPoints_Validation(t *testing.T) {
	tests := map[string]struct {
		value     int32
		wantValid bool
	}{
		"zero percent": {
			value:     0,
			wantValid: true,
		},
		"half percent": {
			value:     50, // 0.5%
			wantValid: true,
		},
		"one percent": {
			value:     100,
			wantValid: true,
		},
		"fifty percent": {
			value:     5000,
			wantValid: true,
		},
		"hundred percent": {
			value:     10000,
			wantValid: true,
		},
		"negative value": {
			value:     -1,
			wantValid: false,
		},
		"over hundred percent": {
			value:     10001,
			wantValid: false,
		},
	}

	for testName, tc := range tests {
		t.Run(testName, func(t *testing.T) {
			// Test that the value is in the expected range
			// Validation: Minimum=0, Maximum=10000
			if tc.wantValid {
				assert.GreaterOrEqual(t, tc.value, int32(0), "Value should be >= 0")
				assert.LessOrEqual(t, tc.value, int32(10000), "Value should be <= 10000")
			} else {
				assert.True(t, tc.value < 0 || tc.value > 10000,
					"Invalid value should be outside range [0, 10000]")
			}
		})
	}
}

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
			if tc.expectedResult.Selector == nil {
				assert.Nil(t, tc.inputSpec.Selector, "Selector should be nil")
			} else {
				assert.NotNil(t, tc.inputSpec.Selector, "Selector should not be nil")
				assert.Equal(t, tc.expectedResult.Selector.MatchLabels, tc.inputSpec.Selector.MatchLabels,
					"Selector match labels should match expected")
			}
		})
	}
}
