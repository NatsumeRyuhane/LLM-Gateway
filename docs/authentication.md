# Application authentication boundary

Status: Implemented for the M1 application-as-subject slice under
[issue #20](https://github.com/NatsumeRyuhane/LLM-Gateway/issues/20)

## Boundary and request flow

The first data-plane authentication boundary accepts only gateway application
credentials. It has no fallback to provider keys, operator sessions, bootstrap
secrets, browser tokens, or identity assertions.

For the models and Chat Completions surfaces, application composition performs
these steps in order:

1. Parse and verify exactly one `Authorization: Bearer ...` value.
2. Produce a transport-independent `PrincipalContext` whose subject is the
   authenticated application and which contains no bearer value.
3. Require the exact `models:read` or `chat:completions:create` scope before
   reading or decoding the public request body.
4. Let the OpenAI-compatible public codec validate the endpoint and normalize
   the bounded conversation/run extension headers exactly once.
5. Bind each normalized attribution value to the authenticated application and
   remove the unscoped value from the validated canonical request passed toward
   routing and provider construction.

Handlers receive only `AuthenticatedModelsRequest` or
`AuthenticatedChatRequest`. Neither type exposes an HTTP request, header map,
bearer value, cookie, forwarding identity, or proxy identity.

## Credential and verifier contract

The accepted encoded class is:

```text
llmgw_app_<credential-id>_<base64url-secret>
```

The credential ID is a stable, non-secret identifier. The secret must decode to
at least 256 bits, and the complete encoded credential cannot exceed 512 bytes.
The parser rejects other credential-class prefixes before repository lookup.

`HMACVerifier` derives an HMAC-SHA-256 value using 256 bits of gateway-held key
material and a versioned application-credential domain separator. A
`CredentialLookup` derived from that value is the only credential material
given to the request-path repository. Repository records store a
`StoredVerifier`, stable credential/application IDs, bounded scopes, expiry,
revocation state, and the explicit application credential class; they do not
store the bearer secret. Verification compares keyed values in constant time.

The repository interface represents an immutable, process-local snapshot. It
must not perform request-time database, filesystem, DNS, or network access. The
boundary enforces the accepted 5 ms lookup and 256 concurrent-verification
maxima and maps stale/unavailable snapshots or lookup overload to bounded safe
canonical failures.

Missing, duplicated, malformed, unknown, expired, revoked, cross-class, and
mismatched credentials all return the same public response fields:

```text
code: auth.invalid_credential
domain: auth
status: 401
message: Authentication failed.
```

Scope denial returns `auth.forbidden` without exposing the effective grants.
Errors contain no rejected value, lookup digest, verifier, scope list, or cache
detail. Authentication itself emits no log containing credential material.

## Attribution and outbound isolation

Application scoping is structural: a conversation or run identifier becomes a
`ScopedIdentifier{ApplicationID, Value}`. The same client value under two
applications is therefore two distinct attribution keys. These values remain
gateway accounting/routing inputs and are absent from the canonical request
given to a provider.

Provider request construction always creates a fresh `http.Request`. Its API
does not accept an inbound request or header map. The complete current outbound
allowlist is `Authorization` populated from the route-owned provider credential,
plus optional `Content-Type`, `Accept`, and `User-Agent` values supplied by the
adapter. Application authorization, cookies, identity, forwarding, proxy,
gateway attribution, and inbound provider-authentication headers cannot enter
that construction path.

Durable credential issuance, display-once delivery, rotation, PostgreSQL-backed
snapshot refresh, provider credential decryption, route registration, OIDC,
operator sessions, and application-signed subject assertions remain outside
this slice.
