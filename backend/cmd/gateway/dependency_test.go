package main

import (
	"go/parser"
	"go/token"
	"strconv"
	"testing"
)

func TestGatewayDoesNotImportMockProvider(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, parser.ImportsOnly)
	if err != nil {
		t.Fatal(err)
	}
	for _, imported := range file.Imports {
		path, err := strconv.Unquote(imported.Path.Value)
		if err != nil {
			t.Fatal(err)
		}
		if path == "github.com/NatsumeRyuhane/LLM-Gateway/backend/internal/mockprovider" {
			t.Fatal("production gateway command imports mock-provider behavior")
		}
	}
}
