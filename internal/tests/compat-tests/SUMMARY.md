# Compatibility Testing Framework - Setup Complete

## What We Built

A comprehensive compatibility testing framework for the Temporal Worker Controller that validates compatibility across multiple Temporal server versions.

## Directory Structure

```
internal/tests/
├── internal/                           # Shared test infrastructure
│   ├── compat_test_suite.go           # Compatibility test cases
│   ├── compat_helpers.go              # Compatibility test helpers
│   ├── integration_test.go            # Integration tests
│   ├── deployment_controller.go       # Test helpers
│   └── ... (other shared test code)
└── compat-tests/
    ├── v1.28.1/                       # Server v1.28.1 tests
    │   ├── go.mod                     # Pins to v1.28.1
    │   └── compat_test.go             
    ├── v1.29.1/                       # Server v1.29.1 tests (has the bug)
    │   ├── go.mod                     # Pins to v1.29.1
    │   └── compat_test.go             
    ├── v1.30.0/                       # Server v1.30.0 tests
    │   ├── go.mod                     # Pins to v1.30.0
    │   └── compat_test.go             
    ├── README.md                      # Technical documentation
    └── SUMMARY.md                     # This file
```

## Quick Start

### Run All Compatibility Tests

```bash
make test-compat
```

### Run Tests for Specific Server Version

```bash
make test-compat-v1.28.1
make test-compat-v1.29.1
make test-compat-v1.30.0
```

## Test Coverage

The compatibility suite currently includes 5 core test cases:

1. **Manual Rollout** - Tests manual deployment strategy
2. **All-At-Once with Gate** - Tests immediate rollout with gate validation
3. **All-At-Once with Failed Gate** - Tests rollout blocking on failed gate
4. **Progressive Rollout (No Current)** - Tests progressive becomes all-at-once
5. **Progressive Rollout (With Current)** - Tests first step of progressive rollout

These tests represent the essential controller functionality that must work across all server versions.

## CI Integration

### GitHub Actions Workflow

`.github/workflows/compatibility-test.yml`

Runs automatically on:
- Pull requests to main
- Pushes to main
- Release tags (v*)
- Manual trigger

The workflow uses a matrix strategy to test all three server versions in parallel.

## Current Compatibility Matrix

| Controller Version | Server v1.28.1 | Server v1.29.1 | Server v1.30.0 |
|-------------------|----------------|----------------|----------------|
| v1.1.0            | ❌             | ❌             | ✅             |
| v1.1.1+           | ✅             | ✅             | ✅             |

### Known Issue

Server versions v1.29.1 and below have a bug that causes incompatibility with controller v1.1.0. Controller v1.1.1+ includes a workaround for this server bug.

## Documentation

- **Technical Details**: `internal/tests/compat-tests/README.md`
- **Usage Guide**: `docs/compatibility-testing.md`

## Next Steps

### For Users

1. Run the tests locally to verify your environment:
   ```bash
   make test-compat-v1.28.1
   ```

2. The tests will automatically run in CI when you create a PR

### For Contributors

1. **Adding New Test Cases**: Edit `internal/tests/internal/compat_test_suite.go`
2. **Adding New Server Versions**: Follow the guide in the README
3. **Debugging Failures**: Check test logs and consult the troubleshooting section

## Implementation Notes

### Module Structure

Each server version test is a separate Go module with its own `go.mod` file. This allows us to pin to different `go.temporal.io/server` versions without conflicts.

The shared test suite lives in `internal/tests/internal/` alongside the existing integration test infrastructure. This maximizes code reuse - the compatibility tests can leverage all the same helper functions, builders, and utilities as the integration tests.

### Test Philosophy

- **Fast**: Uses in-process servers (temporaltest), not containers
- **Focused**: Tests core features, not every edge case
- **Maintainable**: Shared test suite reduces duplication
- **Extensible**: Easy to add new server versions or test cases

## Verification

To verify the setup is working:

```bash
# 1. Ensure dependencies are installed
make envtest

# 2. Generate manifests
make manifests generate

# 3. Run one compatibility test
make test-compat-v1.28.1
```

If the test passes, the framework is ready to use!

## Support

If you encounter issues:

1. Check the [Troubleshooting section](README.md#troubleshooting) in the README
2. Ensure `KUBEBUILDER_ASSETS` is set (run `make envtest`)
3. Run `go mod tidy` in the failing test directory
4. Review the CI logs for additional context

## Credits

This compatibility testing framework was created to address compatibility issues between controller v1.1.0 and server v1.29.1. It provides a systematic way to validate compatibility across versions before releasing new controller versions.

