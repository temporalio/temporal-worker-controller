package testhelpers_test

import (
	"fmt"

	"github.com/temporalio/temporal-worker-controller/internal/testhelpers"
)

func ExamplePtr() {
	// Method 1: Type inference from argument
	stringPtr := testhelpers.Ptr("hello")

	// Method 2: Explicit type parameter
	int32Ptr := testhelpers.Ptr[int32](42)

	// Method 3: Type inference from assignment
	var boolPtr *bool = testhelpers.Ptr(true)

	fmt.Println(*stringPtr, *int32Ptr, *boolPtr)
	// Output: hello 42 true
}
