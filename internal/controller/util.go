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

// getDeprecatedControllerIdentity is used for smooth identity reclamation when rolling
// forward to the new identity format.
func getDeprecatedControllerIdentity() string {
	if identity := os.Getenv(IdentityEnvKey); identity != "" {
		return identity
	}
	return defaults.DeprecatedDefaultControllerIdentity
}

// getControllerIdentity returns the identity with controller env var and namespace uid suffix.
// Presence of both are ensured by main() and in Reconcile() for users of the controller as a library.
func getControllerIdentity() string {
	id := os.Getenv(IdentityEnvKey)
	suffix := os.Getenv(IdentitySuffixEnvKey)
	if id != "" && suffix != "" {
		return id + "/" + suffix
	}
	return ""
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
