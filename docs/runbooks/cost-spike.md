# Runbook — Cost Spike

## Symptoms

- Cost per hour above threshold
- Generation jobs per token spike
- Cache hit rate drops
- Same client repeatedly regenerates assets

## Immediate Checks

1. Query cost events by token/client.
2. Query cost events by asset type.
3. Query generation jobs by endpoint.
4. Check cache hit/miss metrics.
5. Check provider/model routing changes.

## Mitigation

- Temporarily lower token generation limits.
- Disable high-cost models.
- Force draft tier for non-critical clients.
- Block repeated regeneration for same asset if needed.

## Follow-Up

- Improve retrieval-before-generation.
- Add budget alerts.
- Add per-world/session cost caps.
- Review idempotency usage.

---

## Confidence to Implement

**Score: 72/100 — Medium**

The diagnostic queries (cost-by-token, cost-by-asset-type) need a cost-events analytics view or rollup table — implementable but not specified. The mitigation ("temporarily lower token generation limits", "force draft tier", "block regeneration") requires admin tooling that doesn't exist yet, the same per-token rate-limit infrastructure flagged at 75 in `rate-limits.md`, plus the cost-budget reservation flow that's mentioned but not designed. So the runbook is correct in spirit but multiple chunks of platform work need to land first.
