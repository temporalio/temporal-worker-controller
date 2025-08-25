package testhelpers_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	"github.com/temporalio/temporal-worker-controller/internal/testhelpers"
)

// ExampleTestCase demonstrates how to create and use a TestCase for integration testing.
// This example shows the basic structure and usage patterns for TestCase.
func ExampleTestCase() {
	// Create a test case using the builder pattern
	testCaseBuilder := testhelpers.NewTestCase().
		WithInput(
			testhelpers.NewTemporalWorkerDeploymentBuilder().
				WithName("example-worker").
				WithNamespace("default").
				WithManualStrategy().
				WithTargetTemplate("v1"),
		).
		WithExpectedStatus(
			testhelpers.NewStatusBuilder().
				WithTargetVersion("v1", temporaliov1alpha1.VersionStatusInactive, -1, true, false),
		).
		WithWaitTime(5 * time.Second).
		WithSetupFunction(func(t *testing.T, ctx context.Context, tc testhelpers.TestCase, env testhelpers.TestEnv) {
			// Custom setup logic can go here
			fmt.Println("Setting up test environment")
		})

	testCase := testCaseBuilder.BuildWithValues("example-worker", "default", "default")

	// Access the TestCase fields
	twd := testCase.GetTWD()
	fmt.Printf("Testing deployment: %s/%s\n", twd.Namespace, twd.Name)
	fmt.Printf("Strategy: %s\n", twd.Spec.RolloutStrategy.Strategy)

	// Output:
	// Testing deployment: default/example-worker
	// Strategy: Manual
}

// ExampleTestCase_withExistingDeployments demonstrates how to create a TestCase
// that starts with existing deprecated deployments.
func ExampleTestCase_withExistingDeployments() {
	testCaseBuilder := testhelpers.NewTestCase().
		WithInput(
			testhelpers.NewTemporalWorkerDeploymentBuilder().
				WithName("worker-with-history").
				WithNamespace("test-ns").
				WithAllAtOnceStrategy().
				WithTargetTemplate("v2"),
		).
		WithExistingDeployments(testhelpers.DeploymentInfo{}).
		WithExpectedStatus(
			testhelpers.NewStatusBuilder().
				WithTargetVersion("v2", temporaliov1alpha1.VersionStatusInactive, -1, true, false),
		)

	testCase := testCaseBuilder.BuildWithValues("worker-with-history", "test-ns", "default")

	twd := testCase.GetTWD()
	fmt.Printf("Worker: %s/%s\n", twd.Namespace, twd.Name)

	// Output:
	// Worker: test-ns/worker-with-history
}
