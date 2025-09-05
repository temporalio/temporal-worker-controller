// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package v1alpha1

import (
	"context"
	"fmt"
	"time"

	"github.com/temporalio/temporal-worker-controller/internal/defaults"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

const (
	maxTemporalWorkerDeploymentNameLen = 63
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

	// If no selector is specified, default to the pod template's labels
	if s.Selector == nil && s.Template.Labels != nil {
		s.Selector = &v1.LabelSelector{
			MatchLabels: s.Template.Labels,
		}
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
	var allErrs field.ErrorList

	if len(new.GetName()) > maxTemporalWorkerDeploymentNameLen {
		allErrs = append(allErrs,
			field.Invalid(field.NewPath("metadata.name"), new.GetName(), fmt.Sprintf("cannot be more than %d characters", maxTemporalWorkerDeploymentNameLen)),
		)
	}

	allErrs = append(allErrs, validateRolloutStrategy(new.Spec.RolloutStrategy)...)

	if len(allErrs) > 0 {
		return nil, newInvalidErr(new, allErrs)
	}

	return nil, nil
}

func validateRolloutStrategy(s RolloutStrategy) []*field.Error {
	var allErrs []*field.Error

	if s.Strategy == UpdateProgressive {
		rolloutSteps := s.Steps
		if len(rolloutSteps) == 0 {
			allErrs = append(allErrs,
				field.Invalid(field.NewPath("spec.rollout.steps"), rolloutSteps, "steps are required for Progressive rollout"),
			)
		}
		var lastRamp int32
		for i, s := range rolloutSteps {
			// Check duration >= 30s
			if s.PauseDuration.Duration < 30*time.Second {
				allErrs = append(allErrs,
					field.Invalid(field.NewPath(fmt.Sprintf("spec.rollout.steps[%d].pauseDuration", i)), s.PauseDuration.Duration.String(), "pause duration must be at least 30s"),
				)
			}

			// Check ramp value greater than last
			if s.RampPercentageBasisPoints <= lastRamp {
				allErrs = append(allErrs,
					field.Invalid(field.NewPath(fmt.Sprintf("spec.rollout.steps[%d].rampPercentageBasisPoints", i)), s.RampPercentageBasisPoints, "rampPercentageBasisPoints must increase between each step"),
				)
			}
			lastRamp = s.RampPercentageBasisPoints
		}
	}

	return allErrs
}

func newInvalidErr(dep *TemporalWorkerDeployment, errs field.ErrorList) *apierrors.StatusError {
	return apierrors.NewInvalid(dep.GroupVersionKind().GroupKind(), dep.GetName(), errs)
}
