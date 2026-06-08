# Confidence to Implement — `image_platform_data_model.json`

**Score: 90/100 — Very High**

This is the cleanest spec in the PRD pack. Every entity (`generation_job`, `visual_identity`, `visual_identity_version`, `style_profile`, `asset`, `asset_pack`, `provider_attempt`) has typed fields, enum values, and foreign-key relationships that translate directly to DDL. It's a near-1:1 source for the Postgres schema in `docs/db/initial_schema.sql`, with the bonus of `provider_attempt` and `asset_pack` tables that the in-repo schema doesn't yet have — so this data model is actually slightly richer.

Why not 100: a few fields (`canonical_traits_or_features`, `allowed_variation`, `forbidden_drift`, `variant_tags`) are typed `json`/free-form objects, so the schema enforces shape only at the application layer. That's appropriate but lowers confidence in cross-table querying (e.g. "find all assets with `expression=serious`" needs a generated column or JSONB index, which isn't called out).

(This sibling file exists because JSON Schema files can't carry comment blocks without breaking validators — see `frustration_log.md` entry 8.)
