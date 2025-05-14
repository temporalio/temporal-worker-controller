// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package controller

import (
	"testing"

	"github.com/stretchr/testify/assert"
	sdkclient "go.temporal.io/sdk/client"
)

// TestWasModifiedExternally tests the wasModifiedExternally function
func TestWasModifiedExternally(t *testing.T) {
	tests := []struct {
		name                 string
		lastModifierIdentity string
		expectedResult       bool
	}{
		{
			name:                 "Not modified externally when identity matches controller",
			lastModifierIdentity: controllerIdentity,
			expectedResult:       false,
		},
		{
			name:                 "Not modified externally when identity is empty",
			lastModifierIdentity: "",
			expectedResult:       false,
		},
		{
			name:                 "Modified externally when identity doesn't match controller",
			lastModifierIdentity: "some-other-identity",
			expectedResult:       true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := &sdkclient.WorkerDeploymentInfo{
				LastModifierIdentity: tt.lastModifierIdentity,
			}
			result := wasModifiedExternally(info)
			assert.Equal(t, tt.expectedResult, result, "wasModifiedExternally returned unexpected result")
		})
	}
}
