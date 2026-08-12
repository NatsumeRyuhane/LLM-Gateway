package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/NatsumeRyuhane/LLM-Gateway/backend/internal/protocol"
	"github.com/NatsumeRyuhane/LLM-Gateway/backend/internal/provider"
)

// Buffered dispatches and completely validates one buffered generation.
func (a *Adapter) Buffered(ctx context.Context, attempt provider.Attempt, request protocol.ValidatedChatRequest, route provider.ValidatedRoute) (protocol.ValidatedChatResponse, *protocol.CanonicalError) {
	if failure := preflight(attempt, request, route, false); failure != nil {
		return protocol.ValidatedChatResponse{}, attachAttempt(failure, request, attempt, route)
	}
	body, err := json.Marshal(translateRequest(request, route.UpstreamModel()))
	if err != nil {
		return protocol.ValidatedChatResponse{}, attachAttempt(failure(protocol.FailureGatewayInternal, protocol.DomainGateway, protocol.RetryNever, http.StatusInternalServerError, "The gateway could not encode the upstream request."), request, attempt, route)
	}

	requestCtx, cancel := requestContext(ctx, request)
	defer cancel()
	endpoint := route.Endpoint()
	outbound, err := http.NewRequestWithContext(requestCtx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return protocol.ValidatedChatResponse{}, attachAttempt(failure(protocol.FailureGatewayInternal, protocol.DomainGateway, protocol.RetryNever, http.StatusInternalServerError, "The gateway could not create the upstream request."), request, attempt, route)
	}
	setOutboundHeaders(outbound.Header)
	if err := route.ApplyCredential(outbound.Header); err != nil {
		return protocol.ValidatedChatResponse{}, attachAttempt(failure(protocol.FailureGatewayInternal, protocol.DomainGateway, protocol.RetryNever, http.StatusInternalServerError, "The provider route credential is unavailable."), request, attempt, route)
	}

	response, err := a.client.Do(outbound)
	if err != nil {
		return protocol.ValidatedChatResponse{}, attachAttempt(classifyTransport(err), request, attempt, route)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return protocol.ValidatedChatResponse{}, attachAttempt(classifyStatus(response.StatusCode), request, attempt, route)
	}
	if !jsonContentType(response.Header.Get("Content-Type")) {
		return protocol.ValidatedChatResponse{}, attachAttempt(protocolFailure(protocol.FailureProtocolInvalidJSON, "The upstream response is invalid JSON.", "response.content_type", "must be application/json"), request, attempt, route)
	}

	var wire chatResponse
	if err := decodeBounded(response.Body, route.Limits().MaxResponseBodyBytes, &wire); err != nil {
		if requestCtx.Err() != nil {
			return protocol.ValidatedChatResponse{}, attachAttempt(classifyTransport(requestCtx.Err()), request, attempt, route)
		}
		code := protocol.FailureProtocolInvalidJSON
		if errors.Is(err, errResponseTooLarge) {
			code = protocol.FailureUpstreamResponseTooLarge
		}
		return protocol.ValidatedChatResponse{}, attachAttempt(protocolFailure(code, "The upstream response is invalid.", "response.body", "must be one bounded JSON object"), request, attempt, route)
	}
	if wire.Object != "chat.completion" || len(wire.Choices) != 1 || wire.Choices[0].Index != 0 || wire.Choices[0].FinishReason == nil {
		return protocol.ValidatedChatResponse{}, attachAttempt(protocolFailure(protocol.FailureProtocolInvalidJSON, "The upstream response is invalid.", "response", "must contain exactly one complete Chat Completions choice"), request, attempt, route)
	}
	canonical := protocol.CanonicalChatResponse{
		ResponseID: wire.ID, RequestID: request.Canonical().RequestID, AttemptID: attempt.ID, RouteID: route.ID(),
		Model: request.Canonical().Target, CreatedAt: time.Unix(wire.Created, 0).UTC(),
		Message: translateMessage(wire.Choices[0].Message), FinishReason: finishReason(*wire.Choices[0].FinishReason),
	}
	if wire.Usage != nil {
		canonical.Usage = protocol.Some(translateUsage(*wire.Usage, false))
	}
	validated, validationFailure := protocol.ValidateChatResponse(canonical, request)
	return validated, attachAttempt(validationFailure, request, attempt, route)
}

var errResponseTooLarge = errors.New("provider response exceeds limit")

func decodeBounded(reader io.Reader, limit int64, destination any) error {
	encoded, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return err
	}
	if int64(len(encoded)) > limit {
		return errResponseTooLarge
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return fmt.Errorf("response contains trailing data")
	}
	return nil
}

func preflight(attempt provider.Attempt, request protocol.ValidatedChatRequest, route provider.ValidatedRoute, stream bool) *protocol.CanonicalError {
	canonical := request.Canonical()
	if attempt.ID == "" || len(attempt.ID) > request.Limits().MaxIdentifierBytes {
		return failure(protocol.FailureGatewayInternal, protocol.DomainGateway, protocol.RetryNever, http.StatusInternalServerError, "The upstream attempt is invalid.")
	}
	if canonical.Stream != stream {
		return failure(protocol.FailureClientInvalidRequest, protocol.DomainClient, protocol.RetryNever, http.StatusBadRequest, "The request mode does not match the adapter operation.")
	}
	return route.CheckRequest(request)
}

func setOutboundHeaders(header http.Header) {
	header.Set("Accept", "application/json")
	header.Set("Content-Type", "application/json")
	header.Set("User-Agent", userAgent)
}

func jsonContentType(value string) bool {
	mediaType, _, _ := strings.Cut(value, ";")
	return strings.EqualFold(strings.TrimSpace(mediaType), "application/json")
}

func classifyStatus(status int) *protocol.CanonicalError {
	var code protocol.FailureCode
	var retry protocol.RetryDisposition
	var message string
	switch status {
	case http.StatusUnauthorized:
		code, retry, message = protocol.FailureUpstreamAuthenticationFailed, protocol.RetryPreOutputAlternate, "The upstream provider rejected its credential."
	case http.StatusForbidden:
		code, retry, message = protocol.FailureUpstreamPermissionDenied, protocol.RetryPreOutputAlternate, "The upstream provider denied access."
	case http.StatusTooManyRequests:
		code, retry, message = protocol.FailureUpstreamRateLimited, protocol.RetryPreOutputAlternate, "The upstream provider is rate limited."
	case http.StatusRequestEntityTooLarge:
		code, retry, message = protocol.FailureUpstreamContextLimit, protocol.RetryPreOutputAlternate, "The upstream provider rejected the request size."
	default:
		if status >= 500 && status <= 599 {
			code, retry, message = protocol.FailureUpstreamServerError, protocol.RetryPreOutputSameOrAlternate, "The upstream provider failed."
		} else {
			code, retry, message = protocol.FailureUpstreamInvalidStatus, protocol.RetryPreOutputAlternate, "The upstream provider returned an invalid status."
		}
	}
	failure := failure(code, protocol.DomainUpstream, retry, http.StatusBadGateway, message)
	failure.ProviderStatus = status
	return failure
}

func protocolFailure(code protocol.FailureCode, message, path, rule string) *protocol.CanonicalError {
	domain := protocol.DomainProtocol
	if code == protocol.FailureUpstreamResponseTooLarge {
		domain = protocol.DomainUpstream
	}
	return &protocol.CanonicalError{
		Code: code, Domain: domain, RetryDisposition: protocol.RetryPreOutputAlternate,
		SafeMessage: message, HTTPStatus: http.StatusBadGateway,
		Validation: &protocol.ValidationIssue{Path: path, Rule: rule},
	}
}
