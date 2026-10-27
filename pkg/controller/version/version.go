// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package version

import (
	"os"
)

const (
	VersionEnvKey = "CONTROLLER_VERSION"
)

// Version is set by goreleaser via ldflags at build time
var Version = "unknown"

// GetControllerVersion returns the version, preferring build-time injection
// over environment variable
func GetControllerVersion() string {
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
