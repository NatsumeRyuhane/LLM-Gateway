package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	applicationCredentialPrefix = "llmgw_app_"
	maxCredentialBytes          = 512
	maxCredentialIDBytes        = 128
	minimumSecretBytes          = 32
	verifierKeyBytes            = 32
)

var (
	credentialLookupDomain   = []byte("llm-gateway/application-credential/lookup/v1\x00")
	credentialVerifierDomain = []byte("llm-gateway/application-credential/verifier/v1\x00")
)

// CredentialClass prevents repository records from crossing authenticator
// classes even when a storage implementation returns the wrong record.
type CredentialClass string

const CredentialClassApplication CredentialClass = "application"

// CredentialLookup is a keyed, non-reversible repository key. Key is safe for
// process-local map indexing but must not be used as a telemetry label.
type CredentialLookup struct {
	value [sha256.Size]byte
}

// Key returns a deterministic non-secret representation for repository lookup.
func (l CredentialLookup) Key() string {
	return base64.RawURLEncoding.EncodeToString(l.value[:])
}

// StoredVerifier is the keyed non-reversible value persisted with a credential
// record. Its bytes are intentionally not exposed.
type StoredVerifier struct {
	value [sha256.Size]byte
}

// HMACVerifier derives application-credential lookup and verifier values with
// gateway-held key material. It is immutable and safe for concurrent use.
type HMACVerifier struct {
	key [verifierKeyBytes]byte
}

// CredentialVerifier is the sealed verifier contract consumed by Authenticator.
// Keeping the operations narrow prevents repositories from receiving bearer
// values and prevents non-constant-time implementations outside this package.
type CredentialVerifier interface {
	lookupDigest([]byte) [sha256.Size]byte
	digest([]byte) [sha256.Size]byte
	matches(StoredVerifier, [sha256.Size]byte) bool
}

// NewHMACVerifier copies exactly 256 bits of gateway-held verifier key material.
func NewHMACVerifier(key []byte) (*HMACVerifier, error) {
	if len(key) != verifierKeyBytes {
		return nil, fmt.Errorf("application credential verifier key must be exactly %d bytes", verifierKeyBytes)
	}
	verifier := &HMACVerifier{}
	copy(verifier.key[:], key)
	return verifier, nil
}

// DeriveStoredVerifier validates an application credential and derives the
// repository lookup plus stored verifier used by issuance and test fixtures.
// It never returns the presented bearer.
func (v *HMACVerifier) DeriveStoredVerifier(credential string) (CredentialLookup, StoredVerifier, error) {
	presented, err := parseApplicationCredential(credential)
	if err != nil {
		return CredentialLookup{}, StoredVerifier{}, err
	}
	defer presented.clear()
	return CredentialLookup{value: v.lookupDigest(presented.raw)}, StoredVerifier{value: v.digest(presented.raw)}, nil
}

func (v *HMACVerifier) lookupDigest(credential []byte) [sha256.Size]byte {
	return v.digestDomain(credentialLookupDomain, credential)
}

func (v *HMACVerifier) digest(credential []byte) [sha256.Size]byte {
	return v.digestDomain(credentialVerifierDomain, credential)
}

func (v *HMACVerifier) digestDomain(domain, credential []byte) [sha256.Size]byte {
	digest := hmac.New(sha256.New, v.key[:])
	_, _ = digest.Write(domain)
	_, _ = digest.Write(credential)
	var result [sha256.Size]byte
	copy(result[:], digest.Sum(nil))
	return result
}

func (v *HMACVerifier) matches(expected StoredVerifier, actual [sha256.Size]byte) bool {
	return subtle.ConstantTimeCompare(expected.value[:], actual[:]) == 1
}

type presentedCredential struct {
	credentialID string
	raw          []byte
}

func (p *presentedCredential) clear() {
	for index := range p.raw {
		p.raw[index] = 0
	}
}

func parseAuthorization(values []string) (presentedCredential, error) {
	if len(values) != 1 {
		return presentedCredential{}, errors.New("exactly one authorization value is required")
	}
	scheme, credential, found := strings.Cut(values[0], " ")
	if !found || !strings.EqualFold(scheme, "Bearer") || credential == "" || strings.ContainsAny(credential, " \t\r\n") {
		return presentedCredential{}, errors.New("authorization must contain one bearer credential")
	}
	return parseApplicationCredential(credential)
}

func parseApplicationCredential(value string) (presentedCredential, error) {
	if len(value) > maxCredentialBytes || !utf8.ValidString(value) || !strings.HasPrefix(value, applicationCredentialPrefix) {
		return presentedCredential{}, errors.New("invalid application credential")
	}
	remainder := strings.TrimPrefix(value, applicationCredentialPrefix)
	credentialID, encodedSecret, found := strings.Cut(remainder, "_")
	if !found || !validCredentialID(credentialID) || encodedSecret == "" {
		return presentedCredential{}, errors.New("invalid application credential")
	}
	secret, err := base64.RawURLEncoding.DecodeString(encodedSecret)
	if err != nil || len(secret) < minimumSecretBytes {
		return presentedCredential{}, errors.New("invalid application credential")
	}
	for index := range secret {
		secret[index] = 0
	}
	return presentedCredential{credentialID: credentialID, raw: []byte(value)}, nil
}

func validCredentialID(value string) bool {
	if value == "" || len(value) > maxCredentialIDBytes {
		return false
	}
	for index, character := range []byte(value) {
		if character >= 'A' && character <= 'Z' || character >= 'a' && character <= 'z' || character >= '0' && character <= '9' {
			continue
		}
		if index != 0 && (character == '-' || character == '.') {
			continue
		}
		return false
	}
	return true
}

// Repository is the request-path view of an immutable process-local verifier
// snapshot. Implementations must perform no network, filesystem, or database I/O.
type Repository interface {
	LookupApplicationCredential(context.Context, CredentialLookup) (CredentialRecord, bool, error)
}

var (
	// ErrSnapshotUnavailable means no verifier snapshot can serve requests.
	ErrSnapshotUnavailable = errors.New("credential verifier snapshot unavailable")
	// ErrSnapshotStale means the last good verifier snapshot exceeded its age bound.
	ErrSnapshotStale = errors.New("credential verifier snapshot stale")
)

// CredentialRecord is repository metadata for one application credential. It
// contains only a keyed verifier and stable non-secret identifiers.
type CredentialRecord struct {
	Class         CredentialClass
	CredentialID  string
	ApplicationID string
	Verifier      StoredVerifier
	Scopes        []Scope
	ExpiresAt     time.Time
	Revoked       bool
}

// NewCredentialRecord validates and snapshots one repository record.
func NewCredentialRecord(
	credentialID string,
	applicationID string,
	verifier StoredVerifier,
	scopes []Scope,
	expiresAt time.Time,
	revoked bool,
) (CredentialRecord, error) {
	normalizedScopes, ok := normalizeScopes(scopes)
	if !validCredentialID(credentialID) || !validStableID(applicationID) || !ok || verifier == (StoredVerifier{}) {
		return CredentialRecord{}, errors.New("invalid application credential record")
	}
	return CredentialRecord{
		Class: CredentialClassApplication, CredentialID: credentialID, ApplicationID: applicationID,
		Verifier: verifier, Scopes: normalizedScopes, ExpiresAt: expiresAt, Revoked: revoked,
	}, nil
}

func validStableID(value string) bool {
	if value == "" || len(value) > maxCredentialIDBytes || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if character < 0x21 || character == 0x7f {
			return false
		}
	}
	return true
}
