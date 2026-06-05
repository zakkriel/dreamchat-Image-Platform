# PRD 03 — Character and Place Consistency System

## 1. Purpose

This document defines how the DreamChat Image Platform preserves visual consistency for recurring characters and places.

DreamChat is a persistent world product. The visual layer must therefore also be persistent.

If the user meets a character today and asks for more images tomorrow, the character should remain recognizable.

If the user returns to a place after in-world time passes, the place should remain recognizable unless the world state says it changed.

## 2. Core Problem

Image generators are not naturally consistent across independent requests.

Repeating the same prompt is not enough.

Without a consistency system:

- a character’s face may change
- hair or body type may drift
- clothing motifs may disappear
- scars or signature marks may be lost
- places may change architecture
- a recurring landmark may vanish
- style may shift between generations
- world continuity may be damaged visually

For DreamChat, this is especially dangerous because the whole product promise depends on continuity.

## 3. Core Principle

A character or place should not be treated as a prompt.

It should be treated as a persistent visual identity.

The prompt is only one part of that identity.

The full identity should include:

- canonical visual traits
- visual anchors
- reference assets
- style profile
- generation metadata
- seeds/consistency keys
- version history
- allowed and forbidden drift
- state-specific variants

## 4. Character Visual Identity

### 4.1 Definition

A Character Visual Identity is the persistent visual record for a character/entity.

It represents what must remain recognizable across generated images.

### 4.2 Required Fields

A character visual identity should include:

```json
{
  "visual_identity_id": "vis_char_123",
  "world_id": "world_42",
  "character_id": "char_789",
  "identity_type": "character",
  "current_version": 1,
  "canonical_traits": {
    "apparent_age": "mid 30s",
    "body_type": "lean, tall",
    "skin_tone": "olive",
    "face_shape": "angular",
    "hair": "black, shoulder-length, slightly messy",
    "eyes": "dark brown",
    "distinctive_marks": ["thin scar over left eyebrow"],
    "signature_clothing": ["dark red scarf", "weathered leather coat"],
    "silhouette_cues": ["narrow shoulders", "high collar"]
  },
  "style_profile_id": "style_001",
  "seed_strategy": "identity_seeded",
  "identity_seed": "seed_char_789_v1",
  "anchor_asset_ids": ["asset_001", "asset_002"],
  "reference_asset_ids": ["asset_003"],
  "allowed_variation": {
    "expression": true,
    "pose": true,
    "camera_angle": true,
    "lighting": true,
    "minor_clothing_detail": true
  },
  "forbidden_drift": {
    "hair_color": true,
    "eye_color": true,
    "body_type": true,
    "distinctive_marks": true,
    "apparent_age_band": true
  }
}
```

### 4.3 Invariant Traits

Invariant traits are visual facts that should not drift unless the character receives a new visual version.

Examples:

- face structure
- hair color
- eye color
- approximate age band
- skin tone
- major scars
- body build
- species/body morphology where relevant
- signature silhouette
- recurring accessory or motif

### 4.4 Variable Traits

Variable traits may change across normal variants.

Examples:

- expression
- pose
- camera angle
- lighting
- small clothing details
- minor hairstyle movement
- emotion
- dirt/blood/weathering when scene-appropriate
- outfit state if requested

### 4.5 New Character Version

A new character visual version should be created when the character has a meaningful visual change.

Examples:

- major haircut
- aging jump
- injury/scarring
- transformation
- new identity/disguise that becomes persistent
- major outfit/status change
- cybernetic/physical modification
- supernatural change

The old version should remain stored.

The system should know which version is current.

## 5. Place Visual Identity

### 5.1 Definition

A Place Visual Identity is the persistent visual record for a location/place.

It represents what makes the place recognizable across images.

### 5.2 Required Fields

A place visual identity should include:

```json
{
  "visual_identity_id": "vis_place_123",
  "world_id": "world_42",
  "place_id": "place_456",
  "identity_type": "place",
  "current_version": 1,
  "canonical_features": {
    "location_type": "harbor office",
    "architecture": "old stone building with brass-framed windows",
    "layout_cues": ["large central desk", "view over foggy docks"],
    "landmarks": ["green glass lamp", "cracked harbor map on back wall"],
    "palette": "dark teal, brass, wet stone",
    "default_mood": "procedural, tense, humid",
    "lighting_defaults": "dim interior, fog-filtered daylight"
  },
  "style_profile_id": "style_001",
  "seed_strategy": "identity_seeded",
  "identity_seed": "seed_place_456_v1",
  "anchor_asset_ids": ["asset_010", "asset_011"],
  "state_tags": ["intact", "occupied", "rainy"],
  "allowed_variation": {
    "time_of_day": true,
    "weather": true,
    "crowd_level": true,
    "camera_angle": true,
    "lighting": true
  },
  "forbidden_drift": {
    "location_type": true,
    "core_landmarks": true,
    "architecture_family": true,
    "layout_identity": true
  }
}
```

### 5.3 Invariant Place Traits

Invariant traits include:

- location type
- architecture family
- core landmarks
- recurring layout cues
- recognizable palette/mood
- spatial identity
- world-specific design motifs

### 5.4 Variable Place Traits

Variable traits include:

- time of day
- weather
- crowd level
- lighting
- camera angle
- temporary objects
- atmosphere
- seasonal effects
- temporary damage if not canonical

### 5.5 New Place Version

A new place visual version should be created when the place changes canonically.

Examples:

- burned down
- rebuilt
- occupied by a faction
- abandoned
- renovated
- flooded
- corrupted/transformed
- moved into a different era/time state

A night version is not necessarily a new version. It is usually a variant.

A destroyed version is usually a new version.

## 6. Visual Anchors

Visual anchors are the most important generated assets for identity preservation.

They should be used as reference for future generation when supported by the model/backend.

### Character Anchors

Initial character anchors should include:

- neutral front portrait
- 3/4 portrait
- optionally full-body or half-body reference

### Place Anchors

Initial place anchors should include:

- establishing view
- recognizable interior/exterior view
- landmark-focused view where useful

## 7. Reference Strategy

The service should support multiple consistency strategies depending on backend capability.

### 7.1 Prompt-Only Consistency

Uses canonical traits and fixed prompt templates.

Lowest consistency. Useful for early demo only.

### 7.2 Seeded Consistency

Uses stable seeds/consistency keys per identity.

Better but not enough alone.

### 7.3 Reference Image Consistency

Uses saved anchor images as reference inputs.

Preferred for recurring characters/places when backend supports it.

### 7.4 Adapter / LoRA Consistency

Uses LoRAs/adapters for strong consistency.

This should not be required for the first demo, but the architecture should support it later.

### 7.5 Hybrid Consistency

Combines:

- canonical trait prompt
- stable seed
- anchor images
- style profile
- negative drift prompts
- model/version metadata

This should be the target design.

## 8. Provider Capability Floor

The platform preserves visual identity inputs, but visual consistency depends on provider capability. A provider that cannot condition generation on identity, references, or controlled variants must not be used for consistency-critical character/place generation.

This section makes the provider requirement explicit so the consistency claim is testable *before* a provider is selected, not litigated after assets ship drifted.

### 8.1 Minimum capability for recurring character consistency

A provider used for recurring character generation MUST support at least one of:

- reference-image conditioning
- image-to-image conditioning
- multi-reference generation
- LoRA / adapters
- an explicit identity / character-consistency feature exposed by the provider

A pure text-to-image provider with no reference/identity mechanism is **not eligible** for recurring character generation. It may still be used for disposable drafts (see §8.4 routing rule).

### 8.2 Minimum capability for recurring place consistency

A provider used for recurring place generation MUST support at least one of:

- reference-image conditioning
- image-to-image conditioning
- multi-reference generation
- LoRA / adapters
- **seed control + strong prompt adherence** (places tolerate slightly more variance than faces; deterministic seeds plus prompts that survive re-rolls can be enough when reference conditioning isn't available)

### 8.3 Provider capability levels

Every provider model is classified with one or more of these levels. The values match the `ProviderCapability` enum in `docs/api/openapi.yaml`.

| Level | Meaning |
|---|---|
| `draft_only` | Can generate disposable drafts. Not suitable for consistency-critical assets. |
| `scene_capable` | Can generate scene / place visuals with acceptable repeatability across time-of-day / weather variants. |
| `identity_capable` | Can generate recognizable recurring characters using identity inputs (references, seeds + identity prompts, LoRA, or vendor identity feature). |
| `pack_capable` | Can generate multiple controlled variants (angles, expressions, outfits) from the same identity within one batch. Requires `identity_capable` plus reliable conditioning across the batch. |
| `production_capable` | Supports identity, variants, metadata, predictable latency, cost telemetry, and acceptable failure rates under production volume. |

`production_capable` implies `pack_capable` implies `identity_capable`. `scene_capable` is parallel to the identity axis (a model may be scene-capable but not identity-capable, or vice versa).

### 8.4 Routing rules

The provider router (ADR-007) enforces these constraints:

- **Pure text-to-image providers (draft_only)** may be used for: early drafts, disposable artifacts, one-off scene mood images, and benchmark warm-up. They MUST NOT be used for: recurring character generation, character pack generation, or any asset attached to a `visual_identity` that is intended to recur.
- **`identity_capable` or higher** is required for recurring NPC portrait packs and for any generation that references a `visual_identity_id` with `current_version >= 1`.
- **`pack_capable` or higher** is required for character expression / angle pack generation (PRD 04 §4.4 character_expression_pack, character_full_reference_pack) where multiple variants must share an identity.
- **`production_capable`** is required for any provider serving live production traffic (as opposed to internal experimentation or benchmark runs).

The router records the routing decision and its capability rationale on `generation_jobs` for audit.

### 8.5 Acceptance tests

These tests validate that a candidate provider+model actually delivers the consistency the capability level claims. Run before a provider is promoted from experimental to production routing.

#### Character consistency test

1. Generate one character anchor image (neutral front portrait) using the candidate provider with full identity input (canonical traits, style profile, seed, and reference conditioning if supported).
2. Using the same identity inputs, generate five variants:
   - neutral 3/4 angle
   - side angle
   - warm expression
   - tense expression
   - one additional expression at the reviewer's choice
3. A human reviewer scores each of the five variants 1–5 for identity consistency against the anchor (5 = clearly the same person, 1 = no recognizable identity carryover).
4. **Pass condition:** at least 4 of 5 variants score ≥ 4/5.

A provider that fails this test is downgraded from `identity_capable` until the failure mode is understood and re-tested.

#### Place consistency test

1. Generate one place anchor image (establishing wide view) using the candidate provider with full place identity input (canonical features, landmarks, palette, seed, references if supported).
2. Using the same identity inputs, generate five variants:
   - day view
   - night view
   - empty view
   - crowded / busy view
   - altered mood or weather at reviewer's choice
3. A human reviewer scores each variant 1–5 for place consistency against the anchor (5 = clearly the same location, 1 = unrelated).
4. **Pass condition:** at least 4 of 5 variants score ≥ 4/5.

A provider that fails this test is downgraded from `scene_capable` until the failure mode is understood and re-tested.

### 8.6 What this section does not promise

The capability floor and acceptance tests catch the worst provider mismatches before they reach production. They do **not** guarantee:

- Zero drift across long-running worlds (drift detection per §9 remains future work).
- Cross-provider consistency (an asset generated by provider A and a variant generated by provider B may still drift; the router avoids this by pinning a `visual_identity_id` to one provider for its lifetime where possible).
- Identical output across providers at the same capability level (capability is a floor, not a contract for visual style).

## 9. Identity Drift Detection

The first version may not need automated computer-vision verification.

However, the data model should support future drift detection.

Future checks may include:

- face similarity
- CLIP/image embedding similarity
- landmark similarity
- trait classifier checks
- human/user correction
- manual “this does not look like them” feedback

## 10. User and Creator Corrections

The user or creator should eventually be able to correct visual identity.

Examples:

- “Mara has silver hair, not black hair.”
- “This portrait does not look like the same character.”
- “The market should always have the red tower visible.”
- “This place should feel colder and more industrial.”

Corrections should update the visual identity record for future generation.

They should not necessarily rewrite all existing assets.

## 11. Versioning Rules

### Same Version

Use same version when:

- same identity/state
- different expression
- different angle
- different lighting
- different time of day
- same location before/after no canonical change

### Minor Variant

Use minor variant when:

- new expression
- new pose
- new outfit variant that does not change identity
- different weather/time/crowd level

### New Version

Use new version when:

- canonical physical change
- canonical place transformation
- visual correction changes identity anchors
- major style migration for a world
- new persistent era/state

## 12. API Implications

The generation API should never accept only a raw prompt for recurring characters/places.

It should accept or resolve:

- `visual_identity_id`
- `version`
- `style_profile_id`
- `asset_role`
- `variant_request`
- `anchor_asset_ids`
- `reference_asset_ids`

## 13. Acceptance Criteria

This PRD is implemented when:

- characters have persistent visual identity records
- places have persistent visual identity records
- generated assets are linked to identities and versions
- future generations can reuse identity anchors
- variant vs new version is clearly represented
- the same character remains recognizable across asset pack outputs
- the same place remains recognizable across time/weather/angle outputs
- the system stores enough metadata to debug visual drift

---

## Confidence to Implement

**Score: 82/100 — High** *(was 65; +17 after §8 Provider Capability Floor added on 2026-06-05 — the visual-consistency outcome is now testable and the routing constraints prevent unsuitable providers from being used for recurring assets.)*

The two split scores are now better aligned:

1. **The consistency *system* (data model, identity records, versioning rules, hybrid reference strategy plumbing) — ~90/100.** Unchanged: storing canonical traits, anchors, seeds, allowed/forbidden drift, and routing them into prompts is straightforward.
2. **The actual *visual* consistency outcome — ~75/100** (was ~50/100). The provider capability floor (§8) makes the "what does the provider have to support" question explicit, the `ProviderCapability` enum classifies providers concretely (`draft_only`, `scene_capable`, `identity_capable`, `pack_capable`, `production_capable`), routing rules (§8.4) block unsuitable providers from recurring-character or pack jobs, and the acceptance tests (§8.5) make consistency claims falsifiable before launch. What remains uncertain is the *long-tail quality* of any specific provider once accepted — even an `identity_capable` provider may still drift at the margins, which is why drift detection (§9) remains future work.

The averaged score climbs to **82**. To reach 90+ the platform would need a working drift-detection pipeline (§9) so consistency holds over time, not just at acceptance.

