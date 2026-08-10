package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoadHTTPUsesDefaults(t *testing.T) {
	t.Parallel()

	want := GatewayHTTPDefaults()
	got, err := LoadHTTP("GATEWAY", want, mapLookup(nil))
	if err != nil {
		t.Fatalf("LoadHTTP() error = %v", err)
	}
	if got != want {
		t.Fatalf("LoadHTTP() = %#v, want %#v", got, want)
	}
}

func TestLoadHTTPAppliesOverrides(t *testing.T) {
	t.Parallel()

	got, err := LoadHTTP("GATEWAY", GatewayHTTPDefaults(), mapLookup(map[string]string{
		"GATEWAY_HTTP_ADDR":                "0.0.0.0:9090",
		"GATEWAY_HTTP_READ_HEADER_TIMEOUT": "2s",
		"GATEWAY_HTTP_READ_TIMEOUT":        "3s",
		"GATEWAY_HTTP_WRITE_TIMEOUT":       "4s",
		"GATEWAY_HTTP_IDLE_TIMEOUT":        "5s",
		"GATEWAY_HTTP_SHUTDOWN_TIMEOUT":    "6s",
	}))
	if err != nil {
		t.Fatalf("LoadHTTP() error = %v", err)
	}

	if got.Address != "0.0.0.0:9090" {
		t.Errorf("Address = %q, want %q", got.Address, "0.0.0.0:9090")
	}
	if got.ReadHeaderTimeout != 2*time.Second {
		t.Errorf("ReadHeaderTimeout = %v, want 2s", got.ReadHeaderTimeout)
	}
	if got.ReadTimeout != 3*time.Second {
		t.Errorf("ReadTimeout = %v, want 3s", got.ReadTimeout)
	}
	if got.WriteTimeout != 4*time.Second {
		t.Errorf("WriteTimeout = %v, want 4s", got.WriteTimeout)
	}
	if got.IdleTimeout != 5*time.Second {
		t.Errorf("IdleTimeout = %v, want 5s", got.IdleTimeout)
	}
	if got.ShutdownTimeout != 6*time.Second {
		t.Errorf("ShutdownTimeout = %v, want 6s", got.ShutdownTimeout)
	}
}

func TestLoadHTTPRejectsInvalidValuesWithoutEchoingThem(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		key   string
		value string
	}{
		{
			name:  "address",
			key:   "GATEWAY_HTTP_ADDR",
			value: "https://user:do-not-log-me@example.com",
		},
		{
			name:  "duration",
			key:   "GATEWAY_HTTP_READ_TIMEOUT",
			value: "do-not-log-me",
		},
		{
			name:  "non-positive duration",
			key:   "GATEWAY_HTTP_IDLE_TIMEOUT",
			value: "0s",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, err := LoadHTTP("GATEWAY", GatewayHTTPDefaults(), mapLookup(map[string]string{
				test.key: test.value,
			}))
			if err == nil {
				t.Fatal("LoadHTTP() error = nil, want validation error")
			}
			if !strings.Contains(err.Error(), test.key) {
				t.Errorf("error %q does not identify key %q", err, test.key)
			}
			if strings.Contains(err.Error(), test.value) {
				t.Errorf("error %q exposes rejected value", err)
			}
		})
	}
}

func mapLookup(values map[string]string) LookupEnv {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}
