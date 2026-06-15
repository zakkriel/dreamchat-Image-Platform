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

- `route_capability_mismatch`: the only route that matched the request claims a
  `required_capability` (e.g. `pack_capable`) that its provider cannot back —
  EITHER because the provider's adapter does not advertise it (config drift), OR
  because the provider is **synthetic** (mock) and synthetic identity is disabled
  in this environment (the default in live). The resolver dropped the route and
  failed closed.
- "no identity-capable provider configured": no **real** (non-synthetic) provider
  satisfies `identity_capable`. Today BFL `flux-pro-1.1` is `scene_capable` only
  (scenes/artifacts, not recurring characters), and mock is synthetic (dev/test
  only). With only BFL, nothing real can do identity/pack work.

**Synthetic providers and the default-safe policy.** Mock is a synthetic/test
provider. It advertises identity/pack so dev/CI can exercise routing, but it does
**not** participate in identity/pack routing unless `ALLOW_SYNTHETIC_PROVIDERS` is
on. That flag defaults **on in dev/test** and **off in live**, so a public/
production deployment (e.g. Railway with `ENVIRONMENT=live`) fails character/pack
requests closed instead of resolving mock and producing placeholder grids. Mock
still backs scene/artifact routes in any environment. The readiness warning alone
is not the safeguard — fail-closed routing excludes synthetic identity providers
by default.

Note: the current BFL `flux-pro-1.1` is **for scenes and artifacts**, not
recurring characters. Recurring character consistency requires a
reference/identity-capable provider. **Prompt-only retries do not solve recurring
identity** — re-rolling the same text prompt produces a different person, not the
same one.

## 3. Diagnose

Inspect the boot reconciliation logs (API and worker emit them). For each route
they log: `route_id`, `provider_id`, `model_id`, `required_capability`,
`provider_capabilities`, `decision`. Find the route with `decision=invalid` and
read its `reason`:

- `provider_capability_mismatch` — the provider's adapter genuinely cannot back
  the claimed capability (config drift). Fix the route or the config.
- `synthetic_identity_disabled` — the provider COULD back it, but it is synthetic
  and `ALLOW_SYNTHETIC_PROVIDERS` is off. Expected in live; configure a real
  identity provider (or enable the flag in dev/test only).
- `provider_not_registered` — the route points at a provider not wired in this
  process.

The readiness summary line also logs `synthetic_identity_allowed` so you can see
the active policy.

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
- **Dev/test only.** Set `ALLOW_SYNTHETIC_PROVIDERS=true` (the default in
  dev/test). Mock then satisfies identity/pack for local routing — but it will
  NOT make production readiness report a real identity-capable provider, and you
  must never set this in a public/production (`live`) environment, where it would
  re-enable synthetic placeholder grids for character packs.

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
