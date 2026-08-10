# V0 product contract

Status: M0 draft for [issue #1](https://github.com/NatsumeRyuhane/LLM-Gateway/issues/1)

## Product statement

Adaptive LLM Gateway is self-hosted middleware for applications that depend on
inexpensive or operationally inconsistent OpenAI-compatible providers. It adds
identity-aware attribution, evidence-backed route health, deterministic routing,
bounded failover, and operator visibility without becoming an agent runtime or
an end-user chat application.

The primary topology is:

```text
Provider <-> Gateway <-> Application <-> User or agent
```

The gateway and its registered applications operate within one administrative
security domain. An application authenticates to the gateway and may speak for
its own local users. Public, cross-organization gateway SaaS is not a v0 goal.

## Actors and boundaries

| Actor | Responsibility | Trust boundary |
| --- | --- | --- |
| Operator | Registers applications and routes; manages policy and incidents | Controls the gateway deployment |
| Application | Authenticates to the gateway and supplies local user or service context | Trusted to speak for its own subjects |
| Application user | Uses a chat or domain application | Does not authenticate directly to the data plane |
| Agent/service | Makes scoped inference calls with optional run identity | May act independently or for an application user |
| Provider | Serves a concrete model route | Untrusted for availability, protocol correctness, and declared behavior |

Provider credentials, gateway credentials, and identity assertions are separate
credential classes. None may be forwarded across the wrong boundary.

## V0 journeys

### Interactive streaming journey

1. An operator or trusted application registers an OpenAI-compatible endpoint,
   its credential, and one or more concrete model routes.
2. The application requests a namespaced model group through
   `POST /v1/chat/completions`, optionally providing a stable conversation ID.
3. The gateway authenticates the application, resolves its local subject,
   filters eligible routes, and records the policy and evidence snapshot.
4. The gateway selects a route deterministically and begins the upstream call.
5. Before client-visible output, a classified hard failure may consume a bounded
   retry/fallback budget. Once output is visible, the request is never silently
   replayed.
6. The application receives a valid terminal stream or an explicit partial/final
   error, the actual route, and an attempt correlation identifier.
7. The operator can inspect the decision, attempts, latency, usage, and failure
   classification without prompt or completion bodies being retained.

The proposed acceptance client is SillyTavern using Chat Completions streaming.
The exact supported client version will be pinned when M1 integration fixtures
are created.

### Agent tool-call journey

1. An agent sends a Chat Completions request with tools, structured-output
   requirements, and stable run identity.
2. The gateway selects only routes that explicitly declare the required
   capabilities and preserves tool definitions and selection semantics.
3. Streaming tool-call arguments remain ordered and attributable to one attempt.
4. A failure before visible output may fall back. A failure after partial output
   or a potentially actionable tool call is surfaced to the agent and cannot be
   replayed automatically.
5. Cancellation propagates through the gateway to the provider.
6. Usage, duration, attempts, and the terminal outcome are attributed to the
   application, credential, local subject or service, route, group, and run.

The v0 agent acceptance fixture will be a small protocol-level client. A named
agent SDK and full Responses API acceptance are deferred until the Responses
surface is implemented.

## V0 interfaces

The first compatibility target is intentionally narrow:

- `GET /v1/models`;
- `POST /v1/chat/completions`;
- buffered and Server-Sent Events responses;
- the tool-call and structured-output fields exercised by the acceptance
  journeys;
- request cancellation and downstream disconnect propagation.

Provider-specific fields and unsupported OpenAI fields must be rejected or
reported according to the compatibility contract. They must not disappear
silently. `POST /v1/responses` is a planned interface, not an M1 prerequisite.

Configuration, health evidence, usage queries, probes, policies, and audit
actions belong to a separate versioned control API.

## Core terms

- **Provider route:** one provider, model, endpoint/deployment, credential, and
  declared capability/privacy/cost metadata combination.
- **Model group:** a named routing policy containing eligible routes,
  constraints, preferences, fallback rules, and affinity behavior.
- **Application:** a registered trusted integration with independently scoped
  and revocable gateway credentials.
- **Application subject:** a local user or service identified within one
  application namespace.
- **Gateway principal:** an optional canonical accounting identity linked from
  one or more application subjects using approved evidence.
- **Attempt:** one request to one provider route. A client request can have more
  than one attempt only within its retry-safety boundary and budget.
- **Conversation/run affinity:** a preference to retain a previously selected
  route while it remains eligible and sufficiently healthy.

## V0 safety invariants

| Invariant | Verification method |
| --- | --- |
| No automatic replay after client-visible output or a potentially actionable tool call | Integration tests inject failure at every stream boundary and assert attempt count |
| Unsupported features fail explicitly | Provider contract tests submit every capability combination and assert typed errors |
| Sparse, stale, or absent evidence is `unknown` | Unit/property tests cover health state inputs and freshness boundaries |
| Fixed policy and evidence produce deterministic selection | Replay tests compare decisions and tie-breaking across repeated runs |
| Cancellation and client disconnects propagate upstream | Integration tests observe upstream request context cancellation and resource cleanup |
| Prompt, completion, and tool-argument bodies are not retained by default | Telemetry schema tests and redaction tests inspect every exported signal |
| Routing decisions are reconstructable | End-to-end tests require policy version, candidates, exclusions, evidence, affinity, and attempt history |
| Metric dimensions have bounded cardinality | Metric-registration tests reject prohibited identity/request labels |

## Product goals

- Allow compatible clients to adopt the gateway by changing base URL and
  credentials.
- Explain whether a route is unavailable, degraded, recovering, or unobserved
  using visible evidence and confidence.
- Preserve interactive streaming and agent protocol semantics.
- Attribute usage, cost, latency, errors, and decisions without conflating
  application, credential, user/service, principal, route, or group dimensions.
- Combine passive production evidence with isolated, budgeted active probes.
- Compare static, ordered-fallback, and health-aware affinity policies on the
  same reproducible workloads.

## Non-goals for the first release

- Training or fine-tuning models.
- Executing tools or orchestrating agent workflows.
- Perfectly measuring subjective response quality.
- Proving the identity of a remote model from black-box behavior.
- Supporting every provider extension before the adapter contract is stable.
- Operating a public multi-tenant inference service.
- Promising provider uptime that the gateway does not control.

## Open decisions and proposed defaults

| Decision | Proposed default | Must resolve by |
| --- | --- | --- |
| Exact SillyTavern acceptance version | Pin the current stable release when its fixture lands | M1 integration tests |
| Agent acceptance client | Protocol fixture first; select an SDK with Responses support in M4 | M4 planning |
| First two real provider routes | Choose two comparable OpenAI-compatible routes plus one deliberately heterogeneous fallback | M1 provider work |
| Conversation/run identity transport | Documented extension header plus compatible metadata where available | Protocol contract |
| Cross-application identity linking | Isolated application subjects in M1; OIDC/manual linking later | Security contract |
| Initial numeric SLOs | Measure M1 baselines before setting objectives | End of M1 |

The project maintainer owns these decisions. A proposed default allows dependent
design work to proceed, but it is not an accepted decision until its linked
issue or ADR records the outcome.

## Related contracts

- [Architecture](architecture.md)
- [ADR 0001: Go for the gateway](adr/0001-go-for-gateway.md)
- [ADR 0002: modular monolith](adr/0002-modular-monolith.md)
- Reliability, protocol, security, and observability documents are tracked in
  [the M0 work list](TODO.md).
