# Integration Tests

This directory contains integration tests for the Temporal Worker Controller that run against a locally-running Temporal dev server. The tests use Go's native testing framework.

## Prerequisites

1. **Temporal Dev Server**: You need to have a Temporal dev server running locally. You can start one using:

```bash
# Using Docker
docker run --rm -p 7233:7233 temporalio/auto-setup:1.22.3

# Or using temporal CLI
temporal server start-dev
```

2. **Go Dependencies**: Make sure all Go dependencies are installed:

```bash
go mod tidy
```

## Running the Integration Tests

### Run Integration Tests (Recommended)

The integration tests require the proper envtest setup. Use the Makefile target which handles this automatically:

```bash
make test-integration
```

This command will:
1. Set up the envtest environment with proper KUBEBUILDER_ASSETS
2. Start a Temporal dev server in Docker
3. Run the integration tests
4. Clean up the Temporal dev server

### Alternative: Run Without Temporal Server

If you want to run the tests against an existing Temporal server (not managed by the Makefile):

```bash
# Set up envtest and run tests
KUBEBUILDER_ASSETS="$(bin/setup-envtest use 1.27.1 --bin-dir bin -p path)" go test -v ./tests -run TestIntegration
```

### Clean Up

If you need to clean up any leftover Temporal dev server containers:

```bash
make test-integration-clean
```

## Test Structure

The integration test (`tests/integration_test.go`) includes:

1. **Test Environment Setup**: Uses envtest to create a local Kubernetes API server
2. **CRD Loading**: Loads your CRDs from the Helm directory
3. **Controller Setup**: Creates and starts the controller with a real manager
4. **Temporal Client**: Connects to a local Temporal dev server
5. **Happy Path Test**: Creates a `TemporalConnection` and `TemporalWorkerDeployment`, then verifies:
   - The controller reconciles successfully
   - A Kubernetes deployment is created
   - The status is updated correctly

The test uses Go's native `testing` package with standard assertions and error handling.

## Test Configuration

The test is configured to connect to a Temporal dev server at `localhost:7233`. If your dev server is running on a different host or port, update the `HostPort` field in the test:

```go
Spec: temporaliov1alpha1.TemporalConnectionSpec{
    HostPort: "your-host:your-port", // Update this
},
```

## Troubleshooting

### Temporal Server Connection Issues

If the test fails to connect to Temporal:

1. Verify your Temporal dev server is running:
   ```bash
   curl http://localhost:7233/health
   ```

2. Check the Temporal server logs for any errors

3. Ensure no firewall is blocking port 7233

### Kubernetes API Server Issues

If envtest fails to start:

1. **Missing etcd binary**: This is the most common issue. The error `fork/exec /usr/local/kubebuilder/bin/etcd: no such file or directory` means the envtest binaries aren't properly set up.
   
   **Solution**: Use the Makefile target which handles this automatically:
   ```bash
   make test-integration
   ```

2. Check that your Go version is compatible (1.24+)
3. Verify all dependencies are installed: `go mod tidy`
4. Check that the CRD files exist in the Helm directory

### Test Timeouts

If tests are timing out:

1. Increase the timeout values in the `Eventually` calls
2. Check if your system is under heavy load
3. Verify the Temporal server is responsive

## Adding More Tests

To add more integration test scenarios, you can create additional test functions:

```go
func TestTemporalWorkerDeploymentUpdate(t *testing.T) {
    // Test implementation for updates
}

func TestTemporalWorkerDeploymentScale(t *testing.T) {
    // Test implementation for scaling
}
```

Each test function should follow the same pattern:
1. Set up test environment
2. Create test resources
3. Perform the test action
4. Wait for expected state changes
5. Verify results
6. Clean up resources

## CI/CD Integration

For CI/CD pipelines, you can use the Makefile target which handles everything automatically:

```yaml
- name: Run Integration Tests
  run: make test-integration
```

Or if you prefer to manage the Temporal server separately:

```yaml
- name: Start Temporal Dev Server
  run: |
    docker run -d --name temporal-dev -p 7233:7233 temporalio/auto-setup:1.22.3
    sleep 10  # Wait for server to start

- name: Run Integration Tests
  run: |
    KUBEBUILDER_ASSETS="$(bin/setup-envtest use 1.27.1 --bin-dir bin -p path)" go test -v ./tests -run TestIntegration

- name: Cleanup
  if: always()
  run: |
    docker stop temporal-dev || true
    docker rm temporal-dev || true
```

## Makefile Integration

The project Makefile includes targets for running integration tests:

```bash
# Run integration tests with automatic Temporal server setup
make test-integration

# Clean up any leftover Temporal dev server containers
make test-integration-clean
```

These targets are configured to work with the tests in the `tests/` directory. 