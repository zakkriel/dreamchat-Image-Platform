# Frustration Log

A running log of frustrations, doubts, surprises, assumption-based decisions, and any drift from the plan. The goal is to review later to optimize the process.

Format per entry:
- **Timestamp** — ISO date
- **Trigger** — what caused the entry (PRD name, tool, phase, etc.)
- **Category** — `frustration` | `doubt` | `surprise` | `assumption` | `drift` | `process`
- **Note** — what happened, what I assumed, what I'd want fixed next time

---

## 2026-06-05 — Session: PRD confidence scoring

### Entry 1
- **Trigger**: User request setup — `/brainstorming` command
- **Category**: drift
- **Note**: The user typed `/brainstorming` as if invoking a slash command, but it is not in the list of skills available to me in this session (only `session-start-hook`, `deep-research`, `update-config`, `keybindings-help`, `verify`, `code-review`, `simplify`, `fewer-permission-prompts`, `loop`, `claude-api`, `run`, `init`, `review`, `security-review`). I'm proceeding with the literal request (read zip + docs, add confidence scores, create this log) and treating `/brainstorming` as a label for the working mode rather than a real command. Worth confirming with the user later, or registering a real `/brainstorming` skill so this isn't ambiguous.

### Entry 2
- **Trigger**: Initial repo scan
- **Category**: surprise
- **Note**: The repo `/home/user/DRChat---Image-Platform` already contains a `docs/` tree that mirrors and *extends* the contents of the uploaded zip. The zip is a flat PRD pack (`00_README` … `07_superpowers_implementation_prompt`) plus 3 schema files. The repo `docs/` already contains 15 ADRs, an `api/` folder with `openapi.yaml` + per-endpoint markdown, an `architecture/` folder, a `db/initial_schema.sql`, 4 schemas, 4 guidelines, and 5 runbooks. So the question of "where to apply confidence scores" is ambiguous: PRDs only, or specs in `docs/` too? The user said "each PRD/spec", so I'm interpreting that as **both**: PRDs in the zip and each spec/ADR/schema/runbook in `docs/`. I will add scores to all of them.

### Entry 3
- **Trigger**: Decision on where confidence scores live
- **Category**: assumption
- **Note**: The zip is in `/root/.claude/uploads/...` — outside the git repo. To version-control the PRDs *with* their confidence scores, I'll copy the zip contents into a new `prds/` folder inside the repo and annotate those copies. The original zip will remain untouched. This avoids editing files that may be source-of-truth elsewhere.

### Entry 4
- **Trigger**: Confidence scoring rubric
- **Category**: assumption
- **Note**: The user didn't specify a scale. I'm picking **0–100%** with a qualitative band (Very High ≥90, High 75–89, Medium 60–74, Low 40–59, Very Low <40). Each score is "my confidence that I, the current model with current tools and the existing docs, could implement this end-to-end without further human input on requirements." If the user wanted a different rubric (e.g. team confidence, time-to-ship, technical risk), I'll re-score.

### Entry 5
- **Trigger**: Reading 15 ADRs in `docs/adr/`
- **Category**: surprise / doubt
- **Note**: All 15 ADRs share an **identical** Context/Tradeoffs/Notes block ("DreamChat Image Platform needs a clean, independently testable architecture…" / "Requires explicit contracts and discipline…" / "This ADR can be revisited after the first production benchmark."). They look auto-generated from a template, which makes them low-information for actual decision review. The Decision/Consequences sentences are the only file-specific content. I'll still score each on the strength of the *decision* itself, but I'm flagging this as a process smell — ADRs that don't capture the alternatives considered are weak. Worth regenerating them with alternatives + tradeoffs per ADR.

### Entry 6
- **Trigger**: Conflict between `docs/api/openapi.yaml` and `prds/schemas/image_platform_openapi_draft.yaml`
- **Category**: drift
- **Note**: The two OpenAPI specs disagree in non-trivial ways:
  - The PRD draft uses tenant_id/world_id in request bodies + `POST /v1/characters/generate-pack` (entity-id in body).
  - The docs spec uses `POST /v1/characters/{character_id}/visual-identity` and `POST /v1/characters/{character_id}/generate-pack` (entity-id in path) and no tenant_id (just world_id).
  - Job statuses differ: PRD draft has `planning/retrieving_existing_assets/generating_preview/preview_ready/generating_final/final_ready/completed/completed_with_warnings/...`; docs spec has only `queued/running/preview_ready/completed/failed/cancelled`.
  - Quality tier enum differs: PRD `draft|standard|premium`, docs `draft|standard|high`.
  - PRD draft has separate `POST /v1/styles/preview` + `POST /v1/styles/validate`; docs has `POST /v1/styles/{style_id}/preview` and no validate.
  - PRD's `AssetSearchResponse` has `match_type` (exact/variant/fallback cache hit etc.) — useful telemetry. Docs spec drops it.
  - This is a real ambiguity for any implementer. I'm scoring both files but noting both will likely need reconciliation before code. I picked the **docs spec** as more concrete/Go-friendly when scoring overlap.

### Entry 7
- **Trigger**: Scoring PRD 03 (consistency)
- **Category**: doubt
- **Note**: Visual consistency for characters/places across independent generations is a *known-hard* problem for most providers. The PRD specifies data model + reference strategies (prompt-only / seeded / reference-image / LoRA / hybrid), and that's implementable. But the *outcome* — "the same character remains recognizable across asset pack outputs" — depends heavily on whether the chosen provider supports reference-image conditioning and how stable its seeds are. So I'm scoring "consistency *system* code" high but flagging the quality outcome as model-dependent. This split is worth surfacing in the score rationale.

### Entry 8
- **Trigger**: JSON schema files (visual_identity / visual_asset / generation_job / style_profile) and JSON data model
- **Category**: assumption
- **Note**: JSON files can't carry markdown comment blocks without breaking schema validators. I'm creating a sibling `<name>.confidence.md` next to each JSON schema instead of mutating the JSON. Same for the openapi.yaml: I'll put the confidence block at the *bottom* as a YAML comment so the spec stays valid for code generation.

### Entry 9
- **Trigger**: Scoring runbooks
- **Category**: doubt
- **Note**: Runbooks describe "what to do when X breaks" but the code that enables that response (admin tooling, cost aggregation queries, circuit-breaker controls, queue introspection) is largely absent from the specs. So the runbook *narrative* is implementable, but only after backing services are built. I'm scoring runbooks based on whether I could implement the supporting code from current docs, not whether the operational text reads well.

