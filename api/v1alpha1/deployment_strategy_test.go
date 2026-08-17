// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package v1alpha1

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/temporalio/temporal-worker-controller/internal/defaults"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func TestDefaultDeploymentStrategy_DoesNotMutateInput(t *testing.T) {
	maxUnavailable := intstr.FromString("5%")
	rollingUpdate := &appsv1.RollingUpdateDeployment{
		MaxUnavailable: &maxUnavailable,
	}
	in := &appsv1.DeploymentStrategy{RollingUpdate: rollingUpdate}

	out := DefaultDeploymentStrategy(in)
	require.NotNil(t, out)
	assert.Equal(t, appsv1.RollingUpdateDeploymentStrategyType, out.Type)
	require.NotNil(t, out.RollingUpdate.MaxSurge)
	assert.Equal(t, intstr.FromString(defaults.DeploymentMaxSurge), *out.RollingUpdate.MaxSurge)

	assert.Nil(t, rollingUpdate.MaxSurge, "input RollingUpdate must not be mutated")
	assert.Empty(t, in.Type, "input Type must not be mutated")
}
