# ADR-017 — First real reference-conditioned provider for recurring characters (fal.ai FLUX.1 Kontext)

## Status

Accepted. Implements the recurring-character path PRD 03 §8 demands and ADR-016
("Revisit when: a real identity-capable provider is added") anticipated. Extends
ADR-007 (provider adapters) and ADR-016 (capability reconciliation). Continues
the work started in PR #28.

## Context

After ADR-016, identity/pack routes fail closed unless backed by a **real**
identity-capable provider. The only real provider, BFL `flux-pro-1.1`, is
`scene_capable` only (prompt-only text-to-image). The mock provider is synthetic
and blocked from identity/pack by default (`ALLOW_SYNTHETIC_PROVIDERS=false`
everywhere). So character packs correctly fail closed — there is no provider that
can hold a recurring character.

The fix is not a prompt-only "consistency" trick (explicitly rejected by the
floor) and not LoRA training (out of scope). It is a provider that conditions on
**reference images** of the character. `ProviderGenerateRequest.ReferenceURLs`
already exists on the interface but was never populated or consumed.

## Decision

Add one real reference-conditioned provider as the smallest production-useful
vertical slice, wired through the **pack** generation path (the platform's
recurring-character path).

**Provider / model: fal.ai running FLUX.1 Kontext [pro], multi-reference**
(`fal-ai/flux-pro/kontext/multi`; adapter `provider_id = "fal"`, `model_name =
"flux-pro-kontext-multi"`).

Why this one (official docs only):

- **Reference-conditioned, not prompt-only.** FLUX.1 Kontext takes a text prompt
  plus one or more reference images and renders the *same subject* in the
  prompted variation — the documented use case is character consistency and
  identity-preserving edits. (https://fal.ai/models/fal-ai/flux-pro/kontext)
- **Takes reference image URLs directly** via the `image_urls` array — so the
  platform passes presigned URLs of the identity's anchor assets without
  base64/multipart upload. Reference-conditioning, not mandatory image-to-image
  semantics from the caller's view.
  (https://fal.ai/models/fal-ai/flux-pro/kontext/max/multi/api)
- **Lowest integration risk.** fal's queue API (submit → poll status → fetch
  result → download) mirrors the existing BFL submit/poll/download adapter almost
  exactly, so the new adapter reuses the same shape, the same injectable HTTP
  `Doer`, and the same bounded-timeout/poll design.
  (https://docs.fal.ai/model-endpoints/queue/)
- **Deterministic-ish.** Accepts an optional integer `seed`.
- **~1024 output** via `aspect_ratio` (1:1 etc.).
- **Per-image pricing** ($0.04/image for [pro]; [max] is $0.08), representable by
  the existing `provider_model_prices` per-image schema — no cost-model redesign.

### Capability honesty (PRD 03 §8)

The fal adapter advertises `{scene_capable, identity_capable, pack_capable}`,
`Synthetic = false`, and a new `RequiresReferenceImage = true`. It is
deliberately **NOT** `production_capable`: that tier is claimed only after an
acceptance/quality benchmark demonstrates recurring-character consistency. Only
the `pack_capable` route is seeded (migration `0011`), because pack generation is
the path wired end to end with references in this slice. No `scene_capable` fal
route is seeded — fal Kontext requires references and single-image scene/artifact
requests carry none, so BFL keeps serving all scene/artifact work unchanged.

### Reference wiring + fail-closed

- A provider declares it needs references via
  `ProviderCapabilities.RequiresReferenceImage`. The pack worker, when the
  resolved provider sets it, gathers the visual identity's `anchor_asset_ids`,
  mints a presigned high-res URL per anchor (`Storage.Presign`), and threads them
  into `ProviderGenerateRequest.ReferenceURLs` for every pack item (all roles
  condition on the same anchors).
- If the identity has **no** anchor assets, the pack fails terminally with
  `missing_reference_assets` — no provider call, no different character generated.
  The adapter independently fails closed with `ErrReferenceRequired` if ever
  handed an empty reference set (defense in depth).
- Prompt-only providers (mock, BFL) leave `RequiresReferenceImage = false`, so the
  reference path is a no-op and their flows are byte-for-byte unchanged.

## Alternatives considered

- **Add FLUX.1 Kontext under the existing `bfl` provider id.** BFL hosts Kontext
  too, but capability is per-provider-adapter in the registry; overloading `bfl`
  would force the scene-only provider to claim identity, muddying readiness and
  the §8 floor. A distinct `fal` adapter keeps capabilities honest. (BFL Kontext
  also passes the reference as base64 `input_image`, not a URL array.)
- **OpenAI gpt-image-1 edits / Google Gemini image.** Capable, but the edit
  endpoints take multipart file uploads rather than reference URLs, adding an
  upload round-trip; fal's `image_urls` is a cleaner fit for presigned anchors.
- **Replicate predictions API.** Also a clean submit/poll fit, but model choice is
  sprawling and FLUX.1 Kontext on fal is the documented character-consistency
  model with simple per-image pricing.
- **Resolve reference URLs at the API handler (request time).** Nicer 422 UX, but
  presigned URLs would have to survive the queue delay; minting them in the worker
  at generation time keeps the TTL window tiny and matches the acceptance test
  ("the worker builds a provider request containing ReferenceURLs").
- **Mark fal `production_capable` now.** Rejected — no benchmark yet; that would
  re-introduce the exact "claim a capability you can't prove" failure §8 prevents.

## Tradeoffs

- **+** First real recurring-character path: character packs resolve and generate
  end to end in production when `FAL_KEY` is set.
- **+** BFL scene/artifact generation is untouched; mock stays synthetic/blocked.
- **+** No cost-model redesign, no schema migration (seed-only DML), no LoRA, no
  UI.
- **+** Fails closed (clear `missing_reference_assets`) rather than generating a
  different character.
- **−** Reference URLs are presigned high-res objects keyed by the deterministic
  asset-key scheme; an externally-ingested anchor stored under a different scheme
  would not resolve (acceptable for the slice — platform-generated anchors follow
  the scheme).
- **−** Presigned reference URLs expire (`S3_PRESIGN_TTL`, default 15m). They are
  minted at generation time and consumed within the provider's submit/poll window,
  but a very long provider backlog inside a single worker pass could outlast the
  TTL. Documented limitation.
- **−** Quality is unproven: not `production_capable` until benchmarked.

## Consequences

- New adapter `internal/providers/fal`; registered by
  `internal/providers/bootstrap` only when `FAL_KEY` is set (mirrors BFL gating).
- `config`: `FAL_KEY` env var, `ProviderFal`, `AvailableProviders()` includes
  `fal` when keyed, `IMAGE_PROVIDER=fal` validated.
- `providers.ProviderCapabilities` gains `RequiresReferenceImage`;
  `providers.ErrReferenceRequired` is the adapter fail-closed sentinel.
- `jobs.Worker` gains an `Identities` reader + `RefPresignTTL`; the pack worker
  gathers references and fails closed via `missing_reference_assets`.
- Migration `0011_fal_provider_seed.up.sql` seeds the fal model, a `pack_capable`
  text_to_image route (priority 200), and a $0.04/image active price.

### Operational hardening (post-review, PR #29)

- **Attach anchors over the API.** `POST /v1/characters/{character_id}/visual-
  identity/anchors` AND `POST /v1/places/{place_id}/visual-identity/anchors`
  (`AttachAnchorAssetsRequest`) set an identity's `anchor_asset_ids`. Both share
  one validated handler: each asset must be tenant-owned, status `ready`, carry a
  high-res object, and be bound to this identity or unassigned — otherwise
  `422 invalid_anchor_asset`. Character and place are symmetric because BOTH pack
  kinds request `pack_capable` and may resolve the reference-conditioned fal
  route; a character-only anchor flow would let fal break place packs. So a client
  can create an identity, attach anchors, and run a pack with **no manual SQL**.
  Backed by `identities.Repository.SetAnchorAssets` (tenant-scoped, atomic; does
  not bump identity version — anchors are reference provenance).
- **Hardened reference resolution.** `referenceURLsForIdentity` no longer presigns
  a guessed `assets/<id>/high.png`. It LOADS each anchor through the assets
  repository, validates tenant/status/high-res, presigns the asset's ACTUAL
  stored high-res key (`storage.KeyFromCanonicalURL`), and fails the pack closed
  with `invalid_reference_asset` (a bad attached anchor) vs `missing_reference_
  assets` (no anchors at all).
- **fal timeout/cancellation.** The adapter captures `cancel_url` from submit and,
  on a local timeout/context-cancellation after submit, best-effort `PUT`s the
  cancel URL (fresh bounded context) and logs `request_id` + cancel status, so a
  worker timeout does not leave an orphaned, still-billing fal request.

## Revisit when

- An acceptance/quality benchmark validates recurring-character consistency →
  promote fal to `production_capable` and seed higher tiers / [max].
- A dedicated single-character (non-pack) identity generation endpoint is added →
  seed an `identity_capable` fal route and wire references on that path too.
- Reference TTL proves too short under backlog → mint per-item or raise the
  reference-specific TTL.

---

## Confidence to Implement

**Score: 86/100 — High**

The adapter reuses the proven BFL submit/poll/download shape; the capability and
fail-closed plumbing reuse the ADR-016 machinery. Risk is provider-contract
fidelity (the fal queue/result JSON shape is modelled from official docs and
pinned by stub-based unit tests with no real network) and reference-URL
fetchability in production (presigned high-res keys), which the runbook calls out
for the smoke test.
