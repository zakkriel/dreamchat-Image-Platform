# ADR-008 — Use asset-state-first persistence (not prompt-first)

## Status

Accepted for initial implementation.

## Context

The most natural shape for an image platform is "prompt in → image out." Cheap, simple, easy to demo. It also makes everything that DreamChat actually cares about impossible: recurring character recognizability, place continuity across visits, retrieval before regeneration, versioning when a character's appearance changes, and a debuggable trail when visuals drift.

The persistence model is the foundation that ADR-009 (retrieval), PRD 03 (consistency), PRD 04 (packs), and PRD 05 (cache/versioning) all build on. Picking the wrong shape here means rewriting everything above it.

## Decision

The unit of persistence is the **visual asset**, not the prompt. Assets are generated from a chain: visual identity → generation intent → prompt package → job → asset → reusable variant. Each generated asset records its visual_identity_id, version, variant_key, style_profile_id, prompt_hash, seed, provider, and model. The visual identity itself carries canonical traits, anchor assets, consistency keys, and forbidden-drift constraints.

## Alternatives considered

- **Prompt-first.** Store nothing except generated images, regenerate each call. Killed by PRD 03's consistency requirement (the same prompt produces different characters across calls), PRD 05's retrieval-before-generation rule (impossible without a stable identity to query against), and audit (no trail when a character "drifts").
- **Session-based identity.** Each conversation/session re-derives a per-character look from the chat history. Works inside one session, breaks the moment a character recurs in another session, on another device, or shared with another player.
- **Embedding-based identity.** Generate, embed the result, find the "nearest" previous asset on next request. Clever, sometimes good enough. Adds a vector DB and a quality bet (the embedding has to capture identity, not pose/lighting), gives non-deterministic reuse, and doesn't surface "this is version 3" semantics.
- **External CMS** holding portraits with hand-curated images. Solves consistency for hand-built worlds, fails for generated NPCs at runtime.

## Tradeoffs

- **+** Enables retrieval-before-generation (ADR-009), versioning, drift detection (PRD 03 §8), and audit.
- **+** Variant queries are well-typed: "give me the angry expression of char_789 at v2 in style_001" is a SQL query, not a similarity search.
- **+** Provider-agnostic: changing models doesn't invalidate identity.
- **+** Foundation for "asset packs" (PRD 04) — packs are sets of variants attached to one identity+version.
- **−** Requires the identity service and DB schema before any generation works (longer ramp than prompt-first).
- **−** Modeling burden: who decides when an identity-version-bump is warranted (canonical scar vs. temporary makeup)? Product call, not technical.
- **−** Storage cost scales with assets retained per identity; retention policy needed.

## Consequences

- `visual_identities`, `visual_assets`, `visual_identity_versions` (per data model) form the spine of the schema.
- Generation handlers do not accept raw prompts as input for recurring characters/places; they accept identity refs, variant intent, and style refs (per PRD 03 §11).
- The prompt compiler is a separate service that turns (identity + variant + style) into a deterministic prompt package and `prompt_hash`.

## Revisit when

- A class of assets emerges where identity doesn't apply (e.g. one-shot mood-board images) — those may need a parallel "ephemeral asset" path.
- Identity modeling overhead is slowing product iteration on disposable visuals — consider an explicit "draft mode" that skips identity binding.
- Embedding-based retrieval becomes good enough to replace SQL-keyed retrieval for some queries — keep both, route by query type.

---

## Confidence to Implement

**Score: 85/100 — High**

The shift from "prompt → image" to "visual identity → generation intent → prompt package → job → asset → variant" is well captured. It makes downstream choices (caching, reuse, versioning, drift) much cleaner. The implementable parts (identity records, version transitions, variant tags, anchor refs) are all defined in the data model. The non-implementable part — same as PRD 03 — is whether the underlying model actually *honors* identity inputs across calls; that's a provider-quality issue, not an architecture decision.
