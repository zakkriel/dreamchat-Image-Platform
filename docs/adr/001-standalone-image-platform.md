# ADR-001 — Image Platform is a standalone service

## Status

Accepted for initial implementation.

## Context

DreamChat is a persistent AI RPG world product. Image generation, visual identity, asset storage, and provider routing are meaningful but bounded concerns inside that product. The web app already has its own scale, complexity, and release cadence, dominated by chat UI and world-state work. Image generation needs different tooling (workers, queues, S3, provider adapters) and changes at a different rate — provider churn is faster than UI churn.

If image generation lives inside the web app, every provider change, cost-control feature, or worker tuning forces a web-app release and competes with UI work for review attention. It also makes the platform impossible to reuse from admin tools, batch jobs, or future creator/partner clients.

## Decision

Build the DreamChat Image Platform as a standalone HTTP service with its own deployment, Postgres database, Redis queue, and S3 bucket. The web app is one client; admin tooling, benchmark runners, and future creator tools are additional clients of the same API.

## Alternatives considered

- **Embed image logic in the web app.** Fastest path to a single client. Couples provider churn to UI releases, blocks reuse from non-UI clients, and tangles cost/latency telemetry with web-app metrics. Rejected because PRD 01 explicitly wants the platform reusable.
- **Microservices from day 1** (split jobs, storage, identity, provider routing into separate services). Cleanest long-term boundary, but premature: the platform isn't large enough to justify N services worth of ops. A modular monolith (`cmd/api` + `cmd/worker` from the same codebase) gives us the same internal seams without the deploy overhead, and can be split later without changing the public contract.
- **Worker-only service with no API** (web app talks to S3 + DB directly). Avoids one network hop, but moves identity/versioning/retrieval logic into every client and breaks ADR-008's asset-state-first stance.

## Tradeoffs

- **+** Clean public contract; web app, admin tools, batch runners, and future clients share one API surface.
- **+** Image-platform incidents do not block web-app deploys and vice versa.
- **+** Provider experimentation can happen behind the contract without UI involvement.
- **−** One more service to operate (deploys, monitoring, secrets, on-call rotation).
- **−** Inter-service auth and a network hop add latency and a new failure mode.
- **−** Boundary must be enforced in code review; reaching into the platform's DB from the web app must be refused even when convenient.

## Consequences

- The web app treats the Image Platform as an opaque dependency behind a versioned OpenAPI contract (ADR-003).
- Future creator tools, admin consoles, and benchmark runners are additional clients of the same surface.
- Splitting the modular monolith into true microservices later is possible without changing the public contract.

## Revisit when

- The platform's surface area grows enough (e.g. a separate ML inference fleet, a training service) that the monolith starts hurting deploys, on-call, or CI time.
- The inter-service hop becomes a measurable latency tax on the user-facing UX (e.g. p95 image-API roundtrip > 100ms in-region).
- A second product (not DreamChat) needs the platform — at which point we may also need explicit multi-tenant work beyond ADR-004's token-per-tenant model.

---

## Confidence to Implement

**Score: 95/100 — Very High**

"Build it as a separate service" is operationally clear: one Go module, its own deploy unit, its own DB. Nothing here requires invention. The decision *enables* implementability — it isolates the platform from the web app's churn. Mild caveats only: the boundary must be enforced by code review, and inter-service auth/networking has to be configured (S2S token, internal DNS) — not in this ADR.
