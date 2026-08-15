package app

import (
	"context"
	"errors"
	"net/http"

	"github.com/NatsumeRyuhane/LLM-Gateway/backend/internal/auth"
	"github.com/NatsumeRyuhane/LLM-Gateway/backend/internal/openai"
	"github.com/NatsumeRyuhane/LLM-Gateway/backend/internal/protocol"
)

// ApplicationAuthenticator is the narrow authentication dependency consumed by
// the data-plane composition boundary.
type ApplicationAuthenticator interface {
	Authenticate(context.Context, []string) (auth.PrincipalContext, *protocol.CanonicalError)
}

// DataPlaneBoundary authenticates and authorizes public requests before it
// returns transport-independent values to a handler. Raw HTTP identity transport
// never crosses this boundary.
type DataPlaneBoundary struct {
	authenticator ApplicationAuthenticator
	codec         openai.Codec
}

// NewDataPlaneBoundary composes application authentication with the sole public
// OpenAI-compatible codec.
func NewDataPlaneBoundary(authenticator ApplicationAuthenticator, codec openai.Codec) (*DataPlaneBoundary, error) {
	if authenticator == nil {
		return nil, errors.New("data-plane application authenticator is required")
	}
	return &DataPlaneBoundary{authenticator: authenticator, codec: codec}, nil
}

// AuthenticatedModelsRequest is the complete typed input to a models handler.
type AuthenticatedModelsRequest struct {
	RequestID   string
	Principal   auth.PrincipalContext
	Attribution auth.RequestAttribution
}

// AuthenticatedChatRequest is the complete typed input to a Chat Completions
// handler. Request carries no raw attribution; its application-bound form is in
// Attribution and cannot enter provider construction accidentally.
type AuthenticatedChatRequest struct {
	Principal   auth.PrincipalContext
	Attribution auth.RequestAttribution
	Request     protocol.ValidatedChatRequest
}

// AuthenticateModelsRequest verifies the application and exact models scope,
// then lets the public codec normalize the endpoint and attribution syntax once.
func (b *DataPlaneBoundary) AuthenticateModelsRequest(
	request *http.Request,
	metadata openai.RequestMetadata,
) (AuthenticatedModelsRequest, *protocol.CanonicalError) {
	principal, failure := b.authenticate(request, metadata.RequestID, auth.ScopeModelsRead)
	if failure != nil {
		return AuthenticatedModelsRequest{}, failure
	}
	decoded, failure := b.codec.DecodeModelsRequest(request, metadata)
	if failure != nil {
		failure.RequestID = metadata.RequestID
		return AuthenticatedModelsRequest{}, failure
	}
	return AuthenticatedModelsRequest{
		RequestID: decoded.RequestID, Principal: principal,
		Attribution: auth.BindAttribution(principal, decoded.Attribution),
	}, nil
}

// AuthenticateChatCompletionsRequest verifies the application and exact chat
// scope, consumes the codec-normalized attribution, and removes the unscoped
// values from the canonical request passed toward routing and providers.
func (b *DataPlaneBoundary) AuthenticateChatCompletionsRequest(
	request *http.Request,
	metadata openai.RequestMetadata,
) (AuthenticatedChatRequest, *protocol.CanonicalError) {
	principal, failure := b.authenticate(request, metadata.RequestID, auth.ScopeChatCompletionsCreate)
	if failure != nil {
		return AuthenticatedChatRequest{}, failure
	}
	decoded, failure := b.codec.DecodeChatCompletions(request, metadata)
	if failure != nil {
		failure.RequestID = metadata.RequestID
		return AuthenticatedChatRequest{}, failure
	}
	canonical := decoded.Canonical()
	attribution := auth.BindAttribution(principal, canonical.Attribution)
	canonical.Attribution = protocol.Attribution{}
	withoutRawAttribution, failure := protocol.ValidateChatRequest(canonical, decoded.Limits())
	if failure != nil {
		failure.RequestID = metadata.RequestID
		return AuthenticatedChatRequest{}, failure
	}
	return AuthenticatedChatRequest{
		Principal: principal, Attribution: attribution, Request: withoutRawAttribution,
	}, nil
}

func (b *DataPlaneBoundary) authenticate(
	request *http.Request,
	requestID string,
	required auth.Scope,
) (auth.PrincipalContext, *protocol.CanonicalError) {
	var authorization []string
	var requestContext context.Context
	if request == nil {
		requestContext = context.Background()
	} else {
		authorization = request.Header.Values("Authorization")
		requestContext = request.Context()
	}
	principal, failure := b.authenticator.Authenticate(requestContext, authorization)
	if failure == nil {
		failure = auth.AuthorizeScope(principal, required)
	}
	if failure != nil {
		failure.RequestID = requestID
		return auth.PrincipalContext{}, failure
	}
	return principal, nil
}
