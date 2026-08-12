package openai

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/NatsumeRyuhane/LLM-Gateway/backend/internal/protocol"
)

// CorrelationVisibility is an authorization decision made outside the codec.
// Request IDs are always public; attempt and route IDs require explicit grants.
type CorrelationVisibility struct {
	AttemptID bool
	RouteID   bool
}

// EncodedResponse is a complete, deterministic HTTP response representation.
type EncodedResponse struct {
	Status int
	Header http.Header
	Body   []byte
}

// Model is the public, authorization-filtered model metadata accepted by the
// models-list encoder. Provider endpoints and credential data cannot enter it.
type Model struct {
	ID        string
	CreatedAt time.Time
	OwnedBy   string
}

type modelsEnvelope struct {
	Object string       `json:"object"`
	Data   []modelEntry `json:"data"`
}

type modelEntry struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

// EncodeModelsResponse serializes an already authorization-filtered model set.
func (c Codec) EncodeModelsResponse(requestID string, models []Model) (EncodedResponse, *protocol.CanonicalError) {
	if !validPublicIdentifier(requestID, c.limits.MaxIdentifierBytes) {
		return EncodedResponse{}, invalidRequest("request_id", "must be a bounded public identifier")
	}
	entries := make([]modelEntry, len(models))
	for index, model := range models {
		path := formatArrayPath("models", index)
		if !validPublicIdentifier(model.ID, c.limits.MaxTargetBytes) {
			return EncodedResponse{}, invalidRequest(path+".id", "must be a bounded public identifier")
		}
		if model.CreatedAt.IsZero() {
			return EncodedResponse{}, invalidRequest(path+".created_at", "must be set")
		}
		if !validPublicIdentifier(model.OwnedBy, c.limits.MaxIdentifierBytes) {
			return EncodedResponse{}, invalidRequest(path+".owned_by", "must be a bounded public identifier")
		}
		entries[index] = modelEntry{ID: model.ID, Object: "model", Created: model.CreatedAt.Unix(), OwnedBy: model.OwnedBy}
	}
	body, marshalErr := json.Marshal(modelsEnvelope{Object: "list", Data: entries})
	if marshalErr != nil {
		return EncodedResponse{}, encodingFailure(requestID)
	}
	return EncodedResponse{
		Status: http.StatusOK,
		Header: c.responseHeaders(MediaTypeJSON, requestID, "", "", CorrelationVisibility{}),
		Body:   body,
	}, nil
}

type chatCompletionEnvelope struct {
	ID      string                 `json:"id"`
	Object  string                 `json:"object"`
	Created int64                  `json:"created"`
	Model   string                 `json:"model"`
	Choices []chatCompletionChoice `json:"choices"`
	Usage   *usageEnvelope         `json:"usage,omitempty"`
}

type chatCompletionChoice struct {
	Index        int              `json:"index"`
	Message      assistantMessage `json:"message"`
	FinishReason string           `json:"finish_reason"`
}

type assistantMessage struct {
	Role      string         `json:"role"`
	Content   *string        `json:"content"`
	Refusal   *string        `json:"refusal,omitempty"`
	ToolCalls []wireToolCall `json:"tool_calls,omitempty"`
}

type wireToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function wireFunctionCall `json:"function"`
}

type wireFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type usageEnvelope struct {
	PromptTokens            int64                   `json:"prompt_tokens"`
	CompletionTokens        int64                   `json:"completion_tokens"`
	TotalTokens             int64                   `json:"total_tokens"`
	PromptTokensDetails     *promptTokenDetails     `json:"prompt_tokens_details,omitempty"`
	CompletionTokensDetails *completionTokenDetails `json:"completion_tokens_details,omitempty"`
}

type promptTokenDetails struct {
	CachedTokens int64 `json:"cached_tokens"`
}

type completionTokenDetails struct {
	ReasoningTokens int64 `json:"reasoning_tokens"`
}

// EncodeBufferedChatCompletion serializes a fully validated canonical result.
// Refusal, content, tools, usage, route evidence, and attempt evidence remain
// separate public fields or headers.
func (c Codec) EncodeBufferedChatCompletion(response protocol.ValidatedChatResponse, visibility CorrelationVisibility) (EncodedResponse, *protocol.CanonicalError) {
	canonical := response.Canonical()
	message := assistantMessage{Role: "assistant"}
	if len(canonical.Message.Content) != 0 {
		content := joinCanonicalContent(canonical.Message.Content)
		message.Content = &content
	}
	if refusal, present := canonical.Message.Refusal.Get(); present {
		message.Refusal = &refusal
	}
	if len(canonical.Message.ToolCalls) != 0 {
		message.ToolCalls = make([]wireToolCall, len(canonical.Message.ToolCalls))
		for index, call := range canonical.Message.ToolCalls {
			message.ToolCalls[index] = wireToolCall{
				ID: call.ID, Type: "function",
				Function: wireFunctionCall{Name: call.Name, Arguments: call.Arguments},
			}
		}
	}
	envelope := chatCompletionEnvelope{
		ID: canonical.ResponseID, Object: "chat.completion", Created: canonical.CreatedAt.Unix(), Model: canonical.Model,
		Choices: []chatCompletionChoice{{Index: 0, Message: message, FinishReason: string(canonical.FinishReason)}},
	}
	if usage, present := canonical.Usage.Get(); present {
		envelope.Usage = encodeUsage(usage)
	}
	body, marshalErr := json.Marshal(envelope)
	if marshalErr != nil {
		return EncodedResponse{}, encodingFailure(canonical.RequestID)
	}
	return EncodedResponse{
		Status: http.StatusOK,
		Header: c.responseHeaders(MediaTypeJSON, canonical.RequestID, canonical.AttemptID, canonical.RouteID, visibility),
		Body:   body,
	}, nil
}

type publicErrorEnvelope struct {
	Error     publicError `json:"error"`
	RequestID string      `json:"request_id,omitempty"`
}

type publicError struct {
	Message string  `json:"message"`
	Type    string  `json:"type"`
	Code    string  `json:"code"`
	Param   *string `json:"param"`
}

// EncodeError serializes only bounded safe canonical fields. Internal causes,
// provider status/body data, endpoints, and credentials are never included.
func (c Codec) EncodeError(failure *protocol.CanonicalError, visibility CorrelationVisibility) EncodedResponse {
	if failure == nil {
		failure = encodingFailure("")
	}
	code := failure.Code
	domain := failure.Domain
	status := failure.HTTPStatus
	if !knownPublicFailureCode(code) {
		code = protocol.FailureGatewayInternal
		domain = protocol.DomainGateway
		status = http.StatusInternalServerError
	}
	if status < 400 || status > 599 {
		status = http.StatusInternalServerError
	}
	message := failure.SafeMessage
	if message == "" || !utf8.ValidString(message) || len(message) > c.limits.MaxSafeErrorBytes || containsUnsafeControl(message) {
		message = "The gateway could not complete the request."
	}
	var parameter *string
	if failure.Validation != nil && c.validErrorParameter(failure.Validation.Path) {
		value := failure.Validation.Path
		parameter = &value
	}
	requestID := ""
	if validPublicIdentifier(failure.RequestID, c.limits.MaxIdentifierBytes) {
		requestID = failure.RequestID
	}
	body, err := json.Marshal(publicErrorEnvelope{
		Error:     publicError{Message: message, Type: publicErrorType(domain), Code: string(code), Param: parameter},
		RequestID: requestID,
	})
	if err != nil {
		body = []byte(`{"error":{"message":"The gateway could not complete the request.","type":"server_error","code":"gateway.internal","param":null}}`)
		status = http.StatusInternalServerError
	}
	return EncodedResponse{
		Status: status,
		Header: c.responseHeaders(MediaTypeJSON, requestID, failure.AttemptID, failure.RouteID, visibility),
		Body:   body,
	}
}

func encodeUsage(usage protocol.CanonicalUsage) *usageEnvelope {
	result := &usageEnvelope{PromptTokens: usage.InputTokens, CompletionTokens: usage.OutputTokens, TotalTokens: usage.TotalTokens}
	if cached, present := usage.CachedTokens.Get(); present {
		result.PromptTokensDetails = &promptTokenDetails{CachedTokens: cached}
	}
	if reasoning, present := usage.ReasoningTokens.Get(); present {
		result.CompletionTokensDetails = &completionTokenDetails{ReasoningTokens: reasoning}
	}
	return result
}

func (c Codec) responseHeaders(contentType, requestID, attemptID, routeID string, visibility CorrelationVisibility) http.Header {
	header := make(http.Header)
	header.Set("Content-Type", contentType)
	if validPublicIdentifier(requestID, c.limits.MaxIdentifierBytes) {
		header.Set(HeaderRequestID, requestID)
	}
	if visibility.AttemptID && validPublicIdentifier(attemptID, c.limits.MaxIdentifierBytes) {
		header.Set(HeaderAttemptID, attemptID)
	}
	if visibility.RouteID && validPublicIdentifier(routeID, c.limits.MaxIdentifierBytes) {
		header.Set(HeaderRouteID, routeID)
	}
	return header
}

func joinCanonicalContent(parts []protocol.CanonicalContentPart) string {
	var result strings.Builder
	for _, part := range parts {
		result.WriteString(part.Text)
	}
	return result.String()
}

func publicErrorType(domain protocol.FailureDomain) string {
	switch domain {
	case protocol.DomainClient, protocol.DomainCapability, protocol.DomainPolicy, protocol.DomainAffinity:
		return "invalid_request_error"
	case protocol.DomainAuth:
		return "authentication_error"
	case protocol.DomainQuota:
		return "rate_limit_error"
	case protocol.DomainUpstream, protocol.DomainProtocol:
		return "upstream_error"
	default:
		return "server_error"
	}
}

func (c Codec) validErrorParameter(value string) bool {
	return validPublicIdentifier(value, c.limits.MaxIdentifierBytes)
}

func validPublicIdentifier(value string, maximum int) bool {
	return value != "" && len(value) <= maximum && utf8.ValidString(value) && !containsUnsafeControl(value)
}

func containsUnsafeControl(value string) bool {
	for _, character := range value {
		if character < 0x20 || (character >= 0x7f && character <= 0x9f) {
			return true
		}
	}
	return false
}

func encodingFailure(requestID string) *protocol.CanonicalError {
	return &protocol.CanonicalError{
		Code: protocol.FailureGatewayInternal, Domain: protocol.DomainGateway,
		RetryDisposition: protocol.RetryNever, SafeMessage: "The gateway could not encode the response.",
		HTTPStatus: http.StatusInternalServerError, RequestID: requestID,
	}
}

func knownPublicFailureCode(code protocol.FailureCode) bool {
	switch code {
	case protocol.FailureClientInvalidRequest, protocol.FailureClientCancelled, protocol.FailureClientDeadlineExceeded,
		protocol.FailureAuthMissingCredential, protocol.FailureAuthInvalidCredential, protocol.FailureAuthForbidden,
		protocol.FailureQuotaGatewayExceeded, protocol.FailurePolicyUnknownTarget, protocol.FailurePolicyNoEligibleRoute,
		protocol.FailurePolicyAllRoutesOpen, protocol.FailureCapabilityUnsupported, protocol.FailureAffinityRouteIneligible,
		protocol.FailureGatewayOverloaded, protocol.FailureGatewayInternal, protocol.FailureGatewayShutdown,
		protocol.FailureStorageUnavailable, protocol.FailureTelemetryExportFailed, protocol.FailureUpstreamDNSFailed,
		protocol.FailureUpstreamConnectFailed, protocol.FailureUpstreamTLSFailed, protocol.FailureUpstreamTimeout,
		protocol.FailureUpstreamStreamStalled, protocol.FailureUpstreamRedirectRejected, protocol.FailureUpstreamResponseTooLarge,
		protocol.FailureUpstreamAuthenticationFailed, protocol.FailureUpstreamPermissionDenied, protocol.FailureUpstreamRateLimited,
		protocol.FailureUpstreamServerError, protocol.FailureUpstreamContentPolicy, protocol.FailureUpstreamContextLimit,
		protocol.FailureUpstreamInvalidStatus, protocol.FailureProtocolInvalidJSON, protocol.FailureProtocolInvalidSSE,
		protocol.FailureProtocolEarlyEOF, protocol.FailureProtocolEmptyOutput, protocol.FailureProtocolInvalidEventOrder,
		protocol.FailureProtocolInvalidToolCall, protocol.FailureProtocolInvalidStructured,
		protocol.FailureProtocolUsageInconsistent, protocol.FailureProtocolParameterIgnored:
		return true
	default:
		return false
	}
}
