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
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

func (r *TemporalWorkerDeployment) SetupWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(r).
		Complete()
}

//+kubebuilder:webhook:path=/mutate-temporal-io-temporal-io-v1alpha1-temporalworkerdeployment,mutating=true,failurePolicy=fail,sideEffects=None,groups=temporal.io.temporal.io,resources=temporalworkers,verbs=create;update,versions=v1alpha1,name=mtemporalworker.kb.io,admissionReviewVersions=v1

var _ webhook.CustomDefaulter = &TemporalWorkerDeployment{}
var _ webhook.CustomValidator = &TemporalWorkerDeployment{}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the type
func (r *TemporalWorkerDeployment) Default(ctx context.Context, obj runtime.Object) error {
	dep, ok := obj.(*TemporalWorkerDeployment)
	if !ok {
		return apierrors.NewBadRequest("expected a TemporalWorkerDeployment")
	}

	if err := dep.Spec.Default(ctx); err != nil {
		return err
	}

	return nil
}

func (s *TemporalWorkerDeploymentSpec) Default(ctx context.Context) error {
	if s.SunsetStrategy.ScaledownDelay == nil {
		s.SunsetStrategy.ScaledownDelay = &v1.Duration{Duration: defaults.ScaledownDelay}
	}

	if s.SunsetStrategy.DeleteDelay == nil {
		s.SunsetStrategy.DeleteDelay = &v1.Duration{Duration: defaults.DeleteDelay}
	}

	return nil
}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type
func (r *TemporalWorkerDeployment) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	return r.validateForUpdateOrCreate(ctx, obj)
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type
func (r *TemporalWorkerDeployment) ValidateUpdate(ctx context.Context, oldObj runtime.Object, newObj runtime.Object) (admission.Warnings, error) {
	return r.validateForUpdateOrCreate(ctx, newObj)
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type
func (r *TemporalWorkerDeployment) ValidateDelete(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	return nil, nil
}

func (r *TemporalWorkerDeployment) validateForUpdateOrCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	dep, ok := obj.(*TemporalWorkerDeployment)
	if !ok {
		return nil, apierrors.NewBadRequest("expected a TemporalWorkerDeployment")
	}

	return validateForUpdateOrCreate(nil, dep)
}

func validateForUpdateOrCreate(old, new *TemporalWorkerDeployment) (admission.Warnings, error) {
	allErrs := validateRolloutStrategy(new.Spec.RolloutStrategy)
	if len(allErrs) > 0 {
		return nil, newInvalidErr(new, allErrs)
	}
	return nil, nil
}

// validateRolloutStrategy checks constraints that the CRD schema cannot enforce:
// rampPercentage must be strictly increasing across steps, and gate.input and
// gate.inputFrom are mutually exclusive (gate.input is an unstructured JSON field
// invisible to CEL). All other rollout constraints are enforced by the CRD CEL rules.
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

	if s.Gate != nil && s.Gate.Input != nil && s.Gate.InputFrom != nil {
		allErrs = append(allErrs,
			field.Invalid(field.NewPath("spec.rollout.gate"), "input & inputFrom",
				"only one of input or inputFrom may be set"),
		)
	}

	return allErrs
}

func newInvalidErr(dep *TemporalWorkerDeployment, errs field.ErrorList) *apierrors.StatusError {
	return apierrors.NewInvalid(dep.GroupVersionKind().GroupKind(), dep.GetName(), errs)
}
