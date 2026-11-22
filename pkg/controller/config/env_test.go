// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package config_test

import (
	"testing"

	"github.com/temporalio/temporal-worker-controller/pkg/controller/config"
)

func TestGetWRTHPAMatchLabelsStripTemporalPrefix(t *testing.T) {
	tests := map[string]struct {
		value string
		want  bool
	}{
		"unset":          {"", false},
		"false":          {"false", false},
		"true":           {"true", true},
		"uppercase true": {"TRUE", true},
		"invalid":        {"not-a-bool", false},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Setenv(config.WRTHPAMatchLabelsStripTemporalPrefixEnvKey, tc.value)
			if got := config.GetWRTHPAMatchLabelsStripTemporalPrefix(); got != tc.want {
				t.Fatalf("GetWRTHPAMatchLabelsStripTemporalPrefix() = %t, want %t", got, tc.want)
			}
		})
	}
}
