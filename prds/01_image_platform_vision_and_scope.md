# PRD 01 — DreamChat Image Platform Vision and Scope

## 1. Purpose

This document defines the product vision and scope for the DreamChat Image Platform.

The Image Platform is a standalone service that generates, stores, versions, retrieves, and serves persistent visual assets for DreamChat worlds.

It exists to make DreamChat feel more readable, immersive, and continuous without turning the product into a full visual game engine or media-generation app.

DreamChat remains a persistent AI RPG world product first.

The Image Platform supports that product promise by helping the user quickly understand:

- where the current scene is happening
- who is present
- what a recurring character looks like
- what a recurring place feels like
- what artifact, document, object, or context card matters now
- what changed visually when a character, place, or object state changed

## 2. Product Position

The Image Platform is not the core DreamChat engine.

The core DreamChat engine is responsible for:

- persistent world state
- dynamic NPC/entity behavior
- memory
- relationships
- in-world time progression
- backstage updates
- narration
- knowledge boundaries
- canon correction

The Image Platform is the visual continuity layer around that core.

The product rule is:

> DreamChat is world-first and text-first, but not text-only.

Images should help the user enter the world faster, recognize recurring people and places, and reduce cognitive load. Images should not replace narration, memory, or world-state logic.

## 3. Why This Should Be a Standalone Service

The Image Platform should be built as an API/service independent from the DreamChat web app.

The web app should consume the service as one client.

This separation matters because:

- image generation will evolve faster than the web app
- model/provider choices will change over time
- costs and latency need separate measurement
- visual consistency requires its own data model
- caching/retrieval should be testable outside the world engine
- batch generation should not block web app development
- future creator tools or external clients may reuse the same service

The service should be testable with a small playground or API client before it is deeply integrated into the DreamChat UI.

## 4. Core Product Goals

The Image Platform must support the following goals.

### 4.1 Character Consistency

Once a character is visually created, future generated images of that character should remain identifiable.

The system should not rely only on repeating the same prompt.

It must persist a character visual identity record with:

- stable physical traits
- visual anchors
- reference images
- seeds or consistency keys
- style profile
- model/backend metadata
- known visual variants
- version history

### 4.2 Place Consistency

Once a place is visually created, future generated images of that place should remain identifiable.

A location should have its own visual identity record with:

- location type
- layout/architecture cues
- recurring landmarks
- mood/palette/lighting
- time-of-day variants
- weather/state variants where relevant
- version history

A place should be allowed to change, but only through explicit state/version changes.

### 4.3 Fast Preview + Better Final

The product should not block play while waiting for a perfect image.

The service should support two delivery levels:

1. **Preview asset**
   - lower resolution
   - fast enough for interactive web use
   - good enough to show immediately

2. **Final asset**
   - higher resolution
   - better detail
   - replaces or upgrades the preview once ready

The default user experience should feel responsive even if the high-res version finishes later.

### 4.4 Asset Packs at Creation Time

When an important character or place is created, the service should generate a reusable asset pack instead of only one image.

For characters, this may include:

- neutral portrait
- angle variants
- expression variants
- outfit/state variants later

For places, this may include:

- establishing view
- close atmospheric view
- day/night variants
- calm/busy variants
- changed/damaged variants later

This reduces future latency and cost because the web app can reuse assets instead of regenerating everything during play.

### 4.5 Storage and Retrieval Before Regeneration

The service must store generated images and retrieve them when needed.

The system should always attempt:

1. exact retrieval
2. reusable variant retrieval
3. preview retrieval
4. generation only if needed

Regeneration should be deliberate, not the default.

### 4.6 Style Flexibility

DreamChat should not force one universal visual style.

The Image Platform should support:

- open user/creator style prompts
- curated preset style packs
- later: LoRA/adapters/style packs
- later: creator or world-specific style packs

The interaction model should remain genre-agnostic while visuals adapt to the user’s world.

### 4.7 Provider and Model Abstraction

The web app should not know or care which model generated the image.

The Image Platform should hide provider details behind a stable internal contract.

Backends may include:

- external image-generation APIs
- self-hosted models
- fast preview models
- higher-quality final models
- future multimodal/image-editing models

## 5. Initial Supported Asset Types

The first version supports only three asset categories.

### 5.1 Character Assets

Visuals for entities capable of presence, speech, reaction, or agency.

Examples:

- NPC portraits
- user-controlled character portrait, if enabled
- off-screen speaker portrait, if communication is visualized
- narrator/facilitator avatar, if represented as part of the UX

### 5.2 Place / Scene Assets

Visuals for where the current scene happens.

Examples:

- room
- street
- harbor office
- temple
- command center
- apartment
- forest path
- futuristic station
- realistic neighborhood

The main scene visual should usually answer:

> Where are we?

### 5.3 Artifact / Context Assets

Visuals for meaningful non-participant objects or context cards.

Examples:

- letter
- warrant
- photo
- map
- key
- weapon
- relic
- public notice
- relationship card illustration
- faction symbol
- rumor card illustration

Artifacts belong in contextual UI surfaces, not in the participant/avatar area.

## 6. Out of Scope for Initial Version

The first version should not include:

- video generation
- animation
- full character sprite rigs
- full scene composition engine
- every-message image generation
- marketplace
- public creator economy
- end-user model training UX
- large custom LoRA training flow
- mobile image editor polish
- advanced moderation console
- public external developer API
- full media generation layer

These can be added later after the core standalone image service is stable.

## 7. Creative Policy Position

The platform should support mature fictional worlds and should not be sanitized into a generic toy.

The product direction is:

> self-governed creative policy, not vendor-controlled creative refusal as the product boundary.

This means:

- prefer open or self-hostable backends long-term
- avoid over-coupling to provider censorship behavior
- preserve user/world style flexibility
- support mature fictional content where allowed
- still implement legal, age, and platform safety boundaries
- keep policy enforcement as a product/system layer, not as hidden provider behavior only

## 8. Non-Goals

The goal is not to create a general-purpose image-generation website.

The goal is not to compete directly with image-generation platforms.

The goal is not to generate unlimited images on every message.

The goal is not to make DreamChat visually deterministic like a traditional game.

The goal is to create persistent, reusable, recognizable visual assets that support a persistent world experience.

## 9. Success Criteria

The Image Platform succeeds if:

- recurring characters remain visually recognizable
- recurring places remain visually recognizable
- images are retrieved and reused instead of constantly regenerated
- previews are fast enough to support play-first UX
- high-res images improve quality without blocking interaction
- asset packs reduce repeated generation needs
- style can vary per world/user without breaking the product structure
- the web app can consume images through a stable API
- provider/model changes do not require web app redesign
- costs can be measured and controlled per world/session/user

## 10. Failure Criteria

The Image Platform fails if:

- characters drift visually between generations
- places drift visually without intentional world-state change
- the web app blocks waiting for images
- every scene/message causes unnecessary generation
- images reveal hidden/omniscient world state in normal play mode
- style is hardcoded into one DreamChat visual identity
- provider changes break product behavior
- generated assets are not stored or reusable
- cost cannot be tracked
- the image service becomes tangled with the core world engine

## 11. MVP Boundary

The MVP boundary is:

- standalone API service
- async jobs
- image storage
- character visual identity
- place visual identity
- artifact generation
- preview + final delivery
- basic asset packs
- retrieval-before-generation
- style profile support
- metadata and cost/latency logging

Anything beyond that should be parked until the service proves value.

---

## Confidence to Implement

**Score: 90/100 — Very High**

This is a vision/scope PRD, not a build doc — "implementing" it means keeping later design decisions inside its boundaries. The boundaries are clear (what is in vs. out, who owns world state vs. visual state, what counts as success vs. failure). I'm subtracting a few points because two product-level points have implementation-shaped risk: (a) "self-governed creative policy" implies provider choice constraints that may conflict with the easiest production providers; (b) the success criterion "recurring characters remain visually recognizable" is partly a model-quality outcome (see PRD 03 score), not something the platform alone can guarantee.

