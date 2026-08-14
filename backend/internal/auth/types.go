package auth

import (
	"sort"
	"time"

	"github.com/NatsumeRyuhane/LLM-Gateway/backend/internal/protocol"
)

const (
	// ScopeModelsRead permits the OpenAI-compatible models-list surface.
	ScopeModelsRead Scope = "models:read"
	// ScopeChatCompletionsCreate permits the Chat Completions surface.
	ScopeChatCompletionsCreate Scope = "chat:completions:create"
)

const maxScopes = 32

// Scope is one bounded data-plane authorization grant.
type Scope string

// SubjectKind identifies the authenticated subject represented by a principal.
type SubjectKind string

const (
	SubjectKindUser        SubjectKind = "user"
	SubjectKindService     SubjectKind = "service"
	SubjectKindApplication SubjectKind = "application"
)

// AuthenticationMethod identifies the verified authenticator class.
type AuthenticationMethod string

const AuthenticationMethodApplicationKey AuthenticationMethod = "application_key"

// ExternalIdentity is immutable external link evidence. The application-key
// vertical slice never populates it.
type ExternalIdentity struct {
	Issuer  string
	Subject string
}

// PrincipalContext is the transport-independent authenticated identity passed
// to authorization, accounting, and handlers. It deliberately has no field in
// which a bearer value can be retained.
type PrincipalContext struct {
	ApplicationID        string
	CredentialID         string
	SubjectKind          SubjectKind
	SubjectID            string
	GatewayPrincipalID   protocol.Optional[string]
	ExternalIdentity     protocol.Optional[ExternalIdentity]
	OperatorID           protocol.Optional[string]
	Scopes               []Scope
	AuthenticationTime   time.Time
	AuthenticationMethod AuthenticationMethod
}

// HasScope reports whether the validated principal carries one exact grant.
func (p PrincipalContext) HasScope(required Scope) bool {
	index := sort.Search(len(p.Scopes), func(index int) bool {
		return p.Scopes[index] >= required
	})
	return index < len(p.Scopes) && p.Scopes[index] == required
}

func normalizeScopes(scopes []Scope) ([]Scope, bool) {
	if len(scopes) == 0 || len(scopes) > maxScopes {
		return nil, false
	}
	result := append([]Scope(nil), scopes...)
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	for index, scope := range result {
		switch scope {
		case ScopeModelsRead, ScopeChatCompletionsCreate:
		default:
			return nil, false
		}
		if index != 0 && scope == result[index-1] {
			return nil, false
		}
	}
	return result, true
}
