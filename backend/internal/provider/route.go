package provider

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/NatsumeRyuhane/LLM-Gateway/backend/internal/protocol"
)

const maxRouteIdentifierBytes = 128

// AdapterLimits bounds all provider-controlled response parsing. Request and
// canonical output bounds remain owned by protocol.ValidatedChatRequest.
type AdapterLimits struct {
	MaxResponseBodyBytes int64
	MaxErrorBodyBytes    int64
	MaxSSELineBytes      int
	MaxSSEEventBytes     int
}

// DefaultAdapterLimits returns conservative per-attempt parsing bounds.
func DefaultAdapterLimits() AdapterLimits {
	return AdapterLimits{
		MaxResponseBodyBytes: 8 << 20,
		MaxErrorBodyBytes:    8 << 10,
		MaxSSELineBytes:      1 << 20,
		MaxSSEEventBytes:     2 << 20,
	}
}

func (l AdapterLimits) validate() error {
	if l.MaxResponseBodyBytes <= 0 || l.MaxErrorBodyBytes <= 0 || l.MaxSSELineBytes <= 0 || l.MaxSSEEventBytes <= 0 {
		return errors.New("provider route limits must be positive")
	}
	if int64(l.MaxSSEEventBytes) > l.MaxResponseBodyBytes || l.MaxSSELineBytes > l.MaxSSEEventBytes {
		return errors.New("provider route SSE limits must fit within the response bound")
	}
	return nil
}

// Credential is route-bound provider authentication material. Its value is
// never exposed; only the validated placement operation is public.
type Credential struct {
	bearer string
}

// String implements fmt.Stringer without revealing provider material.
func (Credential) String() string { return "[REDACTED]" }

// GoString implements fmt.GoStringer without revealing provider material.
func (Credential) GoString() string { return "provider.Credential{[REDACTED]}" }

// NewBearerCredential validates an OpenAI-compatible bearer credential.
func NewBearerCredential(secret string) (Credential, error) {
	if secret == "" || !utf8.ValidString(secret) || containsControl(secret) {
		return Credential{}, errors.New("provider credential must be non-empty bounded text")
	}
	if len(secret) > 16<<10 {
		return Credential{}, errors.New("provider credential exceeds the configured bound")
	}
	return Credential{bearer: secret}, nil
}

// Apply adds only the route-owned provider credential header.
func (c Credential) Apply(header http.Header) error {
	if c.bearer == "" {
		return errors.New("provider credential is not configured")
	}
	header.Set("Authorization", "Bearer "+c.bearer)
	return nil
}

// Route is configuration input resolved by the route/credential owner before
// it reaches an adapter.
type Route struct {
	ID                    string
	Endpoint              string
	UpstreamModel         string
	Credential            Credential
	Capabilities          protocol.RouteCapabilities
	Limits                AdapterLimits
	AllowInsecureLoopback bool
}

// ValidatedRoute is an immutable adapter input.
type ValidatedRoute struct {
	id            string
	endpoint      url.URL
	upstreamModel string
	credential    Credential
	capabilities  protocol.RouteCapabilities
	limits        AdapterLimits
}

// ValidateRoute parses and snapshots one exact OpenAI-compatible route.
func ValidateRoute(route Route) (ValidatedRoute, error) {
	if !validIdentifier(route.ID, maxRouteIdentifierBytes) {
		return ValidatedRoute{}, errors.New("provider route ID is invalid")
	}
	if !validIdentifier(route.UpstreamModel, 256) {
		return ValidatedRoute{}, errors.New("provider upstream model is invalid")
	}
	if route.Credential.bearer == "" {
		return ValidatedRoute{}, errors.New("provider route credential is missing")
	}
	if err := route.Capabilities.Validate(); err != nil {
		return ValidatedRoute{}, fmt.Errorf("provider route capabilities are invalid: %w", err)
	}
	if err := route.Limits.validate(); err != nil {
		return ValidatedRoute{}, err
	}
	endpoint, err := url.Parse(route.Endpoint)
	if err != nil || endpoint == nil {
		return ValidatedRoute{}, errors.New("provider route endpoint is invalid")
	}
	if err := validateEndpoint(*endpoint, route.AllowInsecureLoopback); err != nil {
		return ValidatedRoute{}, err
	}

	claims := make(map[protocol.Capability]protocol.CapabilityClaim, len(route.Capabilities.Claims))
	for capability, claim := range route.Capabilities.Claims {
		claims[capability] = claim
	}
	return ValidatedRoute{
		id:            route.ID,
		endpoint:      *endpoint,
		upstreamModel: route.UpstreamModel,
		credential:    route.Credential,
		capabilities:  protocol.RouteCapabilities{Claims: claims},
		limits:        route.Limits,
	}, nil
}

// ID returns the stable non-secret route identifier.
func (r ValidatedRoute) ID() string { return r.id }

// UpstreamModel returns the provider-facing model identifier.
func (r ValidatedRoute) UpstreamModel() string { return r.upstreamModel }

// Limits returns the validated response parsing bounds.
func (r ValidatedRoute) Limits() AdapterLimits { return r.limits }

// Endpoint returns a copy of the exact admitted endpoint.
func (r ValidatedRoute) Endpoint() url.URL { return r.endpoint }

// ApplyCredential places only this route's bound provider credential.
func (r ValidatedRoute) ApplyCredential(header http.Header) error {
	return r.credential.Apply(header)
}

// CheckRequest rejects unsupported required semantics before dispatch.
func (r ValidatedRoute) CheckRequest(request protocol.ValidatedChatRequest) *protocol.CanonicalError {
	if r.id == "" || r.upstreamModel == "" || r.endpoint.Host == "" {
		return gatewayFailure("The provider route is invalid.", "route", "must be validated before dispatch")
	}
	missing := r.capabilities.Missing(request.RequiredCapabilities())
	if len(missing) == 0 {
		return nil
	}
	return &protocol.CanonicalError{
		Code:             protocol.FailureCapabilityUnsupported,
		Domain:           protocol.DomainCapability,
		RetryDisposition: protocol.RetryNever,
		SafeMessage:      "The selected provider route does not support the request.",
		HTTPStatus:       http.StatusBadRequest,
		RouteID:          r.id,
		Validation: &protocol.ValidationIssue{
			Path: "required_capabilities",
			Rule: "selected route is missing required canonical capabilities",
		},
	}
}

func validateEndpoint(endpoint url.URL, allowInsecureLoopback bool) error {
	if endpoint.Opaque != "" || endpoint.User != nil || endpoint.Fragment != "" || endpoint.RawQuery != "" || endpoint.Host == "" {
		return errors.New("provider route endpoint must be an absolute URL without userinfo, query, or fragment")
	}
	if endpoint.Scheme == "https" {
		return nil
	}
	if endpoint.Scheme != "http" || !allowInsecureLoopback || !loopbackHost(endpoint.Hostname()) {
		return errors.New("provider route endpoint must use HTTPS")
	}
	return nil
}

func loopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func validIdentifier(value string, maximum int) bool {
	return value != "" && len(value) <= maximum && utf8.ValidString(value) && !containsControl(value)
}

func containsControl(value string) bool {
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return true
		}
	}
	return false
}
