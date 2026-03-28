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

// expectedWorkerResourceTemplateName replicates the naming logic for use in tests.
func expectedWorkerResourceTemplateName(twdName, wrtName, buildID string) string {
	h := sha256.Sum256([]byte(twdName + wrtName + buildID))
	hashSuffix := hex.EncodeToString(h[:4])
	raw := CleanStringForDNS(twdName + "-" + wrtName + "-" + buildID)
	prefix := strings.TrimRight(TruncateString(raw, 47-9), "-")
	return prefix + "-" + hashSuffix
}

func TestComputeWorkerResourceTemplateName(t *testing.T) {
	t.Run("short names produce human-readable result with hash suffix", func(t *testing.T) {
		got := ComputeWorkerResourceTemplateName("my-worker", "my-hpa", "image-abc123")
		// Should start with the human-readable prefix
		assert.True(t, strings.HasPrefix(got, "my-worker-my-hpa-image-abc123-"), "got: %q", got)
		// Should be ≤ 47 chars
		assert.LessOrEqual(t, len(got), 47)
	})

	t.Run("special chars are cleaned for DNS", func(t *testing.T) {
		got := ComputeWorkerResourceTemplateName("my_worker", "my/hpa", "image:latest")
		assert.True(t, strings.HasPrefix(got, "my-worker-my-hpa-image-latest-"), "got: %q", got)
		assert.LessOrEqual(t, len(got), 47)
	})

	t.Run("deterministic — same inputs always produce same name", func(t *testing.T) {
		a := ComputeWorkerResourceTemplateName("w", "r", "b1")
		b := ComputeWorkerResourceTemplateName("w", "r", "b1")
		assert.Equal(t, a, b)
	})

	t.Run("different buildIDs always produce different names (hash suffix)", func(t *testing.T) {
		// Even if the prefix would be identical after truncation, the hash must differ.
		name1 := ComputeWorkerResourceTemplateName("my-worker", "my-hpa", "build-aaa")
		name2 := ComputeWorkerResourceTemplateName("my-worker", "my-hpa", "build-bbb")
		assert.NotEqual(t, name1, name2)
	})

	t.Run("very long names are still ≤ 47 chars and distinct per buildID", func(t *testing.T) {
		longTWD := strings.Repeat("w", 63)
		longWRT := strings.Repeat("r", 253) // maximum k8s object name
		buildID1 := "build-" + strings.Repeat("a", 57)
		buildID2 := "build-" + strings.Repeat("b", 57)

		n1 := ComputeWorkerResourceTemplateName(longTWD, longWRT, buildID1)
		n2 := ComputeWorkerResourceTemplateName(longTWD, longWRT, buildID2)

		assert.LessOrEqual(t, len(n1), 47, "name1 length: %d", len(n1))
		assert.LessOrEqual(t, len(n2), 47, "name2 length: %d", len(n2))
		assert.NotEqual(t, n1, n2, "names must differ even when prefix is fully truncated")
	})

	t.Run("name matches expected formula", func(t *testing.T) {
		got := ComputeWorkerResourceTemplateName("my-worker", "my-hpa", "abc123")
		assert.Equal(t, expectedWorkerResourceTemplateName("my-worker", "my-hpa", "abc123"), got)
	})
}

func TestComputeSelectorLabels(t *testing.T) {
	labels := ComputeSelectorLabels("my-worker", "abc-123")
	assert.Equal(t, "my-worker", labels[twdNameLabel])
	assert.Equal(t, "abc-123", labels[BuildIDLabel])
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
		autoInjectFields(spec, "my-worker-abc123", selectorLabels, nil)
		_, hasKey := spec["scaleTargetRef"]
		assert.False(t, hasKey, "scaleTargetRef should not be injected when absent (user must opt in with {})")
	})

	t.Run("injects scaleTargetRef when empty object (opt-in sentinel)", func(t *testing.T) {
		spec := map[string]interface{}{
			"scaleTargetRef": map[string]interface{}{},
		}
		autoInjectFields(spec, "my-worker-abc123", selectorLabels, nil)
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
		autoInjectFields(spec, "my-worker-abc123", selectorLabels, nil)
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
		autoInjectFields(spec, "my-worker-abc123", selectorLabels, nil)
		selector := spec["selector"].(map[string]interface{})
		_, hasKey := selector["matchLabels"]
		assert.False(t, hasKey, "matchLabels should not be injected when absent (user must opt in with {})")
	})

	t.Run("injects matchLabels when empty object (opt-in sentinel)", func(t *testing.T) {
		spec := map[string]interface{}{
			"selector": map[string]interface{}{
				"matchLabels": map[string]interface{}{},
			},
		}
		autoInjectFields(spec, "my-worker-abc123", selectorLabels, nil)
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
		autoInjectFields(spec, "my-worker-abc123", selectorLabels, nil)
		selector := spec["selector"].(map[string]interface{})
		labels := selector["matchLabels"].(map[string]interface{})
		assert.Equal(t, "label", labels["custom"], "should not overwrite user-provided labels")
	})

	// pod selector labels must NOT bleed into metric selectors (separate injection paths).
	t.Run("pod selector labels are not injected into metric selector matchLabels", func(t *testing.T) {
		metricLabels := map[string]string{
			"temporal_worker_deployment_name": "default_my-worker",
			"temporal_worker_build_id":        "abc123",
			"temporal_namespace":              "my-ns",
		}
		spec := map[string]interface{}{
			"selector": map[string]interface{}{
				"matchLabels": map[string]interface{}{}, // opt-in for pod selector
			},
			"metrics": []interface{}{
				map[string]interface{}{
					"type": "External",
					"external": map[string]interface{}{
						"metric": map[string]interface{}{
							"name": "temporal_backlog_count_by_version",
							"selector": map[string]interface{}{
								"matchLabels": map[string]interface{}{}, // opt-in for metric selector
							},
						},
					},
				},
			},
		}
		autoInjectFields(spec, "my-worker-abc123", selectorLabels, metricLabels)

		// spec.selector.matchLabels gets pod selector labels only
		topSelector := spec["selector"].(map[string]interface{})
		topLabels, ok := topSelector["matchLabels"].(map[string]interface{})
		require.True(t, ok)
		assert.Equal(t, "abc123", topLabels[BuildIDLabel])
		assert.NotContains(t, topLabels, "temporal_worker_deployment_name", "pod selector must not get metric labels")

		// metric selector gets temporal metric labels only
		metrics := spec["metrics"].([]interface{})
		ml := metrics[0].(map[string]interface{})["external"].(map[string]interface{})["metric"].(map[string]interface{})["selector"].(map[string]interface{})["matchLabels"].(map[string]interface{})
		assert.Equal(t, "default_my-worker", ml["temporal_worker_deployment_name"])
		assert.Equal(t, "abc123", ml["temporal_worker_build_id"])
		assert.Equal(t, "my-ns", ml["temporal_namespace"])
		assert.NotContains(t, ml, BuildIDLabel, "metric selector must not get pod selector labels")
	})
}

func TestAutoInjectFields_MetricSelector(t *testing.T) {
	metricLabels := map[string]string{
		"temporal_worker_deployment_name": "default_my-worker",
		"temporal_worker_build_id":        "abc123",
		"temporal_namespace":              "my-ns",
	}
	podLabels := map[string]string{BuildIDLabel: "abc123", twdNameLabel: "my-worker"}

	metricSpec := func(matchLabels interface{}) map[string]interface{} {
		return map[string]interface{}{
			"metrics": []interface{}{
				map[string]interface{}{
					"type": "External",
					"external": map[string]interface{}{
						"metric": map[string]interface{}{
							"name": "temporal_backlog_count_by_version",
							"selector": map[string]interface{}{
								"matchLabels": matchLabels,
							},
						},
					},
				},
			},
		}
	}

	t.Run("injects temporal labels when matchLabels is empty ({})", func(t *testing.T) {
		spec := metricSpec(map[string]interface{}{})
		autoInjectFields(spec, "my-worker-abc123", podLabels, metricLabels)
		ml := spec["metrics"].([]interface{})[0].(map[string]interface{})["external"].(map[string]interface{})["metric"].(map[string]interface{})["selector"].(map[string]interface{})["matchLabels"].(map[string]interface{})
		assert.Equal(t, "default_my-worker", ml["temporal_worker_deployment_name"])
		assert.Equal(t, "abc123", ml["temporal_worker_build_id"])
		assert.Equal(t, "my-ns", ml["temporal_namespace"])
	})

	t.Run("merges temporal labels alongside user labels", func(t *testing.T) {
		spec := metricSpec(map[string]interface{}{"task_type": "Activity"})
		autoInjectFields(spec, "my-worker-abc123", podLabels, metricLabels)
		ml := spec["metrics"].([]interface{})[0].(map[string]interface{})["external"].(map[string]interface{})["metric"].(map[string]interface{})["selector"].(map[string]interface{})["matchLabels"].(map[string]interface{})
		assert.Equal(t, "Activity", ml["task_type"], "user label must be preserved")
		assert.Equal(t, "default_my-worker", ml["temporal_worker_deployment_name"])
		assert.Equal(t, "abc123", ml["temporal_worker_build_id"])
	})

	t.Run("does not inject when matchLabels key is absent", func(t *testing.T) {
		spec := map[string]interface{}{
			"metrics": []interface{}{
				map[string]interface{}{
					"type": "External",
					"external": map[string]interface{}{
						"metric": map[string]interface{}{
							"name":     "temporal_backlog_count_by_version",
							"selector": map[string]interface{}{},
						},
					},
				},
			},
		}
		autoInjectFields(spec, "my-worker-abc123", podLabels, metricLabels)
		sel := spec["metrics"].([]interface{})[0].(map[string]interface{})["external"].(map[string]interface{})["metric"].(map[string]interface{})["selector"].(map[string]interface{})
		_, hasMatchLabels := sel["matchLabels"]
		assert.False(t, hasMatchLabels, "matchLabels must not be created when absent")
	})

	t.Run("no-op when metricSelectorLabels is nil", func(t *testing.T) {
		spec := metricSpec(map[string]interface{}{})
		autoInjectFields(spec, "my-worker-abc123", podLabels, nil)
		ml := spec["metrics"].([]interface{})[0].(map[string]interface{})["external"].(map[string]interface{})["metric"].(map[string]interface{})["selector"].(map[string]interface{})["matchLabels"].(map[string]interface{})
		assert.Empty(t, ml, "no metric labels should be injected when metricSelectorLabels is nil")
	})
}

func TestRenderWorkerResourceTemplate(t *testing.T) {
	hpaSpec := map[string]interface{}{
		"apiVersion": "autoscaling/v2",
		"kind":       "HorizontalPodAutoscaler",
		"spec": map[string]interface{}{
			"scaleTargetRef": map[string]interface{}{}, // opt in to auto-injection
			"minReplicas":    float64(2),
			"maxReplicas":    float64(10),
		},
	}
	rawBytes, err := json.Marshal(hpaSpec)
	require.NoError(t, err)

	wrt := &temporaliov1alpha1.WorkerResourceTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-hpa",
			Namespace: "default",
			UID:       types.UID("wrt-uid-456"),
		},
		Spec: temporaliov1alpha1.WorkerResourceTemplateSpec{
			TemporalWorkerDeploymentRef: temporaliov1alpha1.TemporalWorkerDeploymentReference{
				Name: "my-worker",
			},
			Template: runtime.RawExtension{Raw: rawBytes},
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

	obj, err := RenderWorkerResourceTemplate(wrt, deployment, buildID, "my-temporal-ns")
	require.NoError(t, err)

	// Check metadata — name follows the hash-suffix formula
	assert.Equal(t, expectedWorkerResourceTemplateName("my-worker", "my-hpa", "abc123"), obj.GetName())
	assert.Equal(t, "default", obj.GetNamespace())

	// Check selector labels were added
	labels := obj.GetLabels()
	assert.Equal(t, "abc123", labels[BuildIDLabel])
	assert.Equal(t, "my-worker", labels[twdNameLabel])

	// Check owner reference points to the WRT
	ownerRefs := obj.GetOwnerReferences()
	require.Len(t, ownerRefs, 1)
	assert.Equal(t, "my-hpa", ownerRefs[0].Name)
	assert.Equal(t, "WorkerResourceTemplate", ownerRefs[0].Kind)
	assert.Equal(t, types.UID("wrt-uid-456"), ownerRefs[0].UID)

	// Check scaleTargetRef was auto-injected
	spec, ok := obj.Object["spec"].(map[string]interface{})
	require.True(t, ok)
	ref, ok := spec["scaleTargetRef"].(map[string]interface{})
	require.True(t, ok, "scaleTargetRef should have been auto-injected")
	assert.Equal(t, "my-worker-abc123", ref["name"])
}
