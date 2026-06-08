# ADR-007 — Use provider adapters behind a common interface

## Status

Accepted for initial implementation.

## Context

The Image Platform must work with multiple image-generation providers (BFL, Replicate, Fal, Together, Stability, eventually self-hosted models). Each has different request shapes, polling models, error vocabularies, reference-image conventions, and capability sets. Provider choice changes faster than the API contract, and the router needs to pick a backend by capability tier (`identity_capable`, `pack_capable`, etc. — see PRD 03's provider capability floor).

The boundary between "what the platform's clients see" and "what each provider requires" must be deliberate.

## Decision

All providers implement a common Go interface (`Generate`, `Upscale`, `GetStatus`, `Capabilities`) defined in `internal/providers`. Provider-specific code (HTTP calls, payload shaping, error mapping, polling cadence) lives entirely inside the adapter package; nothing else in the codebase imports a provider SDK. A mock adapter is mandatory for local dev and CI. The provider router (also in `internal/providers`) selects an adapter per request based on quality tier, latency tier, asset type, style profile, capability tier, and circuit-breaker state.

## Alternatives considered

- **Hardcode one provider into handlers.** Fastest to ship. Blocks reuse, makes benchmarking impossible, kills the "swap providers" promise, and forces every cost/latency change into the handler layer.
- **Per-provider sibling endpoints** (`POST /v1/generate/bfl`, `POST /v1/generate/replicate`). Clients must know provider identity; provider churn becomes a client problem. Defeats the point of ADR-001.
- **Go plugins / .so files** for dynamic provider loading. Go plugins have known toolchain restrictions (no cross-compilation, version coupling). We don't need runtime swap; rebuild + redeploy is fine.
- **A scripting layer (Lua/Starlark) for provider-specific transforms.** Power and flexibility, but a much wider attack surface, harder to test, and a new language in the codebase.
- **Vendor's own SDK + thin wrapper per provider** without a unified interface. Common in early stage. Quickly turns into N parallel implementations of the same workflow with subtle differences.

## Tradeoffs

- **+** Provider churn does not reach the public API contract or the business logic.
- **+** Mock adapter enables CI without provider keys and gives deterministic test bytes.
- **+** Router can choose adapter by capability tier (PRD 03), cost class, and circuit-breaker state.
- **+** Benchmarking (PRD 06) becomes structurally possible — run the same job through N adapters.
- **−** Interface widens when a new capability is needed (reference-image conditioning, multi-reference, LoRA loading) — every adapter must opt in.
- **−** Per-provider quirks (seed semantics, async polling intervals, content-policy taxonomy) leak through `Capabilities()` or are normalized away — a real design call each time.
- **−** Mock and real diverge unless covered by contract tests; risk of mock-only correctness.

## Consequences

- `internal/providers/{mock,bfl,replicate,...}` each implement `ImageProvider`.
- Errors are normalized to a fixed vocabulary (`provider_timeout`, `provider_rate_limited`, `provider_content_rejected`, `provider_auth_failed`, `provider_capacity_error`, `provider_invalid_request`, `provider_unknown_error`) before they leave the adapter.
- Each adapter has its own test suite using `httptest` for happy path, error mapping, and timeout behavior (per `docs/guidelines/testing-strategy.md`).
- Circuit-breaker state is stored per (provider, model) and consulted by the router.

## Revisit when

- The interface has grown enough capability-shaped methods that it's hard to implement cleanly (split into role-specific interfaces — `TextToImage`, `ImageToImage`, `Upscaler`).
- Provider routing policy needs to become data-driven beyond the current rule set (consider a policy DSL).
- A second adapter category emerges (e.g. self-hosted inference) that doesn't fit the HTTP-call shape — may need a different transport assumption.

---

## Confidence to Implement

**Score: 85/100 — High**

The Go interface in `docs/architecture/provider-adapters.md` (`Generate`, `Upscale`, `GetStatus`, `Capabilities`) is small and reasonable. The mock adapter is trivial (deterministic placeholder bytes). Risk shows up only when adding *real* adapters: each provider has its own quirks for image-to-image references, seeds, async polling cadence, and content-policy errors — the interface may need to widen. The router decision logic ("character portrait + standard + fast → provider A") is policy-shaped and not pinned down here.
