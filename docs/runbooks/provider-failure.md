# Runbook — Provider Failure

## Symptoms

- Increased job failures
- Provider timeout errors
- Provider rate limit errors
- Queue depth increasing
- Cost events missing actual cost

## Immediate Checks

1. Check provider status page.
2. Check provider adapter error logs.
3. Check recent `provider_call_failure_count`.
4. Check queue depth.
5. Check if failures are isolated to one model/provider.

## Mitigation

- Disable failing provider route if alternate exists.
- Reduce concurrency for provider worker.
- Queue lower-priority jobs.
- Return degraded status from `/health` if needed.

## Follow-Up

- Add provider incident note.
- Classify failed jobs as retryable or terminal.
- Requeue retryable failed jobs after provider recovers.
