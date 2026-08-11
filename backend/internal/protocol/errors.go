package protocol

import (
	"fmt"
	"strings"
)

// FailureCode is an append-only stable failure taxonomy identifier.
type FailureCode string

const (
	FailureClientInvalidRequest         FailureCode = "client.invalid_request"
	FailureClientCancelled              FailureCode = "client.cancelled"
	FailureClientDeadlineExceeded       FailureCode = "client.deadline_exceeded"
	FailureAuthMissingCredential        FailureCode = "auth.missing_credential"
	FailureAuthInvalidCredential        FailureCode = "auth.invalid_credential"
	FailureAuthForbidden                FailureCode = "auth.forbidden"
	FailureQuotaGatewayExceeded         FailureCode = "quota.gateway_exceeded"
	FailurePolicyUnknownTarget          FailureCode = "policy.unknown_target"
	FailurePolicyNoEligibleRoute        FailureCode = "policy.no_eligible_route"
	FailurePolicyAllRoutesOpen          FailureCode = "policy.all_routes_open"
	FailureCapabilityUnsupported        FailureCode = "capability.unsupported"
	FailureAffinityRouteIneligible      FailureCode = "affinity.route_ineligible"
	FailureGatewayOverloaded            FailureCode = "gateway.overloaded"
	FailureGatewayInternal              FailureCode = "gateway.internal"
	FailureGatewayShutdown              FailureCode = "gateway.shutdown"
	FailureStorageUnavailable           FailureCode = "storage.unavailable"
	FailureTelemetryExportFailed        FailureCode = "telemetry.export_failed"
	FailureUpstreamDNSFailed            FailureCode = "upstream.dns_failed"
	FailureUpstreamConnectFailed        FailureCode = "upstream.connect_failed"
	FailureUpstreamTLSFailed            FailureCode = "upstream.tls_failed"
	FailureUpstreamTimeout              FailureCode = "upstream.timeout"
	FailureUpstreamStreamStalled        FailureCode = "upstream.stream_stalled"
	FailureUpstreamRedirectRejected     FailureCode = "upstream.redirect_rejected"
	FailureUpstreamResponseTooLarge     FailureCode = "upstream.response_too_large"
	FailureUpstreamAuthenticationFailed FailureCode = "upstream.authentication_failed"
	FailureUpstreamPermissionDenied     FailureCode = "upstream.permission_denied"
	FailureUpstreamRateLimited          FailureCode = "upstream.rate_limited"
	FailureUpstreamServerError          FailureCode = "upstream.server_error"
	FailureUpstreamContentPolicy        FailureCode = "upstream.content_policy"
	FailureUpstreamContextLimit         FailureCode = "upstream.context_limit"
	FailureUpstreamInvalidStatus        FailureCode = "upstream.invalid_status"
	FailureProtocolInvalidJSON          FailureCode = "protocol.invalid_json"
	FailureProtocolInvalidSSE           FailureCode = "protocol.invalid_sse"
	FailureProtocolEarlyEOF             FailureCode = "protocol.early_eof"
	FailureProtocolEmptyOutput          FailureCode = "protocol.empty_output"
	FailureProtocolInvalidEventOrder    FailureCode = "protocol.invalid_event_order"
	FailureProtocolInvalidToolCall      FailureCode = "protocol.invalid_tool_call"
	FailureProtocolInvalidStructured    FailureCode = "protocol.invalid_structured_output"
	FailureProtocolUsageInconsistent    FailureCode = "protocol.usage_inconsistent"
	FailureProtocolParameterIgnored     FailureCode = "protocol.parameter_ignored"
)

// FailureDomain identifies the boundary responsible for classification.
type FailureDomain string

const (
	DomainClient     FailureDomain = "client"
	DomainAuth       FailureDomain = "auth"
	DomainQuota      FailureDomain = "quota"
	DomainPolicy     FailureDomain = "policy"
	DomainCapability FailureDomain = "capability"
	DomainAffinity   FailureDomain = "affinity"
	DomainGateway    FailureDomain = "gateway"
	DomainStorage    FailureDomain = "storage"
	DomainTelemetry  FailureDomain = "telemetry"
	DomainUpstream   FailureDomain = "upstream"
	DomainProtocol   FailureDomain = "protocol"
)

// RetryDisposition describes whether another attempt can ever be considered.
type RetryDisposition string

const (
	RetryNever                    RetryDisposition = "never"
	RetryPreOutputAlternate       RetryDisposition = "pre_output_alternate"
	RetryPreOutputSameOrAlternate RetryDisposition = "pre_output_same_or_alternate"
	RetryClientDecides            RetryDisposition = "client_decides"
)

// ValidationIssue identifies a failed rule without retaining the rejected value.
type ValidationIssue struct {
	Path string
	Rule string
}

// CanonicalError is the safe provider-neutral failure envelope. Error never
// renders Cause or rejected input values.
type CanonicalError struct {
	Code             FailureCode
	Domain           FailureDomain
	RetryDisposition RetryDisposition
	SafeMessage      string
	HTTPStatus       int
	RequestID        string
	AttemptID        string
	RouteID          string
	OutputVisible    bool
	ToolActionable   bool
	ProviderStatus   int
	Validation       *ValidationIssue
	cause            error
}

// Error implements error with bounded, client-safe information only.
func (e *CanonicalError) Error() string {
	if e == nil {
		return ""
	}
	if e.Validation != nil {
		return fmt.Sprintf("%s: %s (%s)", e.Code, e.SafeMessage, e.Validation.Path)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.SafeMessage)
}

// Unwrap exposes the internal cause to trusted in-process diagnostics. Callers
// must not serialize it to clients or metric labels.
func (e *CanonicalError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func invalidRequest(path, rule string) *CanonicalError {
	return &CanonicalError{
		Code:             FailureClientInvalidRequest,
		Domain:           DomainClient,
		RetryDisposition: RetryNever,
		SafeMessage:      "The request is invalid.",
		HTTPStatus:       400,
		Validation:       &ValidationIssue{Path: path, Rule: rule},
	}
}

func protocolFailure(code FailureCode, safeMessage, path, rule string, outputVisible, toolActionable bool) *CanonicalError {
	retry := RetryPreOutputAlternate
	if outputVisible || toolActionable {
		retry = RetryClientDecides
	}
	domain, status := failureEnvelopeMetadata(code)
	return &CanonicalError{
		Code:             code,
		Domain:           domain,
		RetryDisposition: retry,
		SafeMessage:      safeMessage,
		HTTPStatus:       status,
		OutputVisible:    outputVisible,
		ToolActionable:   toolActionable,
		Validation:       &ValidationIssue{Path: path, Rule: rule},
	}
}

func failureEnvelopeMetadata(code FailureCode) (FailureDomain, int) {
	switch code {
	case FailureClientCancelled:
		return DomainClient, 499
	case FailureClientDeadlineExceeded:
		return DomainClient, 408
	case FailureAuthMissingCredential, FailureAuthInvalidCredential:
		return DomainAuth, 401
	case FailureAuthForbidden:
		return DomainAuth, 403
	case FailureQuotaGatewayExceeded:
		return DomainQuota, 429
	case FailurePolicyUnknownTarget:
		return DomainPolicy, 404
	case FailurePolicyNoEligibleRoute, FailurePolicyAllRoutesOpen:
		return DomainPolicy, 503
	case FailureCapabilityUnsupported:
		return DomainCapability, 400
	case FailureAffinityRouteIneligible:
		return DomainAffinity, 409
	case FailureGatewayOverloaded, FailureGatewayShutdown:
		return DomainGateway, 503
	case FailureGatewayInternal:
		return DomainGateway, 500
	case FailureStorageUnavailable:
		return DomainStorage, 503
	case FailureTelemetryExportFailed:
		return DomainTelemetry, 500
	}
	prefix, _, _ := strings.Cut(string(code), ".")
	switch FailureDomain(prefix) {
	case DomainClient:
		return DomainClient, 400
	case DomainUpstream:
		return DomainUpstream, 502
	case DomainProtocol:
		return DomainProtocol, 502
	default:
		return DomainGateway, 500
	}
}
