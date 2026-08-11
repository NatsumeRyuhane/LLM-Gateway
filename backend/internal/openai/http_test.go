package openai

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/NatsumeRyuhane/LLM-Gateway/backend/internal/protocol"
)

func TestAcceptSelectionConformanceFixture(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "tests", "conformance", "gateway.adapter.v0", "http", "accept-selection.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		ContractVersion string `json:"contract_version"`
		Cases           []struct {
			Name     string `json:"name"`
			Stream   bool   `json:"stream"`
			Accept   string `json:"accept"`
			Expected struct {
				Representation string `json:"representation"`
				Failure        *struct {
					Code   protocol.FailureCode   `json:"code"`
					Domain protocol.FailureDomain `json:"domain"`
				} `json:"failure"`
			} `json:"expected"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.ContractVersion != protocol.ContractVersion {
		t.Fatalf("contract_version = %q", fixture.ContractVersion)
	}
	for _, testCase := range fixture.Cases {
		t.Run(testCase.Name, func(t *testing.T) {
			representation, selectionErr := SelectRepresentation(testCase.Accept, testCase.Stream)
			if testCase.Expected.Failure != nil {
				if selectionErr == nil || selectionErr.Code != testCase.Expected.Failure.Code || selectionErr.Domain != testCase.Expected.Failure.Domain {
					t.Fatalf("SelectRepresentation() = %q, %#v", representation, selectionErr)
				}
				return
			}
			if selectionErr != nil || representation != testCase.Expected.Representation {
				t.Fatalf("SelectRepresentation() = %q, %v; want %q", representation, selectionErr, testCase.Expected.Representation)
			}
		})
	}
}

func TestAcceptSelectionRejectsOutOfGrammarQualityPrecision(t *testing.T) {
	if _, err := SelectRepresentation("application/json;q=0.1234", false); err == nil {
		t.Fatal("SelectRepresentation() accepted an HTTP qvalue with excessive precision")
	}
}
