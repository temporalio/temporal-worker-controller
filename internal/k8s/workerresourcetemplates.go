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

// WorkerResourceTemplateFieldManager is the SSA field manager name used when applying
// WorkerResourceTemplate-rendered resources. A single constant per controller is the
// standard Kubernetes pattern (see e.g. the ResourceClaim controller).
const WorkerResourceTemplateFieldManager = "temporal-worker-controller"

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

// TemplateData holds the variables available in Go template expressions within spec.template.
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
//  1. Unmarshal spec.template into an Unstructured
//  2. Auto-inject scaleTargetRef and matchLabels (Layer 1)
//  3. Render Go templates in all string values (Layer 2)
//  4. Set metadata (name, namespace, labels, owner reference)
func RenderWorkerResourceTemplate(
	wrt *temporaliov1alpha1.WorkerResourceTemplate,
	deployment *appsv1.Deployment,
	buildID string,
	temporalNamespace string,
) (*unstructured.Unstructured, error) {
	// Step 1: unmarshal the raw template directly into an Unstructured object.
	obj := &unstructured.Unstructured{}
	if err := json.Unmarshal(wrt.Spec.Template.Raw, &obj.Object); err != nil {
		return nil, fmt.Errorf("failed to unmarshal spec.template: %w", err)
	}

	data := TemplateData{
		DeploymentName:    deployment.Name,
		TemporalNamespace: temporalNamespace,
		BuildID:           buildID,
	}

	selectorLabels := ComputeSelectorLabels(wrt.Spec.TemporalWorkerDeploymentRef.Name, buildID)

	// Step 2: auto-inject scaleTargetRef and matchLabels into spec subtree.
	// NestedFieldNoCopy returns a live reference so mutations are reflected in obj.Object directly.
	if specRaw, ok, _ := unstructured.NestedFieldNoCopy(obj.Object, "spec"); ok {
		if spec, ok := specRaw.(map[string]interface{}); ok {
			autoInjectFields(spec, data.DeploymentName, selectorLabels)
		}
	}

	// Step 3: render Go templates in all string values.
	rendered, err := renderTemplateValues(obj.Object, data)
	if err != nil {
		return nil, fmt.Errorf("failed to render templates: %w", err)
	}
	renderedMap, ok := rendered.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("unexpected type after template rendering: %T", rendered)
	}
	obj.Object = renderedMap

	// Step 4: set metadata using Unstructured typed methods.
	resourceName := ComputeWorkerResourceTemplateName(wrt.Spec.TemporalWorkerDeploymentRef.Name, wrt.Name, buildID)
	obj.SetName(resourceName)
	obj.SetNamespace(wrt.Namespace)

	// Merge selector labels into any labels already present in the template.
	labels := obj.GetLabels()
	if labels == nil {
		labels = make(map[string]string)
	}
	for k, v := range selectorLabels {
		labels[k] = v
	}
	obj.SetLabels(labels)

	// Set owner reference pointing to the versioned Deployment so k8s GC cleans up
	// the worker resource template when the Deployment is deleted.
	blockOwnerDeletion := true
	isController := true
	obj.SetOwnerReferences([]metav1.OwnerReference{
		{
			APIVersion:         appsv1.SchemeGroupVersion.String(),
			Kind:               "Deployment",
			Name:               deployment.Name,
			UID:                deployment.UID,
			BlockOwnerDeletion: &blockOwnerDeletion,
			Controller:         &isController,
		},
	})

	return obj, nil
}

// autoInjectFields recursively traverses obj and injects scaleTargetRef and matchLabels
// wherever the key is present with an empty-object value. Users signal intent by writing
// `scaleTargetRef: {}` or `matchLabels: {}` in their spec.template. This covers Layer 1
// auto-injection without the risk of adding unknown fields to resource types that don't use them.
//
// Note: {} (empty object) is the required opt-in sentinel. Writing null looks equivalent in
// YAML but the Kubernetes API server deletes null-valued keys before storage, so null is
// never present when the controller reads the object back and injection would not occur.
func autoInjectFields(obj map[string]interface{}, deploymentName string, selectorLabels map[string]string) {
	for k, v := range obj {
		switch k {
		case "scaleTargetRef":
			// Inject when the key is present with an empty-object value (user opted in).
			if isEmptyMap(v) {
				_ = unstructured.SetNestedMap(obj, buildScaleTargetRef(deploymentName), k)
			}
		case "matchLabels":
			// Inject when the key is present with an empty-object value (user opted in).
			if isEmptyMap(v) {
				_ = unstructured.SetNestedStringMap(obj, selectorLabels, k)
			}
		default:
			autoInjectInValue(v, deploymentName, selectorLabels)
		}
	}
}

// isEmptyMap returns true if v is a map[string]interface{} with no entries.
func isEmptyMap(v interface{}) bool {
	m, ok := v.(map[string]interface{})
	return ok && len(m) == 0
}

// buildScaleTargetRef constructs the scaleTargetRef map pointing at the versioned Deployment.
func buildScaleTargetRef(deploymentName string) map[string]interface{} {
	return map[string]interface{}{
		"apiVersion": appsv1.SchemeGroupVersion.String(),
		"kind":       "Deployment",
		"name":       deploymentName,
	}
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
