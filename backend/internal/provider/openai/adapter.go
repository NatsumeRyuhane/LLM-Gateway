package openai

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/NatsumeRyuhane/LLM-Gateway/backend/internal/protocol"
	"github.com/NatsumeRyuhane/LLM-Gateway/backend/internal/provider"
)

const userAgent = "llm-gateway/openai-upstream"

// Adapter is an OpenAI-compatible Chat Completions upstream adapter.
type Adapter struct {
	client *http.Client
	now    func() time.Time
}

var _ provider.Adapter = (*Adapter)(nil)

// New creates an adapter with a dedicated client that ignores ambient proxy
// settings, disables compression, has no cookie jar, and rejects redirects.
func New() *Adapter {
	client := &http.Client{Transport: defaultTransport()}
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &Adapter{client: client, now: time.Now}
}

func defaultTransport() *http.Transport {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DisableCompression = true
	return transport
}

func requestContext(ctx context.Context, request protocol.ValidatedChatRequest) (context.Context, context.CancelFunc) {
	deadline := request.Canonical().Deadline
	if current, ok := ctx.Deadline(); ok && current.Before(deadline) {
		return context.WithCancel(ctx)
	}
	return context.WithDeadline(ctx, deadline)
}

func attachAttempt(failure *protocol.CanonicalError, request protocol.ValidatedChatRequest, attempt provider.Attempt, route provider.ValidatedRoute) *protocol.CanonicalError {
	if failure == nil {
		return nil
	}
	attached := *failure
	if failure.Validation != nil {
		validation := *failure.Validation
		attached.Validation = &validation
	}
	attached.RequestID = request.Canonical().RequestID
	attached.AttemptID = attempt.ID
	attached.RouteID = route.ID()
	return &attached
}

func classifyTransport(err error) *protocol.CanonicalError {
	if errors.Is(err, context.Canceled) {
		return failure(protocol.FailureClientCancelled, protocol.DomainClient, protocol.RetryNever, 499, "The request was cancelled.")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return failure(protocol.FailureUpstreamTimeout, protocol.DomainUpstream, protocol.RetryPreOutputAlternate, http.StatusBadGateway, "The upstream request timed out.")
	}
	return failure(protocol.FailureUpstreamConnectFailed, protocol.DomainUpstream, protocol.RetryPreOutputSameOrAlternate, http.StatusBadGateway, "The upstream provider could not be reached.")
}

func failure(code protocol.FailureCode, domain protocol.FailureDomain, retry protocol.RetryDisposition, status int, message string) *protocol.CanonicalError {
	return &protocol.CanonicalError{Code: code, Domain: domain, RetryDisposition: retry, SafeMessage: message, HTTPStatus: status}
}
