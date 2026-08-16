package config

import (
	"fmt"
	"strconv"
	"time"
)

// MockProvider contains startup-only deterministic scenario settings.
type MockProvider struct {
	Profile   string
	Seed      int64
	StepDelay time.Duration
}

// MockProviderDefaults returns the standalone provider's safe local defaults.
func MockProviderDefaults() MockProvider {
	return MockProvider{Profile: "success.buffered", Seed: 1, StepDelay: 250 * time.Millisecond}
}

// LoadMockProvider applies MOCK_PROVIDER_* startup-only scenario overrides.
func LoadMockProvider(defaults MockProvider, lookup LookupEnv) (MockProvider, error) {
	if lookup == nil {
		return MockProvider{}, fmt.Errorf("MOCK_PROVIDER configuration source is required")
	}
	configured := defaults
	if value, ok := lookup("MOCK_PROVIDER_PROFILE"); ok {
		if !validProfileID(value) {
			return MockProvider{}, fmt.Errorf("MOCK_PROVIDER_PROFILE must be a bounded profile identifier")
		}
		configured.Profile = value
	}
	if value, ok := lookup("MOCK_PROVIDER_SEED"); ok {
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil || parsed == 0 {
			return MockProvider{}, fmt.Errorf("MOCK_PROVIDER_SEED must be a non-zero signed 64-bit integer")
		}
		configured.Seed = parsed
	}
	if value, ok := lookup("MOCK_PROVIDER_STEP_DELAY"); ok {
		parsed, err := time.ParseDuration(value)
		if err != nil || parsed < 0 || parsed > 30*time.Second {
			return MockProvider{}, fmt.Errorf("MOCK_PROVIDER_STEP_DELAY must be a Go duration from 0s through 30s")
		}
		configured.StepDelay = parsed
	}
	if !validProfileID(configured.Profile) || configured.Seed == 0 || configured.StepDelay < 0 || configured.StepDelay > 30*time.Second {
		return MockProvider{}, fmt.Errorf("MOCK_PROVIDER defaults are invalid")
	}
	return configured, nil
}

func validProfileID(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '.' && character != '_' && character != '-' {
			return false
		}
	}
	return true
}
