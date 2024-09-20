// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package controller

import (
	"testing"

	"github.com/stretchr/testify/assert"

	temporaliov1alpha1 "github.com/DataDog/temporal-worker-controller/api/v1alpha1"
)

func TestFindHighestPriorityStatus(t *testing.T) {
	type testCase struct {
		statuses []temporaliov1alpha1.ReachabilityStatus
		expected temporaliov1alpha1.ReachabilityStatus
	}

	for name, tc := range map[string]testCase{
		"empty": {},
		"single status": {
			statuses: []temporaliov1alpha1.ReachabilityStatus{temporaliov1alpha1.ReachabilityStatusReachable},
			expected: temporaliov1alpha1.ReachabilityStatusReachable,
		},
		"multiple statuses": {
			statuses: []temporaliov1alpha1.ReachabilityStatus{
				temporaliov1alpha1.ReachabilityStatusNotRegistered,
				temporaliov1alpha1.ReachabilityStatusClosedOnly,
				temporaliov1alpha1.ReachabilityStatusReachable,
				temporaliov1alpha1.ReachabilityStatusUnreachable,
				temporaliov1alpha1.ReachabilityStatusReachable,
			},
			expected: temporaliov1alpha1.ReachabilityStatusReachable,
		},
	} {
		t.Run(name, func(t *testing.T) {
			actual := findHighestPriorityStatus(tc.statuses)
			assert.Equal(t, tc.expected, actual)
		})
	}
}
