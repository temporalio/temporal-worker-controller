// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package v1alpha1

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

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

// defaultBannedKinds lists resource kinds that the controller blocks by default.
// This prevents TWOR from being misused to run arbitrary services alongside the worker.
// The operator can override this list via the BANNED_KINDS environment variable.
var defaultBannedKinds = []string{"Deployment", "StatefulSet", "Job", "Pod", "CronJob"}

// TemporalWorkerOwnedResourceValidator validates TemporalWorkerOwnedResource objects.
// It holds API-dependent dependencies (client, RESTMapper, controller SA identity).
type TemporalWorkerOwnedResourceValidator struct {
	Client                client.Client
	RESTMapper            meta.RESTMapper
	ControllerSAName      string
	ControllerSANamespace string
	BannedKinds           []string
}

var _ webhook.CustomValidator = &TemporalWorkerOwnedResourceValidator{}

// NewTemporalWorkerOwnedResourceValidator creates a validator from a manager.
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
//   - BANNED_KINDS — comma-separated list of kind names that are not allowed as
//     TemporalWorkerOwnedResource objects (e.g. "Deployment,StatefulSet,Job,Pod,CronJob").
//     Configurable via the ownedResources.bannedKinds Helm value. If unset, the
//     built-in defaultBannedKinds list is used.
func NewTemporalWorkerOwnedResourceValidator(mgr ctrl.Manager) *TemporalWorkerOwnedResourceValidator {
	bannedKinds := defaultBannedKinds
	if env := os.Getenv("BANNED_KINDS"); env != "" {
		parts := strings.Split(env, ",")
		bannedKinds = make([]string, 0, len(parts))
		for _, p := range parts {
			if trimmed := strings.TrimSpace(p); trimmed != "" {
				bannedKinds = append(bannedKinds, trimmed)
			}
		}
	}
	return &TemporalWorkerOwnedResourceValidator{
		Client:                mgr.GetClient(),
		RESTMapper:            mgr.GetRESTMapper(),
		ControllerSAName:      os.Getenv("SERVICE_ACCOUNT_NAME"),
		ControllerSANamespace: os.Getenv("POD_NAMESPACE"),
		BannedKinds:           bannedKinds,
	}
}

// SetupWebhookWithManager registers the validating webhook with the manager.
//
// +kubebuilder:webhook:path=/validate-temporal-io-v1alpha1-temporalworkerownedresource,mutating=false,failurePolicy=fail,sideEffects=None,groups=temporal.io,resources=temporalworkerownedresources,verbs=create;update;delete,versions=v1alpha1,name=vtemporalworkerownedresource.kb.io,admissionReviewVersions=v1
func (v *TemporalWorkerOwnedResourceValidator) SetupWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(&TemporalWorkerOwnedResource{}).
		WithValidator(v).
		Complete()
}

// ValidateCreate validates a new TemporalWorkerOwnedResource.
func (v *TemporalWorkerOwnedResourceValidator) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	twor, ok := obj.(*TemporalWorkerOwnedResource)
	if !ok {
		return nil, apierrors.NewBadRequest("expected a TemporalWorkerOwnedResource")
	}
	return v.validate(ctx, nil, twor, "create")
}

// ValidateUpdate validates an updated TemporalWorkerOwnedResource.
func (v *TemporalWorkerOwnedResourceValidator) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	old, ok := oldObj.(*TemporalWorkerOwnedResource)
	if !ok {
		return nil, apierrors.NewBadRequest("expected a TemporalWorkerOwnedResource for old object")
	}
	newTWOR, ok := newObj.(*TemporalWorkerOwnedResource)
	if !ok {
		return nil, apierrors.NewBadRequest("expected a TemporalWorkerOwnedResource for new object")
	}
	return v.validate(ctx, old, newTWOR, "update")
}

// ValidateDelete checks that the requesting user and the controller service account
// are both authorized to delete the underlying resource kind. This prevents privilege
// escalation: a user who cannot directly delete HPAs should not be able to delete a
// TemporalWorkerOwnedResource that manages HPAs and thereby trigger their removal.
func (v *TemporalWorkerOwnedResourceValidator) ValidateDelete(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	twor, ok := obj.(*TemporalWorkerOwnedResource)
	if !ok {
		return nil, apierrors.NewBadRequest("expected a TemporalWorkerOwnedResource")
	}

	if twor.Spec.Object.Raw == nil {
		return nil, nil // nothing to check
	}

	warnings, errs := v.validateWithAPI(ctx, twor, "delete")
	if len(errs) > 0 {
		return warnings, apierrors.NewInvalid(
			twor.GroupVersionKind().GroupKind(),
			twor.GetName(),
			errs,
		)
	}
	return warnings, nil
}

// validate runs all validation checks (pure spec + API-dependent).
// verb is the RBAC verb to check for the underlying resource ("create" on create, "update" on update).
func (v *TemporalWorkerOwnedResourceValidator) validate(ctx context.Context, old, new *TemporalWorkerOwnedResource, verb string) (admission.Warnings, error) {
	var allErrs field.ErrorList
	var warnings admission.Warnings

	// Pure spec validation (no API calls needed)
	specWarnings, specErrs := validateOwnedResourceSpec(new.Spec, v.BannedKinds)
	warnings = append(warnings, specWarnings...)
	allErrs = append(allErrs, specErrs...)

	// Immutability: workerRef.name must not change on update
	if old != nil && old.Spec.WorkerRef.Name != new.Spec.WorkerRef.Name {
		allErrs = append(allErrs, field.Forbidden(
			field.NewPath("spec").Child("workerRef").Child("name"),
			"workerRef.name is immutable and cannot be changed after creation",
		))
	}

	// Return early if pure validation failed (API checks won't help)
	if len(allErrs) > 0 {
		return warnings, apierrors.NewInvalid(
			new.GroupVersionKind().GroupKind(),
			new.GetName(),
			allErrs,
		)
	}

	// API-dependent checks (RESTMapper scope + SubjectAccessReview)
	apiWarnings, apiErrs := v.validateWithAPI(ctx, new, verb)
	warnings = append(warnings, apiWarnings...)
	allErrs = append(allErrs, apiErrs...)

	if len(allErrs) > 0 {
		return warnings, apierrors.NewInvalid(
			new.GroupVersionKind().GroupKind(),
			new.GetName(),
			allErrs,
		)
	}

	return warnings, nil
}

// validateOwnedResourceSpec performs pure (no-API) validation of the spec fields.
// It checks structural constraints that can be evaluated without talking to the API server.
func validateOwnedResourceSpec(spec TemporalWorkerOwnedResourceSpec, bannedKinds []string) (admission.Warnings, field.ErrorList) {
	var allErrs field.ErrorList
	var warnings admission.Warnings

	if spec.Object.Raw == nil {
		allErrs = append(allErrs, field.Required(
			field.NewPath("spec").Child("object"),
			"object must be specified",
		))
		return warnings, allErrs
	}

	var obj map[string]interface{}
	if err := json.Unmarshal(spec.Object.Raw, &obj); err != nil {
		allErrs = append(allErrs, field.Invalid(
			field.NewPath("spec").Child("object"),
			"<raw>",
			fmt.Sprintf("failed to parse object: %v", err),
		))
		return warnings, allErrs
	}

	// 1. apiVersion and kind must be present
	apiVersionStr, _ := obj["apiVersion"].(string)
	kind, _ := obj["kind"].(string)
	if apiVersionStr == "" {
		allErrs = append(allErrs, field.Required(
			field.NewPath("spec").Child("object").Child("apiVersion"),
			"apiVersion must be specified",
		))
	}
	if kind == "" {
		allErrs = append(allErrs, field.Required(
			field.NewPath("spec").Child("object").Child("kind"),
			"kind must be specified",
		))
	}

	// 2. metadata.name and metadata.namespace must be absent or empty — the controller generates them
	if embeddedMeta, ok := obj["metadata"].(map[string]interface{}); ok {
		if name, ok := embeddedMeta["name"].(string); ok && name != "" {
			allErrs = append(allErrs, field.Forbidden(
				field.NewPath("spec").Child("object").Child("metadata").Child("name"),
				"metadata.name is generated by the controller; remove it from spec.object",
			))
		}
		if ns, ok := embeddedMeta["namespace"].(string); ok && ns != "" {
			allErrs = append(allErrs, field.Forbidden(
				field.NewPath("spec").Child("object").Child("metadata").Child("namespace"),
				"metadata.namespace is set by the controller; remove it from spec.object",
			))
		}
	}

	// 3. Banned kinds
	if kind != "" {
		for _, banned := range bannedKinds {
			if strings.EqualFold(kind, banned) {
				allErrs = append(allErrs, field.Forbidden(
					field.NewPath("spec").Child("object").Child("kind"),
					fmt.Sprintf("kind %q is not allowed; "+
						"the owned resources mechanism is to attach resources that help the worker service run (e.g. scalers), "+
						"not for running arbitrary additional services; "+
						"your controller operator can change this requirement if you have a specific use case in mind", kind),
				))
				break
			}
		}
	}

	// Check spec-level fields
	if innerSpec, ok := obj["spec"].(map[string]interface{}); ok {
		innerSpecPath := field.NewPath("spec").Child("object").Child("spec")

		// 4. minReplicas must not be 0.
		// Scaling to zero is not safe with Temporal's approximate_backlog_count metric: that metric
		// is only emitted while the task queue is loaded in memory (i.e. while at least one worker
		// is polling). If all workers are scaled to zero and the task queue goes idle for ~5 minutes,
		// the metric stops being emitted and resets to zero. If new tasks then arrive but no worker
		// polls, the metric remains zero — making it impossible for a metric-based autoscaler to
		// detect the backlog and scale back up. Until Temporal provides a reliable mechanism for
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

		// 5. scaleTargetRef: if absent, no injection. If present and null, the controller injects it
		// to point at the versioned Deployment. If present and non-null, reject — the controller
		// owns this field when it is present and any hardcoded value would point at the wrong Deployment.
		checkScaleTargetRefNotSet(innerSpec, innerSpecPath, &allErrs)

		// 6. selector.matchLabels: if absent, no injection. If present and null, the controller
		// injects it with the versioned Deployment's selector labels. If present and non-null,
		// reject — the controller owns this field when it is present.
		if selector, ok := innerSpec["selector"].(map[string]interface{}); ok {
			if ml, exists := selector["matchLabels"]; exists && ml != nil {
				allErrs = append(allErrs, field.Forbidden(
					innerSpecPath.Child("selector").Child("matchLabels"),
					"if selector.matchLabels is present, the controller owns it and will set it to the "+
						"versioned Deployment's selector labels; set it to null to opt in to auto-injection, "+
						"or remove it entirely if you do not need label-based selection",
				))
			}
		}
	}

	return warnings, allErrs
}

// checkScaleTargetRefNotSet recursively traverses obj looking for any scaleTargetRef that is
// non-null. If absent, no injection. If null, the controller injects it to point at the versioned
// Deployment. If non-null, reject — the controller owns this field when present, and a hardcoded
// value would point at the wrong (non-versioned) Deployment.
func checkScaleTargetRefNotSet(obj map[string]interface{}, path *field.Path, allErrs *field.ErrorList) {
	for k, v := range obj {
		if k == "scaleTargetRef" {
			if v != nil {
				*allErrs = append(*allErrs, field.Forbidden(
					path.Child("scaleTargetRef"),
					"if scaleTargetRef is present, the controller owns it and will set it to point at the "+
						"versioned Deployment; set it to null to opt in to auto-injection, "+
						"or remove it entirely if you do not need the scaleTargetRef field",
				))
			}
			// null is the correct opt-in sentinel — leave it alone
			continue
		}
		if nested, ok := v.(map[string]interface{}); ok {
			checkScaleTargetRefNotSet(nested, path.Child(k), allErrs)
		}
	}
}

// validateWithAPI performs API-dependent validation: RESTMapper scope check and
// SubjectAccessReview for both the requesting user and the controller service account.
// verb is the RBAC verb to check ("create" on create/update, "delete" on delete).
// It is a no-op when Client or RESTMapper is nil (e.g., in unit tests).
func (v *TemporalWorkerOwnedResourceValidator) validateWithAPI(ctx context.Context, twor *TemporalWorkerOwnedResource, verb string) (admission.Warnings, field.ErrorList) {
	var allErrs field.ErrorList
	var warnings admission.Warnings

	if v.Client == nil || v.RESTMapper == nil {
		return warnings, allErrs
	}

	var obj map[string]interface{}
	if err := json.Unmarshal(twor.Spec.Object.Raw, &obj); err != nil {
		return warnings, allErrs // already caught in validateOwnedResourceSpec
	}
	apiVersionStr, _ := obj["apiVersion"].(string)
	kind, _ := obj["kind"].(string)
	if apiVersionStr == "" || kind == "" {
		return warnings, allErrs // already caught in validateOwnedResourceSpec
	}

	gv, err := schema.ParseGroupVersion(apiVersionStr)
	if err != nil {
		allErrs = append(allErrs, field.Invalid(
			field.NewPath("spec").Child("object").Child("apiVersion"),
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
			field.NewPath("spec").Child("object").Child("apiVersion"),
			apiVersionStr,
			fmt.Sprintf("could not look up %s %s in the API server: %v (ensure the resource type is installed and the apiVersion/kind are correct)", kind, apiVersionStr, err),
		))
		return warnings, allErrs
	}
	var resource string
	for _, mapping := range mappings {
		if mapping.Scope.Name() != meta.RESTScopeNameNamespace {
			allErrs = append(allErrs, field.Forbidden(
				field.NewPath("spec").Child("object").Child("kind"),
				fmt.Sprintf("kind %q is not namespace-scoped; only namespaced resources are allowed in TemporalWorkerOwnedResource", kind),
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
				ResourceAttributes: &authorizationv1.ResourceAttributes{
					Namespace: twor.Namespace,
					Verb:      verb,
					Group:     gv.Group,
					Version:   gv.Version,
					Resource:  resource,
				},
			},
		}
		if err := v.Client.Create(ctx, userSAR); err != nil {
			allErrs = append(allErrs, field.InternalError(
				field.NewPath("spec").Child("object"),
				fmt.Errorf("failed to check requesting user permissions: %w", err),
			))
		} else if !userSAR.Status.Allowed {
			reason := userSAR.Status.Reason
			if reason == "" {
				reason = "permission denied"
			}
			allErrs = append(allErrs, field.Forbidden(
				field.NewPath("spec").Child("object"),
				fmt.Sprintf("requesting user %q is not authorized to %s %s in namespace %q: %s",
					req.UserInfo.Username, verb, kind, twor.Namespace, reason),
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
					Namespace: twor.Namespace,
					Verb:      verb,
					Group:     gv.Group,
					Version:   gv.Version,
					Resource:  resource,
				},
			},
		}
		if err := v.Client.Create(ctx, controllerSAR); err != nil {
			allErrs = append(allErrs, field.InternalError(
				field.NewPath("spec").Child("object"),
				fmt.Errorf("failed to check controller service account permissions: %w", err),
			))
		} else if !controllerSAR.Status.Allowed {
			reason := controllerSAR.Status.Reason
			if reason == "" {
				reason = "permission denied"
			}
			allErrs = append(allErrs, field.Forbidden(
				field.NewPath("spec").Child("object"),
				fmt.Sprintf("controller service account %q is not authorized to %s %s in namespace %q: %s; "+
					"grant it the required RBAC permissions via the Helm chart configuration",
					controllerUser, verb, kind, twor.Namespace, reason),
			))
		}
	}

	return warnings, allErrs
}
