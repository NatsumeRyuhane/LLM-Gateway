package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NatsumeRyuhane/LLM-Gateway/backend/internal/protocol"
)

var fixtureNow = time.Date(2026, 8, 14, 1, 2, 3, 0, time.UTC)

type testRepository struct {
	mu      sync.RWMutex
	records map[string]CredentialRecord
	lookup  func(context.Context, CredentialLookup) (CredentialRecord, bool, error)
	calls   atomic.Int64
}

func (r *testRepository) LookupApplicationCredential(ctx context.Context, lookup CredentialLookup) (CredentialRecord, bool, error) {
	r.calls.Add(1)
	if r.lookup != nil {
		return r.lookup(ctx, lookup)
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	record, found := r.records[lookup.Key()]
	return record, found, nil
}

func TestAuthenticateProducesApplicationPrincipalWithoutBearer(t *testing.T) {
	credential := fixtureCredential("credential-a", 0x41)
	authenticator, _, _ := fixtureAuthenticator(t, credential, "application-a", []Scope{ScopeModelsRead, ScopeChatCompletionsCreate}, time.Time{}, false)

	principal, failure := authenticator.Authenticate(t.Context(), []string{"Bearer " + credential})
	if failure != nil {
		t.Fatalf("Authenticate() failure = %v", failure)
	}
	if principal.ApplicationID != "application-a" || principal.CredentialID != "credential-a" {
		t.Fatalf("principal IDs = %#v", principal)
	}
	if principal.SubjectKind != SubjectKindApplication || principal.SubjectID != "application-a" {
		t.Fatalf("principal subject = %q/%q", principal.SubjectKind, principal.SubjectID)
	}
	if principal.AuthenticationMethod != AuthenticationMethodApplicationKey || !principal.AuthenticationTime.Equal(fixtureNow) {
		t.Fatalf("principal authentication = %q at %s", principal.AuthenticationMethod, principal.AuthenticationTime)
	}
	if !principal.HasScope(ScopeModelsRead) || !principal.HasScope(ScopeChatCompletionsCreate) {
		t.Fatalf("principal scopes = %#v", principal.Scopes)
	}
	if principal.GatewayPrincipalID.IsSet() || principal.ExternalIdentity.IsSet() || principal.OperatorID.IsSet() {
		t.Fatal("application principal unexpectedly carried a linked or operator identity")
	}

	encoded, err := json.Marshal(principal)
	if err != nil {
		t.Fatalf("json.Marshal(principal): %v", err)
	}
	if strings.Contains(string(encoded), credential) || strings.Contains(fmt.Sprintf("%#v", principal), credential) {
		t.Fatal("principal retained or exposed the bearer credential")
	}
}

func TestAuthenticationCredentialFailuresAreIndistinguishable(t *testing.T) {
	valid := fixtureCredential("credential-a", 0x41)
	unknown := fixtureCredential("credential-a", 0x42)
	crossClass := strings.Replace(valid, applicationCredentialPrefix, "llmgw_provider_", 1)

	testCases := []struct {
		name       string
		headers    []string
		credential string
		expiresAt  time.Time
		revoked    bool
		mutate     func(*testRepository, *HMACVerifier)
	}{
		{name: "missing"},
		{name: "multiple", headers: []string{"Bearer " + valid, "Bearer " + valid}},
		{name: "malformed", headers: []string{"Bearer not-an-application-key"}},
		{name: "cross class", headers: []string{"Bearer " + crossClass}},
		{name: "unknown", headers: []string{"Bearer " + unknown}, credential: valid},
		{name: "expired", headers: []string{"Bearer " + valid}, credential: valid, expiresAt: fixtureNow},
		{name: "revoked", headers: []string{"Bearer " + valid}, credential: valid, revoked: true},
		{
			name: "mismatched record", headers: []string{"Bearer " + valid}, credential: valid,
			mutate: func(repository *testRepository, verifier *HMACVerifier) {
				_, stored, err := verifier.DeriveStoredVerifier(valid)
				if err != nil {
					t.Fatal(err)
				}
				record, err := NewCredentialRecord("credential-b", "application-a", stored, []Scope{ScopeModelsRead}, time.Time{}, false)
				if err != nil {
					t.Fatal(err)
				}
				repository.lookup = func(context.Context, CredentialLookup) (CredentialRecord, bool, error) {
					return record, true, nil
				}
			},
		},
		{
			name: "mismatched verifier", headers: []string{"Bearer " + valid}, credential: valid,
			mutate: func(repository *testRepository, verifier *HMACVerifier) {
				_, stored, err := verifier.DeriveStoredVerifier(unknown)
				if err != nil {
					t.Fatal(err)
				}
				record, err := NewCredentialRecord("credential-a", "application-a", stored, []Scope{ScopeModelsRead}, time.Time{}, false)
				if err != nil {
					t.Fatal(err)
				}
				repository.lookup = func(context.Context, CredentialLookup) (CredentialRecord, bool, error) {
					return record, true, nil
				}
			},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			credential := testCase.credential
			if credential == "" {
				credential = valid
			}
			authenticator, repository, verifier := fixtureAuthenticator(
				t, credential, "application-a", []Scope{ScopeModelsRead}, testCase.expiresAt, testCase.revoked,
			)
			if testCase.mutate != nil {
				testCase.mutate(repository, verifier)
			}
			principal, failure := authenticator.Authenticate(t.Context(), testCase.headers)
			if principal.ApplicationID != "" || principal.CredentialID != "" || len(principal.Scopes) != 0 {
				t.Fatalf("Authenticate() principal = %#v", principal)
			}
			assertAuthenticationFailure(t, failure)
			for _, header := range testCase.headers {
				if strings.Contains(failure.Error(), header) {
					t.Fatal("failure exposed rejected authorization value")
				}
			}
		})
	}
}

func TestAuthenticateRejectsRepositoryCredentialClassConfusion(t *testing.T) {
	credential := fixtureCredential("credential-a", 0x41)
	authenticator, repository, _ := fixtureAuthenticator(t, credential, "application-a", []Scope{ScopeModelsRead}, time.Time{}, false)
	for key, record := range repository.records {
		record.Class = CredentialClass("provider")
		repository.records[key] = record
	}

	_, failure := authenticator.Authenticate(t.Context(), []string{"Bearer " + credential})
	assertAuthenticationFailure(t, failure)
}

func TestHMACVerifierIsKeyedAndNonReversible(t *testing.T) {
	credential := fixtureCredential("credential-a", 0x41)
	first := fixtureVerifier(t, 0x11)
	second := fixtureVerifier(t, 0x22)
	firstLookup, firstStored, err := first.DeriveStoredVerifier(credential)
	if err != nil {
		t.Fatal(err)
	}
	secondLookup, _, err := second.DeriveStoredVerifier(credential)
	if err != nil {
		t.Fatal(err)
	}
	if firstLookup.Key() == secondLookup.Key() {
		t.Fatal("different gateway-held keys produced the same lookup")
	}
	if strings.Contains(firstLookup.Key(), credential) || len(firstLookup.Key()) != 43 {
		t.Fatalf("lookup key is not a bounded non-reversible digest: %q", firstLookup.Key())
	}
	if firstLookup.value == firstStored.value {
		t.Fatal("lookup key and stored verifier did not use separate domains")
	}
	if !first.matches(firstStored, first.digest([]byte(credential))) {
		t.Fatal("stored verifier did not match its derived digest")
	}
	if first.matches(firstStored, firstLookup.value) {
		t.Fatal("stored verifier accepted the lookup-domain digest")
	}
	if first.matches(firstStored, second.digest([]byte(credential))) {
		t.Fatal("stored verifier accepted a verifier digest derived with another key")
	}
}

func TestAuthorizeScopeDeniesMissingGrant(t *testing.T) {
	principal := PrincipalContext{Scopes: []Scope{ScopeChatCompletionsCreate, ScopeModelsRead}}
	if failure := AuthorizeScope(principal, ScopeModelsRead); failure != nil {
		t.Fatalf("AuthorizeScope(models) = %v", failure)
	}
	failure := AuthorizeScope(principal, Scope("unknown"))
	if failure == nil || failure.Code != protocol.FailureAuthForbidden || failure.Domain != protocol.DomainAuth ||
		failure.HTTPStatus != 403 || failure.RetryDisposition != protocol.RetryNever {
		t.Fatalf("AuthorizeScope(unknown) = %#v", failure)
	}
}

func TestAuthenticationIsConcurrent(t *testing.T) {
	credential := fixtureCredential("credential-a", 0x41)
	authenticator, repository, _ := fixtureAuthenticatorWithOptions(
		t, credential, "application-a", []Scope{ScopeModelsRead}, time.Time{}, false,
		func(options *Options) {
			options.lookupContext = func(parent context.Context, _ time.Duration) (context.Context, context.CancelFunc) {
				return context.WithCancel(parent)
			}
		},
	)
	const goroutines = 128
	var wait sync.WaitGroup
	start := make(chan struct{})
	failures := make(chan error, goroutines)
	for range goroutines {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			principal, failure := authenticator.Authenticate(t.Context(), []string{"Bearer " + credential})
			if failure != nil || principal.ApplicationID != "application-a" {
				failures <- fmt.Errorf("Authenticate() = %#v, %v", principal, failure)
			}
		}()
	}
	close(start)
	wait.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}
	if repository.calls.Load() != goroutines {
		t.Fatalf("repository calls = %d, want %d", repository.calls.Load(), goroutines)
	}
}

func TestAuthenticationConcurrencyLimitRejectsBeforeLookup(t *testing.T) {
	credential := fixtureCredential("credential-a", 0x41)
	verifier := fixtureVerifier(t, 0x11)
	entered := make(chan struct{})
	release := make(chan struct{})
	repository := &testRepository{lookup: func(context.Context, CredentialLookup) (CredentialRecord, bool, error) {
		close(entered)
		<-release
		return CredentialRecord{}, false, nil
	}}
	options := DefaultOptions()
	options.MaxConcurrentVerification = 1
	options.LookupTimeout = maxLookupTimeout
	authenticator, err := NewAuthenticator(repository, verifier, options)
	if err != nil {
		t.Fatal(err)
	}
	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		_, _ = authenticator.Authenticate(t.Context(), []string{"Bearer " + credential})
	}()
	<-entered

	_, failure := authenticator.Authenticate(t.Context(), []string{"Bearer " + credential})
	if failure == nil || failure.Code != protocol.FailureGatewayOverloaded ||
		failure.RetryDisposition != protocol.RetryPreOutputSameOrAlternate || repository.calls.Load() != 1 {
		t.Fatalf("concurrency rejection = %#v, calls %d", failure, repository.calls.Load())
	}
	close(release)
	<-firstDone
}

func TestAuthenticationMapsSnapshotAndLookupFailures(t *testing.T) {
	credential := fixtureCredential("credential-a", 0x41)
	testCases := []struct {
		name  string
		err   error
		code  protocol.FailureCode
		retry protocol.RetryDisposition
	}{
		{name: "unavailable", err: ErrSnapshotUnavailable, code: protocol.FailureStorageUnavailable, retry: protocol.RetryNever},
		{name: "stale", err: ErrSnapshotStale, code: protocol.FailureStorageUnavailable, retry: protocol.RetryNever},
		{name: "deadline", err: context.DeadlineExceeded, code: protocol.FailureGatewayOverloaded, retry: protocol.RetryPreOutputSameOrAlternate},
		{name: "repository", err: errors.New("repository failure"), code: protocol.FailureStorageUnavailable, retry: protocol.RetryNever},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			verifier := fixtureVerifier(t, 0x11)
			repository := &testRepository{lookup: func(context.Context, CredentialLookup) (CredentialRecord, bool, error) {
				return CredentialRecord{}, false, testCase.err
			}}
			options := DefaultOptions()
			options.Clock = func() time.Time { return fixtureNow }
			authenticator, err := NewAuthenticator(repository, verifier, options)
			if err != nil {
				t.Fatal(err)
			}
			_, failure := authenticator.Authenticate(t.Context(), []string{"Bearer " + credential})
			if failure == nil || failure.Code != testCase.code || failure.SafeMessage != "Gateway temporarily unavailable." ||
				failure.RetryDisposition != testCase.retry {
				t.Fatalf("Authenticate() failure = %#v", failure)
			}
		})
	}
}

func TestAuthenticationFailsClosedWhenRepositoryReturnsAfterDeadline(t *testing.T) {
	credential := fixtureCredential("credential-a", 0x41)
	verifier := fixtureVerifier(t, 0x11)
	_, stored, err := verifier.DeriveStoredVerifier(credential)
	if err != nil {
		t.Fatal(err)
	}
	record, err := NewCredentialRecord("credential-a", "application-a", stored, []Scope{ScopeModelsRead}, time.Time{}, false)
	if err != nil {
		t.Fatal(err)
	}
	repository := &testRepository{lookup: func(ctx context.Context, _ CredentialLookup) (CredentialRecord, bool, error) {
		<-ctx.Done()
		return record, true, nil
	}}
	options := DefaultOptions()
	options.LookupTimeout = time.Millisecond
	options.Clock = func() time.Time { return fixtureNow }
	authenticator, err := NewAuthenticator(repository, verifier, options)
	if err != nil {
		t.Fatal(err)
	}
	_, failure := authenticator.Authenticate(t.Context(), []string{"Bearer " + credential})
	if failure == nil || failure.Code != protocol.FailureGatewayOverloaded || failure.RetryDisposition != protocol.RetryPreOutputSameOrAlternate {
		t.Fatalf("Authenticate() failure = %#v", failure)
	}
}

func TestAuthenticatorRejectsLimitsOutsideThreatModel(t *testing.T) {
	verifier := fixtureVerifier(t, 0x11)
	repository := &testRepository{}
	testCases := []Options{
		{},
		{Clock: time.Now, LookupTimeout: maxLookupTimeout + time.Nanosecond, MaxConcurrentVerification: 1},
		{Clock: time.Now, LookupTimeout: time.Millisecond, MaxConcurrentVerification: maxConcurrentVerification + 1},
	}
	for _, options := range testCases {
		if _, err := NewAuthenticator(repository, verifier, options); err == nil {
			t.Fatalf("NewAuthenticator(%#v) error = nil", options)
		}
	}
}

func TestNewCredentialRecordRejectsUnpresentableCredentialID(t *testing.T) {
	credential := fixtureCredential("credential-a", 0x41)
	verifier := fixtureVerifier(t, 0x11)
	_, stored, err := verifier.DeriveStoredVerifier(credential)
	if err != nil {
		t.Fatal(err)
	}
	for _, credentialID := range []string{"credential_bad", ".credential", "crédential"} {
		if _, err := NewCredentialRecord(credentialID, "application-a", stored, []Scope{ScopeModelsRead}, time.Time{}, false); err == nil {
			t.Fatalf("NewCredentialRecord(%q) error = nil", credentialID)
		}
	}
}

func fixtureAuthenticator(
	t *testing.T,
	credential string,
	applicationID string,
	scopes []Scope,
	expiresAt time.Time,
	revoked bool,
) (*Authenticator, *testRepository, *HMACVerifier) {
	t.Helper()
	return fixtureAuthenticatorWithOptions(t, credential, applicationID, scopes, expiresAt, revoked, nil)
}

func fixtureAuthenticatorWithOptions(
	t *testing.T,
	credential string,
	applicationID string,
	scopes []Scope,
	expiresAt time.Time,
	revoked bool,
	configure func(*Options),
) (*Authenticator, *testRepository, *HMACVerifier) {
	t.Helper()
	verifier := fixtureVerifier(t, 0x11)
	lookup, stored, err := verifier.DeriveStoredVerifier(credential)
	if err != nil {
		t.Fatal(err)
	}
	credentialID := strings.Split(strings.TrimPrefix(credential, applicationCredentialPrefix), "_")[0]
	record, err := NewCredentialRecord(credentialID, applicationID, stored, scopes, expiresAt, revoked)
	if err != nil {
		t.Fatal(err)
	}
	repository := &testRepository{records: map[string]CredentialRecord{lookup.Key(): record}}
	options := DefaultOptions()
	options.Clock = func() time.Time { return fixtureNow }
	if configure != nil {
		configure(&options)
	}
	authenticator, err := NewAuthenticator(repository, verifier, options)
	if err != nil {
		t.Fatal(err)
	}
	return authenticator, repository, verifier
}

func fixtureVerifier(t *testing.T, value byte) *HMACVerifier {
	t.Helper()
	key := make([]byte, verifierKeyBytes)
	for index := range key {
		key[index] = value
	}
	verifier, err := NewHMACVerifier(key)
	if err != nil {
		t.Fatal(err)
	}
	return verifier
}

func fixtureCredential(credentialID string, value byte) string {
	secret := make([]byte, minimumSecretBytes)
	for index := range secret {
		secret[index] = value
	}
	return applicationCredentialPrefix + credentialID + "_" + base64.RawURLEncoding.EncodeToString(secret)
}

func assertAuthenticationFailure(t *testing.T, failure *protocol.CanonicalError) {
	t.Helper()
	if failure == nil {
		t.Fatal("Authenticate() failure = nil")
	}
	if failure.Code != protocol.FailureAuthInvalidCredential || failure.Domain != protocol.DomainAuth ||
		failure.RetryDisposition != protocol.RetryNever || failure.SafeMessage != "Authentication failed." ||
		failure.HTTPStatus != 401 || failure.Validation != nil {
		t.Fatalf("Authenticate() failure = %#v", failure)
	}
}
