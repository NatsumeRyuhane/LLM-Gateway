package config

import (
	"fmt"
	"net"
	"strconv"
	"time"
)

// LookupEnv reads one environment variable without exposing the process-wide
// environment to tests.
type LookupEnv func(string) (string, bool)

// HTTPServer contains the bounded lifecycle settings shared by HTTP processes.
type HTTPServer struct {
	Address           string
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	ShutdownTimeout   time.Duration
}

// GatewayHTTPDefaults returns the conservative initial gateway listener
// settings. The loopback default prevents an unconfigured process from being
// exposed beyond its host.
func GatewayHTTPDefaults() HTTPServer {
	return HTTPServer{
		Address:           "127.0.0.1:8080",
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		ShutdownTimeout:   10 * time.Second,
	}
}

// MockProviderHTTPDefaults returns the initial mock-provider listener settings.
func MockProviderHTTPDefaults() HTTPServer {
	defaults := GatewayHTTPDefaults()
	defaults.Address = "127.0.0.1:8081"
	return defaults
}

// LoadHTTP applies PREFIX_HTTP_* environment overrides to defaults. Validation
// errors identify the setting but deliberately never repeat its value, because
// future configuration sources may contain credentials or endpoint secrets.
func LoadHTTP(prefix string, defaults HTTPServer, lookup LookupEnv) (HTTPServer, error) {
	if lookup == nil {
		return HTTPServer{}, fmt.Errorf("%s configuration source is required", prefix)
	}

	configured := defaults
	addressKey := prefix + "_HTTP_ADDR"
	if value, ok := lookup(addressKey); ok {
		configured.Address = value
	}

	durations := []struct {
		key    string
		target *time.Duration
	}{
		{prefix + "_HTTP_READ_HEADER_TIMEOUT", &configured.ReadHeaderTimeout},
		{prefix + "_HTTP_READ_TIMEOUT", &configured.ReadTimeout},
		{prefix + "_HTTP_WRITE_TIMEOUT", &configured.WriteTimeout},
		{prefix + "_HTTP_IDLE_TIMEOUT", &configured.IdleTimeout},
		{prefix + "_HTTP_SHUTDOWN_TIMEOUT", &configured.ShutdownTimeout},
	}

	for _, setting := range durations {
		value, ok := lookup(setting.key)
		if !ok {
			continue
		}

		parsed, err := time.ParseDuration(value)
		if err != nil || parsed <= 0 {
			return HTTPServer{}, fmt.Errorf("%s must be a positive Go duration such as 5s", setting.key)
		}
		*setting.target = parsed
	}

	if err := validateAddress(addressKey, configured.Address); err != nil {
		return HTTPServer{}, err
	}
	for _, setting := range durations {
		if *setting.target <= 0 {
			return HTTPServer{}, fmt.Errorf("%s must be greater than zero", setting.key)
		}
	}

	return configured, nil
}

func validateAddress(key, address string) error {
	_, port, err := net.SplitHostPort(address)
	if err != nil || port == "" {
		return fmt.Errorf("%s must use host:port form", key)
	}

	if _, err := strconv.ParseUint(port, 10, 16); err != nil {
		return fmt.Errorf("%s must contain a numeric port from 0 through 65535", key)
	}

	return nil
}
