# Reliability indicators and availability contract

Status: Accepted for M0 under [issue #4](https://github.com/NatsumeRyuhane/LLM-Gateway/issues/4)

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
- `admitted`: valid offered requests accepted into the bounded data-plane work
  budget.
- `routed`: valid offered requests for which candidate evaluation begins.
- `upstream_attempted`: requests for which at least one upstream attempt starts.
- `started_stream`: routed streaming requests for which success headers/model
  events are committed to the client.
- `valid_terminal_responses`: buffered responses that validate completely plus
  streams whose required canonical terminal event validates.
- `client_cancelled_before_terminal`: requests terminated by downstream
  cancellation before any gateway/provider terminal outcome.
- `client_cancelled_after_start`: started streams terminated by downstream
  cancellation before a canonical terminal event.
- `ordinary`: non-probe requests admitted from normal client traffic.
- `probe`: requests carrying the gateway's synthetic-traffic marker from
  admission through attempt and accounting records.

Every exclusion remains countable in a separate bounded failure dimension.
Requests do not disappear from all reports merely because they are excluded from
one SLI.

## Measurement conventions

Each request or attempt records monotonic timestamps for `received`,
`admission_decided`, `admitted` when admitted, `attempt_start`,
`first_model_event`, every subsequent model event, and `terminal`. Wall-clock
timestamps remain available for correlation, but elapsed durations use the
monotonic component.

Unless a definition overrides them, every SLI:

- is calculated over a stated half-open window `W = [start, end)`. Admitted
  populations use `admitted`; pre-admission members of `valid_offered` use
  `admission_decided`; and the broader `received` population uses `received`;
- reports ordinary and probe traffic separately and excludes probes from passive
  workload views;
- is dimensioned by buffered/streaming mode, requested model group, selected
  route/provider, required capability class, terminal failure domain/code, and
  usage provenance where applicable;
- uses bounded request-shape buckets rather than prompt, user, request, or run
  identifiers;
- counts missing or corrupt required measurement fields as telemetry defects
  rather than silently dropping them.

For a ratio, an empty denominator produces `no_data`, not 0% or 100%.

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
ordinary_upstream_attempts(W) /
  ordinary_requests_with_an_upstream_attempt(W)
```

Probe amplification is reported separately. Segment by terminal outcome,
routing policy version, retry reason, and ordered attempt count. Preserve the
route sequence for traces and decision records, but never use that unbounded
sequence as a metric label. This measure exposes retry storms and the cost hidden
by a superficially improved completion rate.

### Gateway dispatch latency

For each `upstream_attempted` request `r`:

```text
dispatch_latency(r) = attempt_start(r, 1) - admitted(r)
```

Report p50, p95, and p99 over ordinary requests in `W`, dimensioned by
buffered/streaming mode, requested model group, and required capability class.
Requests that terminate before an upstream attempt are excluded from the
distribution and reported by terminal code. This isolates gateway decision
overhead from provider time to first model event.

### Latency and throughput

For a streaming request `r` whose client-visible attempt `a(r)` produces at
least one valid model event:

```text
upstream_time_to_first_model_event(r) =
  first_model_event(a(r)) - attempt_start(a(r))
```

This is upstream TTFT and is backed by
`llm_gateway_upstream_time_to_first_event_seconds`, whose boundary is dispatch
to the first valid model event. V0 does not label that instrument as end-to-end
TTFT. End-to-end request latency remains the `received`-to-`terminal` measure
below.

For any request with a gateway/provider terminal outcome:

```text
terminal_latency(r) = terminal(r) - received(r)
```

For consecutive client-visible model events `e[i-1]` and `e[i]`, `i >= 2`:

```text
inter_event_latency(r, i) = timestamp(e[i]) - timestamp(e[i-1])
```

Do not label inter-event latency as inter-token latency: providers may batch
multiple tokens into one event. When trustworthy provider-reported output tokens
exist, aggregate generation throughput for a population `P` is:

```text
generation_throughput(P, W) =
  sum(output_tokens(r), r in P and W) /
  sum(terminal(r) - first_model_event(r), r in P and W)
```

The denominator is expressed in seconds. Estimated-token throughput is a
separate series marked `usage_provenance=estimated`; it is never mixed with
provider-reported tokens.

Report p50, p95, and p99 only for per-request latency samples: dispatch latency,
upstream TTFT for the client-visible attempt, and terminal latency, using
request-shape buckets. Inter-event observations remain a separate event-pair
distribution. `generation_throughput(P, W)` remains one aggregate ratio of
population sums and does not produce percentile samples. Provider comparisons
must not mix materially different input/output length, tool, schema, modality,
or context classes. Upstream TTFT excludes requests with no model event;
terminal latency retains both successful and failed outcomes as a dimension;
throughput includes only canonically completed streams with positive generation
duration.

### Incident response indicators

For labeled incident `i` on route `r`:

- `diverted_transition(i)` is the first observed qualifying health/policy
  transition after `incident_start(i)` that makes route `r` ineligible for new
  ordinary requests;
- `recovery_start(i)` is the recorded monotonic timestamp at which recovery
  evaluation begins after mitigation or qualifying provider evidence; and
- `recovered_transition(i)` is the first observed qualifying transition at or
  after `recovery_start(i)` that makes route `r` eligible for new ordinary
  requests under the same detector and policy version.

```text
time_to_detect(i) = first_qualifying_degraded_transition(i) - incident_start(i)

time_to_divert(i) = diverted_transition(i) - incident_start(i)

time_to_recover(i) = recovered_transition(i) - recovery_start(i)

failure_exposure(i) = count(
  failed ordinary attempt starts on route r at or after incident_start(i)
  and before diverted_transition(i)
)

false_degradation_rate(r, W) =
  uncorroborated_degraded_transitions(r, W) / observed_route_hours(r, W)

missed_degradation_ratio(W) =
  labeled_incidents_without_transition_by_deadline(W) /
  labeled_incidents_eligible_for_evaluation(W)
```

Time to detect is `no_data` for a missed degradation. Time to divert and failure
exposure are `no_data` (or explicitly right-censored at the evaluation-window
end) until `diverted_transition` is observed; ordinary attempt counts never
stand in for that transition. Time to recover is likewise `no_data` or censored
until `recovered_transition` is observed. Explicit probes and documented
in-flight affinity are excluded from diversion and exposure, then reported
separately. Dimension the results by route/provider, incident class, detector
version, evidence source, and synthetic/live origin.

Synthetic experiments use the injector's monotonic timestamp as the incident
boundary. Live incidents use the earliest corroborated observation recorded in
the incident timeline; retrospective changes must be audited.

### Probe overhead

For each route `r`:

```text
probe_request_share(r, W) =
  probe_requests_dispatched_to_route(r, W) /
  all_requests_dispatched_to_route(r, W)

probe_token_share(r, W) =
  probe_input_output_tokens(r, W) / all_input_output_tokens(r, W)

probe_cost_share(r, W) =
  estimated_probe_cost(r, W) / estimated_total_cost(r, W)

probe_rate_limit_share(r, W) =
  provider_rate_limit_units_used_by_probes(r, W) /
  configured_or_observed_provider_rate_limit_units(r, W)
```

Also report absolute probe requests, tokens, cost, and failures. Token and cost
shares are `no_data` when their denominator is unknown; reported and estimated
usage remain separate. Probe traffic is excluded from passive workload SLIs.
Each request is counted at most once per route in the request-share operands,
regardless of retries or multiple attempts on that route.

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

## Comparative implementation reference

The M0 review examined QuantumNous/new-api at commit `9c97e78a`, specifically
its [relay retry loop](https://github.com/QuantumNous/new-api/blob/9c97e78aced572d540f227007a675d7d007666ac/controller/relay.go),
[bounded stream end reasons](https://github.com/QuantumNous/new-api/blob/9c97e78aced572d540f227007a675d7d007666ac/relay/common/stream_status.go),
[performance aggregation](https://github.com/QuantumNous/new-api/blob/9c97e78aced572d540f227007a675d7d007666ac/pkg/perf_metrics/metrics.go),
and [scheduled channel tests](https://github.com/QuantumNous/new-api/blob/9c97e78aced572d540f227007a675d7d007666ac/controller/channel-test.go).
The useful patterns are explicit attempt history, separate TTFT/generation
timing, bounded stream termination evidence, and active route checks. This
contract intentionally differs in three places:

- semantic failure codes remain authoritative over configurable HTTP-status
  ranges;
- a clean transport EOF is not canonical stream completion;
- active probe outcomes remain separate from passive availability evidence.

The external implementation is comparative evidence, not a normative dependency.
