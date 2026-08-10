# Failure taxonomy

Status: M0 draft for [issue #4](https://github.com/NatsumeRyuhane/LLM-Gateway/issues/4)

## Purpose

The gateway needs stable, bounded failure classes for routing, client responses,
metrics, incident analysis, and provider contract tests. A transport status alone
is insufficient: HTTP 200 can contain malformed JSON, an incomplete stream, or
an invalid tool call, while a client cancellation is not a provider outage.

Every failed attempt records:

- one stable failure `code` from this document;
- the `domain` responsible for classification, not necessarily moral blame;
- whether client-visible output or an actionable tool call may have occurred;
- retry disposition and remaining retry budget;
- provider and route identifiers in the decision/attempt record;
- a redacted internal cause chain for operators;
- a stable, non-secret client-facing error type and message.

Unknown raw error strings, URLs, request IDs, user IDs, and provider response
bodies must not become metric label values.

## Retry dispositions

| Disposition | Meaning |
| --- | --- |
| `never` | The same logical request must not be retried automatically. |
| `pre_output_alternate` | Another eligible route may be attempted only before the visibility boundary and within the request budget. |
| `pre_output_same_or_alternate` | A bounded same-route retry or alternate route may be used before the visibility boundary; `Retry-After` and policy still apply. |
| `client_decides` | Output may have been visible. The gateway records the partial attempt and returns control to the client. |

Retry disposition is necessary but not sufficient permission. Routing policy,
deadline, quota, cost budget, attempt budget, route affinity, and capability
compatibility must also allow the next attempt.

Provider billing or compute may already have occurred even when no bytes reached
the client. Such an attempt can still be safe from duplicate client/tool effects,
but it is not free: usage and estimated duplicate cost remain recorded.

## Visibility boundary

A request crosses the automatic-retry boundary when any of these occurs:

- response body bytes representing model output are flushed to the client;
- a streaming content/tool-call event is emitted to the client;
- a complete tool call or other potentially actionable provider event is exposed;
- the gateway cannot prove that no such event was exposed.

HTTP headers that contain only gateway metadata do not themselves expose model
output, but implementations should delay committing a success response until an
upstream attempt is accepted. Ambiguity is resolved toward `client_decides`.

## Stable failure classes

### Client and authentication

| Code | Meaning | Retry | Gateway availability impact |
| --- | --- | --- | --- |
| `client.invalid_request` | Invalid JSON, fields, sizes, or mutually incompatible options | `never` | Excluded |
| `client.cancelled` | Client context cancellation or downstream disconnect | `never` | Excluded |
| `client.deadline_exceeded` | Client-supplied deadline elapsed | `never` | Excluded unless gateway violated a shorter internal deadline |
| `auth.missing_credential` | Gateway credential absent | `never` | Excluded |
| `auth.invalid_credential` | Credential invalid, expired, or revoked | `never` | Excluded |
| `auth.forbidden` | Authenticated caller lacks the required scope/ownership | `never` | Excluded |
| `quota.gateway_exceeded` | Gateway-owned application/user/run quota is exhausted | `never` | Excluded and reported separately |

### Policy and capability

| Code | Meaning | Retry | Gateway availability impact |
| --- | --- | --- | --- |
| `policy.unknown_target` | Requested route/group/model does not exist or is not visible | `never` | Excluded |
| `policy.no_eligible_route` | No route satisfies capability, privacy, trust, price, or administrative constraints | `never` | Routing-availability failure, not gateway-process failure |
| `policy.all_routes_open` | Eligible routes exist but all circuits are unavailable | `never` for the current decision | Client-completion failure; route availability |
| `capability.unsupported` | Requested semantic cannot be preserved by any selected adapter | `never` | Excluded from gateway availability; compatibility metric |
| `affinity.route_ineligible` | Affinity route no longer meets hard constraints | Continue policy evaluation | Not a terminal failure by itself |

### Gateway and storage

| Code | Meaning | Retry | Gateway availability impact |
| --- | --- | --- | --- |
| `gateway.overloaded` | Bounded local concurrency/queue capacity is exhausted | `pre_output_same_or_alternate` only if not admitted upstream | Included |
| `gateway.internal` | Invariant violation or unclassified internal error | `pre_output_alternate` only when proven safe | Included |
| `gateway.shutdown` | Instance is draining and cannot accept new work | Caller retry on another instance | Included according to deployment SLO |
| `storage.unavailable` | Required durable state cannot be read/written | Depends on operation and documented degraded mode | Included when it blocks the data plane |
| `telemetry.export_failed` | Exporter/collector rejected or could not receive telemetry | Never changes an in-flight model response | Observability SLI, not data-plane availability |

### Upstream transport

| Code | Meaning | Default retry before output | Gateway availability impact |
| --- | --- | --- | --- |
| `upstream.dns_failed` | Name resolution failed or produced a disallowed address | `pre_output_alternate` | Excluded; route availability |
| `upstream.connect_failed` | TCP connection could not be established | `pre_output_same_or_alternate` | Excluded; route availability |
| `upstream.tls_failed` | TLS negotiation or certificate validation failed | `pre_output_alternate` | Excluded; route availability |
| `upstream.timeout` | Upstream deadline elapsed before output | `pre_output_alternate` | Excluded; route performance/availability |
| `upstream.stream_stalled` | Inter-event deadline elapsed | Before output: alternate; after output: `client_decides` | Excluded; route performance/availability |
| `upstream.redirect_rejected` | Redirect violates endpoint security policy | `pre_output_alternate` | Excluded; route/security evidence |
| `upstream.response_too_large` | Header/body/event exceeds a configured bound | Before output: alternate; after output: `client_decides` | Excluded; route/protocol evidence |

### Upstream HTTP and policy

| Code | Meaning | Default retry before output | Gateway availability impact |
| --- | --- | --- | --- |
| `upstream.authentication_failed` | Provider rejected its provider credential | `pre_output_alternate` | Excluded; route configuration |
| `upstream.permission_denied` | Provider account/model access denied | `pre_output_alternate` | Excluded; route configuration |
| `upstream.rate_limited` | Provider returned 429 or equivalent | Honor `Retry-After`; normally alternate | Excluded; route availability |
| `upstream.server_error` | Provider returned 5xx or equivalent | `pre_output_same_or_alternate` | Excluded; route availability |
| `upstream.content_policy` | Provider rejected content under its policy | Alternate only when group policy explicitly permits policy-diverse fallback | Excluded and never counted as transport failure |
| `upstream.context_limit` | Provider rejected or demonstrably truncated context | Alternate only to a route meeting the declared context requirement | Excluded; capability correctness |
| `upstream.invalid_status` | Unexpected/unmapped HTTP status | `pre_output_alternate` | Excluded; protocol evidence |

### Protocol and semantic conformance

| Code | Meaning | Default retry | Gateway availability impact |
| --- | --- | --- | --- |
| `protocol.invalid_json` | Buffered body or SSE data contains invalid JSON | Before output: alternate; otherwise `client_decides` | Excluded; capability correctness |
| `protocol.invalid_sse` | SSE framing/event fields violate the accepted contract | Before output: alternate; otherwise `client_decides` | Excluded; capability correctness |
| `protocol.early_eof` | Stream ends without a valid terminal event | Before output: alternate; otherwise `client_decides` | Excluded; stream-completion SLI |
| `protocol.empty_output` | Successful status produces no valid output/terminal semantics | Before output: alternate; otherwise `client_decides` | Excluded; capability correctness |
| `protocol.invalid_event_order` | Events, choices, tool fragments, or usage arrive in an invalid order | Before output: alternate; otherwise `client_decides` | Excluded; capability correctness |
| `protocol.invalid_tool_call` | Tool-call identity, arguments, or schema are invalid | Before output: alternate; otherwise `client_decides` | Excluded; capability correctness |
| `protocol.invalid_structured_output` | Output violates an explicitly required schema/format | Before output: alternate; otherwise `client_decides` | Excluded; capability correctness |
| `protocol.usage_inconsistent` | Usage is absent or inconsistent with the adapter contract | Do not replay a successful generation solely for accounting | Accounting/capability evidence |
| `protocol.parameter_ignored` | Controlled evidence shows a required parameter was ignored | Policy-dependent; normally exclude route from matching capability | Capability correctness |

## Classification precedence

When multiple observations exist, classify the earliest causal failure that
determines the client outcome while retaining secondary evidence separately.

Examples:

- A client disconnect followed by upstream cancellation is `client.cancelled`,
  not `upstream.timeout`.
- Invalid SSE followed by EOF is `protocol.invalid_sse`, with early EOF as
  secondary evidence.
- A gateway deadline shorter than the client's deadline that expires because of
  an upstream stall is `upstream.stream_stalled`; failure to enforce the gateway
  deadline correctly is a separate `gateway.internal` defect.
- An HTTP 429 with malformed JSON remains `upstream.rate_limited`; the malformed
  error body is diagnostic evidence.

## Client error contract

Client responses expose a stable gateway error type, a safe message, correlation
ID, retryability hint, and partial-output/attempt metadata where compatible. They
must not expose provider credentials, gateway assertions, raw internal errors,
private endpoint addresses, stack traces, or unredacted provider bodies.

The provider's original status is stored in the attempt record and translated
according to the public compatibility contract. Gateway-internal distinctions
may be coarser on the data-plane wire than in operator evidence.

## Change policy

Codes are append-only within a control/data-plane API version. Renaming or
changing the meaning of a code requires a versioned migration and updated replay
fixtures. Dashboard grouping may evolve without altering stored raw codes.
