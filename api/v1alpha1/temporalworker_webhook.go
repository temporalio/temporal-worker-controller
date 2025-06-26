// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package v1alpha1

import (
	"context"
	"fmt"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
	"time"
)

const (
	defaultScaledownDelay              = 1 * time.Hour
	defaultDeleteDelay                 = 24 * time.Hour
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

	if dep.Spec.SunsetStrategy.ScaledownDelay == nil {
		dep.Spec.SunsetStrategy.ScaledownDelay = &v1.Duration{Duration: defaultScaledownDelay}
	}

	if dep.Spec.SunsetStrategy.DeleteDelay == nil {
		dep.Spec.SunsetStrategy.DeleteDelay = &v1.Duration{Duration: defaultDeleteDelay}
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

	var allErrs field.ErrorList

	if len(dep.Name) > maxTemporalWorkerDeploymentNameLen {
		allErrs = append(allErrs,
			field.Invalid(field.NewPath("name"), dep.Name, fmt.Sprintf("cannot be more than %d characters", maxTemporalWorkerDeploymentNameLen)),
		)
	}

	if len(allErrs) > 0 {
		return nil, apierrors.NewInvalid(
			schema.GroupKind{Group: "your.group", Kind: "TemporalWorkerDeployment"},
			dep.Name,
			allErrs,
		)
	}

	return nil, nil
}
