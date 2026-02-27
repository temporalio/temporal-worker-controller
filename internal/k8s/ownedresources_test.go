// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package k8s

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
)

// expectedOwnedResourceName replicates the naming logic for use in tests.
func expectedOwnedResourceName(twdName, tworName, buildID string) string {
	h := sha256.Sum256([]byte(twdName + tworName + buildID))
	hashSuffix := hex.EncodeToString(h[:4])
	raw := CleanStringForDNS(twdName + "-" + tworName + "-" + buildID)
	prefix := strings.TrimRight(TruncateString(raw, 253-9), "-")
	return prefix + "-" + hashSuffix
}

func TestComputeOwnedResourceName(t *testing.T) {
	t.Run("short names produce human-readable result with hash suffix", func(t *testing.T) {
		got := ComputeOwnedResourceName("my-worker", "my-hpa", "image-abc123")
		// Should start with the human-readable prefix
		assert.True(t, strings.HasPrefix(got, "my-worker-my-hpa-image-abc123-"), "got: %q", got)
		// Should be ≤ 253 chars
		assert.LessOrEqual(t, len(got), 253)
	})

	t.Run("special chars are cleaned for DNS", func(t *testing.T) {
		got := ComputeOwnedResourceName("my_worker", "my/hpa", "image:latest")
		assert.True(t, strings.HasPrefix(got, "my-worker-my-hpa-image-latest-"), "got: %q", got)
		assert.LessOrEqual(t, len(got), 253)
	})

	t.Run("deterministic — same inputs always produce same name", func(t *testing.T) {
		a := ComputeOwnedResourceName("w", "r", "b1")
		b := ComputeOwnedResourceName("w", "r", "b1")
		assert.Equal(t, a, b)
	})

	t.Run("different buildIDs always produce different names (hash suffix)", func(t *testing.T) {
		// Even if the prefix would be identical after truncation, the hash must differ.
		name1 := ComputeOwnedResourceName("my-worker", "my-hpa", "build-aaa")
		name2 := ComputeOwnedResourceName("my-worker", "my-hpa", "build-bbb")
		assert.NotEqual(t, name1, name2)
	})

	t.Run("very long names are still ≤ 253 chars and distinct per buildID", func(t *testing.T) {
		longTWD := strings.Repeat("w", 63)
		longTWOR := strings.Repeat("r", 253) // maximum k8s object name
		buildID1 := "build-" + strings.Repeat("a", 57)
		buildID2 := "build-" + strings.Repeat("b", 57)

		n1 := ComputeOwnedResourceName(longTWD, longTWOR, buildID1)
		n2 := ComputeOwnedResourceName(longTWD, longTWOR, buildID2)

		assert.LessOrEqual(t, len(n1), 253, "name1 length: %d", len(n1))
		assert.LessOrEqual(t, len(n2), 253, "name2 length: %d", len(n2))
		assert.NotEqual(t, n1, n2, "names must differ even when prefix is fully truncated")
	})

	t.Run("name matches expected formula", func(t *testing.T) {
		got := ComputeOwnedResourceName("my-worker", "my-hpa", "abc123")
		assert.Equal(t, expectedOwnedResourceName("my-worker", "my-hpa", "abc123"), got)
	})
}

func TestComputeSelectorLabels(t *testing.T) {
	labels := ComputeSelectorLabels("my-worker", "abc-123")
	assert.Equal(t, "my-worker", labels[twdNameLabel])
	assert.Equal(t, "abc-123", labels[BuildIDLabel])
}

func TestContainsTemplateMarker(t *testing.T) {
	assert.True(t, containsTemplateMarker("hello {{ .DeploymentName }}"))
	assert.False(t, containsTemplateMarker("hello world"))
	assert.False(t, containsTemplateMarker("{ single brace }"))
}

func TestRenderString(t *testing.T) {
	data := TemplateData{
		DeploymentName:    "my-worker-abc123",
		TemporalNamespace: "my-temporal-ns",
		BuildID:           "abc123",
	}

	tests := []struct {
		input string
		want  string
	}{
		{"plain string", "plain string"},
		{"{{ .DeploymentName }}", "my-worker-abc123"},
		{"{{ .TemporalNamespace }}", "my-temporal-ns"},
		{"{{ .BuildID }}", "abc123"},
		{"Monitor for build {{ .BuildID }}", "Monitor for build abc123"},
		{"{{ .DeploymentName }}.{{ .TemporalNamespace }}", "my-worker-abc123.my-temporal-ns"},
	}
	for _, tc := range tests {
		got, err := renderString(tc.input, data)
		require.NoError(t, err)
		assert.Equal(t, tc.want, got)
	}
}

func TestAutoInjectFields_ScaleTargetRef(t *testing.T) {
	selectorLabels := map[string]string{
		BuildIDLabel: "abc123",
		twdNameLabel: "my-worker",
	}

	t.Run("does not inject scaleTargetRef when key is entirely absent", func(t *testing.T) {
		spec := map[string]interface{}{
			"minReplicas": 1,
			"maxReplicas": 5,
		}
		autoInjectFields(spec, "my-worker-abc123", selectorLabels)
		_, hasKey := spec["scaleTargetRef"]
		assert.False(t, hasKey, "scaleTargetRef should not be injected when absent (user must opt in with null)")
	})

	t.Run("injects scaleTargetRef when explicitly null (user opt-in)", func(t *testing.T) {
		spec := map[string]interface{}{
			"scaleTargetRef": nil,
		}
		autoInjectFields(spec, "my-worker-abc123", selectorLabels)
		ref, ok := spec["scaleTargetRef"].(map[string]interface{})
		require.True(t, ok)
		assert.Equal(t, "my-worker-abc123", ref["name"])
		assert.Equal(t, "Deployment", ref["kind"])
		assert.Equal(t, appsv1.SchemeGroupVersion.String(), ref["apiVersion"])
	})

	t.Run("does not overwrite existing scaleTargetRef", func(t *testing.T) {
		spec := map[string]interface{}{
			"scaleTargetRef": map[string]interface{}{
				"name": "custom-deployment",
				"kind": "Deployment",
			},
		}
		autoInjectFields(spec, "my-worker-abc123", selectorLabels)
		ref := spec["scaleTargetRef"].(map[string]interface{})
		assert.Equal(t, "custom-deployment", ref["name"], "should not overwrite user-provided ref")
	})
}

func TestAutoInjectFields_MatchLabels(t *testing.T) {
	selectorLabels := map[string]string{
		BuildIDLabel: "abc123",
		twdNameLabel: "my-worker",
	}

	t.Run("does not inject matchLabels when key is absent", func(t *testing.T) {
		spec := map[string]interface{}{
			"selector": map[string]interface{}{},
		}
		autoInjectFields(spec, "my-worker-abc123", selectorLabels)
		selector := spec["selector"].(map[string]interface{})
		_, hasKey := selector["matchLabels"]
		assert.False(t, hasKey, "matchLabels should not be injected when absent (user must opt in with null)")
	})

	t.Run("injects matchLabels when explicitly null (user opt-in)", func(t *testing.T) {
		spec := map[string]interface{}{
			"selector": map[string]interface{}{
				"matchLabels": nil,
			},
		}
		autoInjectFields(spec, "my-worker-abc123", selectorLabels)
		selector := spec["selector"].(map[string]interface{})
		labels, ok := selector["matchLabels"].(map[string]interface{})
		require.True(t, ok)
		assert.Equal(t, "abc123", labels[BuildIDLabel])
		assert.Equal(t, "my-worker", labels[twdNameLabel])
	})

	t.Run("does not overwrite existing matchLabels", func(t *testing.T) {
		spec := map[string]interface{}{
			"selector": map[string]interface{}{
				"matchLabels": map[string]interface{}{
					"custom": "label",
				},
			},
		}
		autoInjectFields(spec, "my-worker-abc123", selectorLabels)
		selector := spec["selector"].(map[string]interface{})
		labels := selector["matchLabels"].(map[string]interface{})
		assert.Equal(t, "label", labels["custom"], "should not overwrite user-provided labels")
	})
}

func TestRenderOwnedResource(t *testing.T) {
	hpaSpec := map[string]interface{}{
		"apiVersion": "autoscaling/v2",
		"kind":       "HorizontalPodAutoscaler",
		"spec": map[string]interface{}{
			"scaleTargetRef": nil, // opt in to auto-injection
			"minReplicas":    float64(2),
			"maxReplicas":    float64(10),
		},
	}
	rawBytes, err := json.Marshal(hpaSpec)
	require.NoError(t, err)

	twor := &temporaliov1alpha1.TemporalWorkerOwnedResource{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-hpa",
			Namespace: "default",
		},
		Spec: temporaliov1alpha1.TemporalWorkerOwnedResourceSpec{
			WorkerRef: temporaliov1alpha1.WorkerDeploymentReference{
				Name: "my-worker",
			},
			Object: runtime.RawExtension{Raw: rawBytes},
		},
	}

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-worker-abc123",
			Namespace: "default",
			UID:       types.UID("test-uid-123"),
		},
	}
	buildID := "abc123"

	obj, err := RenderOwnedResource(twor, deployment, buildID, "my-temporal-ns")
	require.NoError(t, err)

	// Check metadata — name follows the hash-suffix formula
	assert.Equal(t, expectedOwnedResourceName("my-worker", "my-hpa", "abc123"), obj.GetName())
	assert.Equal(t, "default", obj.GetNamespace())

	// Check selector labels were added
	labels := obj.GetLabels()
	assert.Equal(t, "abc123", labels[BuildIDLabel])
	assert.Equal(t, "my-worker", labels[twdNameLabel])

	// Check owner reference points to the Deployment
	ownerRefs := obj.GetOwnerReferences()
	require.Len(t, ownerRefs, 1)
	assert.Equal(t, "my-worker-abc123", ownerRefs[0].Name)
	assert.Equal(t, "Deployment", ownerRefs[0].Kind)
	assert.Equal(t, types.UID("test-uid-123"), ownerRefs[0].UID)

	// Check scaleTargetRef was auto-injected
	spec, ok := obj.Object["spec"].(map[string]interface{})
	require.True(t, ok)
	ref, ok := spec["scaleTargetRef"].(map[string]interface{})
	require.True(t, ok, "scaleTargetRef should have been auto-injected")
	assert.Equal(t, "my-worker-abc123", ref["name"])
}

func TestRenderOwnedResource_WithTemplates(t *testing.T) {
	objSpec := map[string]interface{}{
		"apiVersion": "monitoring.example.com/v1",
		"kind":       "WorkloadMonitor",
		"spec": map[string]interface{}{
			"targetWorkload": "{{ .DeploymentName }}",
			"description":    "Monitor for build {{ .BuildID }} in {{ .TemporalNamespace }}",
		},
	}
	rawBytes, err := json.Marshal(objSpec)
	require.NoError(t, err)

	twor := &temporaliov1alpha1.TemporalWorkerOwnedResource{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-monitor",
			Namespace: "production",
		},
		Spec: temporaliov1alpha1.TemporalWorkerOwnedResourceSpec{
			WorkerRef: temporaliov1alpha1.WorkerDeploymentReference{
				Name: "my-worker",
			},
			Object: runtime.RawExtension{Raw: rawBytes},
		},
	}

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-worker-abc123",
			Namespace: "production",
			UID:       types.UID("uid-abc"),
		},
	}

	obj, err := RenderOwnedResource(twor, deployment, "abc123", "my-temporal-ns")
	require.NoError(t, err)

	spec, ok := obj.Object["spec"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "my-worker-abc123", spec["targetWorkload"])
	assert.Equal(t, "Monitor for build abc123 in my-temporal-ns", spec["description"])
}
