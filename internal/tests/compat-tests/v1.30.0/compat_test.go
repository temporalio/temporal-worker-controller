package compat_v1_30_0

import (
	"testing"

	compatinternal "github.com/temporalio/temporal-worker-controller/tests/internal"
	"go.temporal.io/server/temporaltest"
)

const ServerVersion = "v1.30.0"

// TestCompatibility runs compatibility tests against Temporal server v1.30.0
func TestCompatibility(t *testing.T) {
	// The only version-specific code: create a server with the version pinned in this module's go.mod
	serverFactory := func(t *testing.T) *temporaltest.TestServer {
		return compatinternal.NewTemporalTestServerWithConfig(t)
	}

	// Everything else is shared - setup, teardown, running tests, etc.
	compatinternal.RunCompatibilityTestsForServerVersion(t, ServerVersion, serverFactory)
}

