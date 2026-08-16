package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoadMockProviderDefaultsAndOverrides(t *testing.T) {
	t.Parallel()
	defaults := MockProviderDefaults()
	got, err := LoadMockProvider(defaults, mapLookup(nil))
	if err != nil || got != defaults {
		t.Fatalf("LoadMockProvider(defaults) = %#v, %v", got, err)
	}
	got, err = LoadMockProvider(defaults, mapLookup(map[string]string{
		"MOCK_PROVIDER_PROFILE":    "timing.slow_first_token",
		"MOCK_PROVIDER_SEED":       "42",
		"MOCK_PROVIDER_STEP_DELAY": "750ms",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if got.Profile != "timing.slow_first_token" || got.Seed != 42 || got.StepDelay != 750*time.Millisecond {
		t.Fatalf("configured = %#v", got)
	}
}

func TestLoadMockProviderRejectsInvalidValuesWithoutEchoingThem(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		key   string
		value string
	}{
		{"MOCK_PROVIDER_PROFILE", "Do Not Echo"},
		{"MOCK_PROVIDER_SEED", "not-a-seed"},
		{"MOCK_PROVIDER_STEP_DELAY", "31s-secret"},
	} {
		t.Run(test.key, func(t *testing.T) {
			t.Parallel()
			_, err := LoadMockProvider(MockProviderDefaults(), mapLookup(map[string]string{test.key: test.value}))
			if err == nil || !strings.Contains(err.Error(), test.key) || strings.Contains(err.Error(), test.value) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}
