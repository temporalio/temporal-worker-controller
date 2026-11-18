package main

import (
	"os/exec"
	"strings"
	"testing"
)

func TestSTSRefresherCommandShowsUsage(t *testing.T) {
	cmd := exec.Command("go", "run", ".", "sts-token-refresher", "--help")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("STS refresher help failed: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "Usage:") {
		t.Fatalf("STS refresher help did not print usage:\n%s", output)
	}
}
