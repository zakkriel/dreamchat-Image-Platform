-- name: InsertGenerationCostEvent :exec
INSERT INTO generation_cost_events (
    id, tenant_id, job_id, asset_id, cost_reservation_id, token_id,
    provider_id, model_id, provider_attempt_id,
    operation, estimated_cost_usd, actual_cost_usd, duration_ms, status, metadata
) VALUES (
    $1, $2, $3, $4, $5, $6,
    $7, $8, $9,
    $10, $11, $12, $13, $14, $15
);
