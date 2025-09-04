// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package controller

import (
	"os"
	"strconv"

	"github.com/temporalio/temporal-worker-controller/internal/defaults"
)

const (
	controllerIdentityMetadataKey = "temporal.io/controller"
	controllerVersionMetadataKey  = "temporal.io/controller-version"

	controllerVersionEnvKey                                    = "CONTROLLER_VERSION"
	controllerIdentityEnvKey                                   = "CONTROLLER_IDENTITY"
	ControllerMaxDeploymentVersionsIneligibleForDeletionEnvKey = "CONTROLLER_MAX_DEPLOYMENT_VERSIONS_INELIGIBLE_FOR_DELETION"
)

// Version is set by goreleaser via ldflags at build time
var Version = "unknown"

// getControllerVersion returns the version, preferring build-time injection over environment variable
func getControllerVersion() string {
	// First check if version was injected at build time
	if Version != "" && Version != "unknown" {
		return Version
	}
	// Fall back to environment variable (set by Helm from image.tag)
	if version := os.Getenv(controllerVersionEnvKey); version != "" {
		return version
	}
	return "unknown"
}

// getControllerIdentity returns the identity from environment variable (set by Helm)
func getControllerIdentity() string {
	if identity := os.Getenv(controllerIdentityEnvKey); identity != "" {
		return identity
	}
	return defaults.ControllerIdentity
}

func GetControllerMaxDeploymentVersionsIneligibleForDeletion() int32 {
	if maxStr := os.Getenv(ControllerMaxDeploymentVersionsIneligibleForDeletionEnvKey); maxStr != "" {
		i, err := strconv.Atoi(maxStr)
		if err == nil {
			return int32(i)
		}
	}
	return defaults.MaxVersionsIneligibleForDeletion
}
