package internal

import (
	"testing"
	"time"

	"go.temporal.io/server/common/dynamicconfig"
	"go.temporal.io/server/temporal"
	"go.temporal.io/server/temporaltest"
)

// ServerFactory is a function that creates a temporaltest.TestServer.
// This allows each version-specific test to create a server with the pinned version.
type ServerFactory func(t *testing.T) *temporaltest.TestServer

// RunCompatibilityTestsForServerVersion runs the compatibility test suite against
// a specific Temporal server version. The serverFactory function creates the server
// with the version pinned in that module's go.mod.
func RunCompatibilityTestsForServerVersion(t *testing.T, serverVersion string, serverFactory ServerFactory) {
	LogCompatibilityTest(t, "current", serverVersion, "Core Compatibility Suite")

	// Set up test environment (K8s, controller, etc.) - all version-independent
	cfg, k8sClient, mgr, _, cleanup := setupTestEnvironment(t)
	defer cleanup()

	// Create test namespace
	testNamespace := createTestNamespace(t, k8sClient)
	defer cleanupTestNamespace(t, cfg, k8sClient, testNamespace)

	// Create Temporal server using the version-specific factory
	ts := serverFactory(t)

	// Run the core compatibility test suite
	suite := GetCoreCompatibilityTestSuite()
	RunCompatibilityTests(t, k8sClient, mgr, ts, testNamespace.Name, suite)
}

// NewTemporalTestServerWithConfig creates a temporaltest.TestServer with the
// standard configuration used for compatibility tests.
func NewTemporalTestServerWithConfig(t *testing.T) *temporaltest.TestServer {
	dc := dynamicconfig.NewMemoryClient()
	// Make versions drain faster for testing
	dc.OverrideValue("matching.wv.VersionDrainageStatusVisibilityGracePeriod", time.Second)
	dc.OverrideValue("matching.wv.VersionDrainageStatusRefreshInterval", time.Second)
	dc.OverrideValue("matching.maxVersionsInDeployment", 6)
	
	return temporaltest.NewServer(
		temporaltest.WithT(t),
		temporaltest.WithBaseServerOptions(temporal.WithDynamicConfigClient(dc)),
	)
}

