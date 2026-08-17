// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package v1alpha1

import (
	"context"
	"fmt"

	"github.com/temporalio/temporal-worker-controller/internal/defaults"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

func (r *WorkerDeployment) SetupWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, r).
		WithCustomDefaulter(r).
		WithCustomValidator(r).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-temporal-io-v1alpha1-workerdeployment,mutating=true,failurePolicy=fail,sideEffects=None,groups=temporal.io,resources=workerdeployments,verbs=create;update,versions=v1alpha1,name=mworkerdeployment.kb.io,admissionReviewVersions=v1

var _ webhook.CustomDefaulter = &WorkerDeployment{}
var _ webhook.CustomValidator = &WorkerDeployment{}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the type
func (r *WorkerDeployment) Default(ctx context.Context, obj runtime.Object) error {
	dep, ok := obj.(*WorkerDeployment)
	if !ok {
		return apierrors.NewBadRequest("expected a WorkerDeployment")
	}

	if err := dep.Spec.Default(ctx); err != nil {
		return err
	}

	return nil
}

func (s *WorkerDeploymentSpec) Default(ctx context.Context) error {
	if s.SunsetStrategy.ScaledownDelay == nil {
		s.SunsetStrategy.ScaledownDelay = &v1.Duration{Duration: defaults.ScaledownDelay}
	}

	if s.SunsetStrategy.DeleteDelay == nil {
		s.SunsetStrategy.DeleteDelay = &v1.Duration{Duration: defaults.DeleteDelay}
	}

	if s.RolloutStrategy.MaxUnavailable == nil {
		maxUnavailable := intstr.FromString(defaults.DeploymentMaxUnavailable)
		s.RolloutStrategy.MaxUnavailable = &maxUnavailable
	}

	if s.RolloutStrategy.MaxSurge == nil {
		maxSurge := intstr.FromString(defaults.DeploymentMaxSurge)
		s.RolloutStrategy.MaxSurge = &maxSurge
	}

	return nil
}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type
func (r *WorkerDeployment) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	return r.validateForUpdateOrCreate(ctx, obj)
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type
func (r *WorkerDeployment) ValidateUpdate(ctx context.Context, oldObj runtime.Object, newObj runtime.Object) (admission.Warnings, error) {
	return r.validateForUpdateOrCreate(ctx, newObj)
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type
func (r *WorkerDeployment) ValidateDelete(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	return nil, nil
}

func (r *WorkerDeployment) validateForUpdateOrCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	dep, ok := obj.(*WorkerDeployment)
	if !ok {
		return nil, apierrors.NewBadRequest("expected a WorkerDeployment")
	}

	return validateForUpdateOrCreate(nil, dep)
}

func validateForUpdateOrCreate(old, new *WorkerDeployment) (admission.Warnings, error) {
	allErrs := validateRolloutStrategy(new.Spec.RolloutStrategy)
	if len(allErrs) > 0 {
		return nil, newInvalidErr(new, allErrs)
	}
	return nil, nil
}

// validateRolloutStrategy checks constraints that the CRD schema cannot enforce:
// rampPercentage must be strictly increasing across steps, and the rules involving
// gate.input, which is an unstructured JSON field invisible to CEL — a rule naming it
// fails to compile and the CRD is rejected at install time. That covers gate.input and
// gate.inputFrom being mutually exclusive, and the binary encodings requiring
// gate.inputFrom. All other rollout constraints are enforced by the CRD CEL rules.
func validateRolloutStrategy(s RolloutStrategy) []*field.Error {
	var allErrs []*field.Error

	if s.Strategy == UpdateProgressive {
		var lastRamp int
		for i, step := range s.Steps {
			if step.RampPercentage <= lastRamp {
				allErrs = append(allErrs,
					field.Invalid(field.NewPath(fmt.Sprintf("spec.rollout.steps[%d].rampPercentage", i)), step.RampPercentage, "rampPercentage must increase between each step"),
				)
			}
			lastRamp = step.RampPercentage
		}
	}

	if isExplicitlyZeroIntOrString(s.MaxUnavailable) &&
		isExplicitlyZeroIntOrString(s.MaxSurge) {
		allErrs = append(
			allErrs,
			field.Invalid(
				field.NewPath("spec.rollout.maxUnavailable"),
				fmt.Sprintf(
					"maxUnavailable=%v, maxSurge=%v",
					s.MaxUnavailable, s.MaxSurge,
				),
				"maxUnavailable and maxSurge cannot both be 0",
			),
		)
	}

	if s.Gate != nil && s.Gate.Input != nil && s.Gate.InputFrom != nil {
		allErrs = append(allErrs,
			field.Invalid(field.NewPath("spec.rollout.gate"), "input & inputFrom",
				"only one of input or inputFrom may be set"),
		)
	}

	if s.Gate != nil {
		isProtoEncoding := s.Gate.Encoding == PayloadMetadataEncodingTypeProtoJSON ||
			s.Gate.Encoding == PayloadMetadataEncodingTypeProto
		switch {
		case s.Gate.Encoding == PayloadMetadataEncodingTypeProto && s.Gate.MessageType == "":
			allErrs = append(allErrs,
				field.Invalid(field.NewPath("spec.rollout.gate.messageType"), s.Gate.MessageType,
					"messageType is required when encoding is binary/protobuf"),
			)
		case s.Gate.MessageType != "" && !isProtoEncoding:
			allErrs = append(allErrs,
				field.Invalid(field.NewPath("spec.rollout.gate.messageType"), s.Gate.MessageType,
					"messageType may only be set when encoding is json/protobuf or binary/protobuf"),
			)
		}

		// The binary encodings describe raw bytes, which inline input cannot carry: it is
		// written as JSON in the resource itself. The bytes have to come from a Secret or a
		// ConfigMap binaryData key instead. Declaring one of these encodings with no input at
		// all is rejected too, since the encoding would then describe a payload that is never
		// sent. This reports encoding as the offending value rather than the input.
		isBinaryEncoding := s.Gate.Encoding == PayloadMetadataEncodingTypeBinary ||
			s.Gate.Encoding == PayloadMetadataEncodingTypeProto
		switch {
		case isBinaryEncoding && s.Gate.Input != nil:
			allErrs = append(allErrs,
				field.Invalid(field.NewPath("spec.rollout.gate.encoding"), s.Gate.Encoding,
					"encoding binary/plain and binary/protobuf cannot be used with inline input, which cannot carry raw bytes; use inputFrom with a Secret or a ConfigMap binaryData key"),
			)
		case isBinaryEncoding && s.Gate.InputFrom == nil:
			allErrs = append(allErrs,
				field.Invalid(field.NewPath("spec.rollout.gate.encoding"), s.Gate.Encoding,
					"encoding binary/plain and binary/protobuf require inputFrom with a Secret or a ConfigMap binaryData key"),
			)
		}
	}

	return allErrs
}

func newInvalidErr(dep *WorkerDeployment, errs field.ErrorList) *apierrors.StatusError {
	return apierrors.NewInvalid(dep.GroupVersionKind().GroupKind(), dep.GetName(), errs)
}

func isExplicitlyZeroIntOrString(v *intstr.IntOrString) bool {
	if v == nil {
		return false
	}
	if v.Type == intstr.Int {
		return v.IntVal == 0
	}
	return v.StrVal == "0" || v.StrVal == "0%"
}
