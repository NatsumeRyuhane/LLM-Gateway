# Reliability indicators and availability contract

Status: M0 draft for [issue #4](https://github.com/NatsumeRyuhane/LLM-Gateway/issues/4)

## Availability boundaries

The gateway cannot guarantee an upstream provider's uptime. Reliability reports
must keep three boundaries separate:

1. **Gateway service availability:** whether valid offered load avoids a
   gateway-owned failure.
2. **Routing availability:** whether at least one route satisfies hard policy and
   is eligible for selection.
3. **Client-perceived completion success:** whether the client receives a valid
   terminal buffered response or stream, regardless of failure domain.

No dashboard may label one of these measures simply `availability` without its
boundary and measurement window.

## Population definitions

For a measurement window `W`:

- `received`: requests that reach the gateway HTTP server.
- `valid_offered`: received requests with valid syntax, an accepted gateway
  credential, sufficient gateway quota, a visible requested target, and a client
  deadline that permits admission.
- `routed`: valid offered requests for which candidate evaluation begins.
- `started_stream`: routed streaming requests for which success headers/model
  events are committed to the client.
- `client_cancelled`: requests terminated by a downstream cancellation before a
  gateway/provider terminal outcome.

Every exclusion remains countable in a separate bounded failure dimension.
Requests do not disappear from all reports merely because they are excluded from
one SLI.

## Initial SLIs

### Gateway service success

```text
1 - gateway_owned_terminal_failures(valid_offered, W) / valid_offered(W)
```

Gateway-owned failures include `gateway.*` and a required `storage.unavailable`
condition. Provider, route-policy, client, authentication, and gateway-quota
outcomes are excluded from the numerator but shown alongside it.

### Routing availability

```text
routed_with_at_least_one_eligible_route(W) / routed(W)
```

Report the major exclusion reason separately: capability, privacy/trust, price,
administrative exclusion, circuit state, or stale/insufficient evidence policy.

### Client-perceived completion success

```text
valid_terminal_responses(valid_offered, W) /
  (valid_offered(W) - client_cancelled_before_terminal(W))
```

A valid streaming terminal response must satisfy the canonical stream contract.
An HTTP 200 with malformed events, early EOF, invalid structured output, or an
invalid tool call is not a completion success.

### Stream completion

```text
canonically_terminated_streams(W) /
  (started_stream(W) - client_cancelled_after_start(W))
```

Client cancellation remains reported as its own rate. It cannot improve route
health unless the upstream cancellation and cleanup contract also succeeds.

### Decision explainability coverage

```text
complete_decision_records(W) / routed(W)
```

A complete record contains the policy version, requested target, candidate set,
hard exclusions, evidence IDs/freshness, affinity result, deterministic score or
ordering inputs, selected route, and all attempts.

### Retry amplification

```text
total_upstream_attempts(W) / requests_with_an_upstream_attempt(W)
```

Segment by outcome and routing policy. This measure exposes retry storms and the
cost hidden by a superficially improved completion rate.

### Gateway dispatch latency

Time from admitted request to the start of the first upstream attempt. Report
p50, p95, and p99 by buffered/streaming and required capability class. This
isolates gateway decision overhead from provider time to first token.

### End-to-end latency

- Time to first model event for streaming requests.
- Time to valid terminal response.
- Inter-event latency and output throughput where event timestamps permit it.

Report p50, p95, and p99 by request-shape buckets. Provider comparisons must not
mix materially different input/output length, tool, schema, modality, or context
classes.

### Incident response indicators

- **Time to detect:** labeled incident start to the first qualifying health-state
  transition.
- **Time to divert:** labeled incident start to the last ordinary request sent to
  the affected route, excluding explicit probes and allowed in-flight affinity.
- **Time to recover:** labeled recovery start to the first state that permits the
  defined level of production traffic.
- **Failure exposure:** ordinary requests sent to the affected route after the
  labeled incident start.
- **False degradation:** degradation transitions without a labeled/confirmed
  material failure, reported per route-hour.
- **Missed degradation:** labeled incidents that never produce the required
  transition within the evaluation window.

Synthetic experiments use the injector's monotonic timestamp as the incident
boundary. Live incidents use the earliest corroborated observation recorded in
the incident timeline; retrospective changes must be audited.

### Probe overhead

Report probe requests, input/output tokens, estimated currency cost, provider
rate-limit share, and fraction of total gateway traffic per route. Probe traffic
is excluded from passive workload SLIs.

## Correctness objectives

These are invariants, not percentile targets or error-budget tradeoffs:

- Zero automatic retries after the visibility boundary.
- Zero client responses containing bytes from more than one upstream attempt.
- Zero silently dropped required capabilities.
- Zero secret or content-body values in default metrics.
- One recorded terminal classification for every admitted request.
- Deterministic decisions for identical policy and evidence snapshots.

Any violation is a correctness incident even if the rolling availability target
is otherwise met.

## Numeric objective policy

M0 does not claim an arbitrary percentage without workload evidence. M1 will
establish baselines using:

- a versioned workload corpus for buffered, conversational streaming, tool, and
  structured-output requests;
- normal, brownout, hard-failure, malformed-protocol, and slow-client profiles;
- one real provider and the deterministic mock provider;
- documented concurrency, request-shape distribution, duration, warm-up, and
  hardware/container resources;
- repeated runs with uncertainty rather than one best run.

Before M2 automatic routing is accepted, the project will set numeric objectives
for gateway service success, client completion, stream completion, dispatch
latency, explainability coverage, retry amplification, and detection/diversion
time. Each objective will state window, population, exclusions, and alerting
burn-rate policy.

Until then, numeric values shown in examples or local dashboards are baselines,
not SLOs or availability guarantees.

## Error budgets and incidents

An error budget exists only after a numeric objective is accepted. Gateway-owned
availability budget and provider/route reliability evidence use different
budgets. A provider outage cannot consume a gateway-process budget, but it can
consume a client-completion or route-availability budget.

Correctness-invariant violations bypass ordinary budget calculations and require
an incident review. Alerting must identify the failed boundary so an operator can
distinguish gateway remediation from route diversion or provider escalation.
