package provider

import (
	"net/http"

	"github.com/NatsumeRyuhane/LLM-Gateway/backend/internal/protocol"
)

func gatewayFailure(message, path, rule string) *protocol.CanonicalError {
	return &protocol.CanonicalError{
		Code:             protocol.FailureGatewayInternal,
		Domain:           protocol.DomainGateway,
		RetryDisposition: protocol.RetryNever,
		SafeMessage:      message,
		HTTPStatus:       http.StatusInternalServerError,
		Validation:       &protocol.ValidationIssue{Path: path, Rule: rule},
	}
}
