# Deterministic mock-provider contract

Status: Accepted implementation contract for
[issue #7](https://github.com/NatsumeRyuhane/LLM-Gateway/issues/7)

## Purpose and boundary

The mock provider is a reusable OpenAI-compatible upstream for deterministic
contract, integration, race, replay, and later benchmark workloads. It owns
synthetic upstream behavior and labeled ground truth. It does not own routing,
retry or fallback policy, health estimation, circuit breaking, active probes,
or silent-degradation detection.

The implementation lives in `backend/internal/mockprovider`. The standalone
`cmd/mock-provider` and in-process tests compose the same handler and immutable
profile catalog. Production gateway packages do not import or register it.

## Versioned fixture catalog

The embedded catalog is
`backend/internal/mockprovider/fixtures/v0/matrix.json`. Its schema is fixed by
the adjacent `schema.json` and identified by
`gateway.mock-provider.matrix.v0`. Profile identifiers and meanings are
append-only inside a schema version. Changing existing wire behavior, expected
classification, or ground-truth meaning requires a new catalog version.

Each profile records:

- a stable identifier, request mode, fixture behavior, and deterministic seed;
- the faithful injection layer: provider handler, transport harness, gateway
  consumer, or downstream client;
- exact wire behavior and ordered synchronization events;
- the expected immediate canonical code, domain, retry disposition, provider
  status, visibility boundary, and terminal evidence when observable today;
- a bounded ground-truth label and the milestone that owns later detection;
- metadata-only observation names that never contain prompts, completions, tool
  arguments, credentials, endpoints, or raw provider bodies.

An empty `expected.failure_code` means the current gateway should treat the
synthetic response as successful. For silent profiles, that is intentional: the
ground-truth label describes what the fixture generated, not what the gateway
has detected.

## Determinism

A scenario is created from a catalog version, profile ID, and signed 64-bit
seed. The same catalog, profile, normalized request, seed, and scenario-local
request ordinal produce the same logical response identifiers, chunks, and
failure sequence. Stateful recovery profiles keep their counters inside one
scenario instance. There is no package-global mutable profile, counter, timer,
or fault state.

Parallel tests create separate scenarios. Tests that exercise an ordered
failure/recovery sequence coordinate request admission explicitly so scheduler
ordering, rather than goroutine arrival timing, defines the ordinal.

## Activation

In-process tests select a profile and seed through the constructor. The
standalone process selects one embedded profile at startup with:

- `MOCK_PROVIDER_PROFILE`, defaulting to `success.buffered`; and
- `MOCK_PROVIDER_SEED`, defaulting to `1`.
- `MOCK_PROVIDER_STEP_DELAY`, defaulting to `250ms` for each gated stream
  chunk and bounded from `0s` through `30s`.

The v0 provider has no mutable HTTP control endpoint. Changing a standalone
scenario requires restarting the disposable process. Consequently there is no
control route that production gateway configuration could accidentally expose.

## Synchronization and lifecycle ownership

Profiles may publish these bounded lifecycle events:

- `request.received`;
- `response.headers_ready`;
- `response.chunk_ready`;
- `response.terminal_ready`;
- `request.cancelled`.

The handler owns no goroutine beyond the request goroutine. A scheduler wait is
always context-aware. Production composition uses real bounded delays;
correctness tests use a manual gate and release lifecycle steps directly.
Timeouts in tests are deadlock watchdogs, not correctness synchronization.

The request context owns cancellation. A stalled or gated response returns as
soon as that context ends, records only `request.cancelled`, and never emits a
later chunk. The HTTP server owns downstream socket closure; the handler stops
on write or flush failure.

## Injection layers

An HTTP handler can reproduce status, body, framing, latency, stall, EOF, tool,
schema, usage, and silent-semantic behavior. It cannot faithfully produce DNS
resolution failure, TLS verification failure, connection refusal, or a
downstream client disconnect. Those matrix rows name the transport or client
harness that owns injection and reuse the closest provider profile only where
the wire behavior is genuinely the same.

## Evidence and privacy

Mock observations contain only schema version, profile ID, seed, request
ordinal, lifecycle event, response mode, and the bounded ground-truth label.
They deliberately exclude request and response content, authorization headers,
provider credentials, endpoint URLs, raw errors, and user-controlled IDs.

Gateway assertions use the existing metadata-only request, decision, and
attempt evidence. Silent-degradation profiles must not synthesize a gateway
failure or health transition merely because the fixture knows its label.

## Extension procedure

1. Add a new profile row without changing existing row meaning.
2. Select the faithful injection layer and behavior kind.
3. Specify synchronization events before writing timing tests.
4. Add direct handler and adapter contract coverage.
5. Add vertical-slice evidence coverage for immediately observable behavior.
6. For silent behavior, assert successful transport plus ground truth only.
7. Run JSON validation, formatting, unit, race, static analysis, build,
   vulnerability, and repository policy checks.
