package v1alpha1

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	authenticationv1 "k8s.io/api/authentication/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// WorkerResourceTemplateValidator validates WorkerResourceTemplate objects.
// It holds API-dependent dependencies (client, RESTMapper, controller SA identity).
// +kubebuilder:object:generate=false
type WorkerResourceTemplateValidator struct {
	Client                client.Client
	RESTMapper            meta.RESTMapper
	ControllerSAName      string
	ControllerSANamespace string
	// AllowedKinds is the explicit list of resource kinds permitted as WorkerResourceTemplate objects.
	// Must be non-empty; when empty or nil, all kinds are rejected.
	// Populated from the ALLOWED_KINDS environment variable (comma-separated).
	AllowedKinds []string
}

var _ webhook.CustomValidator = &WorkerResourceTemplateValidator{}

// ControllerOwnedMetricLabelKeys are the metric selector label keys that the controller
// appends automatically to every metrics[*].external.metric.selector.matchLabels at render
// time. Users must not set these manually — the controller generates the correct per-version
// values and merges them into whatever matchLabels the user provides.
var ControllerOwnedMetricLabelKeys = []string{
	"temporal_worker_deployment_name",
	"temporal_worker_build_id",
	"temporal_namespace",
}

// NewWorkerResourceTemplateValidator creates a validator from a manager.
//
// Three environment variables are read at startup (all injected by the Helm chart):
//
//   - POD_NAMESPACE — namespace in which the controller pod runs; used as the
//     service-account namespace when performing SubjectAccessReview checks for the
//     controller SA. Populated via the downward API (fieldRef: metadata.namespace).
//
//   - SERVICE_ACCOUNT_NAME — name of the Kubernetes ServiceAccount the controller
//     pod runs as; used when performing SubjectAccessReview checks for the controller
//     SA. Populated via the downward API (fieldRef: spec.serviceAccountName).
//
//   - ALLOWED_KINDS — comma-separated list of kind names that are permitted as
//     WorkerResourceTemplate objects (e.g. "HorizontalPodAutoscaler,PodDisruptionBudget").
//     Configurable via workerResourceTemplate.allowedResources[*].kinds in values.yaml.
//     Must be set; when empty or unset, all WorkerResourceTemplate kind submissions are rejected.
func NewWorkerResourceTemplateValidator(mgr ctrl.Manager) *WorkerResourceTemplateValidator {
	var allowedKinds []string
	if env := os.Getenv("ALLOWED_KINDS"); env != "" {
		for _, p := range strings.Split(env, ",") {
			if trimmed := strings.TrimSpace(p); trimmed != "" {
				allowedKinds = append(allowedKinds, trimmed)
			}
		}
	}
	return &WorkerResourceTemplateValidator{
		Client:                mgr.GetClient(),
		RESTMapper:            mgr.GetRESTMapper(),
		ControllerSAName:      os.Getenv("SERVICE_ACCOUNT_NAME"),
		ControllerSANamespace: os.Getenv("POD_NAMESPACE"),
		AllowedKinds:          allowedKinds,
	}
}

// SetupWebhookWithManager registers the validating webhook with the manager.
//
// +kubebuilder:webhook:path=/validate-temporal-io-v1alpha1-workerresourcetemplate,mutating=false,failurePolicy=fail,sideEffects=None,groups=temporal.io,resources=workerresourcetemplates,verbs=create;update;delete,versions=v1alpha1,name=vworkerresourcetemplate.kb.io,admissionReviewVersions=v1
func (v *WorkerResourceTemplateValidator) SetupWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(&WorkerResourceTemplate{}).
		WithValidator(v).
		Complete()
}

// ValidateCreate validates a new WorkerResourceTemplate.
func (v *WorkerResourceTemplateValidator) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	wrt, ok := obj.(*WorkerResourceTemplate)
	if !ok {
		return nil, apierrors.NewBadRequest("expected a WorkerResourceTemplate")
	}
	return v.validate(ctx, nil, wrt, "create")
}

// ValidateUpdate validates an updated WorkerResourceTemplate.
func (v *WorkerResourceTemplateValidator) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	oldWRT, ok := oldObj.(*WorkerResourceTemplate)
	if !ok {
		return nil, apierrors.NewBadRequest("expected a WorkerResourceTemplate for old object")
	}
	newWRT, ok := newObj.(*WorkerResourceTemplate)
	if !ok {
		return nil, apierrors.NewBadRequest("expected a WorkerResourceTemplate for new object")
	}
	return v.validate(ctx, oldWRT, newWRT, "update")
}

// ValidateDelete checks that the requesting user and the controller service account
// are both authorized to delete the underlying resource kind. This prevents privilege
// escalation: a user who cannot directly delete HPAs should not be able to delete a
// WorkerResourceTemplate that manages HPAs and thereby trigger their removal.
func (v *WorkerResourceTemplateValidator) ValidateDelete(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	wrt, ok := obj.(*WorkerResourceTemplate)
	if !ok {
		return nil, apierrors.NewBadRequest("expected a WorkerResourceTemplate")
	}

	if wrt.Spec.Template.Raw == nil {
		return nil, nil // nothing to check
	}

	warnings, errs := v.validateWithAPI(ctx, wrt, "delete")
	if len(errs) > 0 {
		return warnings, apierrors.NewInvalid(
			wrt.GroupVersionKind().GroupKind(),
			wrt.GetName(),
			errs,
		)
	}
	return warnings, nil
}

// validate runs all validation checks (pure spec + API-dependent).
// verb is the RBAC verb to check for the underlying resource ("create" on create, "update" on update).
func (v *WorkerResourceTemplateValidator) validate(ctx context.Context, oldWRT, newWRT *WorkerResourceTemplate, verb string) (admission.Warnings, error) {
	var allErrs field.ErrorList
	var warnings admission.Warnings

	// Pure spec validation (no API calls needed)
	specWarnings, specErrs := validateWorkerResourceTemplateSpec(newWRT.Spec, v.AllowedKinds)
	warnings = append(warnings, specWarnings...)
	allErrs = append(allErrs, specErrs...)

	// Immutability: temporalWorkerDeploymentRef.name must not change on update
	if oldWRT != nil && oldWRT.Spec.TemporalWorkerDeploymentRef.Name != newWRT.Spec.TemporalWorkerDeploymentRef.Name {
		allErrs = append(allErrs, field.Forbidden(
			field.NewPath("spec").Child("temporalWorkerDeploymentRef").Child("name"),
			"temporalWorkerDeploymentRef.name is immutable and cannot be changed after creation",
		))
	}

	// Return early if pure validation failed (API checks won't help)
	if len(allErrs) > 0 {
		return warnings, apierrors.NewInvalid(
			newWRT.GroupVersionKind().GroupKind(),
			newWRT.GetName(),
			allErrs,
		)
	}

	// API-dependent checks (RESTMapper scope + SubjectAccessReview)
	apiWarnings, apiErrs := v.validateWithAPI(ctx, newWRT, verb)
	warnings = append(warnings, apiWarnings...)
	allErrs = append(allErrs, apiErrs...)

	if len(allErrs) > 0 {
		return warnings, apierrors.NewInvalid(
			newWRT.GroupVersionKind().GroupKind(),
			newWRT.GetName(),
			allErrs,
		)
	}

	return warnings, nil
}

// validateWorkerResourceTemplateSpec performs pure (no-API) validation of the spec fields.
// It checks structural constraints that can be evaluated without talking to the API server.
func validateWorkerResourceTemplateSpec(spec WorkerResourceTemplateSpec, allowedKinds []string) (admission.Warnings, field.ErrorList) {
	var allErrs field.ErrorList
	var warnings admission.Warnings

	if spec.Template.Raw == nil {
		allErrs = append(allErrs, field.Required(
			field.NewPath("spec").Child("template"),
			"template must be specified",
		))
		return warnings, allErrs
	}

	var obj map[string]interface{}
	if err := json.Unmarshal(spec.Template.Raw, &obj); err != nil {
		allErrs = append(allErrs, field.Invalid(
			field.NewPath("spec").Child("template"),
			"<raw>",
			fmt.Sprintf("failed to parse template: %v", err),
		))
		return warnings, allErrs
	}

	// 1. apiVersion and kind must be present
	apiVersionStr, _ := obj["apiVersion"].(string)
	kind, _ := obj["kind"].(string)
	if apiVersionStr == "" {
		allErrs = append(allErrs, field.Required(
			field.NewPath("spec").Child("template").Child("apiVersion"),
			"apiVersion must be specified",
		))
	}
	if kind == "" {
		allErrs = append(allErrs, field.Required(
			field.NewPath("spec").Child("template").Child("kind"),
			"kind must be specified",
		))
	}

	// 2. metadata.name and metadata.namespace must be absent or empty — the controller generates them
	if embeddedMeta, ok := obj["metadata"].(map[string]interface{}); ok {
		if name, ok := embeddedMeta["name"].(string); ok && name != "" {
			allErrs = append(allErrs, field.Forbidden(
				field.NewPath("spec").Child("template").Child("metadata").Child("name"),
				"metadata.name is generated by the controller; remove it from spec.template",
			))
		}
		if ns, ok := embeddedMeta["namespace"].(string); ok && ns != "" {
			allErrs = append(allErrs, field.Forbidden(
				field.NewPath("spec").Child("template").Child("metadata").Child("namespace"),
				"metadata.namespace is set by the controller; remove it from spec.template",
			))
		}
	}

	// 3. Allow-list check: kind must appear in allowedKinds. An empty list rejects all kinds.
	if kind != "" {
		found := false
		for _, allowed := range allowedKinds {
			if strings.EqualFold(kind, allowed) {
				found = true
				break
			}
		}
		if !found {
			allErrs = append(allErrs, field.Forbidden(
				field.NewPath("spec").Child("template").Child("kind"),
				fmt.Sprintf("kind %q is not in the allowed list; "+
					"only resource kinds listed in workerResourceTemplate.allowedResources are permitted "+
					"as WorkerResourceTemplate objects; "+
					"ask your cluster operator to add this kind if you have a legitimate use case", kind),
			))
		}
	}

	// Check spec-level fields
	if innerSpec, ok := obj["spec"].(map[string]interface{}); ok {
		innerSpecPath := field.NewPath("spec").Child("template").Child("spec")

		// 4. minReplicas must not be 0.
		// Scaling to zero is not safe with Temporal's approximate_backlog_count metric: that metric
		// is only emitted while the task queue is loaded in memory (i.e. while at least one worker
		// is polling). If all workers are scaled to zero and the task queue goes idle for ~5 minutes,
		// the metric stops being emitted and resets to zero. If new tasks then arrive but no worker
		// polls, the metric remains zero — making it impossible for a metric-based autoscaler to
		// detect the backlog and scale back up. Until Temporal makes this a reliable metric for
		// scaling workers to zero and back, minReplicas=0 is rejected.
		if minReplicas, exists := innerSpec["minReplicas"]; exists {
			if v, ok := minReplicas.(float64); ok && v == 0 {
				allErrs = append(allErrs, field.Invalid(
					innerSpecPath.Child("minReplicas"),
					0,
					"minReplicas must not be 0; scaling Temporal workers to zero is not currently safe: "+
						"Temporal's approximate_backlog_count metric stops being emitted when the task queue is idle "+
						"with no pollers, so a metric-based autoscaler cannot detect a new backlog and scale back up "+
						"from zero once all workers are gone",
				))
			}
		}

		// 5. scaleTargetRef: if absent or empty ({}), the controller injects it to point at
		// the versioned Deployment. If non-empty, reject — the controller owns this field and any
		// hardcoded value would point at the wrong Deployment.
		// Note: {} is the required opt-in sentinel. null is stripped by the k8s API server before
		// storage, so the controller would never see the field and injection would not occur.
		checkScaleTargetRefNotSet(innerSpec, innerSpecPath, &allErrs)

		// 6. spec.selector.matchLabels: the controller owns this exact path. If absent or {},
		// the controller injects the versioned Deployment's selector labels; if non-empty, reject.
		if selector, ok := innerSpec["selector"].(map[string]interface{}); ok {
			if ml, exists := selector["matchLabels"]; exists && ml != nil && !isEmptyMap(ml) {
				allErrs = append(allErrs, field.Forbidden(
					innerSpecPath.Child("selector").Child("matchLabels"),
					"if selector.matchLabels is present, the controller owns it and will set it to the "+
						"versioned Deployment's selector labels; set it to {} to opt in to auto-injection, "+
						"or remove it entirely if you do not need label-based selection",
				))
			}
		}

		// 7. metrics[*].external.metric.selector.matchLabels: the controller appends
		// temporal_worker_deployment_name, temporal_worker_build_id, and temporal_namespace to any
		// metric selector matchLabels that is present. These keys must not be hardcoded —
		// the controller generates the correct per-version values at render time.
		// User labels (e.g. task_type: "Activity") are allowed alongside the controller-owned keys.
		checkMetricSelectorLabelsNotSet(innerSpec, innerSpecPath, &allErrs)
	}

	return warnings, allErrs
}

// checkScaleTargetRefNotSet recursively traverses obj looking for any scaleTargetRef that is
// set to a non-empty value. If absent or empty ({}), the controller injects it to point
// at the versioned Deployment. If non-empty, reject — the controller owns this field when present,
// and a hardcoded value would point at the wrong (non-versioned) Deployment.
//
// Note: {} is the required opt-in sentinel. null is stripped by the Kubernetes API server before
// storage, so the controller would never see the field and injection would not occur.
func checkScaleTargetRefNotSet(obj map[string]interface{}, path *field.Path, allErrs *field.ErrorList) {
	for k, v := range obj {
		if k == "scaleTargetRef" {
			if v != nil && !isEmptyMap(v) {
				*allErrs = append(*allErrs, field.Forbidden(
					path.Child("scaleTargetRef"),
					"if scaleTargetRef is present, the controller owns it and will set it to point at the "+
						"versioned Deployment; set it to {} to opt in to auto-injection, "+
						"or remove it entirely if you do not need the scaleTargetRef field",
				))
			}
			// {} is the opt-in sentinel — leave it alone
			continue
		}
		if nested, ok := v.(map[string]interface{}); ok {
			checkScaleTargetRefNotSet(nested, path.Child(k), allErrs)
		}
	}
}

// checkMetricSelectorLabelsNotSet validates that metrics[*].external.metric.selector.matchLabels
// does not contain any of the controller-owned keys. The controller appends
// temporal_worker_deployment_name, temporal_worker_build_id, and temporal_namespace to whatever
// matchLabels the user provides. User labels (e.g. task_type: "Activity") are permitted alongside
// the controller-owned keys.
func checkMetricSelectorLabelsNotSet(spec map[string]interface{}, path *field.Path, allErrs *field.ErrorList) {
	metrics, ok := spec["metrics"].([]interface{})
	if !ok {
		return
	}
	metricsPath := path.Child("metrics")
	for i, m := range metrics {
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
		ml, ok := sel["matchLabels"].(map[string]interface{})
		if !ok || len(ml) == 0 {
			continue // absent or {} — both valid
		}
		for _, key := range ControllerOwnedMetricLabelKeys {
			if _, exists := ml[key]; exists {
				*allErrs = append(*allErrs, field.Forbidden(
					metricsPath.Index(i).Child("external").Child("metric").Child("selector").Child("matchLabels").Key(key),
					fmt.Sprintf("label %q is managed by the controller; do not set it manually — "+
						"the controller appends temporal_worker_deployment_name, temporal_worker_build_id, and temporal_namespace "+
						"to any matchLabels present (including {})", key),
				))
			}
		}
	}
}

// isEmptyMap returns true if v is a map[string]interface{} with no entries.
func isEmptyMap(v interface{}) bool {
	m, ok := v.(map[string]interface{})
	return ok && len(m) == 0
}

// convertUserInfoExtra converts an authentication ExtraValue map to an authorization ExtraValue map.
// Both types are []string under the hood; the conversion preserves all values exactly.
func convertUserInfoExtra(extra map[string]authenticationv1.ExtraValue) map[string]authorizationv1.ExtraValue {
	if len(extra) == 0 {
		return nil
	}
	out := make(map[string]authorizationv1.ExtraValue, len(extra))
	for k, v := range extra {
		out[k] = authorizationv1.ExtraValue(v)
	}
	return out
}

// validateWithAPI performs API-dependent validation: RESTMapper scope check and
// SubjectAccessReview for both the requesting user and the controller service account.
// verb is the RBAC verb to check ("create" on create/update, "delete" on delete).
func (v *WorkerResourceTemplateValidator) validateWithAPI(ctx context.Context, wrt *WorkerResourceTemplate, verb string) (admission.Warnings, field.ErrorList) {
	var allErrs field.ErrorList
	var warnings admission.Warnings

	if v.Client == nil || v.RESTMapper == nil {
		return warnings, field.ErrorList{field.InternalError(
			field.NewPath("spec").Child("template"),
			fmt.Errorf("validator is not fully initialized: Client and RESTMapper must be set"),
		)}
	}

	var obj map[string]interface{}
	if err := json.Unmarshal(wrt.Spec.Template.Raw, &obj); err != nil {
		return warnings, allErrs // already caught in validateWorkerResourceTemplateSpec
	}
	apiVersionStr, _ := obj["apiVersion"].(string)
	kind, _ := obj["kind"].(string)
	if apiVersionStr == "" || kind == "" {
		return warnings, allErrs // already caught in validateWorkerResourceTemplateSpec
	}

	gv, err := schema.ParseGroupVersion(apiVersionStr)
	if err != nil {
		allErrs = append(allErrs, field.Invalid(
			field.NewPath("spec").Child("template").Child("apiVersion"),
			apiVersionStr,
			fmt.Sprintf("invalid apiVersion: %v", err),
		))
		return warnings, allErrs
	}
	gvk := gv.WithKind(kind)

	// 1. Namespace-scope check via RESTMapper
	mappings, err := v.RESTMapper.RESTMappings(gvk.GroupKind(), gvk.Version)
	if err != nil {
		allErrs = append(allErrs, field.Invalid(
			field.NewPath("spec").Child("template").Child("apiVersion"),
			apiVersionStr,
			fmt.Sprintf("could not look up %s %s in the API server: %v (ensure the resource type is installed and the apiVersion/kind are correct)", kind, apiVersionStr, err),
		))
		return warnings, allErrs
	}
	var resource string
	for _, mapping := range mappings {
		if mapping.Scope.Name() != meta.RESTScopeNameNamespace {
			allErrs = append(allErrs, field.Forbidden(
				field.NewPath("spec").Child("template").Child("kind"),
				fmt.Sprintf("kind %q is not namespace-scoped; only namespaced resources are allowed in WorkerResourceTemplate", kind),
			))
			return warnings, allErrs
		}
		if resource == "" {
			resource = mapping.Resource.Resource
		}
	}

	// 2. SubjectAccessReview: requesting user
	req, reqErr := admission.RequestFromContext(ctx)
	if reqErr == nil && req.UserInfo.Username != "" {
		userSAR := &authorizationv1.SubjectAccessReview{
			Spec: authorizationv1.SubjectAccessReviewSpec{
				User:   req.UserInfo.Username,
				Groups: req.UserInfo.Groups,
				// Some authentication plugins like GKE's IAM plugin rely on certain fields being
				// present in the UserInfo.Extra field, so here we make sure to copy any extra
				// field values from the authentication request into the extra field of the
				// authorization review.
				Extra: convertUserInfoExtra(req.UserInfo.Extra),
				ResourceAttributes: &authorizationv1.ResourceAttributes{
					Namespace: wrt.Namespace,
					Verb:      verb,
					Group:     gv.Group,
					Version:   gv.Version,
					Resource:  resource,
				},
			},
		}
		if err := v.Client.Create(ctx, userSAR); err != nil {
			allErrs = append(allErrs, field.InternalError(
				field.NewPath("spec").Child("template"),
				fmt.Errorf("failed to check requesting user permissions: %w", err),
			))
		} else if !userSAR.Status.Allowed {
			reason := userSAR.Status.Reason
			if reason == "" {
				reason = "permission denied"
			}
			allErrs = append(allErrs, field.Forbidden(
				field.NewPath("spec").Child("template"),
				fmt.Sprintf("requesting user %q is not authorized to %s %s in namespace %q: %s",
					req.UserInfo.Username, verb, kind, wrt.Namespace, reason),
			))
		}
	}

	// 3. SubjectAccessReview: controller service account.
	// When Kubernetes evaluates a real request from a service account token, it automatically
	// considers the SA's username AND the groups the SA belongs to. When we construct a SAR
	// manually (without a real token), we must supply those groups ourselves — Kubernetes will
	// not add them. Without the groups, we could get a false negative if the cluster admin
	// granted permissions to "all SAs in namespace X" via a group binding
	// (e.g. system:serviceaccounts:{namespace}) rather than to the specific SA username. Including
	// the groups here mirrors what Kubernetes would do for a real request from the SA.
	if v.ControllerSAName != "" && v.ControllerSANamespace != "" {
		controllerUser := fmt.Sprintf("system:serviceaccount:%s:%s", v.ControllerSANamespace, v.ControllerSAName)
		controllerSAR := &authorizationv1.SubjectAccessReview{
			Spec: authorizationv1.SubjectAccessReviewSpec{
				User: controllerUser,
				Groups: []string{
					"system:serviceaccounts",
					fmt.Sprintf("system:serviceaccounts:%s", v.ControllerSANamespace),
					"system:authenticated",
				},
				ResourceAttributes: &authorizationv1.ResourceAttributes{
					Namespace: wrt.Namespace,
					Verb:      verb,
					Group:     gv.Group,
					Version:   gv.Version,
					Resource:  resource,
				},
			},
		}
		if err := v.Client.Create(ctx, controllerSAR); err != nil {
			allErrs = append(allErrs, field.InternalError(
				field.NewPath("spec").Child("template"),
				fmt.Errorf("failed to check controller service account permissions: %w", err),
			))
		} else if !controllerSAR.Status.Allowed {
			reason := controllerSAR.Status.Reason
			if reason == "" {
				reason = "permission denied"
			}
			allErrs = append(allErrs, field.Forbidden(
				field.NewPath("spec").Child("template"),
				fmt.Sprintf("controller service account %q is not authorized to %s %s in namespace %q: %s; "+
					"grant it the required RBAC permissions via the Helm chart configuration",
					controllerUser, verb, kind, wrt.Namespace, reason),
			))
		}
	}

	return warnings, allErrs
}
