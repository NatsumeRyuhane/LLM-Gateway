# ADR 0002: Start as a modular monolith

- Status: Accepted
- Date: 2026-08-10
- Issue: [#3](https://github.com/NatsumeRyuhane/LLM-Gateway/issues/3)

## Context

The gateway needs authentication, canonical protocol models, provider adapters,
routing, health estimation, accounting, storage, telemetry, and a control API.
These domains have different responsibilities but initially share one small
deployment, one database, and one maintainer. Splitting them into services would
add distributed failure modes before scale or isolation requirements are known.

## Decision

Build one deployable gateway process with explicit internal package boundaries.
Build the deterministic mock provider as a separate command in the same Go
module. Keep the frontend as an independent build that uses the control API.

Use one Go module under `backend/`. Do not add `go.work` until a second
independently versioned Go module exists for a demonstrated reason.

Domain packages own policy and narrow consumer-side interfaces. Infrastructure
packages implement those interfaces. Provider-specific models do not escape the
adapter package, and the dashboard does not access storage directly.

## Consequences

Positive consequences:

- Local changes remain atomic across routing, storage, and telemetry.
- Integration tests can exercise real boundaries without network orchestration.
- Deployment and debugging start with one process and one trace context.
- Package dependencies still make architectural drift visible.

Costs and risks:

- A process crash can affect both data and control planes.
- Poor package discipline could turn the monolith into a coupled codebase.
- CPU-heavy probes could contend with inference traffic if not isolated or
  bounded.
- Future extraction requires stable contracts and data ownership decisions.

## Guardrails

- No cyclic package dependencies.
- No general `utils`, `common`, or `shared` dumping-ground package.
- Domain policy does not import concrete database or telemetry exporters.
- Provider adapters implement one versioned contract suite.
- Background workers have explicit lifecycle, concurrency, and budget ownership.
- Test-only control surfaces cannot be enabled in production builds/configuration.

## Extraction criteria

Split a component only after evidence shows independently valuable scaling,
fault isolation, security, deployment cadence, or resource control. Document any
split in a new ADR with measured costs and migration/rollback plans.
