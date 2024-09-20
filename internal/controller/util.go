// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package controller

import (
	"golang.org/x/exp/slices"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"

	temporaliov1alpha1 "github.com/DataDog/temporal-worker-controller/api/v1alpha1"
	"github.com/DataDog/temporal-worker-controller/internal/controller/k8s.io/utils"
)

func findHighestPriorityStatus(statuses []temporaliov1alpha1.ReachabilityStatus) temporaliov1alpha1.ReachabilityStatus {
	if len(statuses) == 0 {
		return ""
	}
	slices.SortFunc(statuses, func(a, b temporaliov1alpha1.ReachabilityStatus) int {
		return getStatusPriority(a) - getStatusPriority(b)
	})
	return statuses[len(statuses)-1]
}

func getStatusPriority(s temporaliov1alpha1.ReachabilityStatus) int {
	switch s {
	case temporaliov1alpha1.ReachabilityStatusReachable:
		return 4
	case temporaliov1alpha1.ReachabilityStatusClosedOnly:
		return 3
	case temporaliov1alpha1.ReachabilityStatusUnreachable:
		return 2
	case temporaliov1alpha1.ReachabilityStatusNotRegistered:
		return 1
	}
	return 0
}

func computeBuildID(spec *temporaliov1alpha1.TemporalWorkerSpec) string {
	return utils.ComputeHash(&spec.Template, nil)
}

func newObjectRef(d *appsv1.Deployment) *v1.ObjectReference {
	if d == nil {
		return nil
	}
	return &v1.ObjectReference{
		Kind:            d.Kind,
		Namespace:       d.Namespace,
		Name:            d.Name,
		UID:             d.UID,
		APIVersion:      d.APIVersion,
		ResourceVersion: d.ResourceVersion,
	}
}
