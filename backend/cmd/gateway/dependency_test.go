package main

import (
	"bytes"
	"os/exec"
	"testing"
)

func TestGatewayDoesNotImportMockProvider(t *testing.T) {
	command := exec.CommandContext(t.Context(), "go", "list", "-deps", ".")
	output, err := command.Output()
	if err != nil {
		t.Fatalf("go list gateway dependencies: %v", err)
	}
	for _, dependency := range bytes.Fields(output) {
		if string(dependency) == "github.com/NatsumeRyuhane/LLM-Gateway/backend/internal/mockprovider" {
			t.Fatal("production gateway command imports mock-provider behavior")
		}
	}
}
