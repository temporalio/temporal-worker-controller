// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.
package defaults

import "time"

// Default values for TemporalWorkerDeploymentSpec fields
const (
	ScaledownDelay                   = 1 * time.Hour
	DeleteDelay                      = 24 * time.Hour
	ServerMaxVersions                = 100
	MaxVersionsIneligibleForDeletion = int32(ServerMaxVersions * 0.75)
)
