package worker

import (
	"testing"

	"go.temporal.io/sdk/testsuite"
	"pgregory.net/rapid"
)

// TODO: Add proptest validating that no interactions are possible when the tamagotchi is sleeping
// TODO: Add proptest validating that no interactions other than sleep are possible when the tamagotchi is too tired
// TODO: Add proptest validating that no interactions other than feeding are possible when the tamagotchi is too hungry

func TestTamagotchi(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		testsuite.StartDevServer(nil, testsuite.DevServerOptions{})
	})
}
