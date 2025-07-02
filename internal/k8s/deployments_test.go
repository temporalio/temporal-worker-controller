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

	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	"github.com/temporalio/temporal-worker-controller/internal/testhelpers"
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
				pod1 := testhelpers.MakePodSpec([]v1.Container{{Image: img}}, map[string]string{"pod": "1"})
				pod2 := testhelpers.MakePodSpec([]v1.Container{{Image: img}}, map[string]string{"pod": "2"})

				twd1 := testhelpers.MakeTWD(1, pod1, nil, nil, nil)
				twd2 := testhelpers.MakeTWD(1, pod2, nil, nil, nil)
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
				pod := testhelpers.MakePodSpec([]v1.Container{{Image: img}}, nil)

				twd1 := testhelpers.MakeTWD(1, pod, nil, nil, nil)
				twd2 := testhelpers.MakeTWD(2, pod, nil, nil, nil)
				return twd1, twd2
			},
			expectedPrefix:  "my-test-image",
			expectedHashLen: 4,
			expectEquality:  true, // should be the same
		},
		{
			name: "no containers",
			generateInputs: func() (*temporaliov1alpha1.TemporalWorkerDeployment, *temporaliov1alpha1.TemporalWorkerDeployment) {
				twd := testhelpers.MakeTWD(1, testhelpers.MakePodSpec(nil, nil), nil, nil, nil)
				return twd, nil // only check 1 result, no need to compare
			},
			expectedPrefix:  "",
			expectedHashLen: 10, // expect long hash no prefix
			expectEquality:  false,
		},
		{
			name: "empty image",
			generateInputs: func() (*temporaliov1alpha1.TemporalWorkerDeployment, *temporaliov1alpha1.TemporalWorkerDeployment) {
				twd := testhelpers.MakeTWDWithImage("")
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
				twd := testhelpers.MakeTWDWithImage(taggedDigestImg)
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
				twd := testhelpers.MakeTWDWithImage(taggedNamedImg)
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
				twd := testhelpers.MakeTWDWithImage(digestedImg)
				return twd, nil // only check 1 result, no need to compare
			},
			expectedPrefix:  digest[:maxBuildIdLen-5],
			expectedHashLen: 4,
			expectEquality:  false,
		},
		{
			name: "digested named image",
			generateInputs: func() (*temporaliov1alpha1.TemporalWorkerDeployment, *temporaliov1alpha1.TemporalWorkerDeployment) {
				digestedNamedImg := "docker.io/library/busybo@sha256:" + digest
				twd := testhelpers.MakeTWDWithImage(digestedNamedImg)
				return twd, nil // only check 1 result, no need to compare
			},
			expectedPrefix:  digest[:maxBuildIdLen-5],
			expectedHashLen: 4,
			expectEquality:  false,
		},
		{
			name: "named image",
			generateInputs: func() (*temporaliov1alpha1.TemporalWorkerDeployment, *temporaliov1alpha1.TemporalWorkerDeployment) {
				namedImg := "docker.io/library/busybox"
				twd := testhelpers.MakeTWDWithImage(namedImg)
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
				twd := testhelpers.MakeTWDWithImage(illegalCharsImg)
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
				twd := testhelpers.MakeTWDWithImage(longImg)
				return twd, nil // only check 1 result, no need to compare
			},
			expectedPrefix:  cleanAndTruncateString("ThisIsAVeryLongHumanReadableImage_ThisIsAVeryLongHumanReadableImage_ThisIsAVeryLongHumanReadableImage"[:maxBuildIdLen-5], -1),
			expectedHashLen: 4,
			expectEquality:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			twd1, twd2 := tt.generateInputs()

			var build1, build2 string
			if twd1 != nil {
				build1 = ComputeBuildID(twd1)
				verifyBuildId(t, build1, tt.expectedPrefix, tt.expectedHashLen)
			}
			if twd2 != nil {
				build2 = ComputeBuildID(twd2)
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
	assert.LessOrEqual(t, len(build), maxBuildIdLen)
	assert.Equalf(t, cleanAndTruncateString(build, -1), build, "expected build %s to be cleaned", build)
	split := strings.Split(build, k8sResourceNameSeparator)
	assert.Equalf(t, expectedHashLen, len(split[len(split)-1]), "expected build %s to have %d-digit hash suffix", build, expectedHashLen)
}
