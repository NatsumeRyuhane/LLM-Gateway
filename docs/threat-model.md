# Identity, credential, and upstream endpoint threat model

Status: Accepted for M0 under [issue #5](https://github.com/NatsumeRyuhane/LLM-Gateway/issues/5)

## Purpose and deployment boundary

This document defines the v0 trust model before authentication, identity linking,
or configurable upstream endpoints are implemented. It is a security contract,
not a claim that the gateway can make an untrusted deployment safe.

The supported deployment is self-hosted inside one administrative security
domain. Operators and registered applications are trusted to administer or use
the gateway within their granted scopes. Provider routes, provider responses,
networks, DNS, browsers, and application users are not trusted merely because
they are reachable.

A compromised registered application can impersonate any user or service in its
own application namespace. This is an accepted limitation: the gateway cannot
independently prove an application's local user session. The application must
not be able to impersonate another application's subjects, a gateway operator,
or a provider credential.

Operating a public, adversarial, cross-organization multi-tenant gateway SaaS is
explicitly out of scope.

## Assets

- Provider credentials and their encrypted representations.
- Gateway application credentials and stored verifiers.
- Operator authenticators, sessions, recovery material, and bootstrap state.
- Identity assertions, subject links, scopes, and authorization policy.
- Provider-route endpoints, capability declarations, and routing policy.
- Prompt, completion, tool, embedding, image, audio, and structured-output data.
- Decision, usage, cost, audit, health, and incident records.
- Gateway host, network position, database, signing/encryption keys, and
  observability pipeline.
- Availability and integrity of the data and control planes.

## Actors

| Actor | Trust and authority |
| --- | --- |
| Operator | Trusted to administer this deployment; actions remain authenticated and audited |
| Registered application | Trusted to use assigned scopes and speak for subjects only inside its own namespace |
| Application user or service | Not authenticated directly by the v0 data plane; represented by its application |
| External identity provider | Trusted only after issuer configuration and token verification; claims have claim-specific semantics |
| Provider route | Untrusted for availability, protocol correctness, content, redirects, and declared behavior |
| Network and DNS | Untrusted to preserve name-to-address mappings or response integrity below verified TLS |
| Attacker | May control client input, an application credential, DNS, an endpoint, a provider response, or a browser session |
| Gateway process | Trusted computing base that enforces authentication, authorization, isolation, validation, and redaction |

## Trust boundaries and entry points

```text
application/user
    -- application credential + optional identity assertion --> data plane
operator browser/CLI
    -- operator authentication/session --> control plane
control plane
    -- validated configuration --> PostgreSQL
gateway
    -- route-specific provider credential --> validated provider origin
provider/DNS/network
    -- untrusted HTTP/SSE/body data --> provider adapter
gateway
    -- redacted bounded signals --> logs/traces/metrics/audit
```

Entry points include the versioned data and control APIs, login/bootstrap and
OIDC callbacks, application identity assertions, route/endpoint configuration,
DNS answers and redirects, provider response headers and bodies, SSE events,
configuration and secret injection, database contents, and telemetry exporters.

The dashboard is a control-plane client, not a privileged path around the
control API. Browser code never receives a provider credential or long-lived
application credential.

## Canonical principal context

Authentication produces one typed `PrincipalContext` before authorization or
accounting. It is independent from any HTTP header, JSON field, JWT shape, or
framework context key.

```text
PrincipalContext
  application_id       required for data-plane callers
  credential_id        authenticated gateway credential, never its secret
  subject_kind         user | service | application
  subject_id           opaque application-local identifier
  gateway_principal_id optional canonical accounting identity
  external_identity    optional immutable (issuer, subject) link evidence
  operator_id          present only for an authenticated control-plane actor
  scopes               validated bounded authorization set
  authentication_time  when the presented authenticator was verified
  authentication_method application_key | short_lived_token | oidc | operator_session
```

The application-local identity key is `(application_id, subject_id)`. The
external identity key is `(issuer, subject)`. A gateway principal is a separate,
stable gateway identifier linked to one or more application subjects through an
audited operation.

Email, username, display name, provider account name, and other mutable claims
are attributes only; they are never automatic merge keys. OIDC linking validates
issuer, signature, audience, expiry, not-before time, nonce/state where
applicable, and the subject claim. Manual linking requires operator authority,
records old and new links, and rejects conflicting ownership.

Inbound identity headers or metadata are untrusted input until an authenticated
application-specific assertion policy validates them. After validation, handlers
consume only `PrincipalContext`; they do not re-read transport identity fields.

## Credential classes and non-confusion rules

| Class | Presented by | Accepted at | May be forwarded to |
| --- | --- | --- | --- |
| Gateway application credential | Registered application or trusted BFF | Data plane | Nowhere; it terminates at gateway authentication |
| Application identity assertion | Registered application or trusted BFF | Data-plane authentication boundary | Nowhere; validated claims become `PrincipalContext` |
| Short-lived browser credential | Reference-application BFF | Narrow data-plane scope | Nowhere; never provider-facing |
| Operator authenticator/session | Operator | Control plane only | Configured OIDC provider during its own flow only |
| Provider credential | Gateway provider adapter | Exact validated provider route | Only the configured provider origin and credential placement |
| Bootstrap secret | Initial operator | One-time bootstrap endpoint | Nowhere; invalid after successful bootstrap |

Credential classes use separate storage fields, domain types, parsers, and
header builders. An inbound `Authorization`, cookie, identity, forwarding,
provider-specific authentication, or proxy header is removed before constructing
an upstream request. Adapters add only route-owned credentials and an explicit
allowlist of canonical headers.

No credential parser falls back to another credential class. A syntactically
valid provider key is not a gateway key; an application key is not an operator
session; an identity assertion is not authorization by itself. Tests must prove
cross-class rejection and absence from outbound requests.

### Application assertions and browser boundary

An application identity assertion is accepted only together with the
application's authenticated gateway credential. If the assertion is signed, its
issuer/application binding, audience, algorithm, signature, issued/expiry times,
and replay identifier are verified under that application's configured policy.
It can select only a subject inside the authenticated application namespace and
cannot add scopes beyond the gateway credential.

The M1 reference application uses a backend-for-frontend. The BFF holds the
long-lived application credential and browser session; browser assets receive
neither that credential nor any provider credential. A future direct-browser
mode requires a separately specified, audience-bound, narrowly scoped,
short-lived gateway token and does not inherit authority from the BFF design.

## Credential lifecycle

### Gateway application credentials

- Issue at least 256 bits of cryptographically secure random secret material.
- Bind the credential to one application, a bounded scope set, creation actor,
  creation time, optional expiry, and stable non-secret credential ID.
- Display the secret once over an authenticated TLS control-plane response. A
  later read returns metadata only.
- Store a non-reversible verifier, not the bearer secret. The verifier is keyed
  with gateway-held material kept outside PostgreSQL so a database-only leak does
  not expose usable credentials.
- Compare verifiers in constant time and return one generic authentication
  failure for missing, unknown, expired, revoked, or mismatched credentials.
- Support rotation with an explicit, bounded overlap window; the replacement has
  a new credential ID and independent scopes.
- Make revocation effective immediately in durable state and within a documented,
  bounded authentication-cache interval. Emergency revocation bypasses or
  invalidates the cache.
- Audit issuance, display, scope/expiry change, rotation, revocation, and failed
  use by credential ID and actor without recording the secret.

### Provider credentials

- Encrypt provider credentials at rest with envelope/key material outside the
  database and restrict decryption to the provider-adapter call path.
- Bind each credential to a route/provider type, expected origin, allowed header
  or query placement, and operator-visible metadata.
- Never return stored plaintext after creation. Replacement creates new encrypted
  material and audit evidence; deletion/revocation makes the route ineligible.
- Never use a provider credential in endpoint validation, health evidence labels,
  client errors, logs, traces, metrics, or notification bodies.
- Prevent cross-origin reuse even when two routes use the same provider type.

Secret creation, rotation, revocation, and expiration follow the lifecycle
principles in the [OWASP Secrets Management Cheat Sheet](https://cheatsheetseries.owasp.org/cheatsheets/Secrets_Management_Cheat_Sheet.html).

## Upstream endpoint and network policy

Provider endpoint configuration is operator-only but remains hostile input: an
operator account, imported configuration, database row, DNS zone, or upstream
server may be compromised.

### URL admission

- Production routes use an absolute `https` URL. Reject opaque URLs, userinfo,
  fragments, empty hosts, malformed ports, control characters, ambiguous IP
  spellings, and non-HTTP schemes.
- Plain `http` is allowed only for an explicit development-mode loopback route,
  with no production provider credential and a persistent warning. It is never
  inferred from environment.
- Normalize the host with one parser and retain the original hostname for TLS
  Server Name Indication and certificate verification. Never disable hostname or
  certificate verification for a production route.
- Use a dedicated upstream HTTP transport. Ignore ambient proxy environment
  variables, cookie jars, and inherited client credentials.

### Resolution and dialing

- Resolve every hostname immediately before each new connection. Validate every
  A and AAAA result; mixed allowed/denied answers reject the connection.
- By default, allow only globally reachable unicast destinations. Reject
  unspecified, loopback, private, shared-address, link-local, multicast,
  documentation, benchmarking, reserved, IPv4-mapped, and other non-global
  ranges from the current IANA IPv4 and IPv6 special-purpose registries.
- Private provider networks require an exact operator-configured hostname and
  CIDR allowlist. Every resolved address must fall inside that allowlist. This is
  a deployment exception, not a general `allow_private=true` switch.
- Cloud/container metadata names and addresses remain denied even when private
  routing is enabled, including deployment-configured metadata endpoints,
  `metadata.google.internal`, `metadata.google.internal.`, `metadata.goog`,
  `169.254.169.254`, `fd00:ec2::254`, and `fd20:ce::254`.
- Dial a validated address directly while verifying TLS against the configured
  hostname. Do not perform a second unconstrained resolution between validation
  and connection. Re-resolve and revalidate on every new connection so DNS
  rebinding or later DNS changes cannot escape policy.
- Apply network-layer egress rules as defense in depth; application validation is
  not the only control.

These controls follow the redirect, allowlist, and DNS-pinning guidance in the
[OWASP SSRF Prevention Cheat Sheet](https://cheatsheetseries.owasp.org/cheatsheets/Server_Side_Request_Forgery_Prevention_Cheat_Sheet.html)
and the current [IANA special-purpose address registries](https://www.iana.org/numbers/registries).

### Redirects and credentials

Redirect following is disabled by default. A route may opt into at most three
same-origin redirects when the provider contract requires them. Every hop repeats
URL, scheme, hostname, port, resolution, and address validation. Downgrades,
userinfo, cross-origin redirects, and redirects to a private or metadata target
are rejected.

Provider credentials are never copied to a different origin. Redirect rejection
is recorded as `upstream.redirect_rejected` with the target redacted from routine
client errors and metric labels.

### Request and response bounds

- Enforce connection, TLS-handshake, response-header, first-event, inter-event,
  total-attempt, and downstream-write deadlines.
- Bound response headers, buffered encoded bytes, decoded bytes, decompression
  ratio, SSE line/event size, JSON nesting/field sizes, and error-body capture.
- Disable transparent decompression unless the adapter applies both compressed
  and decompressed limits. Reject unsupported or stacked content encodings.
- Parse SSE incrementally with bounded buffers. Invalid UTF-8/JSON, malformed
  framing, oversized events, invalid event order, early EOF, and missing
  canonical termination receive stable protocol failure codes.
- Never log raw provider bodies by default. Diagnostic capture is separately
  authorized, size/time bounded, encrypted, access audited, and off by default.
- Provider content is data only. The gateway does not execute returned tools,
  follow returned URLs, render HTML, or trust provider-supplied filenames or
  headers as local instructions.

## Control-plane authentication bootstrap

- A new deployment binds the control plane to loopback by default and has no
  default username or password.
- Initial bootstrap requires a high-entropy, short-lived, one-time secret supplied
  out of band. Store only its verifier and invalidate it atomically after the
  first operator is created.
- Refuse non-loopback control-plane exposure while bootstrap is incomplete unless
  an explicit secure external authentication mode is configured.
- Operator authentication and recovery are separate from application and
  provider credentials. Administrative actions require authorization and an
  append-only audit event.
- Cookie sessions, when used, require Secure, HttpOnly, SameSite, CSRF, rotation,
  idle/absolute expiry, and logout/revocation controls. OIDC flows validate state,
  nonce, issuer, signature, audience, and redirect URI exactly.
- Loss of bootstrap/recovery material is an operator recovery event, not a reason
  to introduce a hidden bypass or database-edit authentication path.

## Telemetry, audit, and error policy

Routine logs, traces, metrics, audit records, notifications, and client errors
must not contain bearer credentials, identity assertions, session IDs, cookies,
authorization headers, provider keys, database connection strings, encryption
keys, prompt/completion/tool bodies, signed URLs, or endpoint userinfo/query
secrets.

Allowed evidence includes stable internal application/credential/route IDs,
bounded failure codes, scope names, redacted origin identifiers in restricted
audit views, lifecycle action, actor ID, result, timestamp, and correlation ID.
Metrics never use application subject, principal, request, conversation, run,
raw endpoint, or credential identifiers as labels.

Redaction happens before data leaves the owning package. Sinks provide defense in
depth but are not the first redaction boundary. Redaction tests inject recognizable
canary secrets into every input location and inspect errors and every exported
signal. The exclusions align with the [OWASP Logging Cheat Sheet](https://cheatsheetseries.owasp.org/cheatsheets/Logging_Cheat_Sheet.html).

## Threats and required mitigations

### Authentication availability bounds

V0 authentication fails closed within the following implementation limits:

The supported v0 deployment has exactly one serving gateway instance, as
defined by the architecture contract. Consequently, the concurrency caps below
are instance-local and deployment-wide at the same time. Deployment manifests
pin one replica and the exclusive deployment lease makes any second instance
fail readiness before it can accept data-plane or bootstrap work.

- An application key is at most 512 encoded bytes. Verification permits 20
  attempts per second with a burst of 40 per source-network bucket and 1,000 per
  second with a burst of 2,000 per deployment, with at most 256 verifications in
  flight on the sole serving instance. Excess work is rejected before
  password-hash or signature work.
- Initial bootstrap permits 5 attempts per minute with a burst of 5 per source
  and 20 attempts per minute with a burst of 20 per deployment, with one
  bootstrap verification in flight on the sole serving instance. Success still
  uses the atomic one-time claim described above.
- The application-key verifier cache holds at most 10,000 entries: successful
  verification entries expire within 60 seconds, negative entries within 5
  seconds, and revocation invalidates a positive entry immediately. Cache keys
  are keyed digests, never raw credentials. Source rate-limit state is separately
  bounded to 50,000 entries with a 10-minute idle expiry.
- An application assertion is at most 16 KiB encoded and 32 KiB decoded, with a
  maximum JSON nesting depth of 8, 64 claims, and 4 KiB per string claim. Only
  configured algorithms and pre-registered verification keys are considered.
- Request-time verification performs no DNS, HTTP, JWKS, schema, provider, or
  other remote fetch. It uses only bounded local caches and the configured
  credential store; required keys and metadata are refreshed out of band before
  they become eligible for verification.

All limit failures return the same bounded authentication failure shape and emit
only rate-limited, cardinality-bounded evidence. Implementations may choose lower
deployment-specific limits but cannot raise these maxima without a reviewed
threat-model revision and abuse-test update.

| ID | Threat | Required controls | Residual risk |
| --- | --- | --- | --- |
| `TH-ID-01` | Application impersonates a local subject | Namespace subjects by application; authenticate application; construct `PrincipalContext` once | A compromised application can impersonate its own subjects |
| `TH-ID-02` | Mutable claim merges two people | Link only opaque local keys or OIDC `(issuer, subject)`; audit manual links | Compromised issuer or operator can assert/link incorrectly |
| `TH-CRED-01` | Credential class confusion or forwarding | Typed classes, separate parsers/storage/header builders, outbound allowlist | Adapter implementation defect |
| `TH-CRED-02` | Database/log leak yields bearer secrets | One-time display, verifier/encryption at rest, pre-export redaction, rotation/revocation | Live-process or operator compromise |
| `TH-NET-01` | Endpoint reaches internal/metadata service | HTTPS/global default, special-range denial, exact private allowlist, egress policy | Compromised explicitly allowlisted private service |
| `TH-NET-02` | DNS rebinding changes destination | Resolve/validate at dial, pin validated address per connection, revalidate new connections | Compromised resolver can deny service |
| `TH-NET-03` | Redirect exfiltrates credential | Default deny, same-origin bounded exception, revalidate each hop, never cross-origin auth | Provider origin itself receives its bound credential by design |
| `TH-RESP-01` | Malicious/oversized/compressed response exhausts resources | Encoded/decoded/event/depth bounds, deadlines, cancellation, bounded diagnostic capture | Work within configured bounds can still consume capacity |
| `TH-RESP-02` | Truncated or malformed stream looks successful | Canonical terminal requirement and stable protocol classifications | Provider can return semantically poor but valid content |
| `TH-BOOT-01` | Unclaimed deployment is remotely seized | Loopback default, one-time bootstrap, no default password, fail closed | Host-level attacker can control bootstrap inputs |
| `TH-LOG-01` | Secrets/content escape through observability | Owning-package redaction, bounded schemas, canary tests, restricted audit access | Authorized diagnostic capture deliberately increases exposure |
| `TH-AVAIL-01` | Authentication or security controls become an unbounded DoS surface | Enforce the application-key, bootstrap, cache, sole-instance concurrency, assertion-parsing, and no-request-time-fetch bounds above; reject excess work before expensive verification and reject a second serving instance | Distributed valid-looking traffic within the limits can exhaust provisioned capacity |

## Verification obligations

| Requirement | Later automated evidence |
| --- | --- |
| `SEC-ID-001` Application subjects are unique by `(application_id, subject_id)` | Unit/property tests generate colliding subject IDs across applications and assert isolation |
| `SEC-ID-002` Only `(issuer, subject)` or audited manual evidence links principals | OIDC fixtures mutate email/username while preserving/changing subject and assert link behavior |
| `SEC-ID-003` Handlers use typed `PrincipalContext`, not raw identity headers | Middleware/handler tests inject conflicting headers and validated context |
| `SEC-ID-004` Application assertions cannot add scopes or cross namespaces | Assertion fixtures vary issuer, audience, signature, time, replay ID, subject, and requested scopes |
| `SEC-CRED-001` Credential classes cannot substitute for one another | Table-driven authentication tests present every class at every boundary |
| `SEC-CRED-002` Secrets are one-time, verifier/encrypted at rest, scoped, rotatable, and revocable | Repository/integration tests inspect persistence, rotation overlap, cache invalidation, and audit events |
| `SEC-CRED-003` Inbound credentials never appear upstream | Mock-provider tests capture all outbound headers, query values, redirects, and retries |
| `SEC-NET-001` Only admitted schemes/origins/addresses can be dialed | Table-driven URL/IP tests cover IANA special ranges, alternate spellings, IPv4-mapped IPv6, and private exceptions |
| `SEC-NET-002` DNS and redirects cannot escape admission policy | Deterministic resolver/dialer tests change answers between validation, connection, redirect, and retry |
| `SEC-NET-003` Metadata services remain unreachable | Integration fixtures emulate IPv4/IPv6 and hostname metadata endpoints under every exception mode |
| `SEC-RESP-001` Encoded, decoded, header, JSON, and SSE limits fail closed | Mock-provider fault matrix crosses each limit by one unit and asserts cleanup/classification |
| `SEC-RESP-002` Early EOF and malformed SSE never count as completion | Streaming fixtures inject every termination/framing fault and assert terminal evidence |
| `SEC-BOOT-001` Bootstrap is local, one-time, expiring, and race safe | Concurrent bootstrap tests assert exactly one operator creation and permanent secret invalidation |
| `SEC-LOG-001` Routine signals and errors contain no prohibited data | Canary-secret tests inspect logs, traces, metrics, audit, notifications, and client responses |
| `SEC-AUDIT-001` Security lifecycle and administrative mutations are attributable | Integration tests require actor, target, action, result, time, and correlation fields |
| `SEC-BROWSER-001` Browser artifacts contain no long-lived gateway or provider credential | Built-asset scans and BFF integration tests inspect storage, responses, and outbound calls |
| `SEC-AVAIL-001` Authentication verification remains within the declared availability bounds | Abuse tests exceed each key/assertion size, parse depth/count, per-source/deployment rate, sole-instance concurrency, cache-entry/TTL, and bootstrap limit; deployment tests prove a second instance cannot become ready; instrumented resolvers/transports prove zero request-time remote fetches and bounded work under randomized invalid input |

Security-sensitive requirements block the dependent implementation issue until
their corresponding test fixture exists. Exceptions require a linked decision,
documented owner, expiry, compensating control, and regression test.

## Residual risks

- A trusted operator controls route configuration, identity links, scopes, and
  diagnostic access and can intentionally weaken the deployment.
- A compromised application can impersonate its application-local subjects and
  expose any content it legitimately submits or receives.
- A provider sees requests, credentials, and metadata intentionally sent to its
  bound route and may retain or misuse them under its own policy.
- TLS, DNS, operating-system, database, secret-store, dependency, or host
  compromise can bypass application-layer controls.
- Semantic prompt injection, model quality, remote-model identity, and provider
  truthfulness cannot be proven by endpoint validation.
- Traffic analysis may reveal timing, volume, model group, or route information
  even when bodies and secrets are redacted.

## Non-goals

- Public multi-tenant SaaS isolation or hostile-tenant billing enforcement.
- Direct end-user authentication at the v0 data plane.
- Treating application-supplied local identity as independently verified.
- Executing tools, browsing provider-returned URLs, or rendering provider HTML.
- Malware scanning or data-loss prevention for prompt/completion content.
- Proving which model or hardware served a remote response.
- Protecting against a fully compromised gateway host or authorized malicious
  operator.

## Standards and guidance

- [OpenID Connect Core 1.0](https://openid.net/specs/openid-connect-core-1_0-18.html)
  defines stable issuer-scoped subject identifiers.
- [OWASP SSRF Prevention Cheat Sheet](https://cheatsheetseries.owasp.org/cheatsheets/Server_Side_Request_Forgery_Prevention_Cheat_Sheet.html)
  covers allowlisting, redirects, and DNS-pinning risks.
- [IANA number registries](https://www.iana.org/numbers/registries) are the source
  for current IPv4 and IPv6 special-purpose ranges.
- [AWS EC2 instance metadata](https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/instancedata-data-retrieval.html)
  and [Google Compute Engine metadata](https://docs.cloud.google.com/compute/docs/metadata/querying-metadata)
  document metadata names and IPv4/IPv6 endpoints that remain denied.
- [Go `net/http` documentation](https://pkg.go.dev/net/http) defines redirect,
  proxy, decompression, timeout, and transport behavior that implementations must
  configure deliberately rather than inherit accidentally.
