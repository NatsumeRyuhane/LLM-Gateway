package app

import (
	"context"
	"errors"
	"net/http"
	"unicode/utf8"

	"github.com/NatsumeRyuhane/LLM-Gateway/backend/internal/auth"
	"github.com/NatsumeRyuhane/LLM-Gateway/backend/internal/openai"
	"github.com/NatsumeRyuhane/LLM-Gateway/backend/internal/protocol"
	"github.com/NatsumeRyuhane/LLM-Gateway/backend/internal/provider"
)

// SingleAuthorizedRoute is the complete immutable route source for the first
// vertical slice. It has no candidate list, ranking, affinity, retry, or fallback.
type SingleAuthorizedRoute struct {
	applicationID string
	model         openai.Model
	route         provider.ValidatedRoute
}

// NewSingleAuthorizedRoute injects exactly one application-visible model and
// one already validated provider route.
func NewSingleAuthorizedRoute(applicationID string, model openai.Model, route provider.ValidatedRoute) (*SingleAuthorizedRoute, error) {
	if !boundedIdentifier(applicationID, 128) {
		return nil, errors.New("single-route application ID is invalid")
	}
	if !boundedIdentifier(model.ID, 256) || !boundedIdentifier(model.OwnedBy, 128) || model.CreatedAt.IsZero() {
		return nil, errors.New("single-route public model is invalid")
	}
	if route.ID() == "" || route.Endpoint().Host == "" {
		return nil, errors.New("single-route provider route must be validated")
	}
	return &SingleAuthorizedRoute{applicationID: applicationID, model: model, route: route}, nil
}

func (r *SingleAuthorizedRoute) models(_ context.Context, principal auth.PrincipalContext) []openai.Model {
	if r == nil || principal.ApplicationID != r.applicationID {
		return []openai.Model{}
	}
	return []openai.Model{r.model}
}

func (r *SingleAuthorizedRoute) resolve(
	_ context.Context,
	principal auth.PrincipalContext,
	request protocol.ValidatedChatRequest,
) (provider.ValidatedRoute, *protocol.CanonicalError) {
	if r == nil || principal.ApplicationID != r.applicationID || request.Canonical().Target != r.model.ID {
		return provider.ValidatedRoute{}, &protocol.CanonicalError{
			Code: protocol.FailurePolicyUnknownTarget, Domain: protocol.DomainPolicy,
			RetryDisposition: protocol.RetryNever, SafeMessage: "The requested model is unavailable.",
			HTTPStatus: http.StatusNotFound,
		}
	}
	if failure := r.route.CheckRequest(request); failure != nil {
		return provider.ValidatedRoute{}, failure
	}
	return r.route, nil
}

func boundedIdentifier(value string, maximum int) bool {
	if value == "" || len(value) > maximum || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if character < 0x21 || character == 0x7f {
			return false
		}
	}
	return true
}
