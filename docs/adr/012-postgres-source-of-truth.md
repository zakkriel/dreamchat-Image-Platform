# ADR-012 — Use Postgres as the metadata source of truth

## Status

Accepted for initial implementation.

## Context

The platform's metadata is small per row but rich in relationships: a `visual_asset` belongs to a `visual_identity` which has a `current_version`, references a `style_profile_id`, was created by a `generation_job` that has an `api_token` actor, with N `generation_cost_event` rows. Queries cross these tables constantly: "give me the latest standard-quality angry expression of char_789 in style_001."

Object-store paths and queue messages are not enough — we need transactional writes, indexed queries, and a single source of truth that survives provider/queue/storage churn.

## Decision

**Postgres** holds all metadata: api_tokens, style_profiles, provider_models, visual_identities, visual_identity_versions, visual_assets, asset_packs, generation_jobs, generation_cost_events, idempotency_keys, audit_events. Image bytes go to S3 (ADR-011). Short-lived state (queue, rate limits, idempotency locks) goes to Redis (ADR-013).

## Alternatives considered

- **MongoDB / DynamoDB.** Flexible schema, no migrations. The platform's queries are heavily relational ("assets joined to identities joined to style profiles"); document stores make these awkward and don't help us with the actual hard part (consistency invariants). The data shapes are stable enough that schema is a feature, not a tax.
- **SQLite.** Dev simplicity, zero ops. No concurrent write story for production. Useful for unit tests with `testcontainers` is not needed; pgx + a real Postgres is fine in tests too.
- **MySQL.** Workable. Postgres wins on JSONB (we have several free-form fields like `canonical_visual_traits`), array types (`scopes TEXT[]`, `anchor_asset_ids TEXT[]`), partial indexes, and `INSERT ... ON CONFLICT` for idempotency.
- **Cassandra / DynamoDB / ScyllaDB.** Throughput we don't need and operational overhead we shouldn't take on. Designed for write volumes orders of magnitude beyond the platform's foreseeable load.
- **Postgres with sharding by tenant.** Premature. Single-instance Postgres handles our projected volume for years. Shard if/when needed; the schema already includes `tenant_id` on every row.

## Tradeoffs

- **+** Strong transactions for the "create job + reserve idempotency key + record cost event" pattern.
- **+** JSONB for free-form trait/metadata fields without giving up SQL semantics for the structured columns.
- **+** Mature Go ecosystem: `pgx` + `sqlc` give type-safe queries with explicit SQL.
- **+** Row-level security (RLS) is available if we ever want defense-in-depth for `tenant_id` isolation.
- **−** Operationally heavier than SQLite for single-node dev (mitigated by docker-compose).
- **−** Connection pooling at the worker layer needs care (`pgxpool` or pgbouncer in prod).
- **−** JSONB queries are powerful but slower than column queries; we must promote frequently filtered JSONB fields to columns over time.

## Consequences

- Migration tooling: explicit SQL migrations (per `docs/guidelines/go-service-guidelines.md`), no schema-by-reflection.
- Queries written in SQL via `sqlc` (type-safe), repository layer hides Postgres specifics from services.
- Read replicas as a future scaling path; ADR-013's Redis covers short-lived state so Postgres isn't on the critical path of every queue dequeue.

## Revisit when

- A single Postgres instance is no longer sufficient for write volume → start with read replicas, then consider sharding by `tenant_id`.
- A workload becomes write-heavy in a way that doesn't fit Postgres (e.g. high-rate telemetry) → push that workload to a dedicated store (ClickHouse for analytics, TimescaleDB extension for time-series).
- Multi-region deployment needs introduce cross-region replication latency we can't tolerate.

---

## Confidence to Implement

**Score: 95/100 — Very High**

Postgres for transactional metadata is the default for this shape of service. `docs/db/initial_schema.sql` is already a reasonable starting point, and `sqlc`/`pgx` patterns are mature. Adding the `asset_packs` and `provider_attempts` tables that the PRD data model has but the SQL is missing is a small cleanup.
