# Observability, privacy, and telemetry cardinality contract

Status: Accepted for M0 under [issue #8](https://github.com/NatsumeRyuhane/LLM-Gateway/issues/8)

## Purpose and invariants

Telemetry exists to explain routing and reliability without becoming a second
copy of inference content, credentials, or application identity data. The v0
instrumentation contract is `gateway.telemetry.v0`.

The following are correctness requirements:

- Prompt, completion, refusal, system-instruction, and tool-argument bodies are
  absent from metrics, traces, routine logs, and decision records by default.
- API keys, authorization values, identity assertions, cookies, complete URLs,
  private endpoint addresses, and raw provider error bodies are never telemetry.
- Application subject, principal, request, trace, conversation, run, decision,
  attempt, and credential identifiers are never metric labels.
- Metric instruments and label keys are registered centrally. Runtime data
  cannot create a label key, metric name, span name, or log event name.
- Ordinary traffic, active probes, and synthetic tests remain distinguishable
  at ingestion, storage, queries, alerts, and dashboards.
- One request can be reconstructed from its decision and attempt records even
  when its trace was not sampled.
- Telemetry export failure is observable but cannot change response bytes or the
  routing result of an in-flight request.

The implementation uses OpenTelemetry APIs and OTLP internally. Prometheus is
the first metrics query/exposition target. HTTP instrumentation follows one
pinned OpenTelemetry semantic-convention version per release; gateway-specific
fields use the `gateway.*` namespace.

## Signal and traffic classes

Every request receives exactly one trusted `traffic_class` before routing:

| Value | Source | Reliability use |
| --- | --- | --- |
| `ordinary` | Authenticated application traffic | Passive production SLIs and route evidence |
| `active_probe` | Gateway probe scheduler using a dedicated internal principal and budget | Probe-only measures; may inform health with explicit provenance |
| `synthetic_test` | Deterministic test/fault harness in an isolated environment or allowlisted route | Test reports only; never production passive health or SLOs |

The public client cannot set or override this class. Probe and test credentials,
request contexts, decision records, metric series, dashboards, and storage
partitions carry the class. Derived health may combine ordinary and probe
evidence only through a versioned estimator that retains source-specific counts,
freshness, and weights. Raw classes are never overwritten or merged.

All passive SLI queries in [the reliability contract](slo.md) include
`traffic_class="ordinary"`. A dashboard panel that combines classes must display
the breakdown and cannot be titled as passive or production reliability.

## Correlation model

Identifiers are opaque, non-secret, bounded values. They are attributes on
access-controlled traces, structured events, or records—not metric labels or
span-name fragments.

| Identifier | Creation and scope | Propagation and storage |
| --- | --- | --- |
| `trace_id` / `span_id` | OpenTelemetry/W3C trace context per distributed trace/span | Propagate through trusted gateway services; send provider trace context only to explicitly trusted routes |
| `request_id` | Gateway-generated per accepted HTTP request | Safe client correlation header; routine logs, trace root, decision record |
| `conversation_id` | Optional application-scoped opaque value | Decision/accounting records only by default; never sent to providers or metric labels |
| `run_id` | Optional application-scoped opaque value | Decision/accounting records only by default; never sent to providers or metric labels |
| `decision_id` | Gateway-generated per routing decision | Route span, decision record, attempt records, operator APIs |
| `attempt_id` | Gateway-generated per upstream dispatch | Attempt span/record and authorized response metadata |
| `route_id` | Registered bounded configuration identifier | Traces, records, logs, and approved metrics; never contains endpoint or credential data |

`request_id`, `decision_id`, and `attempt_id` are unique within one deployment
retention horizon. Client-supplied conversation/run values are namespaced by the
authenticated application before lookup. They are validated to the protocol
bounds and treated as pseudonymous data, not trusted trace context.

Do not put identity or correlation fields in W3C baggage that may cross the
provider boundary. Do not derive a metric label by hashing an identifier: a hash
preserves cardinality and remains linkable. Trace exemplars may carry a
`trace_id` because exemplars do not create metric series; exemplar access and
retention follow trace policy.

## Trace contract

Span names are fixed operation classes, never IDs, model names, paths, or error
messages. The data-plane hierarchy is:

```text
gateway.http.request (SERVER)
  gateway.auth.authenticate
  gateway.request.validate
  gateway.route.decide
    gateway.route.load_evidence
    gateway.route.filter_candidates
    gateway.route.evaluate_affinity
    gateway.route.select
  gateway.upstream.attempt (CLIENT, once per attempt)
    provider HTTP client span
  gateway.response.write
  gateway.accounting.finalize
```

Buffered and streaming work share the same root. Attempts are siblings in
attempt order under the root/decision context; a fallback event on the decision
span links the failed and next `attempt_id`. Deferred exporters or accounting
work that outlives the request starts a new trace linked to the request span
instead of falsifying its duration.

The root records the bounded registered route template separately as
`http.route` (for example, `/v1/chat/completions`) and the operation class as
`gateway.operation`. Neither value is interpolated into the span name.

### Common span attributes

Resource attributes identify `service.name`, `service.version`, deployment
environment, and instance. The following request attributes are allowed on
gateway spans when applicable:

```text
gateway.contract.version
gateway.request.id
gateway.traffic.class
gateway.operation
http.route
gateway.stream
gateway.model_group
gateway.capability_class
gateway.decision.id
gateway.policy.id
gateway.policy.version
gateway.route.id
gateway.adapter.kind
gateway.attempt.id
gateway.attempt.ordinal
gateway.output.visible
gateway.tool.actionable
gateway.outcome
gateway.failure.domain
gateway.failure.class
gateway.retry.disposition
gateway.usage.input_tokens
gateway.usage.output_tokens
gateway.usage.provenance
```

`request_id`, `decision_id`, and `attempt_id` may appear on spans for lookup but
never in the span name. Conversation/run identifiers and application subjects
are excluded from routine spans; an operator uses the authorized decision or
accounting API to bridge from those identities to a request and then to a
registered `route_id` or approved redacted route template. Raw endpoint hosts,
credentials, raw model input/output, and raw exception messages are prohibited
in normal records and traces.

The root span ends only after the downstream terminal result and synchronous
cleanup are known. A provider HTTP success does not set gateway success. Span
status is `ERROR` for a failed operation at that span's contract boundary;
expected client/policy rejections retain their typed outcome without turning an
unrelated provider span into an error.

### Trace sampling

The default collector applies parent-based sampling and a bounded tail policy:

- retain 1% of successful ordinary request traces, selected deterministically;
- retain failed, fallback, partial-output, slow-threshold, probe, synthetic, and
  correctness-invariant traces, subject to an emergency capacity cap;
- never base sampling on prompt/output/tool content or application identity;
- record dropped spans/traces by reason through bounded telemetry metrics.

Sampling configuration is versioned and audited. Trace sampling never controls
decision-record persistence or metric accounting. An emergency cap degrades to
metrics and structured records rather than buffering without bound.

## Metric contract

### Registration and cardinality budgets

Metrics use the Prometheus namespace `llm_gateway`. Counters end in `_total`;
Prometheus-exported time and size names include base-unit suffixes. The matching
OTel instrument records UCUM unit `1`, `s`, `By`, or `{token}` as appropriate.

All metric label values come from enums or bounded configuration registries:

| Dimension | V0 maximum |
| --- | ---: |
| `traffic_class` | 3 |
| `operation` | 2 (`models.list`, `chat.completions`) |
| `stream` | 2 |
| `outcome` / `terminal` | 8 each |
| `failure_domain` | 8 |
| `failure_class` | 16 metric-level classes |
| `capability_class` | 8 request-shape buckets |
| `drop_reason` | 8 |
| `direction` | 2 |
| `usage_provenance` | 2 |
| `cost_provenance` | 2 |
| `route_id` | 200 configured routes |
| `model_group` | 100 configured groups |
| `adapter_kind` | 16 registered implementations |
| `exclusion_reason` | 16 stable classes |
| `probe_profile` | 8 registered profiles |
| `health_state` | 5 (`unknown`, `healthy`, `degraded`, `open`, `recovering`) |
| `evidence_source` | 3 (`passive`, `probe`, `synthetic`) |
| `signal` | 4 (`metric`, `trace`, `log`, `record`) |
| `exporter` | 8 configured exporters |
| `audit_action` | 16 stable actions |
| `resource_type` | 8 audited resource classes |
| `audit_outcome` | 4 |

The exact shared vocabularies are:

- `outcome`: `success`, `rejected`, `failed`, `cancelled`, `partial`,
  `incomplete`, `dropped`, `other`;
- `terminal`: `completed`, `failed_pre_output`, `failed_partial`,
  `cancelled_client`, `cancelled_deadline`, `early_eof`,
  `abandoned_fallback`, `other`;
- `failure_domain`: `client`, `auth`, `quota`, `policy`, `gateway`, `storage`,
  `upstream`, `protocol`;
- `failure_class`: `invalid_request`, `cancelled`, `deadline`,
  `authentication`, `authorization`, `quota`, `no_route`, `capability`,
  `overload`, `internal`, `transport`, `rate_limit`, `upstream_rejected`,
  `protocol`, `accounting`, `other`;
- `capability_class`: `text_buffered`, `text_stream`, `tools_buffered`,
  `tools_stream`, `structured_buffered`, `structured_stream`, `mixed`, `other`;
- `exclusion_reason`: `capability`, `privacy`, `trust`, `price`,
  `administrative`, `health`, `circuit`, `evidence_stale`, `evidence_missing`,
  `affinity_ineligible`, `quota`, `concurrency`, `region`, `model_group`,
  `route_disabled`, `other`;
- `drop_reason`: `sampled`, `capacity`, `redaction`, `schema`, `exporter`,
  `shutdown`, `retention`, `other`;
- `audit_action`: `create`, `update`, `delete`, `activate`, `deactivate`,
  `rotate`, `revoke`, `enable`, `disable`, `override`, `clear_override`,
  `export`, `delete_data`, `emergency_access`, `approve_capture`, `other`;
- `resource_type`: `application`, `credential`, `route`, `model_group`,
  `policy`, `probe`, `telemetry`, `incident`.

`direction` is `input|output`; `usage_provenance` is
`provider_reported|gateway_estimated`; `cost_provenance` is
`quoted|estimated`; and `audit_outcome` is `success|denied|failed|other`.
Metric-specific label subsets use only values from these vocabularies.

Configuration exceeding a registry budget is rejected before activation. An
unknown runtime value maps to a declared `other` enum value and emits a schema
violation; it does not create a new series. The cardinality bound below is the
Cartesian maximum for the listed labels, even when many combinations cannot
occur. Standard resource labels such as deployment and service are controlled
separately by the deployment and multiply every bound; pod/instance is dropped
from long-lived service-level views.

### Inventory

| Prometheus name | Type | Unit | Labels | Maximum label sets / exported series | Meaning |
| --- | --- | --- | --- | ---: | --- |
| `llm_gateway_requests_total` | Counter | requests (`1`) | `traffic_class`, `operation`, `stream`, `outcome` | 96 / 96 | Requests reaching a terminal admission/data-plane outcome |
| `llm_gateway_request_duration_seconds` | Histogram | seconds (`s`) | `traffic_class`, `operation`, `stream`, `outcome` | 96 / 1,728 | Admission through downstream terminal/cleanup |
| `llm_gateway_inflight_requests` | UpDownCounter/gauge | requests (`1`) | `traffic_class`, `stream` | 6 / 6 | Currently owned requests |
| `llm_gateway_request_failures_total` | Counter | failures (`1`) | `traffic_class`, `failure_domain`, `failure_class` | 384 / 384 | Stable metric-level terminal classifications |
| `llm_gateway_route_decisions_total` | Counter | decisions (`1`) | `traffic_class`, `outcome`, `capability_class` | 192 / 192 | Routing decisions by bounded result and request shape |
| `llm_gateway_decision_records_total` | Counter | records (`1`) | `traffic_class`, `outcome` | 24 / 24 | Complete, incomplete, or failed decision-record writes |
| `llm_gateway_route_candidate_exclusions_total` | Counter | exclusions (`1`) | `traffic_class`, `route_id`, `exclusion_reason` | 9,600 / 9,600 | Hard/health candidate exclusions; detail remains in records |
| `llm_gateway_dispatch_duration_seconds` | Histogram | seconds (`s`) | `traffic_class`, `stream`, `capability_class`, `outcome` | 384 / 6,912 | Admission to first upstream dispatch or terminal no-route result |
| `llm_gateway_upstream_attempts_total` | Counter | attempts (`1`) | `traffic_class`, `route_id`, `terminal` | 4,800 / 4,800 | Upstream attempts, including abandoned pre-output attempts |
| `llm_gateway_upstream_attempt_duration_seconds` | Histogram | seconds (`s`) | `traffic_class`, `route_id`, `terminal` | 4,800 / 86,400 | Dispatch through attempt cleanup |
| `llm_gateway_upstream_time_to_first_event_seconds` | Histogram | seconds (`s`) | `traffic_class`, `route_id`, `terminal` | 4,800 / 86,400 | Dispatch to first valid model event |
| `llm_gateway_stream_terminals_total` | Counter | streams (`1`) | `traffic_class`, `route_id`, `terminal` | 4,800 / 4,800 | Completed, failed, cancelled, or early-EOF streams |
| `llm_gateway_stream_inter_event_duration_seconds` | Histogram | seconds (`s`) | `traffic_class`, `route_id` | 600 / 10,800 | Time between validated model events |
| `llm_gateway_tokens_total` | Counter | tokens (`{token}`) | `traffic_class`, `route_id`, `direction`, `usage_provenance` | 2,400 / 2,400 | Input/output token accounting, reported or estimated |
| `llm_gateway_cost_usd_total` | Counter | US dollars (`{USD}`) | `traffic_class`, `route_id`, `cost_provenance` | 1,200 / 1,200 | Versioned quoted/estimated cost; no mixed currencies |
| `llm_gateway_route_health_state` | Gauge (one-hot) | state (`1`) | `route_id`, `evidence_source`, `health_state` | 3,000 / 3,000 | Exactly one state value is 1 per route/source; others 0 |
| `llm_gateway_route_evidence_age_seconds` | Gauge | seconds (`s`) | `route_id`, `evidence_source` | 600 / 600 | Age of newest qualifying evidence |
| `llm_gateway_probe_requests_total` | Counter | probes (`1`) | `route_id`, `probe_profile`, `outcome` | 12,800 / 12,800 | Active-probe requests only |
| `llm_gateway_probe_cost_usd_total` | Counter | US dollars (`{USD}`) | `route_id`, `probe_profile`, `cost_provenance` | 3,200 / 3,200 | Probe cost budget consumption |
| `llm_gateway_telemetry_exports_total` | Counter | export batches (`1`) | `signal`, `exporter`, `outcome` | 256 / 256 | Accepted/failed/dropped exporter batches |
| `llm_gateway_telemetry_dropped_records_total` | Counter | records (`1`) | `signal`, `drop_reason` | 32 / 32 | Capacity, sampling, redaction, or schema drops |
| `llm_gateway_audit_changes_total` | Counter | changes (`1`) | `audit_action`, `resource_type`, `audit_outcome` | 512 / 512 | Control-plane mutations without actor/resource IDs |

The inventory bounds use the registered maxima above: `drop_reason=8`,
`direction=2`, `usage_provenance=2`, `cost_provenance=2`, and
`audit_outcome=4`. Adding a value requires a versioned registry and budget
update before instrumentation can emit it.

Each histogram has 15 finite buckets plus `+Inf`, sum, and count, so one label set
exports 18 Prometheus series; the table includes that multiplier. Metric
`outcome` subsets never exceed the global maximum of 8.

Latency histograms use seconds with fixed boundaries
`0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120, 300`.
Time-to-first-event and inter-event views may use a configured subset of those
same boundaries, never per-route custom buckets. Token and cost measures are
counters, not per-request labeled gauges. Retry amplification, completion
rates, throughput, and SLI ratios are recording rules derived from the inventory.

Exact failure codes, policy versions, request IDs, provider status codes, and
model names supplied at runtime belong in records/traces—not metric labels.
Route values in normal records and traces are limited to registered `route_id`
values or approved redacted route templates; raw endpoint hosts are prohibited.
A schema test enumerates every instrument and rejects any label not listed in
this table.

## Structured operational events

Logs conform to the OpenTelemetry log data model. `event_name` is a stable schema
identifier, `body` is a constant human-readable template, and variable values
are typed attributes. Request-associated events include OTel `trace_id` and
`span_id` when available.

Common schema:

```text
timestamp, observed_timestamp
severity_number, severity_text
event_name, schema_version
trace_id?, span_id?
request_id?, decision_id?, attempt_id?, route_id?
traffic_class?, outcome?
failure_domain?, failure_class?, retry_disposition?
policy_id?, policy_version?
duration_ms?, input_tokens?, output_tokens?, usage_provenance?
```

Allowed v0 event names and default severity:

| Event | Severity | Emission rule |
| --- | --- | --- |
| `gateway.request.completed` | INFO | Terminal request summary; successful high-volume events may be sampled |
| `gateway.request.failed` | WARN or ERROR | WARN for client/policy/upstream; ERROR for gateway/storage/protocol correctness failure |
| `gateway.route.decided` | DEBUG | Sampled summary pointing to the durable decision record |
| `gateway.attempt.completed` | DEBUG | Sampled attempt summary |
| `gateway.fallback.scheduled` | WARN | Every automatic fallback, before a new attempt |
| `gateway.stream.partial_failure` | ERROR | Every failure after output visibility |
| `gateway.probe.completed` | INFO | Probe-only event with profile, never prompt/output |
| `gateway.telemetry.export_failed` | ERROR | Exporter/signal and bounded failure class |
| `gateway.telemetry.schema_violation` | ERROR | Dropped prohibited or unknown field/value |
| `gateway.correctness.violation` | ERROR | Every safety-invariant violation; incident candidate |

DEBUG events are off by default in production. Routine log sampling is
deterministic and configured by event name/outcome, never by content or identity.
Errors do not embed a raw Go error string unless it has passed the stable
redaction/classification boundary. Stack traces are limited to gateway-owned
errors in the protected diagnostic sink and remain absent from client errors.

## Routing decision and attempt records

Decision records are the authoritative reconstruction evidence. They live in
the gateway's access-controlled operational store, not a metric label, log body,
or trace-only backend.

```text
RoutingDecisionRecord gateway.decision.v0
  decision_id, request_id, trace_id?
  occurred_at, traffic_class
  application_id                         protected reference
  conversation_correlation?              keyed opaque correlation token
  run_correlation?                       keyed opaque correlation token
  requested_target, capability_class
  policy_id, policy_version, policy_digest
  candidate_snapshot[]
    route_id, route_config_version
    eligible, exclusion_reasons[]
    capability_result, hard_policy_result
    evidence_snapshot_id, evidence_source
    evidence_observed_at, evidence_age_ms
    health_state, confidence_bucket
    deterministic_score_inputs
  affinity
    key_kind, key_present, prior_route_id?
    prior_route_eligible, applied, result_reason
  selected_route_id?, tie_break_input
  attempts[]                              ordered references/summaries
  terminal_outcome, failure_code?
  completeness, missing_fields[]
  schema_version
```

The keyed correlation token supports equality within the retention horizon
without retaining the client-provided conversation/run value in routine
evidence. It is still pseudonymous and access controlled. Policy and evidence
digests use canonical versioned serialization. Candidate order is preserved.
`deterministic_score_inputs` contains named bounded numeric/enum inputs, not
free-form explanations.

```text
AttemptRecord gateway.attempt.v0
  attempt_id, decision_id, ordinal, route_id
  adapter_kind, adapter_version, route_config_version
  started_at, accepted_at?, first_event_at?, ended_at
  output_visible, tool_actionable
  terminal, failure_code?, retry_disposition?
  provider_status?                       bounded integer evidence
  input_tokens?, output_tokens?, usage_provenance
  estimated_cost_usd?, cost_model_version?
  cleanup_result, next_attempt_reason?
  schema_version
```

The record transaction/enqueue boundary follows the architecture contract. If a
complete record cannot be persisted, the request retains an explicit incomplete
record or write-failure counter; it must not disappear silently. Replay tests
reconstruct candidate eligibility, freshness, affinity, selection, and attempt
order from stored records without consulting mutable current configuration.

## Audit records

Control-plane mutations produce append-only `gateway.audit.v0` records:

```text
audit_id, occurred_at, trace_id?, request_id?
actor_principal_id, actor_type, authenticated_via
action, resource_type, resource_id, resource_version
before_digest?, after_digest?, change_fields[]
reason?, approval_reference?
source_network_class, outcome, failure_class?
schema_version
```

Audit records cover route/credential registration and rotation, endpoint and
trust policy changes, model-group/policy activation, health overrides, probe
schedules/budgets, telemetry sampling/redaction/retention changes, exports, and
emergency access. Secrets, assertions, content, raw configuration documents, and
full IP addresses are excluded. Actor and resource identifiers are required in
audit storage but prohibited from audit metric labels.

Audit append failure fails a security-sensitive mutation closed. Records are
integrity protected and readable only through an authorized control-plane API.

## Redaction, diagnostic capture, and retention

### Default allowlist

Instrumentation builds each signal from typed canonical fields; it does not log
arbitrary request/provider objects and then redact them. The exporter applies a
second allowlist and drops unknown fields. URL values are reduced to registered
route IDs or approved redacted route templates; raw endpoint hosts are never
retained in normal records or traces. Headers are excluded except safe bounded
protocol facts. Query strings, fragments, credentials, cookies, assertions,
request/response bodies, tool schemas/arguments, and raw errors are discarded.

Hashing or truncating a secret is not acceptable redaction. Redaction failure
drops the record, increments `llm_gateway_telemetry_dropped_records_total`, and
emits a rate-limited schema-violation event without the rejected value.

### Exceptional content capture

Content capture is disabled in v0 deployments. A future diagnostic capture may
be enabled only through an authenticated, separately authorized control-plane
action that records an audit event and requires all of:

- a documented incident/purpose and approval reference;
- an allowlist of applications/routes and signal fields;
- a rate/sample cap, byte cap, and absolute end time;
- encrypted storage separate from normal telemetry;
- access logging and explicit deletion workflow;
- retention no longer than 24 hours.

Credentials, authorization material, identity assertions, and tool results
classified as secrets remain uncapturable. Captured content never enters
metrics, ordinary traces/logs, dashboards, or support bundles.

### Default retention

| Data | Default retention | Notes |
| --- | ---: | --- |
| Raw metrics | 30 days | Service-level recording rules may retain aggregate, identity-free series for 180 days |
| Sampled traces | 7 days | Protected correctness/incident traces may be promoted to a 30-day incident case by audited action |
| Structured operational logs | 14 days | WARN/ERROR incident events: 30 days |
| Decision and attempt records | 30 days | Usage/accounting aggregates may remain 365 days without request/conversation/run IDs |
| Probe and synthetic records | 30 days | Separate partition and traffic class |
| Audit records | 365 days | Longer legal/operator policy is explicit and documented |
| Exceptional diagnostic content | At most 24 hours | Explicit capture only; never automatic extension |

Retention changes, legal holds, incident promotions, exports, and manual
deletions are audited. Backups inherit the same classification and have a
documented deletion/expiry schedule. Self-hosted operators may shorten defaults;
lengthening them requires an explicit policy version and capacity/privacy review.

## Dashboard information architecture

Every dashboard displays time window, environment, traffic class, policy
version where relevant, evidence freshness, and whether values are measured,
estimated, or missing.

1. **Gateway overview:** gateway service success, routing availability,
   client-completion/stream success, request volume, latency, inflight work,
   retry amplification, and telemetry loss.
2. **Route health:** route state split by passive/probe evidence, freshness,
   exclusions, failure domain/class, time to first event, completion, and
   confidence. Unknown is visually distinct from healthy.
3. **Incidents and diversion:** incident timeline, detection/diversion/recovery,
   failure exposure, policy/health transitions, partial streams, and linked
   decisions/audits.
4. **Attempts and decisions:** candidate/exclusion funnel, affinity results,
   attempt ordinal/terminal, fallbacks, explainability completeness, and links
   to authorized records by IDs outside metric queries.
5. **Usage and cost:** tokens and USD cost by route, model group through joined
   bounded configuration, traffic class, and provenance; probe overhead shown
   separately.
6. **Probes and tests:** schedules, profile outcomes, freshness, budget/rate-limit
   consumption, synthetic fault scenario, and zero contribution to passive SLIs.
7. **Audit and configuration:** policy/config versions, credential rotations,
   health overrides, sampling/redaction/retention changes, failed audit writes,
   and emergency access.

No dashboard URL, variable, or panel query embeds a subject, credential,
conversation, run, request, or attempt identifier. Those lookups use a protected
record-search flow with access auditing.

## Verification contract

The first vertical slice must include automated checks that:

1. register only the inventory above and reject prohibited/unknown metric labels;
2. enumerate every label domain and assert its configured cardinality ceiling;
3. inject canary secrets/content in every request/provider field and prove they
   are absent from all default signal and record exports;
4. prove ordinary, active-probe, and synthetic counters/SLIs remain disjoint;
5. reconstruct a multi-attempt decision from immutable policy/evidence versions,
   candidates, exclusions, affinity, and ordered attempts;
6. verify trace parentage, stable span/event names, cancellation, partial-stream,
   and fallback attributes;
7. exercise exporter failure, queue overflow, sampling, redaction drop, and
   shutdown without changing the data-plane response;
8. verify retention expiry, diagnostic-capture timeout, access audit, and
   deletion behavior with a controllable clock.

## Normative references

- [OpenTelemetry tracing API](https://opentelemetry.io/docs/specs/otel/trace/api/)
  for span hierarchy, context, links, names, status, and lifetime.
- [OpenTelemetry logs data model](https://opentelemetry.io/docs/specs/otel/logs/data-model/)
  for trace correlation, event names, severity, body, and attributes.
- [OpenTelemetry metrics data model](https://opentelemetry.io/docs/specs/otel/metrics/data-model/)
  for counters, gauges, histograms, units, streams, and attributes.
- [Prometheus metric and label naming](https://prometheus.io/docs/practices/naming/)
  for base-unit names and avoiding high-cardinality labels.
- [Reliability contract](slo.md), [failure taxonomy](failure-taxonomy.md),
  [protocol contract](protocol.md), and [threat model](threat-model.md) for the
  domain definitions this signal contract records.
