# ADR-014 — Use standardized error responses (RFC 7807 Problem Details)

## Status

Accepted for initial implementation.

## Context

The platform's clients (web app, admin tool, benchmark runner, future creator clients) all need to handle errors. Without a standard shape, every client implements its own parser, error messages drift in wording and meaning, and debugging across services becomes guesswork. Provider adapters add another layer: each upstream provider has its own error vocabulary, which must be normalized before it reaches a client.

## Decision

All error responses use **RFC 7807 Problem Details** (`application/problem+json`) with a fixed shape: `type` (stable URL), `title` (short summary), `status` (HTTP code), `detail` (human-readable), `request_id` (mandatory, links to logs). Provider-specific errors are normalized to a fixed vocabulary (`provider_timeout`, `provider_rate_limited`, `provider_content_rejected`, `provider_auth_failed`, `provider_capacity_error`, `provider_invalid_request`, `provider_unknown_error`) before they leave the adapter layer.

## Alternatives considered

- **Free-form error messages.** Easiest to ship: just write whatever feels right per endpoint. Highest long-term cost: every client implements N error parsers, error semantics drift, and "is this retryable?" becomes guesswork.
- **Vendor-specific shape** (Stripe-style: typed error codes, parameter-level errors). Richer than 7807, less portable. Justified for products with a developer-facing API as a primary surface; we're internal-first.
- **gRPC status codes.** Rich and standardized. Wrong transport for us (we're HTTP+JSON-first per ADR-003).
- **Plain HTTP status + text body.** Works for trivial cases. Loses the `request_id` linkage to logs, and clients can't programmatically distinguish "rate-limited by us" from "rate-limited by provider."
- **Custom JSON shape** (e.g. `{ "error": { "code": ..., "message": ... } }`). Common pattern, marginally more readable than 7807. Loses the `type` URL convention that lets us document each error class at a stable address.

## Tradeoffs

- **+** Single client-side parser across all endpoints.
- **+** `type` URL gives a stable home for each error class's documentation; clients can branch on URL string.
- **+** `request_id` is mandatory by shape, not by convention — every error response carries it.
- **+** OpenAPI codegen produces typed `ProblemDetails` errors automatically.
- **−** Slightly verbose for trivial 404s (`{ type, title: "Not Found", status: 404, detail: "...", request_id }` vs. plain text).
- **−** Provider error normalization vocabulary needs maintenance — when a provider adds a new failure class, we have to decide which of the seven existing categories it maps to (or add an eighth).
- **−** `type` URL must remain stable forever once published; renaming a URL breaks clients.

## Consequences

- Error vocabulary lives in `docs/api/errors.md` and is the source of truth for client parsers.
- Go side: a typed-error package (`internal/errors`) defines domain errors; an HTTP middleware converts them to `ProblemDetails` at the boundary.
- Provider adapters (ADR-007) own error normalization; nothing else in the codebase imports provider SDKs and so nothing else can leak provider-specific shapes.
- `type` URLs follow `https://docs.dreamchat.ai/errors/{slug}`; even when docs aren't served at that URL, the slug is the contract.

## Revisit when

- A client (notably a partner / external developer) needs richer error detail (parameter-level errors for form validation, suggested next actions). 7807 supports extension members — adopt them rather than switching shapes.
- We adopt gRPC for a subset of internal APIs (translate between gRPC status and 7807 at the gateway).
- The provider error vocabulary grows past ~10 categories and starts feeling lossy.

---

## Confidence to Implement

**Score: 95/100 — Very High**

RFC 7807 problem-details with a `request_id` is well-supported in Go via custom error types + an HTTP middleware that maps domain errors to status + JSON. `docs/api/errors.md` already enumerates the error codes and provider-error normalization (`provider_timeout`, `provider_rate_limited`, etc.). Nothing here is novel.
