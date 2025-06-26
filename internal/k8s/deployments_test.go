// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package k8s

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	temporaliov1alpha1 "github.com/DataDog/temporal-worker-controller/api/v1alpha1"
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
							Status:             v1.ConditionTrue,
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
							Status: v1.ConditionFalse,
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
			healthy, healthySince := IsDeploymentHealthy(tt.deployment)
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
				BuildIDLabel: "v1",
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
				BuildIDLabel: "v2",
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
	_ = v1.AddToScheme(scheme)
	_ = temporaliov1alpha1.AddToScheme(scheme)

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(owner, deploy1, deploy2, deployWithoutLabel).
		WithIndex(&appsv1.Deployment{}, deployOwnerKey, func(rawObj client.Object) []string {
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
	state, err := GetDeploymentState(ctx, fakeClient, "default", "test-worker", "test-worker")
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

func TestGenerateBuildID_SameImageDifferentPods(t *testing.T) {
	img := "my.test_image"
	pod1 := MakePodSpec([]v1.Container{{Image: img}}, map[string]string{"pod": "1"})
	pod2 := MakePodSpec([]v1.Container{{Image: img}}, map[string]string{"pod": "2"})

	twd1 := MakeTWD(1, pod1, nil, nil, nil)
	twd2 := MakeTWD(1, pod2, nil, nil, nil)

	build1 := ComputeBuildID(twd1)
	build2 := ComputeBuildID(twd2)

	// check that the builds have different suffixes
	assert.NotEqual(t, build1, build2)

	verifyBuildId(t, build1, "my-test-image", 4)
	verifyBuildId(t, build2, "my-test-image", 4)
}

func TestGenerateBuildID_SamePodDifferentTWD(t *testing.T) {
	img := "my.test_image"
	pod := MakePodSpec([]v1.Container{{Image: img}}, nil)

	twd1 := MakeTWD(1, pod, nil, nil, nil)
	twd2 := MakeTWD(2, pod, nil, nil, nil)

	build1 := ComputeBuildID(twd1)
	build2 := ComputeBuildID(twd2)

	// check that the build ids are the same despite different TWD, because pod spec is the same
	assert.True(t, build1 == build2)
	verifyBuildId(t, build1, "my-test-image", 4)
}

func TestGenerateBuildID_NoContainers(t *testing.T) {
	twd := MakeTWD(1, MakePodSpec(nil, nil), nil, nil, nil)
	build := ComputeBuildID(twd)
	verifyBuildId(t, build, "", 10) // expect long hash no prefix
}

func TestGenerateBuildID_EmptyImage(t *testing.T) {
	build := ComputeBuildID(MakeTWDWithImage(""))
	verifyBuildId(t, build, "", 10) // expect long hash no prefix
}

func TestGenerateBuildID_ImageFormatting(t *testing.T) {
	verifyBuildIDForImage := func(t *testing.T, imageName, expectedPrefix string) {
		build := ComputeBuildID(MakeTWDWithImage(imageName))
		verifyBuildId(t, build, expectedPrefix, 4)
	}
	digest := "a428de44a9059f31a59237a5881c2d2cffa93757d99026156e4ea544577ab7f3" // 64 chars

	taggedDigestImg := "docker.io/library/busybox:latest@sha256:" + digest
	verifyBuildIDForImage(t, taggedDigestImg, "latest")

	taggedNamedImg := "docker.io/library/busybox:latest"
	verifyBuildIDForImage(t, taggedNamedImg, "latest")

	digestedImg := "docker.io@sha256:" + digest
	verifyBuildIDForImage(t, digestedImg, digest[:maxBuildIdLen-5])

	digestedNamedImg := "docker.io/library/busybo@sha256:" + digest
	verifyBuildIDForImage(t, digestedNamedImg, digest[:maxBuildIdLen-5])

	namedImg := "docker.io/library/busybox"
	verifyBuildIDForImage(t, namedImg, "library-busybox")

	illegalCharsImg := "this.is.my_weird/image"
	verifyBuildIDForImage(t, illegalCharsImg, "this-is-my-weird-image")

	longImg := "ThisIsAVeryLongHumanReadableImage_ThisIsAVeryLongHumanReadableImage_ThisIsAVeryLongHumanReadableImage" // 101 chars
	verifyBuildIDForImage(t, longImg, cleanAndTruncateString(longImg[:maxBuildIdLen-5], -1))
}

func verifyBuildId(t *testing.T, build, expectedPrefix string, expectedHashLen int) {
	assert.Truef(t, strings.HasPrefix(build, expectedPrefix), "expected prefix %s in build %s", expectedPrefix, build)
	assert.LessOrEqual(t, len(build), maxBuildIdLen)
	assert.Equalf(t, cleanAndTruncateString(build, -1), build, "expected build %s to be cleaned", build)
	split := strings.Split(build, k8sResourceNameSeparator)
	assert.Equalf(t, expectedHashLen, len(split[len(split)-1]), "expected build %s to have %d-digit hash suffix", build, expectedHashLen)
}
