# Confidence to Implement — `visual_asset.schema.json`

**Score: 88/100 — High**

Fields cover storage URLs (low/high/thumb), provider/model linkage, prompt hash, seed, and the asset status enum (`pending|preview_ready|ready|failed|archived`). Matches `docs/db/initial_schema.sql` cleanly. Easy to generate Go types from.

Why not higher: `variant_key` is a free-form string — should be a tagged union or at least an enum of known variant keys (per PRD 04: `neutral_front_portrait`, `establishing_wide_view`, etc.) so the type system enforces what the product cares about. `metadata` is also a free `object`, which is appropriate but pushes validation into the application layer.

(Sibling file because JSON Schema files can't carry comment blocks — see `frustration_log.md` entry 8.)
