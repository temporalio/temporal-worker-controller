# Integration Tests

This directory contains integration tests for the Temporal Worker Controller that run against a locally-running Temporal dev server. The tests use Go's native testing framework.

## Prerequisites

**Go Dependencies**: Make sure all Go dependencies are installed:

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
2. Run the integration tests

## Test Structure

The integration test (`internal/integration_test.go`) includes:

1. **Test Environment Setup**: Uses envtest to create a local Kubernetes API server
2. **CRD Loading**: Loads your CRDs from the Helm directory
3. **Controller Setup**: Creates and starts the controller with a real manager
4. **Temporal Client**: Connects to an in-memory Temporal dev server started by the test code
5. **Test**: Creates a `TemporalConnection` and `TemporalWorkerDeployment` based on the provided spec, then verifies:
   - The controller reconciles successfully
   - A Kubernetes deployment is created
   - The observed status converges to what is expected for that test case

The test uses Go's native `testing` package with standard assertions and error handling.

## Troubleshooting

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

To add more integration test scenarios, you can create additional test cases in the existing test function,
or you can create a new test function.

Each test case should use a unique Worker Deployment Name (ideally based on the test case name), because
the same Temporal server and namespace are reused for the duration of `TestIntegration`.
If that becomes an issue, it can be changed, but I think using one `temporaltest` server instance per
functional test and sharing it across test cases will make the tests run a lot faster as we add more 
test cases.

Each test case should pass in:
```go
type TestCase struct {
	// If starting from a particular state, specify that in input.Status
	twd *temporaliov1alpha1.TemporalWorkerDeployment
	// TemporalWorkerDeploymentStatus only tracks the names of the Deployments for deprecated
	// versions, so for test scenarios that start with existing deprecated version Deployments,
	// specify the number of replicas for each deprecated build here.
	existingDeploymentReplicas map[string]int32
	// TemporalWorkerDeploymentStatus only tracks the build ids of the Deployments for deprecated
	// versions, not their images so for test scenarios that start with existing deprecated version Deployments,
	// specify the images for each deprecated build here.
	existingDeploymentImages map[string]string
	expectedStatus           *temporaliov1alpha1.TemporalWorkerDeploymentStatus
	// Time to delay before checking expected status
	waitTime *time.Duration

	// Arbitrary function called at the end of setting up the environment specified by input.Status.
	// Can be used for additional state creation / destruction
	setupFunc func(t *testing.T, ctx context.Context, tc TestCase, env TestEnv)
}
```

Each test function should follow the same pattern:
1. Set up test environment (including preliminary state)
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