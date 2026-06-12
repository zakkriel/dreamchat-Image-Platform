-- Provider routing substrate (Phase 7A, internal/providers/routing).
--
-- ListProviderRoutesForOperation returns every route for an operation joined to
-- its model's status, so the deterministic resolver can filter on active route
-- + active model, match quality/latency tiers and capability, apply provider
-- availability, and tie-break — all in one place it can also unit-test.
--
-- Prices are deliberately NOT consulted here. Route selection is independent of
-- pricing; the resolved model is then priced at cost-reservation time, where a
-- missing/expired active price surfaces as no_price_entry (422). Keeping the two
-- concerns separate is what lets a request fail with no_route vs no_price_entry
-- for the right reason.

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
    m.status              AS model_status
FROM provider_routes r
JOIN provider_models m ON m.id = r.model_id
WHERE r.operation_type = sqlc.arg(operation_type)
ORDER BY r.priority ASC, r.provider_id ASC, r.model_id ASC, r.id ASC;
