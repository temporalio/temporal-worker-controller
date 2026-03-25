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

// RenderedHashLen is the number of hex characters used for rendered-object hashes stored in
// WorkerResourceTemplateVersionStatus.LastAppliedHash. 16 hex chars = 8 bytes = 64 bits,
// giving negligible collision probability across thousands of (WRT × BuildID) pairs.
const RenderedHashLen = 16

// FieldManager is the SSA field manager name used when applying
// WorkerResourceTemplate-rendered resources. A single constant per controller is the
// standard Kubernetes pattern (see e.g. the ResourceClaim controller).
// TODO: Use this when we apply SSA to all resources.
const FieldManager = "temporal-worker-controller"

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
	// K8sNamespace is the Kubernetes namespace of the TemporalWorkerDeployment (e.g. "default").
	// Valid as a Kubernetes label value.
	K8sNamespace string
	// TWDName is the name of the TemporalWorkerDeployment (e.g. "helloworld").
	// Valid as a Kubernetes label value (≤63 chars, no special characters).
	TWDName string
	// TemporalNamespace is the Temporal namespace the worker connects to
	// (e.g. "worker-controller-demo.a2dd6"). Distinct from K8sNamespace.
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

	twdName := wrt.Spec.TemporalWorkerDeploymentRef.Name
	data := TemplateData{
		K8sNamespace:      wrt.Namespace,
		TWDName:           twdName,
		TemporalNamespace: temporalNamespace,
		BuildID:           buildID,
	}

	selectorLabels := ComputeSelectorLabels(twdName, buildID)

	// Labels the controller appends to every metrics[*].external.metric.selector.matchLabels
	// that is present in the template. These identify the exact per-version Prometheus series.
	// Ordering is not a concern: matchLabels is a map; encoding/json serialises map keys in
	// sorted order, so ComputeRenderedObjectHash is deterministic regardless of insertion order.
	metricSelectorLabels := map[string]string{
		"worker_deployment_name":     wrt.Namespace + "_" + twdName,
		"worker_deployment_build_id": buildID,
		"temporal_namespace":         temporalNamespace,
	}

	// Step 2: auto-inject scaleTargetRef, selector.matchLabels, and metric selector labels.
	// NestedFieldNoCopy returns a live reference so mutations are reflected in obj.Object directly.
	if specRaw, ok, _ := unstructured.NestedFieldNoCopy(obj.Object, "spec"); ok {
		if spec, ok := specRaw.(map[string]interface{}); ok {
			autoInjectFields(spec, deployment.Name, selectorLabels, metricSelectorLabels)
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

	// Set owner reference pointing to the WRT so k8s GC cleans up all rendered
	// resource copies when the WRT is deleted.
	// Sunset cleanup (Deployment deleted) is handled explicitly by the controller
	// via plan.DeleteWorkerResources rather than relying on Deployment GC ownership.
	blockOwnerDeletion := true
	isController := true
	obj.SetOwnerReferences([]metav1.OwnerReference{
		{
			APIVersion:         temporaliov1alpha1.GroupVersion.String(),
			Kind:               "WorkerResourceTemplate",
			Name:               wrt.Name,
			UID:                wrt.UID,
			BlockOwnerDeletion: &blockOwnerDeletion,
			Controller:         &isController,
		},
	})

	return obj, nil
}

// autoInjectFields applies the controller-owned injections to the top-level spec map:
//
//   - spec.selector.matchLabels: injected ONLY at this exact path when {} (empty).
//     Opt-in sentinel: absent = no injection; {} = inject pod selector labels.
//
//   - spec.metrics[*].external.metric.selector.matchLabels: temporal metric labels
//     (worker_deployment_name, build_id, temporal_namespace) are merged in whenever
//     matchLabels is present (including {}). User labels like task_type coexist.
//     If matchLabels is absent, no injection occurs for that metric entry.
//
//   - scaleTargetRef: injected anywhere in the spec tree when {} (empty), via
//     injectScaleTargetRefRecursive. Unambiguous across all supported resource types.
func autoInjectFields(spec map[string]interface{}, deploymentName string, podSelectorLabels map[string]string, metricSelectorLabels map[string]string) {
	// spec.selector.matchLabels: {} opt-in sentinel.
	if sel, ok := spec["selector"].(map[string]interface{}); ok {
		if isEmptyMap(sel["matchLabels"]) {
			_ = unstructured.SetNestedStringMap(sel, podSelectorLabels, "matchLabels")
		}
	}

	// metrics[*].external.metric.selector.matchLabels: merge temporal labels whenever present.
	if len(metricSelectorLabels) > 0 {
		if metrics, ok := spec["metrics"].([]interface{}); ok {
			for _, m := range metrics {
				entry, ok := m.(map[string]interface{})
				if !ok {
					continue
				}
				ext, ok := entry["external"].(map[string]interface{})
				if !ok {
					continue
				}
				metricSpec, ok := ext["metric"].(map[string]interface{})
				if !ok {
					continue
				}
				sel, ok := metricSpec["selector"].(map[string]interface{})
				if !ok {
					continue
				}
				if _, exists := sel["matchLabels"]; !exists {
					continue
				}
				// matchLabels is present: merge-inject temporal labels, preserving user labels.
				existing, _, _ := unstructured.NestedStringMap(sel, "matchLabels")
				if existing == nil {
					existing = make(map[string]string)
				}
				for k, v := range metricSelectorLabels {
					existing[k] = v
				}
				_ = unstructured.SetNestedStringMap(sel, existing, "matchLabels")
			}
		}
	}

	// scaleTargetRef: inject anywhere in the spec tree.
	injectScaleTargetRefRecursive(spec, deploymentName)
}

// injectScaleTargetRefRecursive recursively traverses obj and injects scaleTargetRef
// wherever the key is present with an empty-object value (user opted in with {}).
// scaleTargetRef is unambiguous across all supported resource types, so recursive
// injection is safe here.
func injectScaleTargetRefRecursive(obj map[string]interface{}, deploymentName string) {
	for k, v := range obj {
		if k == "scaleTargetRef" {
			if isEmptyMap(v) {
				_ = unstructured.SetNestedMap(obj, buildScaleTargetRef(deploymentName), k)
			}
			continue
		}
		if nested, ok := v.(map[string]interface{}); ok {
			injectScaleTargetRefRecursive(nested, deploymentName)
		} else if arr, ok := v.([]interface{}); ok {
			for _, item := range arr {
				if nestedItem, ok := item.(map[string]interface{}); ok {
					injectScaleTargetRefRecursive(nestedItem, deploymentName)
				}
			}
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
// Strings without template expressions are returned unchanged with zero allocations,
// avoiding the template library's buffer copy for the common case of plain strings.
func renderString(s string, data TemplateData) (string, error) {
	if !strings.Contains(s, "{{") {
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

// ComputeRenderedObjectHash returns a stable, deterministic hash of a rendered Unstructured
// object. The hash is computed from the JSON representation of the object; encoding/json
// serialises map keys in sorted order, so the result is deterministic regardless of map
// iteration order. Returns an empty string (not an error) if marshalling fails — callers
// treat an empty hash as "always apply". When the returned hash is empty, the stored
// LastAppliedHash remains "" in status after a successful apply, so the "always apply"
// behaviour persists across reconcile cycles until marshalling succeeds.
func ComputeRenderedObjectHash(resource *unstructured.Unstructured) string {
	b, err := json.Marshal(resource.Object)
	if err != nil {
		return ""
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:RenderedHashLen/2]) // 8 bytes → 16 hex chars
}

// WorkerResourceTemplateVersionStatusForBuildID builds a per-Build-ID status entry.
// generation is the WRT metadata.generation at the time of the apply; pass 0 on error
// to preserve the last-known-good generation. hash is the controller-internal rendered-object
// hash used for the SSA skip optimisation (pass "" on error).
func WorkerResourceTemplateVersionStatusForBuildID(buildID, resourceName string, generation int64, hash, applyError string) temporaliov1alpha1.WorkerResourceTemplateVersionStatus {
	return temporaliov1alpha1.WorkerResourceTemplateVersionStatus{
		BuildID:               buildID,
		ResourceName:          resourceName,
		LastAppliedGeneration: generation,
		ApplyError:            applyError,
		LastAppliedHash:       hash,
		LastTransitionTime:    metav1.Now(),
	}
}
