// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package k8s_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	"github.com/temporalio/temporal-worker-controller/internal/k8s"
	"github.com/temporalio/temporal-worker-controller/internal/testhelpers"
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
	assert.Equal(t, "worker-v1", state.Deployments["test-worker.v1"].Name)
	assert.Equal(t, "worker-v2", state.Deployments["test-worker.v2"].Name)

	// Verify the deployments are sorted by creation time (oldest first)
	assert.Equal(t, "worker-v1", state.DeploymentsByTime[0].Name)
	assert.Equal(t, "worker-v2", state.DeploymentsByTime[1].Name)

	// Verify refs are correctly created
	assert.Equal(t, "worker-v1", state.DeploymentRefs["test-worker.v1"].Name)
	assert.Equal(t, "worker-v2", state.DeploymentRefs["test-worker.v2"].Name)
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
			expectedPrefix:  "my-test-image",
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
			expectedPrefix:  "my-test-image",
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
			expectedPrefix:  "this-is-my-weird-image",
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
			expectedPrefix:  k8s.CleanAndTruncateString("ThisIsAVeryLongHumanReadableImage_ThisIsAVeryLongHumanReadableImage_ThisIsAVeryLongHumanReadableImage"[:k8s.MaxBuildIdLen-5], -1),
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
	assert.Equalf(t, k8s.CleanAndTruncateString(build, -1), build, "expected build %s to be cleaned", build)
	split := strings.Split(build, k8s.K8sResourceNameSeparator)
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
			baseName:     "worker-name.default",
			buildID:      "abc123",
			expectedName: "worker-name.default-abc123",
		},
		{
			name:         "build ID with dots/dashes",
			baseName:     "worker-name.production",
			buildID:      "image-v2.1.0-a1b2c3d4",
			expectedName: "worker-name.production-image-v2.1.0-a1b2c3d4",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := k8s.ComputeVersionedDeploymentName(tt.baseName, tt.buildID)
			assert.Equal(t, tt.expectedName, result)

			// Verify the format is always baseName-buildID
			expectedSeparator := tt.baseName + "-" + tt.buildID
			assert.Equal(t, expectedSeparator, result)

			// Verify it ends with the build ID
			assert.True(t, strings.HasSuffix(result, "-"+tt.buildID),
				"versioned deployment name should end with '-buildID'")

			// Verify it starts with the base name
			assert.True(t, strings.HasPrefix(result, tt.baseName),
				"versioned deployment name should start with baseName")
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
	versionedName := k8s.ComputeVersionedDeploymentName(workerDeploymentName, buildID)

	// Verify the expected formats
	assert.Equal(t, "hello-world"+k8s.DeploymentNameSeparator+"demo", workerDeploymentName)
	assert.True(t, strings.HasPrefix(versionedName, "hello-world"+k8s.DeploymentNameSeparator+"demo-"))
	assert.True(t, strings.Contains(versionedName, "v1-0-0"), "versioned name should contain cleaned image tag")

	// Verify the version ID combines worker deployment name and build ID
	versionID := k8s.ComputeVersionID(twd)
	expectedVersionID := workerDeploymentName + k8s.VersionIDSeparator + buildID
	assert.Equal(t, expectedVersionID, versionID)
	assert.Equal(t, "hello-world"+k8s.DeploymentNameSeparator+"demo"+k8s.VersionIDSeparator+"v1-0-0-dd84", versionID)
}

// TestNewDeploymentWithPodAnnotations tests that every new pod created has a connection spec hash annotation
func TestNewDeploymentWithPodAnnotations(t *testing.T) {
	connection := temporaliov1alpha1.TemporalConnectionSpec{
		HostPort:        "localhost:7233",
		MutualTLSSecret: "my-secret",
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
			HostPort:        "localhost:7233",
			MutualTLSSecret: "my-tls-secret",
		}

		result := k8s.ComputeConnectionSpecHash(spec)
		assert.NotEmpty(t, result, "Hash should not be empty for valid spec")
		assert.Len(t, result, 64, "SHA256 hash should be 64 characters") // hex encoded SHA256
	})

	t.Run("returns empty hash when hostport is empty", func(t *testing.T) {
		spec := temporaliov1alpha1.TemporalConnectionSpec{
			HostPort:        "",
			MutualTLSSecret: "secret",
		}

		result := k8s.ComputeConnectionSpecHash(spec)
		assert.Empty(t, result, "Hash should be empty when hostport is empty")
	})

	t.Run("is deterministic - same input produces same hash", func(t *testing.T) {
		spec := temporaliov1alpha1.TemporalConnectionSpec{
			HostPort:        "localhost:7233",
			MutualTLSSecret: "my-secret",
		}

		hash1 := k8s.ComputeConnectionSpecHash(spec)
		hash2 := k8s.ComputeConnectionSpecHash(spec)

		assert.Equal(t, hash1, hash2, "Same input should produce identical hashes")
	})

	t.Run("different hostports produce different hashes", func(t *testing.T) {
		spec1 := temporaliov1alpha1.TemporalConnectionSpec{
			HostPort:        "localhost:7233",
			MutualTLSSecret: "same-secret",
		}
		spec2 := temporaliov1alpha1.TemporalConnectionSpec{
			HostPort:        "different-host:7233",
			MutualTLSSecret: "same-secret",
		}

		hash1 := k8s.ComputeConnectionSpecHash(spec1)
		hash2 := k8s.ComputeConnectionSpecHash(spec2)

		assert.NotEqual(t, hash1, hash2, "Different hostports should produce different hashes")
	})

	t.Run("different mTLS secrets produce different hashes", func(t *testing.T) {
		spec1 := temporaliov1alpha1.TemporalConnectionSpec{
			HostPort:        "localhost:7233",
			MutualTLSSecret: "secret1",
		}
		spec2 := temporaliov1alpha1.TemporalConnectionSpec{
			HostPort:        "localhost:7233",
			MutualTLSSecret: "secret2",
		}

		hash1 := k8s.ComputeConnectionSpecHash(spec1)
		hash2 := k8s.ComputeConnectionSpecHash(spec2)

		assert.NotEqual(t, hash1, hash2, "Different mTLS secrets should produce different hashes")
	})

	t.Run("empty mTLS secret vs non-empty produce different hashes", func(t *testing.T) {
		spec1 := temporaliov1alpha1.TemporalConnectionSpec{
			HostPort:        "localhost:7233",
			MutualTLSSecret: "",
		}
		spec2 := temporaliov1alpha1.TemporalConnectionSpec{
			HostPort:        "localhost:7233",
			MutualTLSSecret: "some-secret",
		}

		hash1 := k8s.ComputeConnectionSpecHash(spec1)
		hash2 := k8s.ComputeConnectionSpecHash(spec2)

		assert.NotEqual(t, hash1, hash2, "Empty vs non-empty mTLS secret should produce different hashes")
		assert.NotEmpty(t, hash1, "Hash should still be generated even with empty mTLS secret")
	})
}
