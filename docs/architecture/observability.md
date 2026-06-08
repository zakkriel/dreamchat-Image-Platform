# Observability and Telemetry

## Required Observability

Every request must have:

- request ID
- token ID or anonymous public marker
- endpoint
- response status
- duration

Every generation job must have:

- job ID
- visual identity ID
- asset type
- provider
- model
- queue time
- generation time
- storage time
- total time
- estimated cost
- actual cost if available

Every asset must have:

- asset ID
- provider/model
- prompt hash
- style profile
- variant key
- version
- seed
- cache/reuse status

## Logs

Use structured JSON logs.

Minimum fields:

```json
{
  "level": "info",
  "ts": "2026-06-05T12:00:00Z",
  "service": "dreamchat-image-platform",
  "request_id": "req_123",
  "job_id": "job_456",
  "event": "generation_job.completed"
}
```

## Metrics

Recommended metrics:

```txt
http_request_duration_ms
http_request_count
generation_job_count
generation_job_duration_ms
generation_job_failure_count
provider_call_duration_ms
provider_call_failure_count
asset_cache_hit_count
asset_cache_miss_count
asset_reuse_count
estimated_cost_usd
actual_cost_usd
queue_depth
worker_active_count
```

## Tracing

A generation flow should be traceable across:

```txt
API request -> DB write -> queue enqueue -> worker -> provider -> storage -> DB update
```

## Cost Telemetry

Cost telemetry is mandatory, not optional.

Track:

- per provider
- per model
- per asset type
- per token/client
- per world
- per style profile
- per quality tier

## Alerts

### Alert classes

Two severities:

- **warning** — page on-call during business hours, file an incident
  note.
- **critical** — page on-call immediately, runbook applies.

### Initial alert thresholds

These numbers are for alpha / beta. They were chosen from the latency
targets in PRD 06 §5, the failure-mode lists in PRD 06 §7 and the
runbooks, and the cost-control surface in
`docs/architecture/cost-control.md`. **Revisit after real traffic.**

#### Latency

| Metric | Threshold | Severity |
|---|---|---|
| Preview p95 over 10 min | > 8 s | warning |
| Preview p95 over 10 min | > 15 s | critical |
| Final p95 over 10 min | > 45 s | warning |
| Final p95 over 10 min | > 90 s | critical |
| API p95 (excluding provider wait) over 10 min | > 500 ms | warning |
| API p95 (excluding provider wait) over 10 min | > 1000 ms | critical |

#### Failure rate

| Metric | Threshold | Severity |
|---|---|---|
| Provider error rate over 10 min | > 5% | warning |
| Provider error rate over 10 min | > 15% | critical |
| Job failure rate over 15 min | > 3% | warning |
| Job failure rate over 15 min | > 10% | critical |

`provider-failure.md` covers the response procedure when provider
metrics trip; `failed-jobs.md` covers the job-failure side.

#### Queue

| Metric | Threshold | Severity |
|---|---|---|
| Queue depth over 10 min | > 500 jobs | warning |
| Queue depth over 10 min | > 2000 jobs | critical |
| Oldest queued job age | > 5 min | warning |
| Oldest queued job age | > 15 min | critical |

#### Cost

These thresholds depend on the cost-control pipeline
(`docs/architecture/cost-control.md`). They evaluate against the
daily cost budget for whatever scope is being monitored (tenant /
world / token).

| Metric | Threshold | Severity |
|---|---|---|
| Estimated daily spend vs. budget | > 80% | warning |
| Estimated daily spend vs. budget | > 100% (cap hit) | critical |
| Cost per successful asset vs. 7-day baseline | > +50% | warning |
| Cost per successful asset vs. 7-day baseline | > +100% | critical |

`cost-spike.md` is the response runbook for both.

#### Cache / retrieval

These watch the retrieval-before-generation behavior (ADR-009 +
variant-compatibility-matrix). Low cache reuse means the platform is
regenerating things it shouldn't.

| Metric | Threshold | Severity |
|---|---|---|
| Cache hit rate for reusable assets over 24 h | < 30% | warning |
| Regeneration rate for existing visual identities over 24 h | > 25% | warning |
| Exact + compatible match failure for recurring identities | > 20% | warning |

#### Consistency

Driven by the benchmark corpus
(`prds/schemas/benchmark_corpus_template.md`). Run continuously or per
release; aggregate scores feed these alerts.

| Metric | Threshold | Severity |
|---|---|---|
| Identity consistency benchmark average | < 4 / 5 | warning |
| Identity consistency benchmark average | < 3.5 / 5 | critical |
| Place consistency benchmark average | < 4 / 5 | warning |
| Place consistency benchmark average | < 3.5 / 5 | critical |

A critical consistency alert means the in-production provider has
fallen below the PRD 03 §8.5 acceptance bar. Response: route
consistency-critical traffic to a fallback provider per
`docs/architecture/admin-control-surface.md` and re-run the §8.5
acceptance tests on the failing model before re-enabling.

### Note

These thresholds are **initial values for alpha / beta**. Real
traffic will reveal which ones page too often (raise the warning
threshold) and which never page when they should (lower the critical
threshold). The thresholds should be reviewed monthly during alpha
and quarterly after.

---

## Confidence to Implement

**Score: 88/100 — High** *(was 78; +10 after numeric thresholds added)*

The list of fields, metrics, and alerts is correct. Implementation is the work: a structured logger (zap/slog), OpenTelemetry SDK for spans + metrics, a Prometheus exporter or OTel Collector, and either an APM (Honeycomb, Grafana, Datadog) or self-hosted (Prom + Tempo + Loki). Cost telemetry per token/world/style/quality_tier needs an aggregation table or rollup job — implementable but a separate piece of work. Alert thresholds are now numeric and wireable; they will need tuning after real traffic exposes which are too tight or too loose, but they are no longer blockers.
