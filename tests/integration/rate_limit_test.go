//go:build integration

package integration

import (
	"testing"

	"go.temporal.io/server/common/dynamicconfig"
	"go.temporal.io/server/temporal"
	"go.temporal.io/server/temporaltest"
)

// TestWorkerDeploymentRateLimit runs independently of the rollout suite so a
// focused run only starts the Temporal server configured for rate limiting.
func TestWorkerDeploymentRateLimit(t *testing.T) {
	cfg, k8sClient, _, _, cleanup := setupTestEnvironment(t)
	defer cleanup()
	testNamespace := createTestNamespace(t, k8sClient)
	defer cleanupTestNamespace(t, cfg, k8sClient, testNamespace)

	// Use a dedicated server to keep rate limits isolated from rollout tests.
	// Ten TWDs in the same Temporal namespace produce 10 concurrent DescribeWorkerDeployment
	// calls per second against a 1 RPS limit, reliably triggering ResourceExhausted errors.
	dcRateLimit := dynamicconfig.NewMemoryClient()
	dcRateLimit.OverrideValue(dynamicconfig.MakeKey("frontend.globalNamespaceWorkerDeploymentReadRPS"), 1)
	tsRateLimit := temporaltest.NewServer(
		temporaltest.WithT(t),
		temporaltest.WithBaseServerOptions(temporal.WithDynamicConfigClient(dcRateLimit)),
	)
	runRateLimitTest(t, k8sClient, tsRateLimit, testNamespace.Name)
}
