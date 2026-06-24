-- Provider routing substrate (Phase 7A, internal/providers/routing).
--
-- ListProviderRoutesForOperation returns every route for an operation joined to
-- its model's status and the most-recent active price (if any), so the
-- deterministic resolver can filter on active route + active model, match
-- quality/latency tiers and capability, apply provider availability, apply
-- intent-driven price-aware ranking, and tie-break — all in one place it can
-- also unit-test.
--
-- Pricing is surfaced here only for intent-driven ranking (draft→cheapest,
-- commit→premium). A route with no active price is still a valid candidate; the
-- intent ranker sorts it last (unpriced sorts after all priced routes). The
-- hard no_price_entry (422) is enforced later at cost-reservation time, keeping
-- the two failure modes independent.

-- name: ListProviderRoutesForOperation :many
SELECT
    r.id                  AS route_id,
    r.provider_id         AS provider_id,
    r.model_id            AS model_id,
    r.operation_type      AS operation_type,
    r.required_capability AS required_capability,
    r.preview_capability  AS preview_capability,
    r.quality_tier        AS quality_tier,
    r.latency_tier        AS latency_tier,
    r.is_enabled          AS is_enabled,
    r.priority            AS priority,
    m.status              AS model_status,
    p.price_per_unit      AS price_per_unit
FROM provider_routes r
JOIN provider_models m ON m.id = r.model_id
LEFT JOIN provider_model_prices p
       ON p.provider_id = r.provider_id
      AND p.model_id    = r.model_id
      AND p.operation_type = r.operation_type
      AND p.is_active   = true
WHERE r.operation_type = sqlc.arg(operation_type)
ORDER BY r.priority ASC, r.provider_id ASC, r.model_id ASC, r.id ASC;
