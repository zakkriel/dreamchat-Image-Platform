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

## 8. Identity Drift Detection

The first version may not need automated computer-vision verification.

However, the data model should support future drift detection.

Future checks may include:

- face similarity
- CLIP/image embedding similarity
- landmark similarity
- trait classifier checks
- human/user correction
- manual “this does not look like them” feedback

## 9. User and Creator Corrections

The user or creator should eventually be able to correct visual identity.

Examples:

- “Mara has silver hair, not black hair.”
- “This portrait does not look like the same character.”
- “The market should always have the red tower visible.”
- “This place should feel colder and more industrial.”

Corrections should update the visual identity record for future generation.

They should not necessarily rewrite all existing assets.

## 10. Versioning Rules

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

## 11. API Implications

The generation API should never accept only a raw prompt for recurring characters/places.

It should accept or resolve:

- `visual_identity_id`
- `version`
- `style_profile_id`
- `asset_role`
- `variant_request`
- `anchor_asset_ids`
- `reference_asset_ids`

## 12. Acceptance Criteria

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

**Score: 65/100 — Medium**

Two distinct things are bundled here and they deserve different scores:

1. **The consistency *system* (data model, identity records, versioning rules, hybrid reference strategy plumbing) — ~90/100.** Storing canonical traits, anchors, seeds, allowed/forbidden drift, and routing them into prompts is straightforward.
2. **The actual *visual* consistency outcome — ~50/100.** Whether "the same character remains recognizable across asset pack outputs" is true depends on provider capabilities (does the model accept reference images? are seeds stable across calls? does it respect identity tokens?). Most current text-to-image providers are inconsistent across independent prompts; reference-image conditioning + LoRAs help but aren't universally available. This PRD even acknowledges this with the "first version may not need automated CV verification" hedge.

I'm averaging to **65** to surface the second risk explicitly. The PRD should specify a *minimum* provider capability ("requires image-to-image with reference conditioning" or "requires per-identity LoRA training") to lift confidence; otherwise the acceptance criterion is partly a quality bet on the chosen provider — see `frustration_log.md` entry 7.

