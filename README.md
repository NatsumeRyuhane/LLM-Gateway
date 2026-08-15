# Adaptive LLM Gateway

Adaptive LLM Gateway is a self-hostable reliability and routing layer for
OpenAI-compatible language-model providers. It is designed to preserve the
streaming and agent semantics clients rely on while making provider failures,
fallback decisions, usage, and route health observable.

The project is currently in **M0: contract and engineering foundation**. The
initial milestone deliberately defines safety, protocol, reliability, security,
and observability contracts before implementing automatic failover.

## Product principles

- Prefer explicit, deterministic routing over opaque decisions.
- Never silently replay a request after client-visible output or a potentially
  actionable tool call.
- Treat missing or stale health evidence as `unknown`, not healthy.
- Preserve cancellation, streaming order, tools, structured output, and usage.
- Keep prompt and completion bodies out of telemetry by default.
- Treat engineering evidence—tests, traces, benchmarks, runbooks, and incident
  reports—as part of the product.

## Repository layout

- [`backend/`](backend/README.md) — Go gateway, control API boundaries, and the
  standalone mock-provider process.
- `frontend/` — React, TypeScript, and Tailwind workspace containing the
  gateway-aware reference application and operator dashboard.
- `tests/` — cross-service end-to-end, load, replay, and fault fixtures.
- `deploy/` — containers and local observability stack.
- `docs/` — requirements, architecture, ADRs, reliability contracts, and
  runbooks.

Start with [the v0 product contract](docs/requirements.md),
[the architecture](docs/architecture.md),
[the threat model](docs/threat-model.md), and
[the active work tracker](docs/TODO.md).

Protocol implementers should also read [the canonical protocol](docs/protocol.md)
and [the v0 compatibility matrix](docs/compatibility-matrix.md).

Reliability-test work must follow
[the deterministic mock-provider contract](docs/mock-provider.md) and its
versioned fault-injection matrix.

Instrumentation and dashboard work must follow
[the observability, privacy, and cardinality contract](docs/observability.md).

Agents should start from [the shared context entrypoint](.agents/README.md), then
fetch the live GitHub Issue and Notion design. `.claude` points to the same
instructions; issue bodies and the synchronized design are intentionally not
copied into the repository agent directory.
