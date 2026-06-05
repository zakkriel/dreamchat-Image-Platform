# ADR-005 — Store only hashed API tokens

## Status

Accepted for initial implementation.

## Context

ADR-004 requires per-request DB lookup of bearer tokens. The token-storage scheme decides what happens if the DB (or a backup, or a logs export) leaks: do the credentials in it become usable by an attacker?

The constraint is: bearer tokens must be lookupable in O(1)-ish time on every request, immediately revocable, and useless to anyone who reads the database.

## Decision

The database stores **only** a non-secret token prefix (for lookup) and a one-way hash of the secret portion. The raw token is shown to the user exactly once at creation time; the platform cannot reproduce it later. Hash algorithm: HMAC-SHA256 with a server-side pepper (loaded from env / secrets manager, not in DB) is the default for high-throughput service tokens. Argon2id is acceptable for user-created keys where computational cost on auth is OK.

## Alternatives considered

- **Plaintext storage.** Simplest. Any DB read (insider, backup leak, ops mistake) exposes every credential. Not acceptable.
- **Encrypted at rest only** (Postgres TDE / disk encryption). Protects against physical disk theft but not against an attacker with DB-level read access (the application can decrypt, so an attacker with the same access can too).
- **JWT (no storage at all).** No DB row to leak. But immediate revocation requires a denylist (a DB row, with the same threat surface), and JWT secrets in our service become the new high-value target.
- **Encrypted-at-rest tokens with application-managed key.** Tokens can be decrypted by the running service. A breach that gives the attacker app access still gives them everything.
- **TPM/HSM-backed key wrap.** Strongest answer, operationally heavy for a startup, not justified yet.

## Tradeoffs

- **+** A full DB dump exposes no usable credentials.
- **+** Compatible with immediate revocation (ADR-004 — set `status = 'revoked'` in the same row that holds the hash).
- **+** Pepper-in-env separates the "what's in the DB" attack surface from the "what's on the host" attack surface.
- **−** Token shown only once: UX cost at creation time (admin must save it; rotation is by re-issue, not re-display).
- **−** Slightly more middleware complexity (prefix lookup + constant-time hash compare).
- **−** Pepper rotation needs a procedure (issue new prefixes under new pepper; keep old pepper for the deprecation window; eventually invalidate old tokens).

## Consequences

- `api_tokens` table stores: `token_prefix` (12–16 chars after the family prefix, indexed, non-secret) and `token_hash` (HMAC-SHA256 of secret + pepper).
- Token creation endpoint returns the raw `dci_{env}_<prefix><secret>` exactly once in the response and never logs it.
- Pepper lives in `TOKEN_PEPPER` env var; rotation procedure is documented as a future runbook (not yet written — see `frustration_log.md`).

## Revisit when

- We adopt OAuth/JWT for external clients (ADR-004 revisit) — JWT signing keys then need the same disciplined storage.
- We need offline / detached verification of tokens (rare, but would push toward signed credentials over hashed lookups).
- Audit / compliance regimes require a specific hash family or HSM-backed signing.

---

## Confidence to Implement

**Score: 88/100 — High**

Standard hashed-credential pattern. `docs/architecture/security-and-auth.md` proposes Argon2id/bcrypt for human-created keys or HMAC-SHA256 with a server-side pepper for high-throughput API tokens — both are well-supported in Go (`golang.org/x/crypto/argon2`, `crypto/hmac`). Subtracting points because the prefix length, hash algorithm, and pepper rotation policy are described at the right level but not pinned to single choices.
