// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package k8s

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"text/template"

	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// WorkerResourceTemplateFieldManager returns the SSA field manager string for a given WRT.
//
// The field manager identity has two requirements:
//  1. Stable across reconcile loops — the API server uses it to track which fields
//     this controller "owns". Changing it abandons the old ownership records and
//     causes spurious field conflicts until the old entries expire.
//  2. Unique per WRT instance — if two different WRTs both render resources into
//     the same namespace, their field managers must differ so each can own its own
//     set of fields without conflicting with the other.
//
// Using "twc/{namespace}/{name}" satisfies both: it never changes for a given WRT
// and is globally unique within the cluster (namespace+name is a unique identifier).
// The string is capped at 128 characters to stay within the Kubernetes API limit.
func WorkerResourceTemplateFieldManager(wrt *temporaliov1alpha1.WorkerResourceTemplate) string {
	fm := "twc/" + wrt.Namespace + "/" + wrt.Name
	return TruncateString(fm, 128)
}

const (
	// workerResourceTemplateMaxNameLen is the maximum length of a generated worker resource template name.
	// 47 is chosen to be safe for Deployment resources: a pod name is composed of
	// "{deployment-name}-{rs-hash}-{pod-hash}" where the hashes add ~17 characters,
	// and pod names must be ≤63 characters. Using 47 as the limit ensures that the
	// generated names work even if the user un-bans Deployment as a worker resource template
	// kind, avoiding special-casing per resource type.
	workerResourceTemplateMaxNameLen = 47

	// workerResourceTemplateHashLen is the number of hex characters used for the uniqueness suffix.
	// 8 hex chars = 4 bytes = 32 bits, giving negligible collision probability
	// (< 1 in 10^7 even across thousands of resources in a cluster).
	workerResourceTemplateHashLen = 8
)

// ComputeWorkerResourceTemplateName generates a deterministic, DNS-safe name for the worker resource template
// instance corresponding to a given (twdName, wrtName, buildID) triple.
//
// The name has the form:
//
//	{human-readable-prefix}-{8-char-hash}
//
// The 8-character hash is computed from the full untruncated triple BEFORE any length
// capping occurs. This guarantees that two different triples — including triples that
// differ only in the buildID — always produce different names, even if the human-readable
// prefix is truncated. The buildID is therefore always uniquely represented via the hash,
// regardless of how long twdName or wrtName are.
func ComputeWorkerResourceTemplateName(twdName, wrtName, buildID string) string {
	// Hash the full triple first, before any truncation.
	h := sha256.Sum256([]byte(twdName + wrtName + buildID))
	hashSuffix := hex.EncodeToString(h[:workerResourceTemplateHashLen/2]) // 4 bytes → 8 hex chars

	// Build the human-readable prefix and truncate so the total fits in maxLen.
	// suffixLen = len("-") + workerResourceTemplateHashLen
	const suffixLen = 1 + workerResourceTemplateHashLen
	raw := CleanStringForDNS(twdName + ResourceNameSeparator + wrtName + ResourceNameSeparator + buildID)
	prefix := TruncateString(raw, workerResourceTemplateMaxNameLen-suffixLen)
	// Trim any trailing separator that results from truncating mid-segment.
	prefix = strings.TrimRight(prefix, ResourceNameSeparator)

	return prefix + ResourceNameSeparator + hashSuffix
}

// ComputeSelectorLabels returns the selector labels used by a versioned Deployment.
// These are the same labels set on the Deployment.Spec.Selector.MatchLabels.
func ComputeSelectorLabels(twdName, buildID string) map[string]string {
	return map[string]string{
		twdNameLabel: TruncateString(CleanStringForDNS(twdName), 63),
		BuildIDLabel: TruncateString(buildID, 63),
	}
}

// TemplateData holds the variables available in Go template expressions within spec.object.
type TemplateData struct {
	// DeploymentName is the controller-generated versioned Deployment name.
	DeploymentName string
	// TemporalNamespace is the Temporal namespace the worker connects to.
	TemporalNamespace string
	// BuildID is the Build ID for this version.
	BuildID string
}

// RenderWorkerResourceTemplate produces the Unstructured object to apply via SSA for a given
// WorkerResourceTemplate and versioned Deployment.
//
// Processing order:
//  1. Unmarshal spec.object into an Unstructured
//  2. Auto-inject scaleTargetRef and matchLabels (Layer 1)
//  3. Render Go templates in all string values (Layer 2)
//  4. Set metadata (name, namespace, labels, owner reference)
func RenderWorkerResourceTemplate(
	wrt *temporaliov1alpha1.WorkerResourceTemplate,
	deployment *appsv1.Deployment,
	buildID string,
	temporalNamespace string,
) (*unstructured.Unstructured, error) {
	// Step 1: unmarshal the raw object
	var raw map[string]interface{}
	if err := json.Unmarshal(wrt.Spec.Object.Raw, &raw); err != nil {
		return nil, fmt.Errorf("failed to unmarshal spec.object: %w", err)
	}

	data := TemplateData{
		DeploymentName:    deployment.Name,
		TemporalNamespace: temporalNamespace,
		BuildID:           buildID,
	}

	selectorLabels := ComputeSelectorLabels(wrt.Spec.TemporalWorkerDeploymentRef.Name, buildID)

	// Step 2: auto-inject scaleTargetRef and matchLabels into spec subtree
	if spec, ok := raw["spec"].(map[string]interface{}); ok {
		autoInjectFields(spec, data.DeploymentName, selectorLabels)
	}

	// Step 3: render Go templates in all string values
	rendered, err := renderTemplateValues(raw, data)
	if err != nil {
		return nil, fmt.Errorf("failed to render templates: %w", err)
	}
	renderedMap, ok := rendered.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("unexpected type after template rendering: %T", rendered)
	}
	raw = renderedMap

	// Step 4: set metadata
	resourceName := ComputeWorkerResourceTemplateName(wrt.Spec.TemporalWorkerDeploymentRef.Name, wrt.Name, buildID)

	meta, ok := raw["metadata"].(map[string]interface{})
	if !ok || meta == nil {
		meta = make(map[string]interface{})
	}
	meta["name"] = resourceName
	meta["namespace"] = wrt.Namespace

	// Merge labels
	existingLabels, ok := meta["labels"].(map[string]interface{})
	if !ok || existingLabels == nil {
		existingLabels = make(map[string]interface{})
	}
	for k, v := range selectorLabels {
		existingLabels[k] = v
	}
	meta["labels"] = existingLabels

	// Set owner reference pointing to the versioned Deployment so k8s GC cleans up
	// the worker resource template when the Deployment is deleted.
	blockOwnerDeletion := true
	isController := true
	meta["ownerReferences"] = []interface{}{
		map[string]interface{}{
			"apiVersion":         appsv1.SchemeGroupVersion.String(),
			"kind":               "Deployment",
			"name":               deployment.Name,
			"uid":                string(deployment.UID),
			"blockOwnerDeletion": blockOwnerDeletion,
			"controller":         isController,
		},
	}

	raw["metadata"] = meta

	obj := &unstructured.Unstructured{Object: raw}
	return obj, nil
}

// autoInjectFields recursively traverses obj and injects scaleTargetRef and matchLabels
// wherever the key is present with a null value. Users signal intent by writing
// `scaleTargetRef: null` or `matchLabels: null` in their spec.object. This covers Layer 1
// auto-injection without the risk of adding unknown fields to resource types that don't use them.
func autoInjectFields(obj map[string]interface{}, deploymentName string, selectorLabels map[string]string) {
	for k, v := range obj {
		switch k {
		case "scaleTargetRef":
			// Inject only when the key is present but null (user opted in)
			if v == nil {
				obj[k] = buildScaleTargetRef(deploymentName)
			}
		case "matchLabels":
			// Inject only when the key is present but null (user opted in)
			if v == nil {
				obj[k] = labelsAsInterface(selectorLabels)
			}
		default:
			autoInjectInValue(v, deploymentName, selectorLabels)
		}
	}
}

// buildScaleTargetRef constructs the scaleTargetRef map pointing at the versioned Deployment.
func buildScaleTargetRef(deploymentName string) map[string]interface{} {
	return map[string]interface{}{
		"apiVersion": appsv1.SchemeGroupVersion.String(),
		"kind":       "Deployment",
		"name":       deploymentName,
	}
}

// labelsAsInterface converts a string→string label map to map[string]interface{} for JSON encoding.
func labelsAsInterface(selectorLabels map[string]string) map[string]interface{} {
	labels := make(map[string]interface{}, len(selectorLabels))
	for k, v := range selectorLabels {
		labels[k] = v
	}
	return labels
}

// autoInjectInValue recurses into v if it is a map or a slice of maps.
func autoInjectInValue(v interface{}, deploymentName string, selectorLabels map[string]string) {
	if nested, ok := v.(map[string]interface{}); ok {
		autoInjectFields(nested, deploymentName, selectorLabels)
		return
	}
	if arr, ok := v.([]interface{}); ok {
		for _, item := range arr {
			if nestedItem, ok := item.(map[string]interface{}); ok {
				autoInjectFields(nestedItem, deploymentName, selectorLabels)
			}
		}
	}
}

// renderTemplateValues recursively traverses a JSON-decoded value tree and renders
// Go template expressions in all string values. Returns the modified value.
func renderTemplateValues(v interface{}, data TemplateData) (interface{}, error) {
	switch typed := v.(type) {
	case string:
		rendered, err := renderString(typed, data)
		if err != nil {
			return nil, err
		}
		return rendered, nil
	case map[string]interface{}:
		for k, val := range typed {
			rendered, err := renderTemplateValues(val, data)
			if err != nil {
				return nil, fmt.Errorf("field %q: %w", k, err)
			}
			typed[k] = rendered
		}
		return typed, nil
	case []interface{}:
		for i, item := range typed {
			rendered, err := renderTemplateValues(item, data)
			if err != nil {
				return nil, fmt.Errorf("index %d: %w", i, err)
			}
			typed[i] = rendered
		}
		return typed, nil
	default:
		// numbers, booleans, nil — pass through unchanged
		return v, nil
	}
}

// renderString renders a single string as a Go template with the given data.
// Strings without template expressions are returned unchanged.
func renderString(s string, data TemplateData) (string, error) {
	// Fast path: skip parsing if no template markers
	if !containsTemplateMarker(s) {
		return s, nil
	}
	tmpl, err := template.New("").Option("missingkey=error").Parse(s)
	if err != nil {
		return "", fmt.Errorf("invalid template %q: %w", s, err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("template execution failed for %q: %w", s, err)
	}
	return buf.String(), nil
}

// containsTemplateMarker returns true if s contains "{{" indicating a Go template expression.
func containsTemplateMarker(s string) bool {
	for i := 0; i < len(s)-1; i++ {
		if s[i] == '{' && s[i+1] == '{' {
			return true
		}
	}
	return false
}

// WorkerResourceTemplateVersionStatusForBuildID is a helper to build a status entry.
func WorkerResourceTemplateVersionStatusForBuildID(buildID, resourceName string, applied bool, message string) temporaliov1alpha1.WorkerResourceTemplateVersionStatus {
	return temporaliov1alpha1.WorkerResourceTemplateVersionStatus{
		BuildID:            buildID,
		Applied:            applied,
		ResourceName:       resourceName,
		Message:            message,
		LastTransitionTime: metav1.Now(),
	}
}
