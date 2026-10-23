// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package config

import (
	"os"
	"strconv"

	"github.com/temporalio/temporal-worker-controller/internal/defaults"
)

const (
	IdentityEnvKey                                   = "CONTROLLER_IDENTITY"
	IdentitySuffixEnvKey                             = "CONTROLLER_IDENTITY_SUFFIX"
	MaxDeploymentVersionsIneligibleForDeletionEnvKey = "CONTROLLER_MAX_DEPLOYMENT_VERSIONS_INELIGIBLE_FOR_DELETION"
	WRTHPAMatchLabelsStripTemporalPrefixEnvKey       = "WRT_HPA_MATCH_LABELS_STRIP_TEMPORAL_PREFIX"
)

// GetDeprecatedControllerIdentity is used for smooth identity reclamation when rolling
// forward to the new identity format.
func GetDeprecatedControllerIdentity() string {
	if identity := os.Getenv(IdentityEnvKey); identity != "" {
		return identity
	}
	return defaults.DeprecatedDefaultControllerIdentity
}

// GetControllerIdentity returns the identity with controller env var and namespace uid suffix.
// Presence of both are ensured by main() and in Reconcile() for users of the controller as a library.
func GetControllerIdentity() string {
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

func GetWRTHPAMatchLabelsStripTemporalPrefix() bool {
	stripPrefix, err := strconv.ParseBool(os.Getenv(WRTHPAMatchLabelsStripTemporalPrefixEnvKey))
	return err == nil && stripPrefix
}
