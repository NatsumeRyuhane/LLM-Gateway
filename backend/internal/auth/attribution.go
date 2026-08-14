package auth

import "github.com/NatsumeRyuhane/LLM-Gateway/backend/internal/protocol"

// ScopedIdentifier makes the authenticated application namespace inseparable
// from a client-provided opaque identifier.
type ScopedIdentifier struct {
	ApplicationID string
	Value         string
}

// RequestAttribution is the application-bound form of public codec attribution.
// It is never translated into provider headers.
type RequestAttribution struct {
	ConversationID protocol.Optional[ScopedIdentifier]
	RunID          protocol.Optional[ScopedIdentifier]
}

// BindAttribution consumes normalized public-codec values. It deliberately has
// no HTTP request or header parameter, so gateway extension syntax remains
// owned by the public codec.
func BindAttribution(principal PrincipalContext, normalized protocol.Attribution) RequestAttribution {
	return RequestAttribution{
		ConversationID: scopeIdentifier(principal.ApplicationID, normalized.ConversationID),
		RunID:          scopeIdentifier(principal.ApplicationID, normalized.RunID),
	}
}

func scopeIdentifier(applicationID string, value protocol.Optional[string]) protocol.Optional[ScopedIdentifier] {
	normalized, present := value.Get()
	if !present {
		return protocol.None[ScopedIdentifier]()
	}
	return protocol.Some(ScopedIdentifier{ApplicationID: applicationID, Value: normalized})
}
