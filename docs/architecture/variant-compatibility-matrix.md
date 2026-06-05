# Variant Compatibility Matrix

## 1. Purpose

`docs/adr/009-retrieval-before-generation.md`, `prds/05_storage_retrieval_versioning_and_cache_strategy.md`, and `docs/architecture/asset-versioning.md` all reference "variant match" without defining which variants are acceptable substitutes for which. This document closes that gap.

The matrix turns "retrieval before generation" from a vague principle into a deterministic, product-safe algorithm. It is the source of truth for fallback rules.

## 2. Why this matters — the product-safety rule

> **Fallback must never visually contradict known world state. It is better to show no image or a loading placeholder than to show a misleading variant.**

This rule overrides every other rule in this document. If a candidate fallback would visually contradict the scene (a warm smile while the character has just been informed of a death; a sunny day view of a city that is currently under siege; an intact letter that has been burned in canon), it is `invalid_match` regardless of its `compatibility_score`. When in doubt, generate.

## 3. Four compatibility outcomes

Every retrieval lookup against an existing asset resolves to exactly one of these:

| Outcome | Meaning |
|---|---|
| `exact_match` | The requested variant exists with the requested style profile, state version, and quality tier (or higher) and can be used directly. |
| `compatible_match` | The requested variant does not exist, but another stored variant is **product-safe to substitute** under this matrix. Returned without UX warning. |
| `preview_fallback` | A different variant is **shown temporarily** while the correct variant is being generated. UX should mark the result as provisional and refresh when the real asset lands. |
| `invalid_match` | The substitute must **not** be used. The retrieval result is "miss"; the only path forward is to generate. |

`compatible_match` and `preview_fallback` are different in intent: `compatible_match` is "good enough that we don't need to generate," while `preview_fallback` is "we're going to generate, but show this in the meantime."

## 4. Retrieval rule

The retrieval layer (called by every generation handler per ADR-009, and directly by `POST /v1/assets/search`) executes:

1. **Try exact match.** Owner + variant_key + state_version + style_profile_id + style_profile_version + quality_tier ≥ requested + asset_state = active. If found, return `exact_match`.
2. **Try compatible match.** Apply the per-entity rules below (§7, §8, §9). If a candidate has compatibility outcome `compatible_match` *and* the request's `fallback_policy` allows it, return `compatible_match` with the matrix rule recorded in `fallback_reason`.
3. **Try preview fallback** *only* if the request's `fallback_policy` is `preview_allowed` or `any_existing`. Apply the per-entity rules below. Return `preview_fallback` with `fallback_reason` set. The caller is responsible for triggering generation of the real variant.
4. **Otherwise generate a new asset.** Return `generated_required` (no asset yet; the response carries `generation_recommended: true` plus a hint at which endpoint and roles to request).
5. **Never use `invalid_match`.** If a candidate would be misleading per the per-entity rules or §2's product-safety rule, it is treated as no candidate at all.

The result of this rule lands in `AssetSearchResponse.match_type`.

## 5. `fallback_policy`

Every retrieval call carries a `fallback_policy` (default `compatible_only`). Allowed values:

| Value | Meaning | When to use |
|---|---|---|
| `none` | Exact match only, otherwise treat as miss and (for generation paths) generate. | Identity anchor creation; canonical-trait queries; admin "is this asset really here?" probes. |
| `compatible_only` | `exact_match` or `compatible_match` allowed. No preview substitution. | Normal product UX where the displayed image must be reliable and stable. Default. |
| `preview_allowed` | `exact_match`, `compatible_match`, or `preview_fallback` allowed. Caller commits to showing the preview as provisional and refreshing when the real asset arrives. | Scene canvas, participant avatars during live play when waiting would hurt UX. |
| `any_existing` | All match types allowed including marginal candidates. **Admin/debug only — not allowed in normal product UX.** | Benchmark runner inspection; admin asset-explorer; data-quality dashboards. |

Generation endpoints (`POST /v1/characters/{id}/generate-pack`, etc.) accept `fallback_policy` too; the policy controls whether a retrieval hit *short-circuits* the generation. Default for generation endpoints is `compatible_only` (skip generation only when an exact or compatible asset already exists).

## 6. Variant dimensions

The matrix is defined over these dimensions. Each asset declares values across them (some may be null per asset type):

| Dimension | Source | Example values |
|---|---|---|
| `asset_type` | `VisualAsset.asset_type` | `character_portrait`, `place_scene`, `artifact`, `expression`, `angle_variant` |
| `variant_key` | `VisualAsset.variant_key` | `neutral_front`, `warm_expression`, `night_view`, `sealed_letter` |
| `variant_family` | new field on VisualAsset | `neutral`, `warm`, `tense`, `strong_emotion`, `establishing`, `weather`, `document_clean`, `document_altered` |
| `pose` | `variant_tags.pose` | `standing`, `seated`, `combat_ready` |
| `angle` | `variant_tags.angle` | `front`, `three_quarter`, `side_profile`, `back` |
| `expression` | `variant_tags.expression` | `neutral`, `warm`, `serious`, `tense`, `angry`, `terrified`, `crying` |
| `mood` | `variant_tags.mood` | `calm`, `tense`, `celebratory`, `mourning`, `industrial` |
| `time_of_day` | `variant_tags.time_of_day` | `dawn`, `day`, `dusk`, `night` |
| `weather` | `variant_tags.weather` | `clear`, `rain`, `storm`, `fog`, `snow` |
| `state_version` | new field on VisualAsset | integer; the canonical state version of the entity at generation time |
| `style_profile_id` | `VisualAsset.style_profile_id` | `style_dark_cinematic` |
| `aspect_ratio` | `VisualAsset.metadata.aspect_ratio` | `1:1`, `4:3`, `16:9`, `9:16` |
| `quality_tier` | `VisualAsset.quality_tier` | `draft`, `standard`, `high` |

A retrieval request specifies the *requested* values; the matrix decides which stored variants are eligible substitutes.

## 7. Character variant compatibility

### 7.1 Generic presence (low specificity request — "any portrait will do")

Used when the UI needs a recognizable image of the character without specifying expression or angle (e.g. the participants list at scene start).

- `neutral_front`, `neutral_3q`, and `neutral_bust` are **`compatible_match`** for generic character presence.
- `neutral_front` is **`preview_fallback`** for missing mild expressions (`warm`, `serious`) when `fallback_policy = preview_allowed`.
- `neutral_front` **must not** silently replace strong emotional states (see §7.2) — that case is `invalid_match`.

### 7.2 Expression compatibility

| Requested | `compatible_match` substitutes | `preview_fallback` substitutes | `invalid_match` (never substitute) |
|---|---|---|---|
| `warm_expression` | `smiling` (same family) | `neutral_front` | strong-emotion variants |
| `smiling` | `warm_expression` | `neutral_front` | strong-emotion variants |
| `tense` | `serious` | `neutral_front` | warm-family variants |
| `serious` | `tense` (mild) | `neutral_front` | warm-family variants |
| `angry` | — | — | every other expression — **always generate** |
| `terrified` | — | — | every other expression — **always generate** |
| `injured` | — | — | every other expression — **always generate** (state change) |
| `crying` | — | — | every other expression — **always generate** |
| `romantic / intimate` | — | — | every other expression — **always generate** |
| `disguised` | — | — | every other expression — **always generate** |
| `battle_damaged` | — | — | every other expression — **always generate** (state change) |

**Rule:** strong-emotion requests (`angry`, `terrified`, `injured`, `crying`, `romantic/intimate`, `disguised`, `battle_damaged`) must generate if no exact match exists. They may be marked `preview_fallback` against `neutral_front` only when the request explicitly opts in via `fallback_policy: preview_allowed` *and* the variant is annotated `fallback_allowed: true` (off by default for strong-emotion targets).

### 7.3 Angle compatibility

- `front` and `three_quarter` are **`compatible_match`** for each other when used for normal portrait display in the participants area or scene canvas.
- `side_profile` is **`preview_fallback`** for portrait display (UI may need to crop). It is **`invalid_match`** for identity anchor creation — anchors must be front or three-quarter so reference conditioning works.
- `full_body` **must not** fall back to `bust` or any portrait crop when the UI specifically needs clothing/body silhouette (outfit selection, costume preview, full-character context).
- `back` view never substitutes for any front-facing request.

### 7.4 State compatibility (canonical visual change)

Character state versions are **strict**:

- If a character has a new canonical visual state — scar, haircut, uniform, injury, age shift, disguise, transformation, supernatural change — old-state assets are **`invalid_match`** for current-state display.
- Old-state assets remain valid only when explicitly requested as historical / flashback / memory (UI sets `state_version` to the older value).
- Forward substitution (use newer state for older state request) is also `invalid_match`.

This is the "version vs. variant" line from `docs/architecture/asset-versioning.md` enforced at retrieval time.

## 8. Place variant compatibility

### 8.1 Generic place presence (low-specificity request)

- `establishing_wide_day` is **`compatible_match`** for generic place card / world-codex thumbnail.
- `establishing_wide` (no time-of-day) is **`preview_fallback`** for missing time-specific variants when `fallback_policy = preview_allowed`.
- Close-detail views (`landmark_detail`, `interior_view`) are **`invalid_match`** for "give me a sense of this place" requests — they don't establish the location.

### 8.2 Time of day

- `day` is **`invalid_match`** as a substitute for `night`. The opposite is also `invalid_match`.
- `day` and `night` may be **`preview_fallback`** for each other **only** when `fallback_policy = preview_allowed`. UI should show "loading night variant" in that case.
- `dawn` and `dusk` may be `compatible_match` for each other (lighting is similar) but not for `day` or `night`.

### 8.3 Weather / mood

- Mild weather variants (`clear`, `light_rain`, `overcast`) are **`compatible_match`** for each other when the scene is calm.
- `storm`, `fire`, `flood`, `warzone`, `abandoned`, `damaged`, `destroyed`, `celebration` are **strong-mood / state-altering** variants. They are **`invalid_match`** for calm/default requests and vice versa.
- A celebratory or somber scene must not use a default neutral variant — that contradicts the world state.

### 8.4 State / version (canonical place change)

Place state is **strict**:

- A destroyed / burned / renovated / occupied / corrupted / faction-controlled place creates a **new `state_version`** per `docs/architecture/asset-versioning.md`.
- Previous-version place assets are **`invalid_match`** for current-state display.
- They remain valid only when used for history / memory / flashback / timeline (UI explicitly requests `state_version = N-k`).
- Forward substitution (use older state for newer state request) is also `invalid_match` — that would show a building intact after it has burned.

## 9. Artifact variant compatibility

### 9.1 Generic artifact

- The clean / default artifact image is **`compatible_match`** for representing the artifact in inventory or context sidebar.
- `damaged`, `bloodstained`, `encrypted`, `opened`, `broken`, `forged`, `transformed` artifact states are **separate `state_version`s** or strict variants. They are **`invalid_match`** for the default state, and vice versa.

### 9.2 Documents

Documents have especially strict compatibility because their visual identity carries information:

- `sealed_letter`, `opened_letter`, `signed_document`, `forged_document`, `burned_document` are **`invalid_match`** for each other. The state of the document is *part of the canon*.
- A generic "document" image is **`preview_fallback`** only (and only when `fallback_policy = preview_allowed`). Production UX should generate the specific state.

### 9.3 Symbols and logos

Symbol identity is **strict**:

- A different symbol variant is **`invalid_match`** unless it is the same symbol at the same `state_version`.
- This includes faction sigils, brand marks, magical glyphs, heraldic crests, official seals. Substituting one for another would imply a world-state lie.

## 10. Schema and API additions

### 10.1 New fields on `VisualAsset`

| Field | Type | Purpose |
|---|---|---|
| `variant_family` | string | Grouping for matrix lookups (`neutral`, `warm`, `strong_emotion`, `establishing`, `document_clean`, …). |
| `state_version` | integer | State version for the entity at generation time (independent of `visual_identity_version`, which is identity-level). |
| `compatibility_tags` | string[] | Free-form tags that the matrix consults (`generic_presence`, `preview_safe`, `identity_anchor_eligible`). |
| `fallback_allowed` | boolean | Whether this asset may be returned as `preview_fallback` for other variants. Default `false` for strong-emotion targets and altered-state artifacts/documents. |
| `fallback_rank` | integer | Lower = preferred fallback within its compatibility tier. Used to pick the best candidate when several would qualify. |
| `is_identity_anchor` | boolean | True for anchor assets used as reference inputs. **Anchor assets must never be returned as a `compatible_match` for a non-anchor request** — they are reference inputs, not display assets. |

### 10.2 Request: `fallback_policy`

Added to:
- `AssetSearchRequest` (controls what counts as a hit).
- `GenerateCharacterPackRequest`, `GeneratePlacePackRequest`, `GenerateArtifactRequest` (controls whether retrieval-before-generation short-circuits the job; default `compatible_only`).

### 10.3 Response: `match_type` (renamed values), `compatibility_score`, `fallback_reason`

`AssetSearchResponse` carries:

- `match_type`: one of `exact_match`, `compatible_match`, `preview_fallback`, `generated_required`.
- `compatibility_score`: 0.0–1.0. `1.0` for `exact_match`. Lower for compatible / preview fallback; the score is the matrix rule's confidence that the substitution is safe (`fallback_rank` combined with the rule's intrinsic confidence).
- `fallback_reason`: when `match_type ∈ {compatible_match, preview_fallback}`, names the matrix rule that allowed the substitution (e.g. `"character.expression.warm→smiling.compatible"`, `"place.time_of_day.day↔night.preview_only"`). Useful for debugging and for audit when the UX feels off.

## 11. Implementation guidance

- The matrix is encoded in `internal/assets/compatibility.go` (or equivalent) as a table indexed by `(entity_type, requested_variant_family, candidate_variant_family)` returning one of the four outcomes plus a confidence score.
- The retrieval algorithm (`internal/assets/retrieval.go`) implements §4's steps. It must be **deterministic** — same inputs always produce the same outcome — so generation decisions are reproducible and the cache-hit metrics are honest.
- The product-safety rule (§2) is implemented as a final filter: even when the matrix says "compatible," a separate check against known world-state hints in the request (`scene_mood`, `recent_canonical_events`) may downgrade the result to `invalid_match` and force generation. This filter is initially a stub; product can grow it over time.
- The matrix must be unit-testable end-to-end with golden tables. `docs/guidelines/testing-strategy.md` "Golden Tests" already specifies the pattern for the prompt compiler — the matrix gets the same treatment.

## 12. Related documents

- `docs/adr/009-retrieval-before-generation.md` — the decision this matrix implements.
- `docs/architecture/asset-versioning.md` — version vs. variant boundary.
- `prds/05_storage_retrieval_versioning_and_cache_strategy.md` — retrieval algorithm prose; this matrix is the concrete rules behind §8's "variant match."
- `prds/03_character_and_place_consistency_system.md` §8 — provider capability floor; identity-anchor compatibility (§7.3) intersects with the consistency requirements there.
- `docs/api/openapi.yaml` — `FallbackPolicy`, `MatchType` enums; `VisualAsset` and `AssetSearchRequest`/`AssetSearchResponse` field additions.

---

## Confidence to Implement

**Score: 90/100 — Very High**

The matrix turns the previously-vague "variant match" into a typed lookup table: four outcomes, twelve dimensions, explicit rules per entity type, and an unambiguous algorithm in §4. The Go side is a static table plus a deterministic function; golden tests cover correctness. Subtracting points because the product-safety filter (§2, §11) — preventing visual contradictions of known world state — is described but not yet specified in terms of which world-state hints the retrieval call must consider. That's a follow-up product decision, not a blocker.
