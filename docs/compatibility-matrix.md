# V0 OpenAI compatibility matrix

Status: Accepted for M0 under [issue #6](https://github.com/NatsumeRyuhane/LLM-Gateway/issues/6)

This matrix defines the public v0 wire surface. It is narrower than the current
OpenAI API by design. Compatibility means a listed semantic is preserved or
rejected explicitly; it never means unknown fields are forwarded blindly.

## Status legend

| Status | Meaning |
| --- | --- |
| Supported | Required gateway behavior for every eligible v0 route |
| Conditional | Accepted only when the selected route has tested capability support |
| Unsupported | Intentionally outside the product contract; reject explicitly |
| Deferred | Known future candidate, not implemented in v0; reject explicitly |

Unsupported and deferred input returns a stable invalid-request or
`capability.unsupported` error before provider dispatch. Unknown fields are
invalid. No field is silently ignored, approximated through prompting, or passed
through to a provider.

## Endpoints

| Public endpoint | Status | V0 behavior |
| --- | --- | --- |
| `GET /v1/models` | Supported | Lists authorized gateway model groups/aliases, not provider credentials or private endpoint data |
| `POST /v1/chat/completions` | Supported | One-choice buffered and SSE text generation contract |
| `POST /v1/responses` | Deferred | Planned after Chat Completions fixtures stabilize |
| `/v1/embeddings` | Deferred | Canonical embedding contract not yet defined |
| `/v1/images/*` | Deferred | Image generation/edit contract not yet defined |
| `/v1/audio/*` | Deferred | Speech/transcription contract not yet defined |
| `/v1/realtime*` | Deferred | WebSocket/realtime lifecycle not yet defined |
| `/v1/moderations` | Unsupported | Gateway does not provide a moderation product surface in v0 |
| `/v1/assistants*`, `/v1/threads*` | Unsupported | Gateway is not an agent state/runtime service |
| `/v1/batches`, `/v1/files`, `/v1/uploads` | Unsupported | Durable OpenAI platform resources are not proxied |
| Fine-tuning, vector-store, container, eval, admin endpoints | Unsupported | Provider/platform management is outside the data plane |

## HTTP and gateway extensions

| Element | Status | V0 behavior |
| --- | --- | --- |
| `Authorization: Bearer <gateway credential>` | Supported | Authenticates the registered application; never forwarded upstream |
| `Content-Type: application/json` | Supported | Required for Chat Completions request bodies |
| `Accept: application/json` | Supported | Buffered response |
| `Accept: text/event-stream` or `stream: true` | Supported | SSE response when an eligible streaming route exists |
| `X-Gateway-Conversation-ID` | Supported | Optional opaque application-scoped attribution; never forwarded or used as a metric label |
| `X-Gateway-Run-ID` | Supported | Optional opaque application-scoped attribution; never forwarded or used as a metric label |
| `X-Gateway-Request-ID` response header | Supported | Gateway correlation ID |
| `X-Gateway-Attempt-ID` response header | Supported | Opaque final attempt ID for authorized caller |
| `X-Gateway-Route-ID` response header | Supported | Opaque selected route ID for authorized caller |
| Inbound provider authentication/proxy/forwarding headers | Unsupported | Stripped and rejected when security-sensitive; adapter constructs its own allowlisted upstream headers |
| OpenAI organization/project headers | Unsupported | Gateway application scope replaces OpenAI account selection |

For streaming success, the gateway buffers the first valid model event before
committing route/attempt response headers. Error responses may contain a request
ID but do not disclose private route or endpoint information.

## Chat Completions request

| Field | Status | Canonical behavior |
| --- | --- | --- |
| `model` | Supported | Required authorized gateway model group or concrete route alias |
| `messages` | Supported | Required ordered message array |
| `stream` | Supported | Defaults to `false`; `true` requires streaming capability |
| `stream_options.include_usage` | Supported | Requests a final usage chunk when available; interrupted streams may lack it |
| `tools` with `type: function` | Conditional | Requires function-tool capability and supported schema subset |
| `tool_choice: none/auto/required` | Conditional | Each mode is a separate route capability |
| Named function `tool_choice` | Conditional | Requires specific-tool-choice capability and a declared matching function |
| `parallel_tool_calls` | Conditional | Explicit value must be preserved; route must declare support |
| `response_format: text` | Supported | Default canonical output |
| `response_format: json_object` | Conditional | Complete output must parse as a JSON object |
| `response_format: json_schema` | Conditional | Complete output must validate; strict mode requires native strict support |
| `temperature` | Conditional | Exact explicit value preserved or route excluded |
| `top_p` | Conditional | Exact explicit value preserved or route excluded |
| `seed` | Conditional | Preserved only for a route that declares seed support; no determinism guarantee is invented |
| `stop` | Conditional | String or bounded string array normalized without semantic change |
| `max_completion_tokens` | Conditional | Canonical `max_output_tokens`; route limit also applies |
| `max_tokens` | Conditional | Legacy alias for `max_completion_tokens`; both together are invalid |
| `n` | Supported | Must be absent or exactly `1`; other values reject |
| `frequency_penalty`, `presence_penalty` | Deferred | Rejected until cross-provider semantics and fixtures exist |
| `logit_bias` | Deferred | Tokenizer-dependent semantics are not canonicalized in v0 |
| `logprobs`, `top_logprobs` | Deferred | Token-level response contract not defined |
| `modalities`, `audio` | Deferred | Text output only |
| `prediction` | Deferred | Provider-specific optimization behavior not canonicalized |
| `reasoning_effort` and reasoning controls | Deferred | Model-family-specific semantics not canonicalized |
| `service_tier` | Unsupported | Provider billing/scheduling tier cannot bypass route policy |
| `store` | Unsupported | Gateway does not opt requests into provider storage through the public v0 API |
| `metadata` | Deferred | Not accepted as identity/attribution; extension headers are canonical |
| `user`, `safety_identifier` | Deferred | Provider user identifiers require a separate privacy contract |
| `web_search_options` or hosted tools | Unsupported | Gateway does not expose provider-hosted tool execution in v0 |
| Deprecated `functions`, `function_call` | Deferred | Use `tools` and `tool_choice`; no implicit rewrite in v0 |

## Message and content fields

| Field or role | Status | V0 behavior |
| --- | --- | --- |
| `developer` role | Conditional | Preserved only by routes declaring developer-role support |
| `system` role | Conditional | Preserved exactly; never merged with developer messages silently |
| `user` role | Supported | Text input |
| `assistant` role | Supported | Text/refusal/tool-call history |
| `tool` role with `tool_call_id` | Conditional | Requires tools capability and a prior matching assistant tool call |
| Deprecated `function` role | Deferred | Rejected in v0 |
| Message `name` | Conditional | Bounded and preserved only when route declares participant-name support |
| String `content` | Supported | Normalizes to one canonical text part |
| Array `{type: text, text}` content | Supported | Ordered text parts preserved |
| Image URL/base64 content parts | Deferred | Multimodal canonical model not yet defined |
| Audio, file, refusal input, or provider-specific parts | Deferred | Rejected explicitly |
| Assistant `tool_calls` history | Conditional | IDs, order, function names, and exact argument strings preserved |
| Assistant `refusal` history | Deferred | Output refusal is supported; refusal as input history is not yet canonicalized |

## Function tools and structured output

| Semantic | Status | V0 behavior |
| --- | --- | --- |
| Function name and description | Conditional | Bounded, unique names; descriptions preserved |
| Function `parameters` JSON Schema | Conditional | Must parse and fit supported size/depth/subset |
| Function `strict` | Conditional | Requires native strict function-schema capability |
| Multiple declared functions | Conditional | Preserved within configured count/size bounds |
| Multiple returned tool calls | Conditional | Requires route capability; calls remain independently indexed |
| Incremental tool arguments | Conditional | Exact ordered fragments assembled under bounds |
| Tool argument syntax validation | Supported when tools used | Complete argument string must be valid JSON object before completion |
| Tool argument schema validation | Supported when schema supplied | Recorded separately from syntax; client still authorizes/executes tool |
| Gateway tool execution | Unsupported | Gateway never executes model-selected functions |
| Prompt-based tool/JSON emulation | Unsupported | Does not count as capability preservation |
| JSON-object final validation | Supported when requested | Invalid JSON becomes `protocol.invalid_structured_output` |
| Strict JSON-Schema final validation | Supported when requested | Invalid schema output is a typed failure; no repair |
| Streaming strict structured output | Conditional | Partial deltas may be visible; final validation failure cannot replay |

## Buffered response

| Field | Status | V0 behavior |
| --- | --- | --- |
| `id` | Supported | Stable gateway response ID |
| `object` | Supported | `chat.completion` |
| `created` | Supported | Gateway response creation timestamp |
| `model` | Supported | Requested gateway model group/alias; provider model remains attempt evidence |
| `choices` | Supported | Exactly one element at index 0 |
| `choices[0].message.role` | Supported | `assistant` |
| `choices[0].message.content` | Supported | Text or null when tool calls/refusal require it |
| `choices[0].message.refusal` | Conditional | Preserved when provider and adapter declare refusal support |
| `choices[0].message.tool_calls` | Conditional | Complete canonical function calls only |
| `finish_reason: stop/length/tool_calls/content_filter` | Conditional | Each route declares supported reasons; unknown reason rejects |
| `finish_reason: function_call` | Deferred | Deprecated legacy form is not emitted |
| `usage.prompt_tokens` | Conditional | Canonical input tokens when reported/estimated |
| `usage.completion_tokens` | Conditional | Canonical output tokens when reported/estimated |
| `usage.total_tokens` | Conditional | Validated total when semantics are known |
| Cached/reasoning usage details | Conditional | Emitted only with declared semantics and provenance |
| `system_fingerprint` | Deferred | Provider-specific evidence is not part of the public v0 response |
| Provider-specific response fields | Unsupported | Not passed through |

Estimated usage is distinguishable in gateway extensions/accounting evidence and
is never mislabeled as provider-reported. Missing usage does not cause replay of
a successful generation.

## Streaming response

| Element | Status | V0 behavior |
| --- | --- | --- |
| `Content-Type: text/event-stream` | Supported | SSE with bounded incremental parsing and flushing |
| `data: {chat.completion.chunk}` | Supported | Valid OpenAI-compatible chunk serialization |
| Stable chunk `id`, `model`, `created`, `object` | Supported | Gateway-owned values remain consistent for one response |
| Initial assistant role delta | Supported | May accompany the first model event |
| Ordered content/refusal deltas | Supported/Conditional | Never reordered or merged across attempts |
| Tool-call `index`, `id`, `name`, argument deltas | Conditional | Arbitrary provider chunking normalized to ordered canonical events |
| `finish_reason` chunk | Supported | Emitted only after output/tool/structured validation permits completion |
| Final empty-choice usage chunk | Supported when requested/available | Precedes `[DONE]`; all earlier chunks carry null/absent usage |
| `data: [DONE]` | Supported | Emitted only for canonical completion |
| SSE comments/keepalive | Deferred | Gateway keepalive format not part of v0 compatibility contract |
| Provider event names or raw chunks | Unsupported | Never passed through directly |
| Error after visible output | Supported as partial failure behavior | End stream without false `[DONE]`; record typed terminal evidence; never retry |

## Errors

Public errors use an OpenAI-compatible `{error: {message, type, code, param}}`
shape plus a correlation ID where compatible. The stable gateway code and safe
message come from the [failure taxonomy](failure-taxonomy.md).

| Condition | V0 result |
| --- | --- |
| Unknown field or invalid type/value combination | `client.invalid_request` before dispatch |
| Explicit but unsupported/deferred semantic | `capability.unsupported` before dispatch or no eligible route |
| No route preserves all required capabilities | `policy.no_eligible_route` |
| Provider transport/status failure | Typed `upstream.*` error with safe translation |
| Malformed/invalid provider response | Typed `protocol.*` error |
| Failure after visible output | Partial terminal evidence; no automatic replay or mixed-attempt bytes |
| Client cancellation/disconnect | `client.cancelled`; upstream context cancelled |

Provider credentials, raw provider bodies, private endpoint data, stack traces,
and unbounded provider messages never appear in public errors.

## Conformance rule

An adapter/route is eligible for a conditional row only after the corresponding
`gateway.adapter.v0` positive and negative fixtures pass. Operator declarations
can disable a capability but cannot create support that conformance evidence did
not establish.

This matrix is versioned with the provider contract suite. A row changing from
unsupported/deferred to conditional/supported requires fixtures and release
notes; changing a supported semantic incompatibly requires a new public API or
adapter-contract version.

## Reference

The wire names and chunk/tool/usage shapes track the
[official OpenAI Chat API reference](https://developers.openai.com/api/reference/resources/chat).
The gateway's support status and stricter safety semantics are defined here, not
inferred from whichever provider happens to accept a field.
