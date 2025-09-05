package utils

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestComputeHash(t *testing.T) {
	tests := []struct {
		name           string
		template       *corev1.PodTemplateSpec
		collisionCount *int32
		short          bool
		expectedLength int
		expectedResult string
	}{
		{
			name:           "basic template with short hash",
			template:       makePodTemplateSpec("test-pod", "nginx:latest"),
			collisionCount: nil,
			short:          true,
			expectedLength: 4, // Short hash should be 4 digits
			expectedResult: "dcb4",
		},
		{
			name:           "basic template with full hash",
			template:       makePodTemplateSpec("test-pod", "nginx:latest"),
			collisionCount: nil,
			short:          false,
			expectedLength: 10, // Full hash should be 10 digits
			expectedResult: "444444dcb4",
		},
		{
			name:           "template with collision count",
			template:       makePodTemplateSpec("test-pod", "nginx:latest"),
			collisionCount: int32Ptr(5),
			short:          true,
			expectedLength: 4,
			expectedResult: "bb97",
		},
		{
			name:           "empty template",
			template:       makePodTemplateSpec("", ""),
			collisionCount: nil,
			short:          true,
			expectedLength: 4,
			expectedResult: "598f",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hash := ComputeHash(tt.template, tt.collisionCount, tt.short)

			// Check that hash is expected
			if tt.expectedResult != hash {
				t.Errorf("ComputeHash() expected result %s but got %s", tt.expectedResult, hash)
			}

			// For short hashes, verify they are 4 digits
			if tt.short && len(hash) != 4 {
				t.Errorf("ComputeHash() with short=true returned hash with length %d, expected 4", len(hash))
			}

			// For non-hashes, verify they are 10 digits
			if !tt.short && len(hash) != 10 {
				t.Errorf("ComputeHash() with short=false returned hash with length %d, expected 10", len(hash))
			}

			// Verify hash is deterministic for same inputs
			hash2 := ComputeHash(tt.template, tt.collisionCount, tt.short)
			if hash != hash2 {
				t.Errorf("ComputeHash() is not deterministic: got %s, then %s", hash, hash2)
			}
		})
	}
}

func TestComputeHashDeterministic(t *testing.T) {
	// Test that the same template produces the same hash
	template := makePodTemplateSpec("deterministic-test", "test:latest")

	hash1 := ComputeHash(template, nil, false)
	hash2 := ComputeHash(template, nil, false)

	if hash1 != hash2 {
		t.Errorf("ComputeHash() is not deterministic: got %s, then %s", hash1, hash2)
	}
}

func TestComputeHashDifferentInputs(t *testing.T) {
	// Test that different inputs produce different hashes
	template1 := makePodTemplateSpec("test1", "test:latest")
	template2 := makePodTemplateSpec("test2", "test:latest") // Different name

	hash1 := ComputeHash(template1, nil, false)
	hash2 := ComputeHash(template2, nil, false)

	if hash1 == hash2 {
		t.Errorf("ComputeHash() produced same hash for different templates: %s", hash1)
	}
}

func int32Ptr(i int32) *int32 {
	return &i
}

func makePodTemplateSpec(name, image string) *corev1.PodTemplateSpec {
	return &corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:  "main",
					Image: image,
				},
			},
		},
	}
}
