# Project work tracker

GitHub Issues are the source of truth. This file records what has landed in the
repository so documentation and implementation do not drift from the public
backlog.

## M0 — Contract and engineering foundation

| Issue | Deliverable | Status |
| --- | --- | --- |
| [#1](https://github.com/NatsumeRyuhane/LLM-Gateway/issues/1) | V0 product contract and safety invariants | Complete on foundation branch; pending merge |
| [#2](https://github.com/NatsumeRyuhane/LLM-Gateway/issues/2) | Repository governance and CI quality contract | Active default-branch ruleset requires signed merge commits, resolved review threads, and all six CI checks |
| [#3](https://github.com/NatsumeRyuhane/LLM-Gateway/issues/3) | Foundational architecture decisions | Complete in contract pack |
| [#4](https://github.com/NatsumeRyuhane/LLM-Gateway/issues/4) | Failure taxonomy, SLIs, and availability contract | Complete in contract pack |
| [#5](https://github.com/NatsumeRyuhane/LLM-Gateway/issues/5) | Identity, credential, and endpoint threat model | Complete in contract pack |
| [#6](https://github.com/NatsumeRyuhane/LLM-Gateway/issues/6) | Canonical protocol and provider-adapter contract | Complete in contract pack |
| [#7](https://github.com/NatsumeRyuhane/LLM-Gateway/issues/7) | Deterministic mock-provider and fault matrix | Deferred; #9 provides the health-only process that later fault tests will extend |
| [#8](https://github.com/NatsumeRyuhane/LLM-Gateway/issues/8) | Observability, privacy, and cardinality contract | Complete in contract pack |
| [#9](https://github.com/NatsumeRyuhane/LLM-Gateway/issues/9) | Go modular-monolith backend scaffold | Implemented with two commands, explicit package boundaries, bounded lifecycle, health semantics, and CI checks |

## Later milestones

- M1: observable BYOK streaming vertical slice.
- M2: multi-route passive reliability and operator views.
- M3: active probes, health estimation, and drift baselines.
- M4: explainable policy routing and agent depth.
- M5: shared operation, hardening, and portfolio evidence.

Later milestone issues will be decomposed after M0 freezes the contracts they
depend on. This avoids speculative tickets whose acceptance criteria would be
invalidated by unresolved foundational decisions.
