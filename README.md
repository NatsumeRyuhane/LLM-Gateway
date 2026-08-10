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

## Planned repository layout

- `backend/` — Go gateway, control API, and deterministic mock provider.
- `frontend/` — React, TypeScript, and Tailwind operator dashboard.
- `tests/` — cross-service end-to-end, load, replay, and fault fixtures.
- `deploy/` — containers and local observability stack.
- `docs/` — requirements, architecture, ADRs, reliability contracts, and
  runbooks.

Start with [the v0 product contract](docs/requirements.md),
[the architecture](docs/architecture.md), and [the active work tracker](docs/TODO.md).

Agents should start from [the shared context entrypoint](.agents/README.md), then
fetch the live GitHub Issue and Notion design. `.claude` points to the same
instructions; issue bodies and the synchronized design are intentionally not
copied into the repository agent directory.
