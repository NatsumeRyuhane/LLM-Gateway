package mockprovider

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestEmbeddedCatalogIsValidAndIsolated(t *testing.T) {
	catalog, err := LoadCatalog()
	if err != nil {
		t.Fatalf("LoadCatalog() error = %v", err)
	}
	if catalog.SchemaVersion() != MatrixSchemaVersion || catalog.DefaultProfile() != "success.buffered" {
		t.Fatalf("catalog metadata = %q %q", catalog.SchemaVersion(), catalog.DefaultProfile())
	}
	profiles := catalog.Profiles()
	if len(profiles) != 33 {
		t.Fatalf("profile count = %d", len(profiles))
	}
	profile, ok := catalog.Profile("success.streaming")
	if !ok || len(profile.Behavior.Steps) == 0 {
		t.Fatalf("streaming profile = %#v, ok = %v", profile, ok)
	}
	profile.Behavior.Steps[0] = "mutated"
	again, _ := catalog.Profile("success.streaming")
	if again.Behavior.Steps[0] == "mutated" {
		t.Fatal("Profile returned mutable catalog storage")
	}
}

func TestExternalProfilesCannotCreateHandlerScenarios(t *testing.T) {
	catalog, err := LoadCatalog()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewScenario(catalog, "transport.dns_failure", ScenarioOptions{}); err == nil {
		t.Fatal("NewScenario() accepted a transport-harness profile")
	}
}

func TestCatalogRejectsSchemaConstrainedProfileValues(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Profile)
	}{
		{name: "detection owner", mutate: func(profile *Profile) { profile.DetectionOwner = "later" }},
		{name: "invalid evidence", mutate: func(profile *Profile) { profile.Evidence = []string{"not evidence"} }},
		{name: "duplicate evidence", mutate: func(profile *Profile) { profile.Evidence = []string{"request.received", "request.received"} }},
		{name: "too much evidence", mutate: func(profile *Profile) { profile.Evidence = repeatedStrings("request.received", 17) }},
		{name: "terminal", mutate: func(profile *Profile) { profile.Expected.Terminal = "unknown" }},
		{name: "provider status", mutate: func(profile *Profile) { profile.Expected.ProviderStatus = 600 }},
		{name: "behavior status", mutate: func(profile *Profile) { profile.Behavior.Status = 99 }},
		{name: "retry after", mutate: func(profile *Profile) { profile.Behavior.RetryAfter = strings.Repeat("x", 129) }},
		{name: "bytes", mutate: func(profile *Profile) { profile.Behavior.Bytes = 16<<20 + 1 }},
		{name: "failure count", mutate: func(profile *Profile) { profile.Behavior.FailuresBeforeSuccess = -1 }},
		{name: "too many steps", mutate: func(profile *Profile) { profile.Behavior.Steps = repeatedStrings("step", 65) }},
		{name: "long step", mutate: func(profile *Profile) { profile.Behavior.Steps = []string{strings.Repeat("x", 65)} }},
		{name: "too many synchronization events", mutate: func(profile *Profile) {
			profile.SynchronizationEvents = []Event{EventRequestReceived, EventResponseHeadersReady, EventResponseChunkReady, EventResponseTerminalReady, EventRequestCancelled, EventRequestReceived}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var document matrixDocument
			if err := json.Unmarshal(embeddedMatrix, &document); err != nil {
				t.Fatal(err)
			}
			test.mutate(&document.Profiles[0])
			encoded, err := json.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := loadCatalog(encoded); err == nil {
				t.Fatal("loadCatalog() accepted a schema-constrained invalid value")
			}
		})
	}
}

func repeatedStrings(value string, count int) []string {
	values := make([]string, count)
	for index := range values {
		values[index] = value
	}
	return values
}
