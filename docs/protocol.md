# Canonical protocol and provider-adapter contract

Status: Accepted for M0 under [issue #6](https://github.com/NatsumeRyuhane/LLM-Gateway/issues/6)

## Purpose and versioning

The canonical protocol is the boundary between the public OpenAI-compatible data
plane, routing policy, and provider-specific wire formats. Routing consumes only
canonical requests, capabilities, events, usage, and failure classifications.
Provider quirks terminate inside adapters.

The initial contract version is `gateway.adapter.v0`. A compatible change may
add optional fields or event metadata without changing existing meaning. A
change to required fields, ordering, defaults, or semantics requires a new
contract version and parallel conformance fixtures during migration.

The public v0 surface is intentionally smaller than the current OpenAI API. The
[compatibility matrix](compatibility-matrix.md) is authoritative for supported,
conditional, unsupported, and deferred wire fields. Unknown fields are rejected;
there is no implicit provider passthrough mode.

## Layer boundaries

```text
OpenAI-compatible HTTP/SSE
    -> public codec and validation
    -> CanonicalChatRequest + PrincipalContext
    -> routing and attempt policy
    -> provider adapter + validated route
    -> provider HTTP/SSE
    -> provider adapter
    -> CanonicalResponse or CanonicalEvent stream
    -> public codec
    -> OpenAI-compatible HTTP/SSE
```

The public codec owns wire names, defaults, aliases, status mapping, and SSE
serialization. Routing owns eligibility, deterministic selection, affinity,
attempt budgets, and retry decisions. An adapter owns provider request creation,
provider response parsing, and lossless translation to the canonical contract.

An adapter must not:

- select a route or decide whether another attempt is allowed;
- access application credentials or raw identity assertions;
- forward unknown public fields or inbound authentication headers;
- write client-visible bytes directly;
- execute tools or follow model-returned URLs;
- turn an unsupported semantic into prompt text or silently drop it;
- log prompt, completion, tool-argument, credential, or raw error-body content.

## Canonical request

```text
CanonicalChatRequest
  contract_version       gateway.adapter.v0
  request_id             stable gateway request ID
  target                 requested model group or authorized concrete route
  messages[]             ordered CanonicalMessage values
  tools[]                optional function declarations
  tool_choice            none | auto | required | function(name)
  parallel_tool_calls    optional explicit boolean
  response_format        text | json_object | json_schema(schema, strict)
  sampling               optional temperature, top_p, seed, stop[]
  max_output_tokens      optional positive bound
  stream                 boolean
  include_usage          boolean
  attribution            optional conversation_id and run_id
  deadline               effective request deadline
  required_capabilities  derived immutable capability set
```

`PrincipalContext` is supplied beside the request by authenticated middleware as
defined in the [threat model](threat-model.md). It is not populated from a model
request field and is never forwarded upstream.

### Messages and content

```text
CanonicalMessage
  role        developer | system | user | assistant | tool
  name        optional bounded participant name
  content[]   zero or more text parts in v0
  refusal     optional assistant refusal text in output
  tool_calls[] assistant function calls, when present in history
  tool_call_id required for tool-role messages
```

Message order is preserved exactly. Empty content is valid only where the role
contract permits tool calls without assistant text. Text is valid UTF-8 and is
bounded per message and request. Image, audio, file, and provider-specific
content parts are deferred and rejected in v0.

The public string form and an array containing only `{type: "text", text: ...}`
normalize to the same canonical text parts. No other coercion is allowed.

Adapters preserve `refusal` separately from `content`; they never convert one
to the other. Buffered provider refusal text maps directly to
`CanonicalMessage.refusal`. In streaming responses, ordered `refusal.delta`
values append only to that field, while `output_text.delta` values append only
to `content`. Refusal in request history remains deferred in v0 and is rejected
before canonical request construction.

### Function tools

```text
CanonicalFunctionTool
  name         stable bounded function name
  description  optional text
  parameters   JSON Schema object
  strict       optional boolean
```

V0 supports function tools only. Tool names are unique within the request.
Schemas must parse, remain within size/depth limits, and use the supported JSON
Schema subset. A route is eligible only when its tested capabilities preserve
the requested `tool_choice`, strictness, and parallel-call semantics.

Adapters never execute functions. Prompt-based emulation is not capability
support. If an adapter cannot preserve a requested tool semantic, it returns
`capability.unsupported` before dispatch or the route is excluded before
selection.

### Structured output

- `text` applies no JSON constraint.
- `json_object` requires a complete syntactically valid JSON object.
- `json_schema` requires a complete value that validates against the supplied
  schema; `strict=true` also requires native strict-schema support from the
  selected route.

The gateway validates the complete structured value even when the provider
claims native enforcement. It does not repair invalid JSON or schema output. In
streaming mode, deltas may become visible before complete validation; a final
validation failure is therefore an explicit partial-stream failure and cannot
trigger automatic replay.

### Parameters and defaults

The public codec records whether each optional parameter was absent or explicit.
An absent parameter may use the provider's declared default when the canonical
contract does not prescribe one. An explicit parameter requires tested adapter
support and must not be dropped, renamed to a weaker semantic, or replaced with
a different value without a documented canonical alias.

`max_tokens` and `max_completion_tokens` are public aliases for canonical
`max_output_tokens`; supplying both is invalid. `n` is supported only when equal
to 1. Sampling parameters are conditional capabilities because some model routes
do not accept them.

### Attribution extensions

Gateway-aware clients may send:

- `X-Gateway-Conversation-ID`
- `X-Gateway-Run-ID`

Each value is an opaque, application-scoped UTF-8 identifier with a 128-byte
maximum after normalization. Empty values, control characters, and multiple
conflicting values are rejected. These identifiers are stored in decision and
accounting records, never forwarded to providers, and never used as metric
labels. JSON `metadata` is not an identity fallback in v0.

## Provider routes and capabilities

```text
ProviderRoute
  route_id
  adapter_kind
  adapter_version
  adapter_contract_version
  endpoint_reference
  credential_reference
  upstream_model
  capabilities
  limits
  timeout_policy
  data_policy_tags
  administrative_state
```

Endpoint and credential references are resolved only by their owning
infrastructure packages. Routing sees opaque route IDs and bounded metadata, not
secret material or provider URLs.

Capabilities are structured declarations, not arbitrary strings:

```text
RouteCapabilities
  endpoint.chat_completions.buffered
  endpoint.chat_completions.streaming
  message.roles.{developer,system,user,assistant,tool}
  message.participant_name
  message.refusal_output
  content.text
  tools.function
  tools.function_schema_strict
  tools.choice.{none,auto,required,specific}
  tools.parallel
  structured.{json_object,json_schema,json_schema_strict,streaming}
  parameter.{temperature,top_p,seed,stop,max_output_tokens}
  usage.{buffered,streaming,cache_details,reasoning_details}
  finish_reason.{stop,length,tool_calls,content_filter}
```

Each capability has `supported`, `unsupported`, or `unverified` state plus the
contract-fixture version that established it. `unverified` is ineligible when the
request requires that capability. Operator configuration may downgrade a tested
capability but cannot upgrade one without new conformance evidence.

Limits include request bytes, message/tool/schema counts and sizes, context and
output limits, response/SSE bounds, and concurrency where relevant. A request
that exceeds a route limit is ineligible for that route; it is never truncated
silently.

## Canonical buffered response

```text
CanonicalChatResponse
  response_id
  request_id
  attempt_id
  route_id
  model
  created_at
  message
  finish_reason
  usage
```

The v0 response contains exactly one choice. The message may contain text,
refusal text, or one or more complete function tool calls. `finish_reason` is one
of `stop`, `length`, `tool_calls`, or `content_filter`; an unknown provider reason
is a protocol error until explicitly mapped by the adapter contract.

The buffered public codec serializes `CanonicalMessage.refusal` as
`choices[0].message.refusal` and keeps normal text in
`choices[0].message.content`. It does not concatenate refusal and content.

Buffered provider responses are fully parsed and validated before the public
status and body are committed. A failure before that boundary may be eligible for
fallback. The public response exposes opaque route and attempt IDs only to an
authorized caller; it never exposes endpoint or credential data.

## Usage model

```text
CanonicalUsage
  input_tokens
  output_tokens
  total_tokens
  input_details.cached_tokens      optional
  output_details.reasoning_tokens  optional
  provenance                       provider_reported | gateway_estimated | unavailable
  partial                          boolean
```

Token counts are non-negative and must satisfy known invariants. Provider usage
is preferred when the adapter contract establishes its meaning. Gateway
estimates are marked explicitly and never represented as provider-reported.
Missing usage does not turn an otherwise valid generation into a replay; it
produces accounting evidence such as `protocol.usage_inconsistent` according to
the provider contract.

For streaming Chat Completions, a requested usage summary is serialized before
`[DONE]` when available. An interrupted stream may lack final usage, consistent
with the [official OpenAI Chat API reference](https://developers.openai.com/api/reference/resources/chat).

## Canonical stream events

```text
response.started
output_text.delta
refusal.delta
tool_call.started
tool_call.arguments.delta
tool_call.completed
usage.updated
response.completed
response.failed
response.cancelled
```

Every event carries `request_id`, `attempt_id`, `route_id`, a monotonically
increasing per-attempt sequence, and a monotonic observation timestamp. IDs and
sequence values are gateway evidence; provider IDs are retained separately when
safe and necessary.

### State machine

```text
prepared
  -> started
      -> active
          -> completed
          -> failed
          -> cancelled
```

- `response.started` occurs exactly once and moves `prepared -> started`.
- The first delta or tool-call event moves `started -> active`.
- `output_text.delta` and `refusal.delta` contain non-empty ordered UTF-8 text.
- The public stream codec serializes those events as distinct `content` and
  `refusal` deltas; their accumulated values map to the corresponding buffered
  `CanonicalMessage` fields without cross-field coercion.
- Each tool-call index receives exactly one `tool_call.started`, zero or more
  ordered argument deltas, and exactly one `tool_call.completed`.
- `usage.updated` is optional, monotonic, and cannot reduce a prior count.
- Exactly one terminal event occurs. No event follows a terminal event.
- `response.completed` requires a recognized finish reason, complete tool calls,
  valid structured output, and canonical provider termination.
- EOF, handler return, or HTTP success alone is not canonical completion.

The public SSE codec emits valid Chat Completion chunks and a final `data: [DONE]`
only after `response.completed`. A `response.failed` after output visibility ends
the stream without a false success sentinel and records partial output evidence.

### Tool-call assembly and action boundary

Tool calls are keyed by canonical choice and tool index. An adapter may receive
the ID, name, and argument fragments across arbitrary provider chunks. It must
preserve fragment order, reject conflicting IDs/names or invalid event order,
and assemble the exact argument string under configured bounds.

`tool_call.completed` requires a stable ID, supported function name, and complete
JSON argument object. Schema validation is reported separately from syntactic
assembly because the client remains responsible for authorization and execution.

A tool call becomes potentially actionable when a complete
`tool_call.completed` representation is flushed to the client. The gateway does
not execute it. An internally completed but unexposed tool call does not create a
client side effect; once any tool fragment or other model output is exposed,
however, the earlier output-visibility boundary already forbids automatic retry.

### Visibility boundary

The client-visible automatic-retry boundary is the first flush containing any
model-derived role, text, refusal, tool ID/name/argument fragment, finish reason,
or actionable tool call. Gateway-only HTTP headers are not model output, but the
gateway buffers the first valid model event before committing a successful
stream so route and attempt headers cannot describe an abandoned attempt.

After visibility, all upstream, parsing, deadline, or downstream-write failures
are terminal for that client request. The gateway records partial evidence and
never mixes bytes from another attempt.

## Error contract and translation

```text
CanonicalError
  code                 stable failure-taxonomy code
  domain               client | auth | quota | policy | capability | affinity | gateway | storage | telemetry | upstream | protocol
  retry_disposition    never | pre_output_alternate | pre_output_same_or_alternate | client_decides
  safe_message
  http_status
  request_id
  attempt_id           optional
  route_id             optional and authorization-filtered
  output_visible
  tool_actionable
  provider_status      internal evidence only
  cause                redacted internal chain only
```

Adapters translate provider transport, status, body, and protocol failures to the
stable [failure taxonomy](failure-taxonomy.md). HTTP status is evidence, not the
classification by itself. Translation preserves blame domain and retry
disposition while removing credentials, endpoints, raw bodies, stack traces,
provider request IDs, and content from client messages and metric labels.

Unknown provider errors map to the narrowest safe existing code and retain a
redacted internal cause. Adding a new stable code requires updating the taxonomy
and contract fixtures; adapters do not invent public codes independently.

## Adapter interface obligations

A conforming adapter provides the equivalent of:

```text
Capabilities(route) -> RouteCapabilities
Prepare(route, CanonicalChatRequest) -> ProviderRequest | CanonicalError
ParseBuffered(route, ProviderResponse) -> CanonicalChatResponse | CanonicalError
ParseStream(route, ProviderResponse, EmitCanonicalEvent) -> CanonicalError?
```

`Prepare` is deterministic for the same route and canonical request. It builds a
new provider request for each attempt and cannot mutate the canonical request.
Provider credentials are attached only after endpoint validation and never enter
canonical types.

Parsers enforce provider status/content type, encoded and decoded bounds, JSON or
SSE framing, event ordering, field types, IDs, finish semantics, and usage
invariants. A provider's claim of success does not bypass validation.

## Attempt, fallback, and cleanup lifecycle

```text
selected -> prepared -> dispatched -> upstream_accepted
  -> output_visible -> terminal
```

Every attempt owns its context, deadline, response body, parser state, usage,
failure classification, and cleanup result. The decision record links attempts
in order and records why each next attempt was or was not permitted.

Fallback is allowed only when:

- the failure disposition permits it;
- no model output or actionable tool call was visible;
- request deadline and attempt/cost budgets remain;
- the next route satisfies every required capability and hard policy;
- cancellation has not occurred.

Client cancellation, downstream disconnect, or deadline ends the request and is
never transformed into fallback. Provider compute or billing may already have
occurred before visibility; usage and duplicate cost remain attempt evidence.

Response bodies are always closed. Reuse/draining is bounded and performed only
when safe; cleanup never blocks past the attempt deadline. All attempt goroutines
terminate before request ownership is released.

## Cancellation, deadlines, and slow consumers

- Derive each attempt context from the client request context and the smaller of
  client, gateway, and route deadlines.
- Propagate cancellation to DNS/dial, request upload, response read, parser,
  stream writer, and adapter-owned workers.
- Use bounded queues or synchronous pull-based flow. Never buffer an unbounded
  stream or the full completion merely for token counting.
- A slow downstream applies backpressure only within a bounded buffer. A write
  deadline cancels the attempt and closes the upstream body when the client does
  not consume data.
- Do not keep reading or billing provider output after confirmed downstream
  cancellation merely to finish a response.
- Distinguish client cancellation, client deadline, upstream timeout, idle stream
  timeout, downstream write failure, and gateway shutdown in terminal evidence.

## Versioned provider contract tests

Every adapter declares the highest conformance fixture version it passes. The
`gateway.adapter.v0` suite uses deterministic provider servers and canonical
goldens; real-provider smoke tests supplement but never replace it.

Required suites:

1. **Request translation:** roles, ordered text, tools, tool choice, strict
   schema, explicit sampling values, attribution stripping, and credential/header
   isolation.
2. **Buffered responses:** text, refusal, finish reasons, multiple tool calls,
   structured output, usage/details, unknown fields, and invalid types.
3. **Streaming:** arbitrary chunk boundaries, split UTF-8, role/content/refusal,
   interleaved tool indexes, split tool arguments, usage-before-terminal, and
   canonical `[DONE]` behavior.
4. **Malformed responses:** non-2xx bodies, wrong content type, invalid JSON/SSE,
   oversized headers/events/bodies, conflicting tool IDs, invalid order, unknown
   finish reason, early EOF, decompression overflow, and missing terminal event.
5. **Lifecycle:** cancellation before dispatch, before output, after output,
   client disconnect, slow consumer, every deadline, body close, goroutine exit,
   and connection reuse safety.
6. **Fallback boundary:** every failure point immediately before and after the
   first visible byte/tool fragment, asserting exact attempt count and no mixed
   output.
7. **Capability truthfulness:** each declared capability has a positive and
   negative fixture; undeclared/unverified semantics reject before dispatch.
8. **Security and privacy:** canary credentials/content never reach the wrong
   boundary or routine signals; provider credentials remain bound to the tested
   origin across redirects and retries.

Fixtures store provider wire input/output, expected canonical values/events,
expected failure code/domain/disposition, visibility state, cleanup assertions,
and contract version. A capability is not production-eligible until its fixture
passes under race detection where concurrency is involved.

The initial capability-selection manifests are versioned with this contract:

| Capability | Fixture | Required assertions |
| --- | --- | --- |
| `message.participant_name` | `tests/conformance/gateway.adapter.v0/capabilities/message-participant-name.json` | A supported route preserves the bounded name; unsupported and unverified routes reject before dispatch |
| `tools.function_schema_strict` | `tests/conformance/gateway.adapter.v0/capabilities/tools-function-schema-strict.json` | A supported route preserves function-tool `strict: true`; unsupported and unverified routes reject before dispatch |

`tools.function_schema_strict` applies only to strict function-tool parameter
schemas. It is independent of `structured.json_schema_strict`, which applies to
the requested response format; a request using both requires both capabilities.

## Normative references

- [OpenAI Chat API reference](https://developers.openai.com/api/reference/resources/chat)
  for the compatibility target's request, response, chunk, tool-call, finish,
  and usage shapes.
- [Compatibility matrix](compatibility-matrix.md) for the exact v0 public surface.
- [Failure taxonomy](failure-taxonomy.md) for stable error and retry semantics.
- [Threat model](threat-model.md) for identity, credentials, endpoint safety, and
  response bounds.
