package mockprovider

import "testing"

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
