package k8s

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBuildTemporalTokens(t *testing.T) {
	got := BuildTemporalTokens("ns", "mywd", "buildX", "tns")
	assert.Equal(t, "ns_mywd", got["__TEMPORAL_WORKER_DEPLOYMENT_NAME__"])
	assert.Equal(t, "buildX", got["__TEMPORAL_WORKER_BUILD_ID__"])
	assert.Equal(t, "tns", got["__TEMPORAL_NAMESPACE__"])
}

func TestSubstituteTemporalTokens(t *testing.T) {
	tokens := BuildTemporalTokens("ns", "mywd", "buildX", "tns")

	t.Run("replaces all three tokens in a single string", func(t *testing.T) {
		obj := map[string]interface{}{
			"q": `x{dn="__TEMPORAL_WORKER_DEPLOYMENT_NAME__",b="__TEMPORAL_WORKER_BUILD_ID__",n="__TEMPORAL_NAMESPACE__"}`,
		}
		SubstituteTemporalTokens(obj, tokens)
		assert.Equal(t, `x{dn="ns_mywd",b="buildX",n="tns"}`, obj["q"])
	})

	t.Run("no-op when no tokens are present", func(t *testing.T) {
		obj := map[string]interface{}{"q": "no tokens here"}
		SubstituteTemporalTokens(obj, tokens)
		assert.Equal(t, "no tokens here", obj["q"])
	})

	t.Run("unknown __FOO__ tokens pass through untouched", func(t *testing.T) {
		obj := map[string]interface{}{"q": "__UNKNOWN_TOKEN__ and __TEMPORAL_WORKER_BUILD_ID__"}
		SubstituteTemporalTokens(obj, tokens)
		assert.Equal(t, "__UNKNOWN_TOKEN__ and buildX", obj["q"])
	})

	t.Run("walks nested maps", func(t *testing.T) {
		obj := map[string]interface{}{
			"spec": map[string]interface{}{
				"triggers": map[string]interface{}{
					"query": "b=__TEMPORAL_WORKER_BUILD_ID__",
				},
			},
		}
		SubstituteTemporalTokens(obj, tokens)
		spec := obj["spec"].(map[string]interface{})
		triggers := spec["triggers"].(map[string]interface{})
		assert.Equal(t, "b=buildX", triggers["query"])
	})

	t.Run("walks slices of maps", func(t *testing.T) {
		obj := map[string]interface{}{
			"triggers": []interface{}{
				map[string]interface{}{"query": "b=__TEMPORAL_WORKER_BUILD_ID__"},
				map[string]interface{}{"query": "n=__TEMPORAL_NAMESPACE__"},
			},
		}
		SubstituteTemporalTokens(obj, tokens)
		triggers := obj["triggers"].([]interface{})
		t0 := triggers[0].(map[string]interface{})
		t1 := triggers[1].(map[string]interface{})
		assert.Equal(t, "b=buildX", t0["query"])
		assert.Equal(t, "n=tns", t1["query"])
	})

	t.Run("walks slices of strings", func(t *testing.T) {
		obj := map[string]interface{}{
			"labels": []interface{}{"__TEMPORAL_WORKER_BUILD_ID__", "literal"},
		}
		SubstituteTemporalTokens(obj, tokens)
		labels := obj["labels"].([]interface{})
		assert.Equal(t, "buildX", labels[0])
		assert.Equal(t, "literal", labels[1])
	})

	t.Run("non-string leaves are left alone", func(t *testing.T) {
		obj := map[string]interface{}{
			"minReplicaCount": float64(1),
			"pollingInterval": float64(15),
			"enabled":         true,
		}
		SubstituteTemporalTokens(obj, tokens)
		assert.Equal(t, float64(1), obj["minReplicaCount"])
		assert.Equal(t, float64(15), obj["pollingInterval"])
		assert.Equal(t, true, obj["enabled"])
	})

	t.Run("repeated token occurrences all substituted", func(t *testing.T) {
		obj := map[string]interface{}{
			"q": "__TEMPORAL_WORKER_BUILD_ID__-__TEMPORAL_WORKER_BUILD_ID__",
		}
		SubstituteTemporalTokens(obj, tokens)
		assert.Equal(t, "buildX-buildX", obj["q"])
	})
}
