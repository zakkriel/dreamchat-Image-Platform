# Runbook — Provider Capability Misconfiguration / No Identity-Capable Provider

> Implements PRD 03 §8 (Provider Capability Floor). See
> `docs/adr/016-provider-capability-reconciliation.md`.

This runbook covers two related failures:

1. A character/pack request fails with `route_capability_mismatch` (HTTP 422).
2. Character/pack generation is unavailable because **no real identity-capable
   provider is configured**.

Both are honest, fail-closed outcomes: the platform refuses to route
consistency-critical work to a provider that cannot hold identity, rather than
silently producing drifted recurring characters.

## 1. Symptoms

- Character or pack create returns `422 route_capability_mismatch`.
- Boot logs contain a WARN line:
  `provider route reconciliation: route disabled (fail-closed)` with
  `reason=provider_capability_mismatch`.
- Boot logs contain:
  `provider capability readiness: no identity-capable provider configured`.
- Scene/place/artifact generation still works (those need only `scene_capable`).

## 2. What it means

- `route_capability_mismatch`: a `provider_routes` row claims a
  `required_capability` (e.g. `pack_capable`) that the provider's adapter does
  not actually advertise. The resolver dropped the route and failed closed.
- "no identity-capable provider configured": no **real** (non-synthetic) provider
  satisfies `identity_capable`. Today BFL `flux-pro-1.1` is `scene_capable` only
  (scenes/artifacts, not recurring characters), and mock is synthetic (dev/test
  only). With mock disabled, nothing real can do identity/pack work.

Note: the current BFL `flux-pro-1.1` is **for scenes and artifacts**, not
recurring characters. Recurring character consistency requires a
reference/identity-capable provider. **Prompt-only retries do not solve recurring
identity** — re-rolling the same text prompt produces a different person, not the
same one.

## 3. Diagnose

Inspect the boot reconciliation logs (API and worker emit them). For each route
they log: `route_id`, `provider_id`, `model_id`, `required_capability`,
`provider_capabilities`, `decision`. Find the route with `decision=invalid`.

Cross-check the claimed capability against the adapter's `Capabilities()`:

- `mock` (synthetic): `{draft_only, scene_capable, identity_capable, pack_capable,
  production_capable}`.
- `bfl` `flux-pro-1.1`: `{draft_only, scene_capable}`.

The §8.3 hierarchy applies to provider-satisfies-route only:
`production_capable` ⊇ `pack_capable` ⊇ `identity_capable`; `scene_capable` and
`draft_only` are parallel and never satisfy identity/pack.

## 4. Resolve

Choose based on intent:

- **The route was misconfigured.** Correct the route's `required_capability` to
  one the provider actually backs (e.g. `scene_capable` for BFL), or remove the
  route. Do NOT raise the provider model's advertised capabilities to match the
  route — capabilities reflect what the adapter can really do and are gated by the
  PRD 03 §8.5 acceptance tests.
- **You genuinely need identity/pack generation.** Configure a real
  reference/identity-capable provider and add a route whose `required_capability`
  it actually satisfies. Promote it only after it passes the §8.5 character/place
  consistency acceptance tests. Until then, character/pack jobs fail closed by
  design.
- **Dev/test only.** Enable the mock provider (synthetic). Mock satisfies
  identity/pack for local routing, but it will NOT make production readiness
  report a real identity-capable provider.

## 5. Verify

- Re-run boot; confirm the route logs `decision=valid` and the readiness line is
  `provider capability readiness` (INFO) with
  `real_identity_capable_provider=true` when a real provider was added.
- Re-issue the character/pack create; confirm `202 Accepted` (or the appropriate
  cost/idempotency outcome) instead of `422 route_capability_mismatch`.
- Confirm scene/place/artifact flows still succeed.

## 6. Do not

- Do not bump `provider_models.capabilities` to silence the error without the
  §8.5 acceptance evidence — that re-introduces the exact silent-drift risk this
  control prevents.
- Do not expect prompt tuning or retries to deliver recurring identity from a
  scene-only provider.
