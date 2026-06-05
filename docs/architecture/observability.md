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

Initial alerts:

- provider error rate high
- queue depth high
- cost spike
- job failure rate high
- storage upload failures
- token auth failures spike
- API latency spike

---

## Confidence to Implement

**Score: 78/100 — High**

The list of fields, metrics, and alerts is correct. Implementation is the work: a structured logger (zap/slog), OpenTelemetry SDK for spans + metrics, a Prometheus exporter or OTel Collector, and either an APM (Honeycomb, Grafana, Datadog) or self-hosted (Prom + Tempo + Loki). All standard, but the "cost telemetry per token/world/style/quality_tier" requires aggregation queries that can become expensive without a separate analytics table or rollup job. The alert thresholds aren't numbered yet (what's "high"?) — needs benchmarking before they can be wired up usefully.
