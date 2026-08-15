package auth

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"time"

	"github.com/NatsumeRyuhane/LLM-Gateway/backend/internal/protocol"
)

const (
	maxLookupTimeout          = 5 * time.Millisecond
	maxConcurrentVerification = 256
)

// Clock lets authentication tests control expiry and authentication time.
type Clock func() time.Time

// Options are the resolved request-path availability limits. Values may be
// lowered for a deployment but cannot exceed the accepted threat-model maxima.
type Options struct {
	Clock                     Clock
	LookupTimeout             time.Duration
	MaxConcurrentVerification int
	lookupContext             lookupContextFactory
}

type lookupContextFactory func(context.Context, time.Duration) (context.Context, context.CancelFunc)

// DefaultOptions returns the accepted v0 authentication limits.
func DefaultOptions() Options {
	return Options{
		Clock:                     time.Now,
		LookupTimeout:             maxLookupTimeout,
		MaxConcurrentVerification: maxConcurrentVerification,
		lookupContext:             context.WithTimeout,
	}
}

// Authenticator verifies application credentials without retaining bearer
// values. It is safe for concurrent use.
type Authenticator struct {
	repository Repository
	verifier   CredentialVerifier
	clock      Clock
	lookup     time.Duration
	lookupCtx  lookupContextFactory
	inFlight   chan struct{}
}

// NewAuthenticator validates all dependencies and resolved limits.
func NewAuthenticator(repository Repository, verifier CredentialVerifier, options Options) (*Authenticator, error) {
	if repository == nil {
		return nil, errors.New("application credential repository is required")
	}
	if verifier == nil {
		return nil, errors.New("application credential verifier is required")
	}
	if concrete, ok := verifier.(*HMACVerifier); ok && concrete == nil {
		return nil, errors.New("application credential verifier is required")
	}
	if options.Clock == nil {
		return nil, errors.New("authentication clock is required")
	}
	if options.LookupTimeout <= 0 || options.LookupTimeout > maxLookupTimeout {
		return nil, fmt.Errorf("authentication lookup timeout must be within (0, %s]", maxLookupTimeout)
	}
	if options.MaxConcurrentVerification <= 0 || options.MaxConcurrentVerification > maxConcurrentVerification {
		return nil, fmt.Errorf("authentication concurrency must be within [1, %d]", maxConcurrentVerification)
	}
	if options.lookupContext == nil {
		options.lookupContext = context.WithTimeout
	}
	return &Authenticator{
		repository: repository,
		verifier:   verifier,
		clock:      options.Clock,
		lookup:     options.LookupTimeout,
		lookupCtx:  options.lookupContext,
		inFlight:   make(chan struct{}, options.MaxConcurrentVerification),
	}, nil
}

// Authenticate verifies one HTTP Authorization value and returns an
// application-as-subject principal. Missing, malformed, unknown, expired,
// revoked, cross-class, and mismatched credentials share one safe failure.
func (a *Authenticator) Authenticate(ctx context.Context, authorizationValues []string) (PrincipalContext, *protocol.CanonicalError) {
	select {
	case a.inFlight <- struct{}{}:
		defer func() { <-a.inFlight }()
	default:
		return PrincipalContext{}, overloadedFailure()
	}

	presented, err := parseAuthorization(authorizationValues)
	if err != nil {
		return PrincipalContext{}, authenticationFailure()
	}
	defer presented.clear()

	lookupDigest := a.verifier.lookupDigest(presented.raw)
	verifierDigest := a.verifier.digest(presented.raw)
	lookup := CredentialLookup{value: lookupDigest}
	lookupContext, cancel := a.lookupCtx(ctx, a.lookup)
	record, found, lookupErr := a.repository.LookupApplicationCredential(lookupContext, lookup)
	lookupContextErr := lookupContext.Err()
	cancel()
	if errors.Is(lookupContextErr, context.DeadlineExceeded) {
		return PrincipalContext{}, overloadedFailure()
	}
	if lookupErr != nil {
		if errors.Is(lookupErr, ErrSnapshotUnavailable) || errors.Is(lookupErr, ErrSnapshotStale) {
			return PrincipalContext{}, storageFailure()
		}
		if errors.Is(lookupErr, context.DeadlineExceeded) {
			return PrincipalContext{}, overloadedFailure()
		}
		return PrincipalContext{}, storageFailure()
	}
	if !found {
		return PrincipalContext{}, authenticationFailure()
	}

	now := a.clock().UTC()
	validClass := record.Class == CredentialClassApplication
	validIDs := validCredentialID(record.CredentialID) && validStableID(record.ApplicationID)
	validCredentialID := constantTimeEqualString(record.CredentialID, presented.credentialID)
	validVerifier := a.verifier.matches(record.Verifier, verifierDigest)
	scopes, validScopes := normalizeScopes(record.Scopes)
	validTime := record.ExpiresAt.IsZero() || now.Before(record.ExpiresAt)
	if !validClass || !validIDs || !validCredentialID || !validVerifier || !validScopes || !validTime || record.Revoked {
		return PrincipalContext{}, authenticationFailure()
	}

	return PrincipalContext{
		ApplicationID:        record.ApplicationID,
		CredentialID:         record.CredentialID,
		SubjectKind:          SubjectKindApplication,
		SubjectID:            record.ApplicationID,
		GatewayPrincipalID:   protocol.None[string](),
		ExternalIdentity:     protocol.None[ExternalIdentity](),
		OperatorID:           protocol.None[string](),
		Scopes:               scopes,
		AuthenticationTime:   now,
		AuthenticationMethod: AuthenticationMethodApplicationKey,
	}, nil
}

// AuthorizeScope applies one exact surface grant to a typed principal.
func AuthorizeScope(principal PrincipalContext, required Scope) *protocol.CanonicalError {
	if !principal.HasScope(required) {
		return forbiddenFailure()
	}
	return nil
}

func constantTimeEqualString(left, right string) bool {
	if len(left) != len(right) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}

func authenticationFailure() *protocol.CanonicalError {
	return &protocol.CanonicalError{
		Code: protocol.FailureAuthInvalidCredential, Domain: protocol.DomainAuth,
		RetryDisposition: protocol.RetryNever, SafeMessage: "Authentication failed.", HTTPStatus: 401,
	}
}

func forbiddenFailure() *protocol.CanonicalError {
	return &protocol.CanonicalError{
		Code: protocol.FailureAuthForbidden, Domain: protocol.DomainAuth,
		RetryDisposition: protocol.RetryNever, SafeMessage: "Access denied.", HTTPStatus: 403,
	}
}

func overloadedFailure() *protocol.CanonicalError {
	return &protocol.CanonicalError{
		Code: protocol.FailureGatewayOverloaded, Domain: protocol.DomainGateway,
		RetryDisposition: protocol.RetryPreOutputSameOrAlternate, SafeMessage: "Gateway temporarily unavailable.", HTTPStatus: 503,
	}
}

func storageFailure() *protocol.CanonicalError {
	return &protocol.CanonicalError{
		Code: protocol.FailureStorageUnavailable, Domain: protocol.DomainStorage,
		RetryDisposition: protocol.RetryNever, SafeMessage: "Gateway temporarily unavailable.", HTTPStatus: 503,
	}
}
