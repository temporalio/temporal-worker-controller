// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package controller

import (
	"os"
	"strconv"

	"github.com/temporalio/temporal-worker-controller/internal/defaults"
)

// Event reason constants for TemporalWorkerDeployment.
//
// These strings appear in Kubernetes Event objects (kubectl get events) and are
// internal to the controller's implementation. They are not part of the CRD
// status API and may change between releases. Do not write alerting or automation
// that depends on these strings.
const (
	ReasonPlanGenerationFailed       = "PlanGenerationFailed"
	ReasonPlanExecutionFailed        = "PlanExecutionFailed"
	ReasonDeploymentCreateFailed     = "DeploymentCreateFailed"
	ReasonDeploymentDeleteFailed     = "DeploymentDeleteFailed"
	ReasonDeploymentScaleFailed      = "DeploymentScaleFailed"
	ReasonDeploymentUpdateFailed     = "DeploymentUpdateFailed"
	ReasonTestWorkflowStartFailed    = "TestWorkflowStartFailed"
	ReasonVersionPromotionFailed     = "VersionPromotionFailed"
	ReasonMetadataUpdateFailed       = "MetadataUpdateFailed"
	ReasonManagerIdentityClaimFailed = "ManagerIdentityClaimFailed"
)

const (
	IdentityMetadataKey = "temporal.io/controller"
	VersionMetadataKey  = "temporal.io/controller-version"

	VersionEnvKey                                    = "CONTROLLER_VERSION"
	IdentityEnvKey                                   = "CONTROLLER_IDENTITY"
	IdentitySuffixEnvKey                             = "CONTROLLER_IDENTITY_SUFFIX"
	MaxDeploymentVersionsIneligibleForDeletionEnvKey = "CONTROLLER_MAX_DEPLOYMENT_VERSIONS_INELIGIBLE_FOR_DELETION"

	serverDeleteVersionIdentity = "try-delete-for-add-version"
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
	if version := os.Getenv(VersionEnvKey); version != "" {
		return version
	}
	return "unknown"
}

// getControllerIdentity returns the identity from environment variable (set by Helm).
// Returns empty string if unset. main() enforces this at startup, but that check is
// bypassed if the reconciler is used as a library (e.g. embedded in another controller
// manager or in tests). An empty return means the env var was not set before starting.
func getControllerIdentity() string {
	if identity := os.Getenv(IdentityEnvKey); identity != "" {
		return identity
	}
	return defaults.ToBeDeprecatedDefaultControllerIdentity
}

// getControllerIdentityWithNamespaceUID returns the identity which will be used in the
// next release. Used in this release for smooth rollback identity reclamation.
func getControllerIdentityWithNamespaceUID() string {
	return getControllerIdentity() + "/" + os.Getenv(IdentitySuffixEnvKey)
}

func GetControllerMaxDeploymentVersionsIneligibleForDeletion() int32 {
	if maxStr := os.Getenv(MaxDeploymentVersionsIneligibleForDeletionEnvKey); maxStr != "" {
		i, err := strconv.Atoi(maxStr)
		if err == nil {
			return int32(i)
		}
	}
	return defaults.MaxVersionsIneligibleForDeletion
}
