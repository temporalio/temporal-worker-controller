// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package controller

import (
	"os"
)

const (
	controllerIdentityKey     = "temporal.io/controller"
	controllerVersionKey      = "temporal.io/controller-version"
	defaultControllerIdentity = "temporal-worker-controller"
)

// getControllerVersion returns the version from environment variable (set by Helm from image.tag)
func getControllerVersion() string {
	if version := os.Getenv("CONTROLLER_VERSION"); version != "" {
		return version
	}
	return "unknown"
}

// getControllerIdentity returns the identity from environment variable (set by Helm)
func getControllerIdentity() string {
	if identity := os.Getenv("CONTROLLER_IDENTITY"); identity != "" {
		return identity
	}
	return defaultControllerIdentity
}
