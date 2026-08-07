// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package v1alpha1

import (
	"fmt"

	"github.com/temporalio/temporal-worker-controller/internal/defaults"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

// DefaultDeploymentStrategy returns a deep copy of s with unset fields filled
// using the same defaults as the apps/v1 Deployment API. The input is never
// mutated. A nil input returns nil.
func DefaultDeploymentStrategy(s *appsv1.DeploymentStrategy) *appsv1.DeploymentStrategy {
	if s == nil {
		return nil
	}
	out := s.DeepCopy()
	if out.Type == "" {
		out.Type = appsv1.RollingUpdateDeploymentStrategyType
	}
	if out.Type == appsv1.RollingUpdateDeploymentStrategyType {
		if out.RollingUpdate == nil {
			out.RollingUpdate = &appsv1.RollingUpdateDeployment{}
		}
		if out.RollingUpdate.MaxUnavailable == nil {
			maxUnavailable := intstr.FromString(defaults.DeploymentMaxUnavailable)
			out.RollingUpdate.MaxUnavailable = &maxUnavailable
		}
		if out.RollingUpdate.MaxSurge == nil {
			maxSurge := intstr.FromString(defaults.DeploymentMaxSurge)
			out.RollingUpdate.MaxSurge = &maxSurge
		}
	}
	return out
}

// validateDeploymentStrategy checks constraints that the CRD schema cannot
// enforce for spec.deploymentStrategy.
func validateDeploymentStrategy(s *appsv1.DeploymentStrategy) []*field.Error {
	if s == nil {
		return nil
	}

	var allErrs []*field.Error
	path := field.NewPath("spec.deploymentStrategy")

	switch s.Type {
	case "", appsv1.RollingUpdateDeploymentStrategyType, appsv1.RecreateDeploymentStrategyType:
	default:
		allErrs = append(allErrs, field.NotSupported(
			path.Child("type"),
			s.Type,
			[]string{
				string(appsv1.RollingUpdateDeploymentStrategyType),
				string(appsv1.RecreateDeploymentStrategyType),
			},
		))
	}

	if s.Type == appsv1.RecreateDeploymentStrategyType && s.RollingUpdate != nil {
		allErrs = append(allErrs, field.Forbidden(
			path.Child("rollingUpdate"),
			"may not be set when type is Recreate",
		))
	}

	if s.RollingUpdate != nil &&
		isExplicitlyZeroIntOrString(s.RollingUpdate.MaxUnavailable) &&
		isExplicitlyZeroIntOrString(s.RollingUpdate.MaxSurge) {
		allErrs = append(allErrs, field.Invalid(
			path.Child("rollingUpdate"),
			fmt.Sprintf("maxUnavailable=%v, maxSurge=%v", s.RollingUpdate.MaxUnavailable, s.RollingUpdate.MaxSurge),
			"maxUnavailable and maxSurge cannot both be 0",
		))
	}

	return allErrs
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
