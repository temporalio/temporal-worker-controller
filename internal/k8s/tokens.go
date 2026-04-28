package k8s

import "strings"

// Token names are public API. Changing them is a breaking change.
const (
	TokenTemporalWorkerDeploymentName = "__TEMPORAL_WORKER_DEPLOYMENT_NAME__"
	TokenTemporalWorkerBuildID        = "__TEMPORAL_WORKER_BUILD_ID__"
	TokenTemporalNamespace            = "__TEMPORAL_NAMESPACE__"
)

// BuildTemporalTokens returns the token-to-value map used by SubstituteTemporalTokens.
// The values mirror the matchLabels injection values 1:1 so that both mechanisms
// target the same Prometheus series when used in the same template.
func BuildTemporalTokens(twdNamespace, twdName, buildID, temporalNamespace string) map[string]string {
	return map[string]string{
		TokenTemporalWorkerDeploymentName: twdNamespace + "_" + twdName,
		TokenTemporalWorkerBuildID:        buildID,
		TokenTemporalNamespace:            temporalNamespace,
	}
}

// SubstituteTemporalTokens walks obj recursively and replaces every recognised
// token occurrence inside every string leaf. Non-string leaves are untouched.
// Unknown __FOO__-style tokens pass through unchanged — the function only
// substitutes tokens present in the map.
func SubstituteTemporalTokens(obj map[string]interface{}, tokens map[string]string) {
	for k, v := range obj {
		obj[k] = substituteTokensInValue(v, tokens)
	}
}

func substituteTokensInValue(v interface{}, tokens map[string]string) interface{} {
	switch vv := v.(type) {
	case string:
		for token, replacement := range tokens {
			vv = strings.ReplaceAll(vv, token, replacement)
		}
		return vv
	case map[string]interface{}:
		SubstituteTemporalTokens(vv, tokens)
		return vv
	case []interface{}:
		for i, item := range vv {
			vv[i] = substituteTokensInValue(item, tokens)
		}
		return vv
	default:
		return v
	}
}
