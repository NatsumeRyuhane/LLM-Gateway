package openai

import "github.com/NatsumeRyuhane/LLM-Gateway/backend/internal/protocol"

func invalidRequest(path, rule string) *protocol.CanonicalError {
	return &protocol.CanonicalError{
		Code:             protocol.FailureClientInvalidRequest,
		Domain:           protocol.DomainClient,
		RetryDisposition: protocol.RetryNever,
		SafeMessage:      "The request is invalid.",
		HTTPStatus:       400,
		Validation:       &protocol.ValidationIssue{Path: path, Rule: rule},
	}
}

func unsupported(path, rule string) *protocol.CanonicalError {
	return &protocol.CanonicalError{
		Code:             protocol.FailureCapabilityUnsupported,
		Domain:           protocol.DomainCapability,
		RetryDisposition: protocol.RetryNever,
		SafeMessage:      "The requested capability is not supported.",
		HTTPStatus:       400,
		Validation:       &protocol.ValidationIssue{Path: path, Rule: rule},
	}
}
