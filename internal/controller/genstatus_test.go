// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package controller

import (
	"testing"

	"github.com/stretchr/testify/assert"
	sdkclient "go.temporal.io/sdk/client"
)

// TestIsExternalModification tests the function that checks if an error is an external modification error
func TestIsExternalModification(t *testing.T) {
	// Regular error is not an external modification
	err := assert.AnError
	assert.False(t, isExternalModification(err))

	// ExternalModificationError is detected correctly
	extErr := &externalModificationError{
		Resource:             "Test",
		Name:                 "test-name",
		LastModifierIdentity: "someone-else",
	}
	assert.True(t, isExternalModification(extErr))

	// Wrapped error is also detected
	wrappedErr := extErr
	assert.True(t, isExternalModification(wrappedErr))
}

// TestExternalModificationErrorString tests the String() method of ExternalModificationError
func TestExternalModificationErrorString(t *testing.T) {
	extErr := &externalModificationError{
		Resource:             "Test",
		Name:                 "test-name",
		LastModifierIdentity: "someone-else",
	}
	assert.Contains(t, extErr.Error(), "Test 'test-name' was modified by an external system: someone-else")
}

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
