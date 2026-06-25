# PRD 08 — NPC Expression Sprite-Sheet Pipeline

**Type:** PRD (product requirements — what & why & acceptance). Implementation lives in `image_platform/architecture/sprite_sheet_pipeline.md` and the chunk build plan; this doc declares product intent and constraints only, never implementation detail.
**Status:** Draft — pending sign-off.
**Repo target:** `prds/08_npc_expression_sprite_pipeline.md` (DreamChat Image Platform repo, D-6).
**Supersedes:** in this repo, **PRD 04** (`prds/04_asset_packs_variants_and_expressions.md`) **§4 (Character Starter Pack — its NPC portrait/expression-pack content)** for the NPC expression-pack and expression sprite-sheet pipeline. PRD 04 §5 (place packs), §6 (artifact assets), §7 (preview/final for packs), §8 (pack templates), and §11 (UI consumption) remain in force, untouched. *(This PRD corresponds to the backend program's "PRD 08 §5–6"; the asset-packs/sprite-sheet PRD is **PRD 04** in this repo.)*
**Why a new doc, not an edit:** PRD 04 predates the content-governance contract (ADR-P002 / E-1) and the governed `POST /v1/generations` request contract shipped in the platform's Chunk 2. The standalone `generate-*` endpoints described in the prior API addendum would be ungoverned generation doors, which is now disallowed. This PRD restates the expression-pipeline requirements current to that constraint.

---

## 1. Purpose

Let a meaningful NPC visually react during play — neutral, suspicious, warm, angry, and a broader expression set — **without runtime image generation**, by generating one coherent expression sprite sheet ahead of time and slicing it into individually addressable, reusable expression assets.

## 2. Product principle

> Generate the visual range early. Reuse it during play. Never block narration on image work.

A meaningful NPC must not be a single static portrait. Expressions are produced as **one coherent set** (one generation) so the character stays recognizable across moods, then sliced into parts the UI can swap instantly.

## 3. Why this matters

The chosen UX uses circular NPC avatars with active-speaker highlighting and emotional reactions during dialogue. If expressions were generated on demand mid-play, the experience would be slow, expensive, and visually inconsistent (drift between separately generated images). One sheet → many slices solves cost, latency, and identity consistency at once.

## 4. Scope

**In scope (this pipeline / this chunk):**
- Generating one NPC expression sprite sheet against a fixed layout contract.
- Deterministic slicing of that sheet into individual expression assets.
- Parent/child asset relationships and expression metadata.
- Runtime retrieval of the best expression asset, with a clean fallback chain (read path).
- Provider capability awareness and sheet-failure handling.

**Out of scope (explicit non-goals):**
- Location scene-state variant packs — governed by PRD 04 §5 (place packs), separate workstream.
- Artifact/item visuals and the Aux sidebar — PRD 04 §6.
- Any runtime/on-demand generation during normal play (allowed only as an explicit, essential exception, not the default).
- Per-character LoRA, self-hosted inference, programmatic transforms, semantic dedup — not specified in any ratified doc; out of this PRD.
- Free-text prompt input — content derives from the NPC's structured visual identity, never a user prompt (preserves the governance model; consistent with the no-prompt-field contract).

## 5. Requirements

### R1 — Expression sheet generation
When a meaningful NPC is created or promoted, the platform generates one expression sprite sheet from the NPC's visual identity, following a fixed layout contract. V1 contract: a 2×5 grid, 10 cells, single identity, consistent outfit/angle/style/lighting, no in-cell text or frames, safe inner margin.
Default cell order: neutral, warm, amused, suspicious, angry, afraid, sad, surprised, focused, exhausted. The expression set may be adapted by genre, maturity setting, and character type.

### R2 — Deterministic slicing
The generated sheet is sliced into individual expression assets by the fixed grid contract, deterministically (same sheet + same contract → same slices). Each slice becomes a normal, independently addressable, reusable visual asset.

### R3 — Parent/child relationships & metadata
The original sheet is stored as a parent asset; each slice is a child carrying its expression key, crop index, crop box, source visual-identity version, and generating-job reference. (The data model for this shipped in the platform's Chunk 1: `sprite_sheet_contract` / `sprite_sheet_slice` tables and the `visual_assets` parent/child columns.)

### R4 — Runtime expression retrieval (read path)
The DreamChat core requests an expression asset by desired expression (and optional angle) with a fallback policy. The platform returns, in order: (1) exact expression match, (2) closest mapped expression, (3) neutral, (4) base portrait. **Runtime retrieval must not trigger generation by default.** This is a read endpoint and does not pass through the generation/governance/cost path.

### R5 — Governance, async, and routing constraints
- **No generation request may bypass content governance.** Sheet generation is a generation request and must pass the governed path (the verified-envelope gate, ADR-P002 / E-1) before any provider dispatch. No new standalone ungoverned generate endpoint is introduced. *(How this routes — through the existing `/v1/generations` contract vs. a governed sheet sub-path — is an implementation decision for the chunk spec, not this PRD.)*
- **Generation is asynchronous and never blocks narration** (D-8). The play loop never waits on a sheet.
- Content is derived from the NPC's structured visual identity after the gate; the gate never reads a prompt.

### R6 — Provider capability & failure handling
- The platform routes sheet generation only to providers that advertise sprite-sheet capability; where a provider cannot produce a reliable grid, it falls back to separate-expression generation.
- Failure modes have defined behavior: provider ignores the grid → mark sheet failed quality, retry or fall back to separate expressions; some cells invalid → store the valid cells, mark missing expressions, use the fallback map; identity drift too high → reject or route to manual review; sheet too slow → serve the base portrait first and continue the sheet in the background; cost spike → throttle pack generation, prefer the smaller PoC pack.

## 6. Acceptance criteria

1. A meaningful NPC can be given a 2×5 expression sheet via a single governed generation request.
2. The sheet is sliced deterministically into 10 individually addressable, reusable expression assets.
3. Each slice is retrievable by expression and preserves the NPC identity closely enough for UI use.
4. A requested expression that doesn't exist falls back cleanly (closest → neutral → base portrait).
5. Runtime retrieval never triggers generation.
6. No generation request reaches a provider without passing content governance.
7. Narration latency is unaffected by sheet generation (async, D-8).
8. Partial-sheet and provider-incapable cases degrade per R6 rather than failing the NPC outright.

## 7. Constraints & dependencies

- **Already shipped (platform Chunk 1):** the sprite-sheet data model (`sprite_sheet_contract`, `sprite_sheet_slice`, and `visual_assets` parent/child columns). This pipeline builds on it; no fresh schema for the expression path beyond verification.
- **Open verification (not an assumption to carry):** confirm whether Chunk 1 also added the `scene_state` / `match_tags` columns (those serve location variant packs, PRD 04 §5) or only the expression-sheet columns. If absent, they are out of scope here and belong to the location-pack workstream.
- **Governed-routing reconciliation:** the prior API addendum's standalone `POST .../generate-expression-sheet` is superseded as an *ungoverned* door; the governed routing is resolved in the chunk spec. The **read** endpoints (`GET .../expression-asset`, scene-state resolution) remain valid as retrieval paths.
- **Single source of truth (D-6):** on sign-off, this PRD is authoritative for the expression pipeline; PRD 04 §4 (NPC portrait/expression-pack content) is marked superseded with a pointer here.

## 8. Open questions (resolve at chunk brainstorm, not silently)

- Cells-per-sheet ceiling before identity drift exceeds the QA bar (research suggests usable to ~10, watch drift past ~9–10) — pin the per-cell quality gate and the drifted-cell re-gen fallback.
- Governed routing shape: extend `/v1/generations` to carry a sheet/grid intent (lifting the Chunk 2 `grid.enabled` 501-gate) vs. a dedicated governed sheet path. Implementation call for the chunk spec.
- Manual-review queue: in or out for V1 (PRD 04 §4 implies optional).

## 9. Source documents

Draws from / refines: PRD 04 `prds/04_asset_packs_variants_and_expressions.md` §4 (NPC portrait/expression-pack content); `image_platform/architecture/sprite_sheet_pipeline.md`; `image_platform/api/asset_pack_and_sprite_sheet_api_addendum.md`; `image_platform/implementation_prompt_v2.md`. Governed by: `prd_private_public_content_governance.md`, ADR-P002, and the Rules Register (E-1, D-6, D-8). Builds on platform Chunk 1 schema.
