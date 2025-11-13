# Compatibility Testing Guide

This guide explains the compatibility testing framework for the Temporal Worker Controller and how to use it to validate compatibility between different controller and server versions.

## Quick Start

Run all compatibility tests:

```bash
make test-compat
```

Run tests for a specific server version:

```bash
make test-compat-v1.28.1
make test-compat-v1.29.1
make test-compat-v1.30.0
```

## What is Compatibility Testing?

Compatibility testing validates that the Temporal Worker Controller works correctly with different versions of the Temporal server. This is critical because:

1. **Version Skew**: Users may update the controller without updating the server, or vice versa
2. **Bug Validation**: We need to verify that bug fixes work across multiple server versions
3. **Release Confidence**: Before releasing a new controller version, we can test against multiple server versions
4. **Documentation**: The tests serve as living documentation of our compatibility guarantees

## Example: The v1.29.1 Bug

This framework was created after discovering that controller v1.1.0 was incompatible with Temporal server v1.29.1 and below due to a bug in the server. The compatibility tests now validate:

- ✅ Controller v1.1.1+ works with server v1.28.1 (bug workaround in controller)
- ✅ Controller v1.1.1+ works with server v1.29.1 (bug workaround in controller)
- ✅ Controller v1.1.1+ works with server v1.30.0

## Framework Architecture

```
internal/tests/
├── internal/                   # Shared test infrastructure
│   ├── compat_test_suite.go   # Compatibility test cases
│   ├── compat_helpers.go      # Compatibility test helpers
│   └── ... (other test helpers)
└── compat-tests/
    ├── v1.28.1/               # Server v1.28.1 tests
    ├── v1.29.1/               # Server v1.29.1 tests
    └── v1.30.0/               # Server v1.30.0 tests
```

Each server version directory:
- Has its own `go.mod` pinning that specific server version
- Imports and runs the shared test suite from `internal/tests/internal/`
- Can optionally add version-specific tests

## CI Integration

The compatibility tests run automatically in CI:

### On Pull Requests
Tests run when you modify controller code, ensuring your changes don't break compatibility.

### On Release Tags
When you create a release tag (e.g., `v1.1.1`), the full compatibility matrix runs to validate the release.

### Manual Triggers
You can manually trigger the workflow from the GitHub Actions tab.

## Supported Versions

Current compatibility matrix:

| Controller Version | Server v1.28.1 | Server v1.29.1 | Server v1.30.0 |
|-------------------|----------------|----------------|----------------|
| v1.1.0            | ❌             | ❌             | ✅             |
| v1.1.1+           | ✅             | ✅             | ✅             |

## When to Run Compatibility Tests

### Required

- ✅ Before releasing a new controller version
- ✅ After fixing a compatibility bug
- ✅ When updating dependencies (especially Temporal SDK/API)

### Recommended

- ✅ During feature development that touches versioning logic
- ✅ When adding new deployment strategies
- ✅ Periodically (e.g., weekly) as part of CI health checks

### Optional

- When making changes to documentation
- When updating tests that don't affect controller behavior

## Adding Test Coverage

To add a new test case that should work across all server versions:

1. Edit `internal/tests/internal/compat_test_suite.go`
2. Add your test case to the suite
3. Run `make test-compat` to validate across all versions

Example:

```go
{
    Name: "manual-rollout-with-gate",
    Builder: testhelpers.NewTestCase().
        WithInput(
            testhelpers.NewTemporalWorkerDeploymentBuilder().
                WithManualStrategy().
                WithGate(true).
                WithTargetTemplate("v1.0"),
        ).
        WithExpectedStatus(
            testhelpers.NewStatusBuilder().
                WithTargetVersion("v1.0", temporaliov1alpha1.VersionStatusInactive, -1, true, false),
        ),
},
```

## Testing Against New Server Versions

When a new Temporal server version is released:

1. Create a new test directory (e.g., `v1.31.0/`)
2. Add a Makefile target
3. Update the GitHub Actions workflow
4. Run the tests locally
5. Document the results

See `internal/tests/compat-tests/README.md` for detailed instructions.

## Interpreting Test Results

### All Tests Pass ✅
The controller is compatible with all tested server versions. Safe to release!

### Some Tests Fail ❌
Investigate which server versions are affected:
- Is this a known incompatibility?
- Do we need to update minimum server version requirements?
- Is this a regression that needs fixing?

### Tests Flake 🟡
Compatibility tests use in-process servers which can be timing-sensitive:
- Check for race conditions
- Increase timeout values if needed
- Consider if the flake indicates a real compatibility issue

## Comparison with Integration Tests

| Aspect | Integration Tests | Compatibility Tests |
|--------|------------------|---------------------|
| **Purpose** | Test all features deeply | Test core features across versions |
| **Server Versions** | Single version (v1.28.1) | Multiple versions |
| **Test Count** | Many (~50+ cases) | Few (~5-10 cases) |
| **Execution Time** | Slower (~5-10 min) | Faster (~2-5 min per version) |
| **Run Frequency** | Every commit | On PRs and releases |

Both test types are important and complement each other:
- **Integration tests** ensure features work correctly
- **Compatibility tests** ensure features work across versions

## Future Improvements

Planned enhancements to the compatibility framework:

### 1. Controller Version Matrix
Test old controller versions against new servers. This helps validate that users can safely upgrade their servers without upgrading the controller.

### 2. Upgrade/Downgrade Tests
Test live upgrades:
- Server upgrade while controller runs
- Controller upgrade while server runs
- Rollback scenarios

### 3. Docker-Based Tests
For more realistic testing, use actual Temporal server containers instead of `temporaltest`.

### 4. Performance Baselines
Track performance metrics across versions to catch performance regressions.

### 5. Automated Compatibility Badge
Generate a badge showing the compatibility matrix for the README.

## Troubleshooting

### "KUBEBUILDER_ASSETS not set"

```bash
make envtest
```

### "Cannot find package"

```bash
cd internal/tests/compat-tests/v1.XX.X
go mod tidy
```

### Tests Timeout

Increase timeout values or check system load. The in-process server can be slow on first run.

## Further Reading

- [Full Compatibility Testing README](../internal/tests/compat-tests/README.md) - Detailed technical documentation
- [Integration Tests README](../internal/tests/README.md) - About integration testing
- [Architecture](./architecture.md) - Controller architecture overview

## Questions?

If you have questions about compatibility testing, please:
1. Check the detailed README in `internal/tests/compat-tests/`
2. Review existing test cases for examples
3. Open a GitHub discussion or issue

