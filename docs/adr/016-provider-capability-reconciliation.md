# ADR-016 — Reconcile provider capabilities and fail closed on misconfigured routes

## Status

Accepted. Implements PRD 03 §8 (Provider Capability Floor). Extends ADR-007
(provider adapters) and the Phase 7A route resolver.

## Context

PRD 03 §8 defines a capability floor for recurring character/place consistency:
a route used for identity/pack work MUST be backed by a provider that can
actually condition on identity (references, seeds + identity prompts, multi-
reference, LoRA, or a vendor identity feature). The capability levels form a
hierarchy — `production_capable` ⊇ `pack_capable` ⊇ `identity_capable`, with
`scene_capable` on a parallel axis.

The route table (`provider_routes`) is mutable config. A route carries a
`required_capability` the resolver trusted verbatim: request-to-route matching
compared the request's requested capability to `route.required_capability`
exactly, and provider *availability* was checked, but nothing verified that the
provider adapter behind a route could actually back the capability the route
claimed. So a DB route could claim `pack_capable` while wired to a provider whose
adapter only advertises `scene_capable`/`draft_only`. Nothing caught the lie, and
consistency-critical work would be routed to a provider that cannot hold
identity — silently producing drifted recurring characters.

Concrete current state: the only real provider, BFL `flux-pro-1.1`, is a pure
text-to-image model classified `{draft_only, scene_capable}` — suitable for
scenes/artifacts, not recurring characters. The mock provider is the only
provider claiming identity/pack, and it is synthetic (test/dev only). With mock
disabled, there is **no real identity-capable provider configured** — a fact that
was invisible.

## Decision

Reconcile configured routes against the capabilities the registered provider
adapters actually advertise, and fail closed:

1. A single capability-satisfaction helper (`providers.CapabilitySatisfies` /
   `CapabilitiesSatisfy`) encodes the §8.3 hierarchy. It is used ONLY for
   provider-satisfies-route validation. Request-to-route matching stays exact on
   `route.required_capability`, so cheap `scene_capable` work is never routed to
   an expensive identity/pack route.
2. At boot, `routing.Reconcile` checks every route against the provider
   capability index and logs each decision (route id, provider id, model id,
   required capability, provider capabilities, decision) plus an identity-
   readiness summary. Invalid routes are disabled by exclusion + loud WARN logs;
   startup is not aborted — this matches the repo's existing fail-at-resolution
   pattern (an unconfigured provider is simply not registered, and the request
   fails clearly).
3. At route resolution, the resolver enforces the same provider-satisfies-route
   check as defense-in-depth and returns a distinct
   `route_capability_mismatch` (HTTP 422) when the only matching route's provider
   cannot back its claimed capability. **The check runs LAST**, only on routes
   that already survived every request-scoped filter (operation, availability,
   quality, exact `required_capability`, preview). An unrelated invalid route can
   therefore never change the error a request sees for routes it would not have
   served (e.g. an overstated pack route does not make a scene request return
   `route_capability_mismatch`).
4. Readiness distinguishes real providers from synthetic/test-only providers (a
   `Synthetic` marker on `ProviderCapabilities`; mock sets it). **Synthetic
   providers do not participate in identity/pack routing by default.** A synthetic
   provider satisfies identity-axis routes (identity/pack/production) only when
   `ALLOW_SYNTHETIC_PROVIDERS` is on — defaulting on in dev/test and **off in
   live** (mirrors the `OPENAPI_DOCS_ENABLED` env-default precedent). So a
   public/production deployment with only a scene-capable real provider fails
   character/pack requests closed instead of resolving synthetic placeholder
   grids. Synthetic providers still back scene/draft routes in any environment.
   The readiness warning alone is **not** the control — fail-closed routing
   excludes synthetic identity providers by default; the warning is observability.

Route resolution already runs before cost reservation in the handler, so a
fail-closed rejection happens before any budget hold is taken — no dangling
reservation.

## Alternatives considered

- **Trust the DB route's `required_capability` (status quo).** Simplest, but the
  whole point of §8 is that config can drift; trusting it defeats the floor.
- **Fail startup on any invalid route.** Louder, but brittle: one stale row would
  take the whole API down, and the resolver already fails the affected request
  closed. Rejected in favor of disable-by-exclusion + loud logs.
- **A DB column marking a provider "real" vs synthetic.** More machinery and a
  migration; the adapter already knows what it is, so a code-level `Synthetic`
  marker is the smaller, truthful source.
- **Apply the hierarchy to request-to-route matching too.** Would let a
  `scene_capable` request collapse onto a `pack_capable` route, routing cheap
  work to expensive identity providers. Explicitly rejected — matching stays
  exact; the hierarchy is provider-satisfies-route only.
- **Let mock keep satisfying identity/pack everywhere (readiness warning only).**
  Rejected: a Railway/public deployment would still resolve the seeded mock pack
  route and emit placeholder grids for character packs — the exact silent failure
  §8 exists to prevent. The synthetic policy makes the warning enforceable.
- **Run the provider-satisfies-route filter before the request filters.** Simpler
  to place next to availability, but an unrelated invalid route then changes the
  error for requests it would never serve. Rejected in favor of running it last,
  scoped to actual candidate routes.

## Tradeoffs

- **+** Config can no longer overstate a provider's capability; the floor is
  enforced by code, not docs.
- **+** Misconfigured identity/pack routes fail closed with a specific error.
- **+** Missing real identity provider is visible at boot in structured logs.
- **+** No provider integration, schema migration, or cost-model change required.
- **−** The resolver now depends on a provider capability index being wired
  (production wires it; tests that don't are unaffected — the check is skipped
  when the index is empty).
- **−** Two capability concepts now coexist (exact match for requests; hierarchy
  for provider validation); the distinction must stay documented to avoid
  confusion.

## Consequences

- `providers.CapabilitySatisfies` / `CapabilitiesSatisfy` /
  `ProviderSatisfiesRoute` / `AssessIdentityReadiness` are the single source of
  capability semantics. `ProviderSatisfiesRoute` applies both the hierarchy and
  the synthetic policy and is shared by the resolver and the reconciler so boot
  logs and resolution decisions never diverge.
- `ProviderCapabilities.Synthetic` marks mock/fixture providers; the
  `ALLOW_SYNTHETIC_PROVIDERS` env var (default dev/test on, live off) gates
  whether they back identity/pack routes, wired via
  `Resolver.WithSyntheticIdentityAllowed`.
- `routing.Reconcile` / `GatherRoutes` / `LogReconciliation` run at API and
  worker boot; the resolver gains `WithProviderCapabilities` and a
  `route_capability_mismatch` failure.
- `internal/providers/bootstrap` is the shared provider-registration seam so API
  and worker agree on which providers exist and what they can do.
- Recurring character consistency requires adding a reference/identity-capable
  provider; prompt-only retries do not solve recurring identity. Until such a
  provider is configured, character/pack jobs fail closed instead of producing
  placeholders.

## Revisit when

- A real identity-capable provider is added (the readiness signal flips, and the
  acceptance tests in PRD 03 §8.5 gate its promotion).
- Capability levels gain a new axis or value (extend the implication map; it
  fails closed for unknown values until classified).
- Route validity needs to be persisted/queryable beyond boot logs (consider a
  reconciliation status surfaced via the admin API).

---

## Confidence to Implement

**Score: 90/100 — Very High**

The change is contained: a pure capability helper, a boot-time reconciler, one
new resolver filter stage, and a synthetic marker. It adds no provider
integration, no migration, and no cost-model change. Risk is limited to keeping
the two capability concepts (exact request matching vs hierarchical provider
validation) distinct, which the tests pin explicitly.
