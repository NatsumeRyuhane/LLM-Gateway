# ADR 0001: Use Go for the gateway

- Status: Accepted
- Date: 2026-08-10
- Issue: [#3](https://github.com/NatsumeRyuhane/LLM-Gateway/issues/3)

## Context

The gateway is a long-running HTTP proxy whose correctness depends on streaming,
cancellation, bounded concurrency, connection reuse, timeouts, and predictable
resource ownership. It must support race testing, fuzzing, reproducible builds,
and simple self-hosted deployment. The project is also intended to demonstrate
systems-oriented engineering rather than only framework assembly.

Python/FastAPI would reduce initial language-learning cost and remain suitable
for offline probe analysis. TypeScript would share a language with the dashboard
but would not remove the need to learn streaming and resource-lifecycle details.

## Decision

Implement the production gateway and deterministic mock provider in Go. Start
with Go 1.26.5 and standard-library HTTP primitives. Python may be used later for
offline experiments when it creates a real analysis boundary, not as a second
production service by default.

## Consequences

Positive consequences:

- `context.Context` provides a conventional cancellation/deadline path.
- The standard HTTP stack supports streaming without a framework runtime.
- The race detector, fuzzing, benchmarks, and profiling are first-party tools.
- A single compiled binary simplifies container and self-hosted deployment.
- Explicit concurrency and error handling make reliability behavior reviewable.

Costs and risks:

- The maintainer is learning Go and must be able to explain generated and
  AI-assisted code.
- Incorrect goroutine, channel, timer, response-body, or transport ownership can
  create subtle leaks and races.
- Go's compact syntax does not replace deliberate protocol and state-machine
  design.
- Some health research may be faster to prototype in Python.

## Verification obligations

Every concurrency-bearing change must document ownership and cancellation. CI
will run formatting, vet/static analysis, unit tests, race tests, vulnerability
analysis, and builds. Stream parsers and state machines will receive fuzz or
property coverage. Representative workloads will be profiled before scale claims
are made.

The maintainer should be able to explain:

- goroutine creation and termination;
- channel ownership and closing;
- synchronization and race avoidance;
- HTTP transport and response-body reuse;
- stream flushing and slow-consumer backpressure;
- error wrapping/classification;
- allocations in the streaming hot path.

## Reversal signals

Reconsider the language boundary if measured development risk prevents a correct
vertical slice, if a required provider SDK cannot be implemented safely without
disproportionate effort, or if health-analysis workloads demonstrate a clear
need for a separately deployed Python component. A learning curve by itself is
not evidence for a rewrite.
