// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package k8s_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	"github.com/temporalio/temporal-worker-controller/internal/k8s"
	"github.com/temporalio/temporal-worker-controller/internal/testhelpers"
	"go.temporal.io/sdk/contrib/envconfig"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestIsDeploymentHealthy(t *testing.T) {
	tests := []struct {
		name            string
		deployment      *appsv1.Deployment
		expectedHealthy bool
		expectedTimeNil bool
	}{
		{
			name: "healthy deployment",
			deployment: &appsv1.Deployment{
				Status: appsv1.DeploymentStatus{
					Conditions: []appsv1.DeploymentCondition{
						{
							Type:               appsv1.DeploymentAvailable,
							Status:             corev1.ConditionTrue,
							LastTransitionTime: metav1.NewTime(time.Now()),
						},
					},
				},
			},
			expectedHealthy: true,
			expectedTimeNil: false,
		},
		{
			name: "unhealthy deployment",
			deployment: &appsv1.Deployment{
				Status: appsv1.DeploymentStatus{
					Conditions: []appsv1.DeploymentCondition{
						{
							Type:   appsv1.DeploymentAvailable,
							Status: corev1.ConditionFalse,
						},
					},
				},
			},
			expectedHealthy: false,
			expectedTimeNil: true,
		},
		{
			name: "no conditions",
			deployment: &appsv1.Deployment{
				Status: appsv1.DeploymentStatus{
					Conditions: []appsv1.DeploymentCondition{},
				},
			},
			expectedHealthy: false,
			expectedTimeNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			healthy, healthySince := k8s.IsDeploymentHealthy(tt.deployment)
			assert.Equal(t, tt.expectedHealthy, healthy)
			if tt.expectedTimeNil {
				assert.Nil(t, healthySince)
			} else {
				assert.NotNil(t, healthySince)
			}
		})
	}
}

func TestGetDeploymentState(t *testing.T) {
	ctx := context.Background()

	// Create test TemporalWorkerDeployment owner
	owner := &temporaliov1alpha1.TemporalWorkerDeployment{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "temporal.io/v1alpha1",
			Kind:       "TemporalWorkerDeployment",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-worker",
			Namespace: "default",
			UID:       types.UID("test-owner-uid"),
		},
	}

	// Create test deployments with proper owner references
	deploy1 := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "worker-v1",
			Namespace: "default",
			Labels: map[string]string{
				k8s.BuildIDLabel: "v1",
			},
			CreationTimestamp: metav1.NewTime(time.Now().Add(-2 * time.Hour)),
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: "temporal.io/v1alpha1",
					Kind:       "TemporalWorkerDeployment",
					Name:       "test-worker",
					UID:        "test-owner-uid",
					Controller: func() *bool { b := true; return &b }(),
				},
			},
		},
	}

	deploy2 := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "worker-v2",
			Namespace: "default",
			Labels: map[string]string{
				k8s.BuildIDLabel: "v2",
			},
			CreationTimestamp: metav1.NewTime(time.Now().Add(-1 * time.Hour)),
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: "temporal.io/v1alpha1",
					Kind:       "TemporalWorkerDeployment",
					Name:       "test-worker",
					UID:        "test-owner-uid",
					Controller: func() *bool { b := true; return &b }(),
				},
			},
		},
	}

	// Create deployment without build ID label (should be ignored)
	deployWithoutLabel := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "worker-no-label",
			Namespace: "default",
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: "temporal.io/v1alpha1",
					Kind:       "TemporalWorkerDeployment",
					Name:       "test-worker",
					UID:        "test-owner-uid",
					Controller: func() *bool { b := true; return &b }(),
				},
			},
		},
	}

	// Create scheme and fake client with field indexer
	scheme := runtime.NewScheme()
	_ = appsv1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = temporaliov1alpha1.AddToScheme(scheme)

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(owner, deploy1, deploy2, deployWithoutLabel).
		WithIndex(&appsv1.Deployment{}, k8s.DeployOwnerKey, func(rawObj client.Object) []string {
			deploy := rawObj.(*appsv1.Deployment)
			owner := metav1.GetControllerOf(deploy)
			if owner == nil {
				return nil
			}
			if owner.APIVersion != "temporal.io/v1alpha1" || owner.Kind != "TemporalWorkerDeployment" {
				return nil
			}
			return []string{owner.Name}
		}).
		Build()

	// Test the GetDeploymentState function
	state, err := k8s.GetDeploymentState(ctx, fakeClient, "default", "test-worker", "test-worker")
	require.NoError(t, err)

	// Verify the state is constructed correctly
	assert.NotNil(t, state)
	assert.Equal(t, 2, len(state.Deployments), "Should have 2 deployments (deployments without build ID label should be ignored)")
	assert.Equal(t, 2, len(state.DeploymentsByTime))
	assert.Equal(t, 2, len(state.DeploymentRefs))

	// Verify the content of the maps
	assert.Equal(t, "worker-v1", state.Deployments["v1"].Name)
	assert.Equal(t, "worker-v2", state.Deployments["v2"].Name)

	// Verify the deployments are sorted by creation time (oldest first)
	assert.Equal(t, "worker-v1", state.DeploymentsByTime[0].Name)
	assert.Equal(t, "worker-v2", state.DeploymentsByTime[1].Name)

	// Verify refs are correctly created
	assert.Equal(t, "worker-v1", state.DeploymentRefs["v1"].Name)
	assert.Equal(t, "worker-v2", state.DeploymentRefs["v2"].Name)
}

func TestGenerateBuildID(t *testing.T) {
	digest := "a428de44a9059f31a59237a5881c2d2cffa93757d99026156e4ea544577ab7f3"
	tests := []struct {
		name            string
		generateInputs  func() (*temporaliov1alpha1.TemporalWorkerDeployment, *temporaliov1alpha1.TemporalWorkerDeployment)
		expectedPrefix  string
		expectedHashLen int
		expectEquality  bool // if true, both build ids should be equal
	}{
		{
			name: "same image different pod specs",
			generateInputs: func() (*temporaliov1alpha1.TemporalWorkerDeployment, *temporaliov1alpha1.TemporalWorkerDeployment) {
				img := "my.test_image"
				pod1 := testhelpers.MakePodSpec([]corev1.Container{{Image: img}}, map[string]string{"pod": "1"}, "")
				pod2 := testhelpers.MakePodSpec([]corev1.Container{{Image: img}}, map[string]string{"pod": "2"}, "")

				twd1 := testhelpers.MakeTWD("", "", 1, pod1, nil, nil, nil)
				twd2 := testhelpers.MakeTWD("", "", 1, pod2, nil, nil, nil)
				return twd1, twd2
			},
			expectedPrefix:  "my.test_image",
			expectedHashLen: 4,
			expectEquality:  false, // should be different
		},
		{
			name: "same pod specs different TWD spec",
			generateInputs: func() (*temporaliov1alpha1.TemporalWorkerDeployment, *temporaliov1alpha1.TemporalWorkerDeployment) {
				img := "my.test_image"
				pod := testhelpers.MakePodSpec([]corev1.Container{{Image: img}}, nil, "")
				twd1 := testhelpers.MakeTWD("", "", 1, pod, nil, nil, nil)
				twd2 := testhelpers.MakeTWD("", "", 2, pod, nil, nil, nil)
				return twd1, twd2
			},
			expectedPrefix:  "my.test_image",
			expectedHashLen: 4,
			expectEquality:  true, // should be the same
		},
		{
			name: "no containers",
			generateInputs: func() (*temporaliov1alpha1.TemporalWorkerDeployment, *temporaliov1alpha1.TemporalWorkerDeployment) {
				twd := testhelpers.MakeTWD("", "", 1, testhelpers.MakePodSpec(nil, nil, ""), nil, nil, nil)
				return twd, nil // only check 1 result, no need to compare
			},
			expectedPrefix:  "",
			expectedHashLen: 10, // expect long hash no prefix
			expectEquality:  false,
		},
		{
			name: "empty image",
			generateInputs: func() (*temporaliov1alpha1.TemporalWorkerDeployment, *temporaliov1alpha1.TemporalWorkerDeployment) {
				twd := testhelpers.MakeTWDWithImage("", "", "")
				return twd, nil // only check 1 result, no need to compare
			},
			expectedPrefix:  "",
			expectedHashLen: 10, // expect long hash no prefix
			expectEquality:  false,
		},
		{
			name: "tagged digest image",
			generateInputs: func() (*temporaliov1alpha1.TemporalWorkerDeployment, *temporaliov1alpha1.TemporalWorkerDeployment) {
				taggedDigestImg := "docker.io/library/busybox:latest@sha256:" + digest
				twd := testhelpers.MakeTWDWithImage("", "", taggedDigestImg)
				return twd, nil // only check 1 result, no need to compare
			},
			expectedPrefix:  "latest",
			expectedHashLen: 4,
			expectEquality:  false,
		},
		{
			name: "tagged named image",
			generateInputs: func() (*temporaliov1alpha1.TemporalWorkerDeployment, *temporaliov1alpha1.TemporalWorkerDeployment) {
				taggedNamedImg := "docker.io/library/busybox:latest"
				twd := testhelpers.MakeTWDWithImage("", "", taggedNamedImg)
				return twd, nil // only check 1 result, no need to compare
			},
			expectedPrefix:  "latest",
			expectedHashLen: 4,
			expectEquality:  false,
		},
		{
			name: "digested image",
			generateInputs: func() (*temporaliov1alpha1.TemporalWorkerDeployment, *temporaliov1alpha1.TemporalWorkerDeployment) {
				digestedImg := "docker.io@sha256:" + digest
				twd := testhelpers.MakeTWDWithImage("", "", digestedImg)
				return twd, nil // only check 1 result, no need to compare
			},
			expectedPrefix:  digest[:k8s.MaxBuildIdLen-5],
			expectedHashLen: 4,
			expectEquality:  false,
		},
		{
			name: "digested named image",
			generateInputs: func() (*temporaliov1alpha1.TemporalWorkerDeployment, *temporaliov1alpha1.TemporalWorkerDeployment) {
				digestedNamedImg := "docker.io/library/busybo@sha256:" + digest
				twd := testhelpers.MakeTWDWithImage("", "", digestedNamedImg)
				return twd, nil // only check 1 result, no need to compare
			},
			expectedPrefix:  digest[:k8s.MaxBuildIdLen-5],
			expectedHashLen: 4,
			expectEquality:  false,
		},
		{
			name: "named image",
			generateInputs: func() (*temporaliov1alpha1.TemporalWorkerDeployment, *temporaliov1alpha1.TemporalWorkerDeployment) {
				namedImg := "docker.io/library/busybox"
				twd := testhelpers.MakeTWDWithImage("", "", namedImg)
				return twd, nil // only check 1 result, no need to compare
			},
			expectedPrefix:  "library-busybox",
			expectedHashLen: 4,
			expectEquality:  false,
		},
		{
			name: "illegal chars image",
			generateInputs: func() (*temporaliov1alpha1.TemporalWorkerDeployment, *temporaliov1alpha1.TemporalWorkerDeployment) {
				illegalCharsImg := "this.is.my_weird/image"
				twd := testhelpers.MakeTWDWithImage("", "", illegalCharsImg)
				return twd, nil // only check 1 result, no need to compare
			},
			expectedPrefix:  "this.is.my_weird-image",
			expectedHashLen: 4,
			expectEquality:  false,
		},
		{
			name: "long image",
			generateInputs: func() (*temporaliov1alpha1.TemporalWorkerDeployment, *temporaliov1alpha1.TemporalWorkerDeployment) {
				longImg := "ThisIsAVeryLongHumanReadableImage_ThisIsAVeryLongHumanReadableImage_ThisIsAVeryLongHumanReadableImage" // 101 chars
				twd := testhelpers.MakeTWDWithImage("", "", longImg)
				return twd, nil // only check 1 result, no need to compare
			},
			expectedPrefix:  k8s.TruncateString("ThisIsAVeryLongHumanReadableImage_ThisIsAVeryLongHumanReadableImage_ThisIsAVeryLongHumanReadableImage"[:k8s.MaxBuildIdLen-5], -1),
			expectedHashLen: 4,
			expectEquality:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			twd1, twd2 := tt.generateInputs()

			var build1, build2 string
			if twd1 != nil {
				build1 = k8s.ComputeBuildID(twd1)
				verifyBuildId(t, build1, tt.expectedPrefix, tt.expectedHashLen)
			}
			if twd2 != nil {
				build2 = k8s.ComputeBuildID(twd2)
				verifyBuildId(t, build2, tt.expectedPrefix, tt.expectedHashLen)
			}

			// Check equality based on test case
			if tt.expectEquality {
				assert.Equal(t, build1, build2, "Build IDs should be equal for same pod spec")
			} else {
				assert.NotEqual(t, build1, build2, "Build IDs should be different for different pod specs")
			}
		})
	}
}

func verifyBuildId(t *testing.T, build, expectedPrefix string, expectedHashLen int) {
	assert.Truef(t, strings.HasPrefix(build, expectedPrefix), "expected prefix %s in build %s", expectedPrefix, build)
	assert.LessOrEqual(t, len(build), k8s.MaxBuildIdLen)
	assert.Equalf(t, k8s.TruncateString(build, -1), build, "expected build %s to be truncated", build)
	split := strings.Split(build, k8s.ResourceNameSeparator)
	assert.Equalf(t, expectedHashLen, len(split[len(split)-1]), "expected build %s to have %d-digit hash suffix", build, expectedHashLen)
}

func TestComputeVersionedDeploymentName(t *testing.T) {
	tests := []struct {
		name         string
		baseName     string
		buildID      string
		expectedName string
	}{
		{
			name:         "simple base and build ID",
			baseName:     "worker-name",
			buildID:      "abc123",
			expectedName: "worker-name-abc123",
		},
		{
			name:         "build ID with dots/dashes",
			baseName:     "worker-name",
			buildID:      "image-v2.1.0-a1b2c3d4",
			expectedName: "worker-name-image-v2-1-0-a1b2c3d4",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := k8s.ComputeVersionedDeploymentName(tt.baseName, tt.buildID)
			assert.Equal(t, tt.expectedName, result)

			// Verify the format is always baseName-buildID
			expected := tt.baseName + k8s.ResourceNameSeparator + k8s.CleanStringForDNS(tt.buildID)
			assert.Equal(t, expected, result)

			// Verify it ends with the build ID
			assert.True(t, strings.HasSuffix(result, k8s.ResourceNameSeparator+k8s.CleanStringForDNS(tt.buildID)),
				"versioned deployment name should end with '-cleaned(buildID)'")

			// Verify it starts with the base name
			assert.True(t, strings.HasPrefix(result, tt.baseName),
				"versioned deployment name should start with baseName")

			// Verify it is cleaned for DNS
			assert.Equal(t, result, k8s.CleanStringForDNS(result))
		})
	}
}

func TestComputeWorkerDeploymentName_Integration_WithVersionedName(t *testing.T) {
	// Integration test showing the naming pipeline from TemporalWorkerDeployment to final K8s Deployment name
	twd := &temporaliov1alpha1.TemporalWorkerDeployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "hello-world",
			Namespace: "demo",
		},
		Spec: temporaliov1alpha1.TemporalWorkerDeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Image: "temporal/hello-world:v1.0.0",
						},
					},
				},
			},
		},
	}

	// Test the full pipeline: TemporalWorkerDeployment -> worker deployment name -> versioned deployment name
	workerDeploymentName := k8s.ComputeWorkerDeploymentName(twd)
	buildID := k8s.ComputeBuildID(twd)
	versionedName := k8s.ComputeVersionedDeploymentName(twd.Name, buildID)

	// Verify the expected formats
	assert.Equal(t, "demo"+k8s.WorkerDeploymentNameSeparator+"hello-world", workerDeploymentName)
	assert.True(t, strings.HasPrefix(versionedName, "hello-world-"))
	assert.True(t, strings.Contains(versionedName, k8s.CleanStringForDNS("v1.0.0")), "versioned name should contain cleaned image tag")
}

// TestNewDeploymentWithPodAnnotations tests that every new pod created has a connection spec hash annotation
func TestNewDeploymentWithPodAnnotations(t *testing.T) {
	connection := temporaliov1alpha1.TemporalConnectionSpec{
		HostPort:           "localhost:7233",
		MutualTLSSecretRef: &temporaliov1alpha1.SecretReference{Name: "my-secret"},
	}

	deployment := k8s.NewDeploymentWithOwnerRef(
		&metav1.TypeMeta{},
		&metav1.ObjectMeta{Name: "test", Namespace: "default"},
		&temporaliov1alpha1.TemporalWorkerDeploymentSpec{},
		"test-deployment",
		"build123",
		connection,
	)

	expectedHash := k8s.ComputeConnectionSpecHash(connection)
	actualHash := deployment.Spec.Template.Annotations[k8s.ConnectionSpecHashAnnotation]

	assert.Equal(t, expectedHash, actualHash, "Deployment should have correct connection spec hash annotation")
}

func TestComputeConnectionSpecHash(t *testing.T) {
	t.Run("generates non-empty hash for valid connection spec", func(t *testing.T) {
		spec := temporaliov1alpha1.TemporalConnectionSpec{
			HostPort:           "localhost:7233",
			MutualTLSSecretRef: &temporaliov1alpha1.SecretReference{Name: "my-tls-secret"},
		}

		result := k8s.ComputeConnectionSpecHash(spec)
		assert.NotEmpty(t, result, "Hash should not be empty for valid spec")
		assert.Len(t, result, 64, "SHA256 hash should be 64 characters") // hex encoded SHA256
	})

	t.Run("returns empty hash when hostport is empty", func(t *testing.T) {
		spec := temporaliov1alpha1.TemporalConnectionSpec{
			HostPort:           "",
			MutualTLSSecretRef: &temporaliov1alpha1.SecretReference{Name: "secret"},
		}

		result := k8s.ComputeConnectionSpecHash(spec)
		assert.Empty(t, result, "Hash should be empty when hostport is empty")
	})

	t.Run("is deterministic - same input produces same hash", func(t *testing.T) {
		spec := temporaliov1alpha1.TemporalConnectionSpec{
			HostPort:           "localhost:7233",
			MutualTLSSecretRef: &temporaliov1alpha1.SecretReference{Name: "my-secret"},
		}

		hash1 := k8s.ComputeConnectionSpecHash(spec)
		hash2 := k8s.ComputeConnectionSpecHash(spec)

		assert.Equal(t, hash1, hash2, "Same input should produce identical hashes")
	})

	t.Run("different hostports produce different hashes", func(t *testing.T) {
		spec1 := temporaliov1alpha1.TemporalConnectionSpec{
			HostPort:           "localhost:7233",
			MutualTLSSecretRef: &temporaliov1alpha1.SecretReference{Name: "same-secret"},
		}
		spec2 := temporaliov1alpha1.TemporalConnectionSpec{
			HostPort:           "different-host:7233",
			MutualTLSSecretRef: &temporaliov1alpha1.SecretReference{Name: "same-secret"},
		}

		hash1 := k8s.ComputeConnectionSpecHash(spec1)
		hash2 := k8s.ComputeConnectionSpecHash(spec2)

		assert.NotEqual(t, hash1, hash2, "Different hostports should produce different hashes")
	})

	t.Run("different mTLS secrets produce different hashes", func(t *testing.T) {
		spec1 := temporaliov1alpha1.TemporalConnectionSpec{
			HostPort:           "localhost:7233",
			MutualTLSSecretRef: &temporaliov1alpha1.SecretReference{Name: "secret1"},
		}
		spec2 := temporaliov1alpha1.TemporalConnectionSpec{
			HostPort:           "localhost:7233",
			MutualTLSSecretRef: &temporaliov1alpha1.SecretReference{Name: "secret2"},
		}

		hash1 := k8s.ComputeConnectionSpecHash(spec1)
		hash2 := k8s.ComputeConnectionSpecHash(spec2)

		assert.NotEqual(t, hash1, hash2, "Different mTLS secrets should produce different hashes")
	})

	t.Run("empty mTLS secret vs non-empty produce different hashes", func(t *testing.T) {
		spec1 := temporaliov1alpha1.TemporalConnectionSpec{
			HostPort:           "localhost:7233",
			MutualTLSSecretRef: nil,
		}
		spec2 := temporaliov1alpha1.TemporalConnectionSpec{
			HostPort:           "localhost:7233",
			MutualTLSSecretRef: &temporaliov1alpha1.SecretReference{Name: "some-secret"},
		}

		hash1 := k8s.ComputeConnectionSpecHash(spec1)
		hash2 := k8s.ComputeConnectionSpecHash(spec2)

		assert.NotEqual(t, hash1, hash2, "Empty vs non-empty mTLS secret should produce different hashes")
		assert.NotEmpty(t, hash1, "Hash should still be generated even with empty mTLS secret")
	})

	t.Run("different API key secrets produce different hashes", func(t *testing.T) {
		spec1 := temporaliov1alpha1.TemporalConnectionSpec{
			HostPort: "localhost:7233",
			APIKeySecretRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: "secret1"},
				Key:                  "api-key1"},
		}
		spec2 := temporaliov1alpha1.TemporalConnectionSpec{
			HostPort: "localhost:7233",
			APIKeySecretRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: "secret2"},
				Key:                  "api-key2"},
		}
		hash1 := k8s.ComputeConnectionSpecHash(spec1)
		hash2 := k8s.ComputeConnectionSpecHash(spec2)

		assert.NotEqual(t, hash1, hash2, "Different API key secrets should produce different hashes")
	})

	t.Run("empty API key secret vs non-empty produce different hashes", func(t *testing.T) {
		spec1 := temporaliov1alpha1.TemporalConnectionSpec{
			HostPort:        "localhost:7233",
			APIKeySecretRef: nil,
		}
		spec2 := temporaliov1alpha1.TemporalConnectionSpec{
			HostPort: "localhost:7233",
			APIKeySecretRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: "secret"},
				Key:                  "api-key"},
		}
		hash1 := k8s.ComputeConnectionSpecHash(spec1)
		hash2 := k8s.ComputeConnectionSpecHash(spec2)

		assert.NotEqual(t, hash1, hash2, "Empty vs non-empty API key secret should produce different hashes")
		assert.NotEmpty(t, hash1, "Hash should still be generated even with empty API key secret")
	})

	t.Run("same API key secret name produce the same hash", func(t *testing.T) {
		spec1 := temporaliov1alpha1.TemporalConnectionSpec{
			HostPort: "localhost:7233",
			APIKeySecretRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: "secret"},
				Key:                  "api-key"},
		}
		spec2 := temporaliov1alpha1.TemporalConnectionSpec{
			HostPort: "localhost:7233",
			APIKeySecretRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: "secret"},
				Key:                  "api-key"},
		}

		hash1 := k8s.ComputeConnectionSpecHash(spec1)
		hash2 := k8s.ComputeConnectionSpecHash(spec2)

		assert.Equal(t, hash1, hash2, "Same API key secret name should produce the same hash")
	})

}

func TestNewDeploymentWithOwnerRef_EnvironmentVariablesAndVolumes(t *testing.T) {
	tests := map[string]struct {
		connection        temporaliov1alpha1.TemporalConnectionSpec
		expectedEnvVars   map[string]string
		unexpectedEnvVars []string
	}{
		"without mTLS": {
			connection: temporaliov1alpha1.TemporalConnectionSpec{
				HostPort: "localhost:7233",
			},
			expectedEnvVars: map[string]string{
				"TEMPORAL_ADDRESS":         "localhost:7233",
				"TEMPORAL_NAMESPACE":       "test-namespace",
				"TEMPORAL_DEPLOYMENT_NAME": "test-deployment",
				"TEMPORAL_WORKER_BUILD_ID": "test-build-id",
			},
			unexpectedEnvVars: []string{"TEMPORAL_TLS", "TEMPORAL_TLS_CLIENT_KEY_PATH", "TEMPORAL_TLS_CLIENT_CERT_PATH"},
		},
		"with mTLS": {
			connection: temporaliov1alpha1.TemporalConnectionSpec{
				HostPort:           "mtls.localhost:7233",
				MutualTLSSecretRef: &temporaliov1alpha1.SecretReference{Name: "my-tls-secret"},
			},
			expectedEnvVars: map[string]string{
				"TEMPORAL_ADDRESS":              "mtls.localhost:7233",
				"TEMPORAL_NAMESPACE":            "test-namespace",
				"TEMPORAL_DEPLOYMENT_NAME":      "test-deployment",
				"TEMPORAL_WORKER_BUILD_ID":      "test-build-id",
				"TEMPORAL_TLS":                  "true",
				"TEMPORAL_TLS_CLIENT_KEY_PATH":  "/etc/temporal/tls/tls.key",
				"TEMPORAL_TLS_CLIENT_CERT_PATH": "/etc/temporal/tls/tls.crt",
			},
			unexpectedEnvVars: []string{},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			spec := &temporaliov1alpha1.TemporalWorkerDeploymentSpec{
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:  "worker",
								Image: "temporal/worker:latest",
							},
						},
					},
				},
				WorkerOptions: temporaliov1alpha1.WorkerOptions{
					TemporalNamespace: "test-namespace",
				},
			}

			deployment := k8s.NewDeploymentWithOwnerRef(
				&metav1.TypeMeta{},
				&metav1.ObjectMeta{Name: "test", Namespace: "default"},
				spec,
				"test-deployment",
				"test-build-id",
				tt.connection,
			)

			// Verify expected environment variables are present
			container := deployment.Spec.Template.Spec.Containers[0]
			envMap := make(map[string]string)
			for _, env := range container.Env {
				envMap[env.Name] = env.Value
			}

			for expectedKey, expectedValue := range tt.expectedEnvVars {
				actualValue, exists := envMap[expectedKey]
				assert.True(t, exists, "Environment variable %s should be present", expectedKey)
				assert.Equal(t, expectedValue, actualValue, "Environment variable %s should have correct value", expectedKey)
			}

			// Verify unexpected environment variables are not present
			for _, unexpectedKey := range tt.unexpectedEnvVars {
				value, exists := envMap[unexpectedKey]
				assert.False(t, exists, "Environment variable %s should not be present, but found value: %s", unexpectedKey, value)
			}

			// For mTLS case, verify volume mounts and volumes are configured
			if tt.connection.MutualTLSSecretRef != nil {
				assert.Len(t, container.VolumeMounts, 1)
				assert.Equal(t, "temporal-tls", container.VolumeMounts[0].Name)
				assert.Equal(t, "/etc/temporal/tls", container.VolumeMounts[0].MountPath)

				assert.Len(t, deployment.Spec.Template.Spec.Volumes, 1)
				assert.Equal(t, "temporal-tls", deployment.Spec.Template.Spec.Volumes[0].Name)
				assert.Equal(t, tt.connection.MutualTLSSecretRef.Name, deployment.Spec.Template.Spec.Volumes[0].VolumeSource.Secret.SecretName)
			}
		})
	}
}

// createTestCerts generates a self-signed certificate and private key for testing purposes.
// WARNING: This uses a lighter-weight 1024-bit RSA key for faster test execution.
// DO NOT copy this for production use - use 2048-bit or higher keys for security.
func createTestCerts(t *testing.T) (certPath, keyPath string) {
	t.Helper()

	// Create temp directory for certificate files
	tempDir := t.TempDir()
	certPath = filepath.Join(tempDir, "client.pem")
	keyPath = filepath.Join(tempDir, "client.key")

	// Generate a self-signed certificate for testing (using 2048-bit for security)
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			CommonName: "test",
		},
		NotBefore:   time.Now(),
		NotAfter:    time.Now().Add(time.Hour),
		KeyUsage:    x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}

	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &privateKey.PublicKey, privateKey)
	require.NoError(t, err)

	// Write certificate file directly
	certFile, err := os.Create(certPath)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, certFile.Close()) })
	require.NoError(t, pem.Encode(certFile, &pem.Block{Type: "CERTIFICATE", Bytes: certDER}))

	// Write private key file directly
	keyDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	require.NoError(t, err)
	keyFile, err := os.Create(keyPath)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, keyFile.Close()) })
	require.NoError(t, pem.Encode(keyFile, &pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))

	return certPath, keyPath
}

func TestNewDeploymentWithOwnerRef_EnvConfigSDKCompatibility(t *testing.T) {
	// Test that the environment variables injected by the controller
	// can be parsed by the official Temporal SDK envconfig package
	tests := map[string]struct {
		connection temporaliov1alpha1.TemporalConnectionSpec
		namespace  string
	}{
		"without TLS": {
			connection: temporaliov1alpha1.TemporalConnectionSpec{
				HostPort: "test.temporal.example:9999",
			},
			namespace: "test-namespace-no-tls",
		},
		"with TLS": {
			connection: temporaliov1alpha1.TemporalConnectionSpec{
				HostPort:           "mtls.temporal.example:8888",
				MutualTLSSecretRef: &temporaliov1alpha1.SecretReference{Name: "test-tls-secret"},
			},
			namespace: "test-namespace-with-tls",
		},
		"with API key": {
			connection: temporaliov1alpha1.TemporalConnectionSpec{
				HostPort: "test.temporal.example:9999",
				APIKeySecretRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: "test-api-key-secret"},
					Key:                  "api-key"},
			},
			namespace: "test-namespace-with-api-key",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			spec := &temporaliov1alpha1.TemporalWorkerDeploymentSpec{
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:  "worker",
								Image: "temporal/worker:latest",
							},
						},
					},
				},
				WorkerOptions: temporaliov1alpha1.WorkerOptions{
					TemporalNamespace: tt.namespace,
				},
			}

			deployment := k8s.NewDeploymentWithOwnerRef(
				&metav1.TypeMeta{},
				&metav1.ObjectMeta{Name: "test", Namespace: "default"},
				spec,
				"test-deployment",
				"test-build-id",
				tt.connection,
			)

			// Extract environment variables from the deployment
			container := deployment.Spec.Template.Spec.Containers[0]

			// Infer whether TLS is expected from connection spec
			expectTLS := tt.connection.MutualTLSSecretRef != nil || tt.connection.APIKeySecretRef != nil

			if expectTLS {
				// Create temporary test certificate files
				certPath, keyPath := createTestCerts(t)

				// First assert that certificate paths match expected volume mount paths.
				// Then override the paths in the spec since we have to use a temp dir in tests.
				for i := range container.Env {
					if container.Env[i].Name == "TEMPORAL_TLS_CLIENT_CERT_PATH" {
						assert.Equal(t, "/etc/temporal/tls/tls.crt", container.Env[i].Value, "Certificate path should match volume mount")
						container.Env[i].Value = certPath
					}
					if container.Env[i].Name == "TEMPORAL_TLS_CLIENT_KEY_PATH" {
						assert.Equal(t, "/etc/temporal/tls/tls.key", container.Env[i].Value, "Key path should match volume mount")
						container.Env[i].Value = keyPath
					}
				}
			} else if tt.connection.APIKeySecretRef != nil {
				for i := range container.Env {
					if container.Env[i].Name == "TEMPORAL_API_KEY" {
						assert.Equal(t, tt.connection.APIKeySecretRef.Name, container.Env[i].ValueFrom.SecretKeyRef.Name, "API key secret name should match")
						assert.Equal(t, "api-key", container.Env[i].ValueFrom.SecretKeyRef.Key, "API key secret key should be 'api-key'")
					}
				}
			}

			// Set environment variables using t.Setenv() to simulate the runtime environment
			for _, env := range container.Env {
				if env.Name == "TEMPORAL_API_KEY" {
					// setting a dummy value here since API values are read from an actual secret object in runtime
					// moreover, env.Value is nil for TEMPORAL_API_KEY since it used the ValueFrom field (check deployments.go)
					t.Setenv(env.Name, "test-api-key-value")
					continue
				}
				t.Setenv(env.Name, env.Value)
			}

			// Use the envconfig package to load client options for TLS case
			clientOptions, err := envconfig.LoadDefaultClientOptions()
			require.NoError(t, err, "envconfig should successfully parse environment variables")

			// Verify that the parsed client options match our expectations
			assert.Equal(t, tt.connection.HostPort, clientOptions.HostPort, "HostPort should be parsed from TEMPORAL_ADDRESS")
			assert.Equal(t, tt.namespace, clientOptions.Namespace, "Namespace should be parsed from TEMPORAL_NAMESPACE")
			if tt.connection.APIKeySecretRef != nil {
				assert.Equal(t, "test-api-key-value", os.Getenv("TEMPORAL_API_KEY"), "API key should be parsed from TEMPORAL_API_KEY")
			}

			// Verify other client option fields that should have default/empty values
			assert.Empty(t, clientOptions.Identity, "Identity should be empty when not set via env vars")
			assert.Nil(t, clientOptions.Logger, "Logger should be nil when not set")
			assert.Nil(t, clientOptions.MetricsHandler, "MetricsHandler should be nil when not set")
			assert.Empty(t, clientOptions.Interceptors, "Interceptors should be empty when not set")

			if expectTLS {
				assert.NotNil(t, clientOptions.ConnectionOptions.TLS, "TLS should be configured for mTLS connection")
			} else if tt.connection.APIKeySecretRef != nil {
				// An empty TLS config is configured when an API key is used without mTLS secrets
				assert.Equal(t, clientOptions.ConnectionOptions.TLS, &tls.Config{}, "Empty TLS config should be configured for API key connection")
			} else {
				assert.Nil(t, clientOptions.ConnectionOptions.TLS, "TLS config should not be configured for non-mTLS connection")
			}

			// Note: TEMPORAL_DEPLOYMENT_NAME and TEMPORAL_WORKER_BUILD_ID are not part of client options
			// but are used by the worker for versioning - they should still be available as env vars
			assert.Equal(t, "test-deployment", os.Getenv("TEMPORAL_DEPLOYMENT_NAME"), "TEMPORAL_DEPLOYMENT_NAME should be set")
			assert.Equal(t, "test-build-id", os.Getenv("TEMPORAL_WORKER_BUILD_ID"), "TEMPORAL_WORKER_BUILD_ID should be set")
		})
	}
}
