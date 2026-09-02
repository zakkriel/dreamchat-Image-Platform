# ADR-I005 — A second scene route, and refusals that must not be retried

## Status

Accepted, 2026-09-01. Extends ADR-007 (provider adapters), ADR-016 (capability
reconciliation) and ADR-017 (reference-conditioned provider). Written after an
outage, so every claim below is a measurement rather than a projection.

## Context

ADR-017 gave identity/pack work a real provider (fal, FLUX.1 Kontext). It left
`scene_capable` where ADR-016 found it: **one** real route, BFL `flux-pro-1.1`.
That was not noticed as a risk because it reads as a provider list of two.

It is not. Kontext is **reference-conditioned** and fails closed without a
reference image — deliberately, since that guard is what stops a recurring
character drifting. A location, an object or a world cover has nothing to
condition on. So scene work could never resolve to fal at all, and `mock` sits at
priority 100 but is filtered out in production by
`ALLOW_SYNTHETIC_PROVIDERS=false`. **BFL was a single point of failure for every
background in the product.**

On 2026-09-01 BFL's account ran out of credit and `submit` began answering `402`.
What followed was worse than "no new backgrounds", and the mechanism is the
reason this ADR exists:

1. The adapter mapped every non-2xx to one generic error, so an unpaid invoice
   was indistinguishable from a `429`.
2. The world backend's art reconciler re-commissioned the failed owners every two
   minutes — **875 failed jobs in 24 hours** — because its pending predicate
   treats `asset_id NULL AND job_id NULL` as "undrawn", which is exactly the state
   a failed slot is left in.
3. Those doomed submits consumed the token's entire **1000-requests/hour** budget.
4. The asset **read** path spends the same budget. `GET /v1/assets/{id}` started
   answering `429`, the consumer turned that into `404`, and its frontend drew
   placeholders.

**544 assets sat `ready` in storage throughout.** A billing failure on one
provider hid every picture the other, fully-paid provider had rendered. The
outage was caused by the retrying, not by the invoice.

## Decision

**1. Classify refusals that cannot change, and let consumers see the difference.**
`providers.ErrProviderUnpaid` + error code `provider_unpaid`, distinct from
`provider_content_rejected` because the remedies differ — one needs a payment, the
other different content. `docs/api/errors.md` now states which codes are terminal
and which to retry. Only `402` is classified; `429` and `5xx` stay retryable, and
both directions are pinned by tests, because sweeping transient failures into the
terminal bucket would strand recoverable renders forever.

**2. Terminal for the job, but still walkable.** `terminalGenerationError` includes
it, so no asynq attempt re-asks a settled question. It is deliberately **not** added
to the fallback-walk stop. Walking around a *moderation* decision would circumvent
the rejecting provider's policy; walking around an unpaid invoice simply uses a
provider that **is** paid, which is the entire point of having more than one. This
is the half a later "treat all terminal errors alike" tidy-up would break, so it
carries its own test.

**3. Give `scene_capable` a second real route.** `fal_t2i` — FLUX1.1 [pro],
prompt-only, on the same queue API this adapter already speaks. Its own provider
id, per ADR-016/017: capability reconciliation compares **adapter-reported**
capabilities against the seeded route and fails closed, and this adapter's set is
deliberately *narrower* than Kontext's — `scene_capable` only. Claiming identity or
pack here would let recurring-character work resolve to a prompt-only endpoint and
render a different face on every call. Migration `0021` seeds it at priority **150**,
ahead of bfl's 200; bfl stays enabled as the genuine fallback.

**4. Availability is derived from the registry, never restated.**
`bootstrap.AvailableProviders(cfg)` = `Registry(cfg).Available()`.
`config.AvailableProviders` is **deleted**.

Point 4 is the one that would not have been written without checking production.
The resolver drops any route whose provider is absent from its availability map
(`routing.go` stage 2). That map was a hand-written list of `mock|bfl|fal`, while
the boot reconciler judges routes from `CapabilityIndex`, which **is** the registry.
When the two disagree a route reconciles `decision="valid"` in the startup log and
is then silently unselectable — the worst available failure shape, because the log
says the route is fine. Measured: the new scene route took **zero** requests (66
attempts, all bfl), and `fal_dev`'s routes from migration 0020 **had never once been
selectable**.

**5. The permissiveness dial is sent on every provider that has one.**
`BFL_SAFETY_TOLERANCE`, default 6 (BFL's own default is 2 of 6, near strictest);
`enable_safety_checker` + `safety_tolerance` on `fal_t2i` (fal defaults the checker
**true**). `config.go` already named this trap for `FAL_SAFETY_CHECKER` — *"leaving
it unset would silently import the vendor's policy as the product boundary"* — and
the protection had been applied to fal and never to BFL, so every background this
product rendered was moderated at a vendor default while the world's own latitude
block said otherwise (20 slots terminal on `Content Moderated`). Asking is not
overriding: a rejection is still possible and still terminal.

## Consequences

- Scene work no longer depends on one account being in credit. A `402` walks to the
  paid route instead of blacking out reads.
- A billing or moderation refusal costs one attempt, not one attempt every two
  minutes forever.
- Adding an adapter makes it reachable with nothing else to remember. The class of
  bug where a route is valid-but-unselectable is gone, not documented.
- `fal_dev` became selectable for the first time as a side effect. Its identity/pack
  routes at priority 150 now genuinely take work, which is what migration 0020
  intended and never achieved.
- The cross-repo string coupling this created — the consumer matches a prefix of the
  error code — is gated both ways by `harness/check.sh terminal-codes` in the
  workspace, because neither repo can see it alone.

## Revisit when

- A provider exposes a permissiveness dial outside the documented range assumed here
  (BFL 1–6, fal 1–5 on v1.1). The values are pinned as numbers in tests precisely so
  a change must be argued rather than slipped in.
- The per-token rate ceiling is revisited. Reads are presigned-URL mints — no model
  call, no money — and cannot be cached, so a single gallery page legitimately mints
  dozens. The platform default of 1000/hour was sized for a write-shaped workload and
  the consumer's token has been raised to 20000/hour, 300/min. **That number is an
  assumption** from ~50 mints observed in one log window; it wants review under real
  traffic. Separate read and write tokens would isolate the two budgets properly and
  is the better answer if this recurs.
- `fal_t2i` earns a quality tier above `standard`. Like ADR-017, it claims no tier it
  has not benchmarked.
