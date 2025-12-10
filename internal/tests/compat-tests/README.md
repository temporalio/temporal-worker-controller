# Temporal Worker Controller Compatibility Testing

This directory contains a compatibility testing framework that validates the controller works correctly with different versions of the Temporal server.

## Overview

The compatibility testing framework helps ensure that:
- New controller versions remain compatible with older server versions
- We catch breaking changes before releasing
- We maintain a clear compatibility matrix
- We can validate specific bug fixes across versions

## Architecture

```
internal/tests/
├── internal/                          # Shared test infrastructure
│   ├── compat_test_suite.go          # Compatibility test cases
│   ├── compat_helpers.go             # Compatibility test helpers  
│   ├── integration_test.go           # Integration tests
│   ├── deployment_controller.go      # Test helpers
│   └── ... (other test infrastructure)
└── compat-tests/
    ├── v1.28.1/                      # Tests for server v1.28.1
    │   ├── go.mod                    # Pins server to v1.28.1
    │   └── compat_test.go            # Runs test suite
    ├── v1.29.1/                      # Tests for server v1.29.1
    │   ├── go.mod                    # Pins server to v1.29.1
    │   └── compat_test.go            # Runs test suite
    └── v1.30.0/                      # Tests for server v1.30.0
        ├── go.mod                    # Pins server to v1.30.0
        └── compat_test.go            # Runs test suite
```

### Key Design Decisions

1. **Separate Go Modules**: Each server version has its own `go.mod` file. This allows us to pin to different server versions without conflicts.

2. **Shared Test Infrastructure**: The compatibility test suite lives in `internal/tests/internal/` alongside the existing integration test helpers. This maximizes code reuse and avoids duplication.

3. **In-Process Server**: We use `temporaltest` which spins up an in-process Temporal server. This is fast and doesn't require Docker.

4. **Focused Tests**: We test core functionality that must work regardless of server version. We don't test every feature, just the essentials.

## Running Tests Locally

### Prerequisites

```bash
# Install envtest if not already installed
make envtest

# Generate manifests
make manifests generate
```

### Run All Compatibility Tests

```bash
make test-compat
```

This will run the test suite against all configured server versions.

### Run Tests for Specific Server Version

```bash
# Test against v1.28.1
make test-compat-v1.28.1

# Test against v1.29.1
make test-compat-v1.29.1

# Test against v1.30.0
make test-compat-v1.30.0
```

### Run Tests Directly

You can also run tests directly using Go:

```bash
cd internal/tests/compat-tests/v1.28.1
KUBEBUILDER_ASSETS="$(../../bin/setup-envtest use 1.27.1 --bin-dir ../../bin -p path)" \
  go test -v -tags test_dep -run TestCompatibility
```

## CI Integration

The compatibility tests run automatically in GitHub Actions:

- **On Pull Requests**: Runs when changes affect controller code
- **On Main Branch**: Runs on every push to main
- **On Tags**: Runs when creating release tags (e.g., `v1.1.1`)
- **Manual Trigger**: Can be triggered via workflow_dispatch

The workflow runs tests in parallel across all server versions using a matrix strategy.

## Supported Server Versions

| Server Version | Status | Notes |
|---------------|--------|-------|
| v1.28.1 | ✅ Supported | Baseline compatibility version |
| v1.29.1 | ⚠️ Has Bug | Controller v1.1.0 NOT compatible |
| v1.30.0 | ✅ Supported | Latest tested version |

### Known Incompatibilities

- **Controller v1.1.0** is NOT compatible with **Server v1.29.1 and below** due to a bug in the server
- **Controller v1.1.1+** handles the bug and is compatible with **Server v1.28.1+**
- **Server v1.29.2+** (when released) will contain the bug fix

## Adding a New Server Version

To add tests for a new server version (e.g., v1.31.0):

### 1. Create Directory Structure

```bash
mkdir -p internal/tests/compat-tests/v1.31.0
```

### 2. Create go.mod

Create `internal/tests/compat-tests/v1.31.0/go.mod`:

```go
module github.com/temporalio/temporal-worker-controller/tests/compat-v1.31.0

go 1.24.5

require (
	github.com/temporalio/temporal-worker-controller v1.0.1
	github.com/temporalio/temporal-worker-controller/tests v0.0.0
	go.temporal.io/api v1.50.1
	go.temporal.io/sdk v1.35.0
	go.temporal.io/server v1.31.0  // <- Update this version
	k8s.io/api v0.34.0
	k8s.io/apimachinery v0.34.0
	k8s.io/client-go v0.34.0
	sigs.k8s.io/controller-runtime v0.21.0
)

replace github.com/temporalio/temporal-worker-controller => ../../../..
replace github.com/temporalio/temporal-worker-controller/tests => ../..
```

### 3. Create Test File

Copy an existing test file (e.g., from `v1.30.0/compat_test.go`) and update:
- Package name: `package compat_v1_31_0`
- ServerVersion constant: `ServerVersion = "v1.31.0"`
- Log messages and namespace names

### 4. Run go mod tidy

```bash
cd internal/tests/compat-tests/v1.31.0
go mod tidy
```

### 5. Add Makefile Target

In the main `Makefile`, add:

```makefile
.PHONY: test-compat-v1.31.0
test-compat-v1.31.0: manifests generate envtest
	@echo "Running compatibility tests against v1.31.0..."
	cd internal/tests/compat-tests/v1.31.0 && \
	KUBEBUILDER_ASSETS="$(shell $(ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN) -p path)" \
	go test -v -tags test_dep -run TestCompatibility
```

And update the `test-compat` target:

```makefile
.PHONY: test-compat
test-compat: test-compat-v1.28.1 test-compat-v1.29.2 test-compat-v1.30.0 test-compat-v1.31.0
```

### 6. Update GitHub Actions Workflow

In `.github/workflows/compatibility-test.yml`, add to the matrix:

```yaml
strategy:
  matrix:
    server_version:
      - v1.28.1
      - v1.29.2
      - v1.30.0
      - v1.31.0  # Add here
```

### 7. Test Locally

```bash
make test-compat-v1.31.0
```

### 8. Update Documentation

Update the "Supported Server Versions" table in this README.

## Adding New Test Cases

To add new test cases to the compatibility suite:

### 1. Add to Compatibility Test Suite

Edit `internal/tests/internal/compat_test_suite.go` and add to the `TestCases` slice in `GetCoreCompatibilityTestSuite()`:

```go
{
    Name: "my-new-test-case",
    Builder: testhelpers.NewTestCase().
        WithInput(
            testhelpers.NewTemporalWorkerDeploymentBuilder().
                // ... configure test
        ).
        WithExpectedStatus(
            // ... define expected outcome
        ),
},
```

### 2. Test Against All Versions

Run the full compatibility suite to ensure your new test passes on all server versions:

```bash
make test-compat
```

If a test should only run on certain server versions, you can create version-specific test suites or add conditional logic in the test.

## Troubleshooting

### Tests Fail Locally But Pass in CI

- Ensure `KUBEBUILDER_ASSETS` is set correctly
- Run `make envtest` to install/update envtest binaries
- Check that you've run `make manifests generate`

### "Cannot find module" Errors

```bash
# From the root of the repo
cd internal/tests/compat-tests/v1.XX.X
go mod tidy
```

### Test Timeout Issues

The tests use in-process Temporal servers which can be slow on first run. If you see timeouts:
- Increase timeout values in the test
- Check if your machine is under load
- Ensure you're not running too many tests in parallel

### Server Version Not Available

If a specific Temporal server version isn't available:
- Check if the version exists: https://github.com/temporalio/temporal/releases
- Ensure you're using a valid semver (e.g., `v1.28.1` not `1.28.1`)
- Update your Go dependencies: `go get go.temporal.io/server@v1.XX.X`

## Design Philosophy

### What to Test

✅ **DO test:**
- Core controller operations (create, update, rollout)
- Basic version management
- Deployment strategies (manual, all-at-once, progressive)
- Gates and basic validation
- Status reporting

❌ **DON'T test:**
- Every edge case (that's what integration tests are for)
- UI/UX features
- Performance characteristics
- Optional features that vary by version

### Test Isolation

Each test should:
- Create its own namespace
- Clean up after itself
- Not depend on other tests
- Be idempotent

### Speed vs Coverage

We prioritize fast tests over exhaustive tests:
- Use a small set of representative tests
- Tests should complete in < 5 minutes per version
- If tests get too slow, consider splitting into separate suites

## Future Enhancements

Potential improvements to this framework:

1. **Controller Version Matrix**: Test old controller versions against new servers (requires checking out different controller versions)

2. **Upgrade/Downgrade Tests**: Test server upgrades while controller is running, and vice versa

3. **Docker-Based Tests**: For more realistic testing, use actual Temporal server containers instead of temporaltest

4. **Performance Baselines**: Track performance metrics across versions

5. **Compatibility Badge**: Auto-generate a compatibility matrix badge for the README

## Questions?

If you have questions about compatibility testing:
- Check existing tests in `internal/tests/internal/` for examples
- Review the testhelpers package: `internal/testhelpers/`
- Open a discussion in the GitHub repository

