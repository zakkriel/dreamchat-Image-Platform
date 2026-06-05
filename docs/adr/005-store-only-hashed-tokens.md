# ADR-005 — Store Only Hashed API Tokens

## Status

Accepted for initial implementation.

## Context

DreamChat Image Platform needs a clean, independently testable architecture for persistent visual assets.

## Decision

Raw API tokens are shown only once. The database stores token hashes and lookup prefixes only.

## Consequences

Positive:

- A token database leak should not expose usable credentials.
- The decision supports standalone testing and future evolution.

Tradeoffs:

- Requires explicit contracts and discipline.
- May feel heavier than a quick direct integration, but it prevents coupling and drift.

## Notes

This ADR can be revisited after the first production benchmark.

## Confidence to Implement

**Score: 88/100 — High**

Standard hashed-credential pattern. `docs/architecture/security-and-auth.md` proposes Argon2id/bcrypt for human-created keys or HMAC-SHA256 with a server-side pepper for high-throughput API tokens — both are well-supported in Go (`golang.org/x/crypto/argon2`, `crypto/hmac`). Subtracting points because the decision says "lookup prefixes only" but doesn't fix the prefix length, hash algorithm, or pepper rotation policy — implementer picks, but with no guidance these choices later become migration debt.
