# PRD 04 — Asset Packs, Variants, and Expressions

## 1. Purpose

This document defines how the DreamChat Image Platform creates reusable asset packs for characters, places, and artifacts.

The goal is to avoid one-off images that are constantly regenerated.

Important characters and places should receive packs of reusable assets that can support many future scenes quickly and cheaply.

## 2. Core Principle

When a character or place becomes important, generate a small reusable pack.

Do not generate only one image and then regenerate from scratch every time the character or place appears.

For DreamChat, asset packs are a continuity tool, a cost-control tool, and a latency-control tool.

## 3. Asset Pack Types

The first version supports three pack categories:

- character packs
- place packs
- artifact assets

Artifact assets may later have packs, but the first version can treat them as simpler one-off or small-set assets.

## 4. Character Starter Pack

### 4.1 When to Generate

Generate a character starter pack when:

- a generated NPC becomes meaningful
- the user explicitly saves/keeps a character
- a character becomes recurring
- a character enters the visible scene participants area repeatedly
- the user or creator requests visual identity

Do not generate full packs for every disposable background character.

### 4.2 Minimum Pack

The first character pack should include:

1. `neutral_front_portrait`
2. `neutral_three_quarter_portrait`
3. `side_angle_portrait`
4. `warm_or_smiling_expression`
5. `serious_or_tense_expression`
6. `angry_or_defensive_expression`
7. `surprised_or_shocked_expression`

This creates enough coverage for early UI use.

### 4.3 Optional Later Pack Items

Later character packs may include:

- sad expression
- afraid expression
- injured state
- formal outfit
- travel outfit
- combat/work outfit
- intimate/casual outfit where appropriate
- full-body reference
- half-body reference
- profile icon crop
- transparent-background cutout

### 4.4 Character Asset Roles

Use explicit roles rather than vague filenames.

Recommended asset roles:

```text
neutral_front_portrait
neutral_three_quarter_portrait
side_angle_portrait
expression_warm
expression_serious
expression_angry
expression_surprised
expression_sad
expression_afraid
outfit_formal
outfit_travel
outfit_work
state_injured
profile_icon
full_body_reference
```

### 4.5 Character Pack Metadata

Each asset should include:

- character id
- visual identity id
- visual identity version
- asset role
- expression tag
- angle tag
- outfit/state tag
- style profile id
- model/backend
- prompt version
- seed/consistency key
- preview URL
- final URL
- generation job id

## 5. Place Starter Pack

### 5.1 When to Generate

Generate a place pack when:

- a location becomes recurring
- the current scene starts in a new important location
- the location becomes part of the world map/codex
- the user returns to the place after meaningful in-world time
- the place needs a persistent scene canvas visual

Do not generate full packs for throwaway locations.

### 5.2 Minimum Pack

The first place pack should include:

1. `establishing_wide_view`
2. `closer_atmospheric_view`
3. `day_view`
4. `night_view`
5. `calm_or_empty_view`
6. `busy_or_active_view`

### 5.3 Optional Later Pack Items

Later place packs may include:

- rainy/weather variant
- winter/seasonal variant
- damaged state
- rebuilt state
- faction-occupied state
- interior view
- exterior view
- map-like view
- point-of-interest detail

### 5.4 Place Asset Roles

Recommended asset roles:

```text
establishing_wide_view
closer_atmospheric_view
day_view
night_view
calm_empty_view
busy_active_view
interior_view
exterior_view
weather_rain
weather_snow
state_damaged
state_rebuilt
state_occupied
landmark_detail
```

### 5.5 Place Pack Metadata

Each asset should include:

- place id
- visual identity id
- visual identity version
- asset role
- time of day
- weather
- crowd level
- place state
- style profile id
- model/backend
- prompt version
- seed/consistency key
- preview URL
- final URL
- generation job id

## 6. Artifact Assets

### 6.1 When to Generate

Generate artifact/context visuals when:

- an object becomes narratively meaningful
- the object appears in the Aux Context Sidebar
- the user receives, reads, inspects, or studies the object
- the artifact is recurring or likely to matter later

Examples:

- letter
- map
- key
- weapon
- photo
- warrant
- relic
- public notice
- faction symbol
- medical file
- contract
- encrypted message

### 6.2 Initial Artifact Roles

Recommended roles:

```text
artifact_card
artifact_closeup
document_preview
map_preview
symbol_or_emblem
item_icon
photo_preview
notice_or_poster
```

### 6.3 Artifact Metadata

Each asset should include:

- artifact id
- asset role
- artifact version
- visible text handling mode
- style profile id
- model/backend
- preview URL
- final URL
- generation job id

## 7. Preview and Final for Packs

A pack generation job should be able to return partial results.

For example:

1. generate preview thumbnails for all requested pack items
2. return preview-ready status
3. generate high-res finals in the background
4. update each asset as final becomes ready

The web app should be able to use preview assets immediately.

## 8. Pack Templates

The API should support named pack templates.

### 8.1 Character Pack Templates

Recommended initial templates:

- `character_minimal_portrait_pack`
- `character_expression_pack`
- `character_full_reference_pack`

### 8.2 Place Pack Templates

Recommended initial templates:

- `place_minimal_scene_pack`
- `place_time_of_day_pack`
- `place_state_pack`

### 8.3 Artifact Templates

Recommended initial templates:

- `artifact_card_single`
- `artifact_document_preview`
- `artifact_icon_and_closeup`

## 9. Batch Generation Behavior

Pack generation should be treated as batch generation.

The service should support:

- batch planning
- partial completion
- batch-level metadata
- asset-level metadata
- retrying failed items only
- reusing existing assets within the pack

## 10. Pack Generation Triggering Rules

### 10.1 Character Trigger Rules

Generate a pack when character importance crosses a threshold.

Possible inputs:

- user explicitly saves character
- character appears in multiple scenes
- character is promoted from background to recurring
- relationship edge becomes meaningful
- user requests portrait
- creator marks character as important

### 10.2 Place Trigger Rules

Generate a pack when place importance crosses a threshold.

Possible inputs:

- scene starts in new important location
- user returns to location
- location becomes tied to thread/conflict
- location is added to world map/codex
- creator marks place as important

### 10.3 Artifact Trigger Rules

Generate visual when:

- artifact is currently inspected
- artifact becomes inventory/context card
- artifact is saved to known world/timeline
- artifact is recurring

## 11. UI Consumption

The web app should use packs as follows:

- Main Scene Canvas uses place assets
- Scene Participants uses character portraits/expression assets
- Aux Context Sidebar uses artifact/context assets
- World Workspace can show galleries/history/version comparisons later

The UI should not need to know generation details.

It should request the best asset for the current context.

## 12. Acceptance Criteria

This PRD is implemented when:

- character starter packs can be generated
- place starter packs can be generated
- artifact visuals can be generated
- pack jobs support preview and final outputs
- pack assets have explicit roles
- assets are linked to visual identities
- failed pack items can be retried without regenerating everything
- the web app can retrieve assets by role/expression/angle/state
- packs reduce repeated generation needs during play

---

## Confidence to Implement

**Score: 80/100 — High**

Pack templates and asset roles (`neutral_front_portrait`, `establishing_wide_view`, etc.) are enumerated explicitly, which makes batch generation a tractable orchestration problem: plan roles → reuse existing → enqueue missing → partial completion → retry only failures. The metadata fields per asset are clear. Two open questions: (1) the "trigger rules" for when to generate a pack ("character importance crosses a threshold") are policy-shaped and unspecified — fine to leave to the caller, but acceptance criteria don't say so explicitly; (2) "preview ready for all roles, then finals upgrade in background" requires careful job-state machine work to avoid race conditions between preview and final writes against the same asset_id. Both are implementable; neither is novel.

