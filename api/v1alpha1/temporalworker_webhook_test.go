// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package v1alpha1

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func TestTemporalWorkerDeployment_ValidateCreate(t *testing.T) {
	tests := []struct {
		name        string
		obj         runtime.Object
		expectError bool
		errorMsg    string
	}{
		{
			name:        "valid temporal worker deployment",
			obj:         MakeTWDWithName("valid-worker"),
			expectError: false,
		},
		{
			name:        "temporal worker deployment with name too long",
			obj:         MakeTWDWithName("this-is-a-very-long-temporal-worker-deployment-name-that-exceeds-the-maximum-allowed-length-of-sixty-three-characters"),
			expectError: true,
			errorMsg:    "cannot be more than 63 characters",
		},
		{
			name: "invalid object type",
			obj: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
			},
			expectError: true,
			errorMsg:    "expected a TemporalWorkerDeployment",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			webhook := &TemporalWorkerDeployment{}

			warnings, err := webhook.ValidateCreate(ctx, tt.obj)

			if tt.expectError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorMsg)
			} else {
				require.NoError(t, err)
			}

			// Warnings should always be nil for this implementation
			assert.Nil(t, warnings)
		})
	}
}

func TestTemporalWorkerDeployment_ValidateUpdate(t *testing.T) {
	tests := []struct {
		name        string
		oldObj      runtime.Object
		newObj      runtime.Object
		expectError bool
		errorMsg    string
	}{
		{
			name:        "valid update",
			oldObj:      nil,
			newObj:      MakeTWDWithName("valid-worker"),
			expectError: false,
		},
		{
			name:        "update with name too long",
			oldObj:      nil,
			newObj:      MakeTWDWithName("this-is-a-very-long-temporal-worker-deployment-name-that-exceeds-the-maximum-allowed-length-of-sixty-three-characters"),
			expectError: true,
			errorMsg:    "cannot be more than 63 characters",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			webhook := &TemporalWorkerDeployment{}

			warnings, err := webhook.ValidateUpdate(ctx, tt.oldObj, tt.newObj)

			if tt.expectError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorMsg)
			} else {
				require.NoError(t, err)
			}

			// Warnings should always be nil for this implementation
			assert.Nil(t, warnings)
		})
	}
}

func TestTemporalWorkerDeployment_ValidateDelete(t *testing.T) {
	ctx := context.Background()
	webhook := &TemporalWorkerDeployment{}

	obj := &TemporalWorkerDeployment{
		ObjectMeta: metav1.ObjectMeta{
			Name: "worker",
		},
	}

	warnings, err := webhook.ValidateDelete(ctx, obj)

	// ValidateDelete should always return nil, nil
	assert.NoError(t, err)
	assert.Nil(t, warnings)
}
