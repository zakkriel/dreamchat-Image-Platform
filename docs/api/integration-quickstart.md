# Integration quickstart — world backend → Image Platform

Audience: the `dreamchat-world-backend` service integrating against this platform
for the PoC (one background per scene, one portrait per entity).

**Every request/response in this document was executed against a real stack**
(API + worker + Postgres + Redis + MinIO, `IMAGE_PROVIDER=mock`) and the bodies
below are copied from that run — they are not illustrative sketches. Where a call
fails, the failure shown is the real one.

## 0. Contract of record

Generation is asynchronous. `POST` returns `202` with a `job_id`; you learn the
outcome by **reading** `GET /v1/jobs/{job_id}` and then
`GET /v1/jobs/{job_id}/assets`.

Outbound webhooks exist (`docs/api/openapi.yaml` → `webhooks`) but are a **latency
hint only**. Delivery is at-least-once, some transitions deliberately never emit,
and the event body carries IDs only. **A consumer that ignores webhooks entirely
is still correct.** Do not make readiness depend on them.

Two invariants bind you:

- **IDs over the wire, URLs fetched on demand.** Persist `asset_id`. Never persist
  a `*_download_url` — they are presigned per read and expire at
  `url_expires_at` (default 15 minutes).
- **Reuse is the default.** Re-issuing the same generation request is a zero-cost
  cache hit returning the *same* `asset_id`. Never re-request an image to
  "refresh" it. Only `force_regenerate` produces a new render.

## 1. Prerequisites — read this or your first call returns 422

### 1.1 An identity-capable provider must be configured

`POST /v1/generations` forces a hard `identity_capable` capability floor. With
stock config (`IMAGE_PROVIDER=mock`, no `FAL_KEY`, `ALLOW_SYNTHETIC_PROVIDERS`
unset) **every** call fails:

```http
HTTP/1.1 422 Unprocessable Entity

{"code":"route_capability_mismatch",
 "message":"no identity-capable provider configured for this route's required capability",
 "request_id":"784461de-0da5-4ed7-b6f2-91520f6bc98e"}
```

`ALLOW_SYNTHETIC_PROVIDERS` defaults to **false in every environment** (including
dev), so `mock` does not back identity work unless you opt in. Choose one:

| Environment | Setting | Effect |
|---|---|---|
| Local / CI | `ALLOW_SYNTHETIC_PROVIDERS=true` | `mock` backs identity routes; deterministic placeholder PNGs |
| Real images | `FAL_KEY=<key>` | registers the reference-conditioned fal adapter (`identity_capable`) |

`BFL_API_KEY` does **not** help here: BFL is `scene_capable` only and is
deliberately not eligible for identity work.

At boot the API logs its verdict — check it before debugging anything else:

```json
{"msg":"provider capability readiness","real_identity_capable_provider":false,
 "synthetic_identity_capable_provider":true,"synthetic_identity_providers":["mock"],
 "synthetic_identity_allowed":true,"invalid_routes":3}
```

### 1.1.1 First-portrait bootstrap — two sequences, pick by environment

The identity floor interacts with a second rule: **`fal` is reference-conditioned**
(`Capabilities().RequiresReferenceImage == true`). It conditions on the identity's
**anchor assets**, and an anchor must itself already be a `ready` asset. So the
very first portrait for a brand-new identity is a chicken-and-egg problem *only on
the fal path*. `mock` and `bfl` do not require references (`bfl` asserts
`RequiresReferenceImage == false`).

**Sequence A — synthetic (dev / CI / this handshake). No anchor needed at all.**

```
ALLOW_SYNTHETIC_PROVIDERS=true          # mock may back identity-axis routes

1. POST /v1/styles                                   -> style_profile_id
2. POST /v1/characters/{id}/visual-identity          -> visual_identity_id
3. POST /v1/generations {subject.identity_id}        -> 202  (no anchors involved)
4. GET  /v1/jobs/{job_id} -> completed
```

There is **nothing to bootstrap**: mock satisfies the `identity_capable` floor
through the capability hierarchy (`pack_capable ⊇ identity_capable`) and needs no
reference image, so step 3 succeeds against a zero-anchor identity. Output is a
deterministic placeholder PNG. Verified end to end.

**Sequence B — real images via fal. An anchor is required, and it must not be a placeholder.**

```
FAL_KEY=<key>                           # registers fal (identity/pack)
BFL_API_KEY=<key>                       # registers bfl (scene) — REQUIRED for step 3

1. POST /v1/styles                                   -> style_profile_id
2. POST /v1/characters/{id}/visual-identity          -> visual_identity_id
3. POST /v1/artifacts/{artifact_id}/generate         -> 202   # scene_capable
        {"provider_id": "bfl", ...}                          # PIN bfl — see below
   GET  /v1/jobs/{job_id}/assets                     -> asset_id (status=ready)
   GET  /v1/assets/{asset_id} | jq .provider_id      -> MUST be "bfl", not "mock"
4. POST /v1/characters/{id}/visual-identity/anchors
        {"world_id": "...", "anchor_asset_ids": ["<asset_id from 3>"]}
5. POST /v1/generations {subject.identity_id}        -> 202   # identity_capable -> fal
```

Why step 3 uses a *different* endpoint: there is **no asset-upload endpoint**, so
the first `ready` asset must be generated — and `POST /v1/generations` forces the
`identity_capable` floor, so it can never route to BFL. The artifact endpoint
requires only `scene_capable`, which BFL satisfies. That is the one way to obtain a
real first anchor through the API.

**Step 3 MUST pin `provider_id: "bfl"`, and turning `ALLOW_SYNTHETIC_PROVIDERS`
off does not do it for you.** The synthetic policy only gates the *identity axis*
(`internal/providers/capability.go` — "Scene/draft routes are unaffected by the
synthetic policy"). Step 3 asks for `scene_capable`, so mock stays eligible no
matter how that flag is set, and route priority is **lower-is-preferred**: mock's
scene routes are `100`, `route_bfl_text_to_image_standard` is `200`. An unpinned
step 3 therefore resolves **mock** and hands you a placeholder grid that passes
every anchor check. `provider_id` is a hard, fail-closed filter
(`ErrRequestedProviderUnavailable` → `422 provider_preference_unavailable` when
the key is unset), so pinning it either gets you BFL or tells you plainly that it
cannot. Verify the anchor's stamped `provider_id` before step 4 — that one check
is the difference between a real portrait and a smoke-over-grid.

**Do not use a synthetic asset as a fal anchor.** A mock-generated asset *would*
pass anchor validation — it is `ready`, has a `high_res_url`, and
`POST /v1/generations` leaves `visual_identity_id` NULL so it counts as unassigned
— but conditioning fal on a placeholder grid produces garbage. Synthetic-anchor-
then-fal is **not** the recovery; it is a way to get a technically-valid pipeline
with worthless output.

Skipping the anchor on the fal path fails **closed**, before any provider call, so
nothing is billed: the job goes terminally `failed` with
`error_code: missing_reference_assets` (or `invalid_reference_asset` if an attached
anchor is unusable). Covered by `TestSingleImageWithoutReferencesFailsClosed` and
`TestSingleImageFailTerminalEmitsWebhookEvent`.

Anchor validation rejects an asset that is not `ready`, has no `high_res_url`,
belongs to a **different world**, or is already bound to a different identity
(`422 invalid_anchor_asset`). The anchor set is replaced wholesale on each call.

**When both are configured** (`FAL_KEY` set *and* `ALLOW_SYNTHETIC_PROVIDERS=true`,
as in the handshake stack) both providers satisfy the identity floor and the
resolver picks by route `priority` **ascending**: the mock route is `100`, fal's is
`200`, so **mock wins and every portrait is a placeholder**. This is the single most
common "why are my images grey squares" cause, and nothing about the response
indicates it — you get a normal `202` and a normal `ready` asset.

Two ways to tell which provider actually served a job, after the fact:

```bash
# per asset: provider_id / model_id are stamped provenance
curl "$BASE/v1/assets/<asset_id>" -H "Authorization: Bearer $TOKEN" \
  | jq '{provider_id, model_id}'
# -> {"provider_id":"mock","model_id":"pm_mock_v1"}      placeholders
# -> {"provider_id":"fal","model_id":"pm_fal_flux_kontext_multi"}   real
```

…and the boot log's one-line verdict:

```json
{"msg":"provider capability readiness","real_identity_capable_provider":true,
 "real_identity_providers":["fal"],"synthetic_identity_capable_provider":true,
 "synthetic_identity_providers":["mock"],"synthetic_identity_allowed":true,
 "invalid_routes":1}
```

**To flip to real output**, remove mock from the identity axis rather than trying to
out-prioritise it:

```bash
export FAL_KEY=<key>
export ALLOW_SYNTHETIC_PROVIDERS=false   # or unset it; false is the default
make start                               # restart so the API re-reads config
```

Both `scripts/dev.sh` and `docker-compose.yml` read `ALLOW_SYNTHETIC_PROVIDERS`
from the host (`${ALLOW_SYNTHETIC_PROVIDERS:-true}` locally), so exporting it is
enough — no file edit. After the restart the boot log must show
`synthetic_identity_allowed:false`, and mock's routes are then excluded from the
identity axis, leaving `route_fal_text_to_image_identity` as the only match.

**This immediately makes Sequence B's anchor mandatory** — fal is
reference-conditioned, so any identity without a usable anchor now fails with
`missing_reference_assets` instead of quietly returning a placeholder. Flip the
provider and add the anchor step in the same change, not separately.

**The flip does not retire portraits you already generated.** Exact reuse is keyed
on tenant + render hash only (`internal/db/queries/visual_assets.sql`
`FindReadyGenerationByPromptHash`: `tenant_id + prompt_hash + asset_type='artifact'
 + variant_key='default' + status='ready'`), and provider is **deliberately not
part of the key** (`internal/assets/generation_hash.go`: "Provider/model identity
is deliberately NOT part of the key"). So an identity that already has a `ready`
mock placeholder keeps returning that exact placeholder after the flip — a
zero-cost cache hit, no fal call, forever. You get stale grids on everything
already generated and `missing_reference_assets` on everything new.

`/v1/generations` has **no `force_regenerate`** (only the artifact and pack
requests do), and it decodes with `DisallowUnknownFields`, so sending one is a
hard `422 invalid or unknown field` — not a silent no-op. Two real options:

1. **Platform-side (preferred):** archive the stale placeholder assets
   (`status='archived'`). The lookup requires `status='ready'`, so reuse misses
   and the next request generates for real. Do this in the **same window** as the
   flip: while mock still backs the identity axis, a miss simply regenerates
   another `ready` mock asset under the same hash and undoes the archive.
2. **Client-side:** change any hash input — identity id, the identity's
   `display_name`, `subject.anchor_asset_id`, `subject.derive_from`,
   `render.intent`, `max_megapixels` (default `4.0`), or `render.transform`.
   Attaching anchors and then sending `subject.anchor_asset_id` is the natural
   one, since it changes the hash as a side effect of doing the bootstrap.

### 1.2 Token and scopes

Two env vars on your side, never persisted:

```
DREAMCHAT_IMAGE_BASE_URL=http://localhost:8081
DREAMCHAT_IMAGE_API_TOKEN=dci_dev_<prefix>_<secret>
```

Sent as `Authorization: Bearer <token>`. `tenant_id` is derived server-side from
the token and **must not** appear in any request body. The four scopes this
quickstart needs:

| Scope | Used by |
|---|---|
| `styles:write` | `POST /v1/styles` |
| `images:write` | visual-identity upsert, `POST /v1/generations` |
| `jobs:read` | `GET /v1/jobs/{job_id}` |
| `images:read` | `GET /v1/jobs/{job_id}/assets`, `GET /v1/assets/{id}` |

Scope checks are AND across the listed set and fail `403 forbidden`.

These four are exactly what the sequence below needs and nothing more. Note the
consequence: **`GET /v1/styles` returns `403 forbidden`** with this set, because
listing needs `styles:read`. Create your style profile once and persist its `id`;
add `styles:read` only if you actually need to enumerate styles at runtime.

## 2. The governance envelope

`POST /v1/generations` requires a governance envelope. **All seven fields are
required by the contract**; six of them are hard-validated by the handler and
return `422 invalid_request` naming the missing field.

| Field | Required by | Notes |
|---|---|---|
| `schema_version` | handler + verifier | any non-empty string, e.g. `"1.0"` |
| `classification_id` | handler + verifier | opaque to this service |
| `visibility` | handler + verifier | opaque to this service |
| `content_class` | handler + verifier | **opaque** — stored and logged, never parsed |
| `authorized_by` | handler + verifier | must appear in `GOVERNANCE_AUTHORIZED_ISSUERS` |
| `issued_at` | verifier + schema | RFC3339; freshness-checked |
| `signature` | handler + verifier | non-empty; see below |

### Placeholder signature format under `log_only`

Signature verification is currently `StubSignatureVerifier`, which **passes every
signature unconditionally** — the real canonicalization is a cross-system contract
with core that is not yet designed (`TODO(core-signing)`), and this repo will not
invent it.

Therefore: **any non-empty string is accepted today.** Use a self-describing
sentinel so it can never be mistaken for real crypto and is greppable when core
ships signing:

```
"signature": "stub-unsigned-v1"
```

Do not send an empty string — that is a hard `422`, independent of mode.

### `log_only` semantics you must not misread

`GOVERNANCE_ENFORCEMENT=log_only` (the default) means **the request proceeds
regardless of the verdict**, but the verdict is still recorded. Verified live:

| Envelope | HTTP | `audit_events` row |
|---|---|---|
| all 7 fields, allowlisted `authorized_by` | `202` | `media.eligibility_verified` |
| `authorized_by` not in the allowlist | `202` | `media.eligibility_blocked` / `unknown_issuer` |
| `issued_at` omitted | `202` | `media.eligibility_blocked` / `missing_field` |

**A `202` does not mean your envelope was accepted.** Under `enforce` each blocked
row above becomes `403 governance_blocked`. Get `authorized_by` onto the allowlist
and send `issued_at` now, or flipping enforcement later breaks you silently-then-loudly.

Freshness: `issued_at` must be within `GOVERNANCE_MAX_AGE` (default 24h) of now
and no more than 2 minutes in the future, else `stale`.

## 3. The verified call sequence

### Step 1 — style profile (required, once)

```bash
curl -X POST "$BASE/v1/styles" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"name":"dreamchat-default","style_mode":"open_prompt",
       "positive_prompt":"painterly, soft rim light, cinematic",
       "negative_prompt":"text, watermark","default_quality_tier":"standard"}'
```

```json
{"id":"sty_17c36e08344742e1","name":"dreamchat-default","style_mode":"open_prompt",
 "positive_prompt":"painterly, soft rim light, cinematic","negative_prompt":"text, watermark",
 "default_quality_tier":"standard","status":"active"}
```

Traps (both hit on the first attempts of the verification run, both `400`):
`positive_prompt` is required (not `prompt_fragment`), and `style_mode` must be one
of `open_prompt | preset_style | creator_style | provider_native`.

Style profiles created through the API are **tenant-wide** (`world_id` is NULL);
per-world style profiles are not creatable via the API. Create one and **persist its
`id`** — listing via `GET /v1/styles` needs `styles:read`, which is deliberately not
in the four scopes below, so that call returns `403` for a minimal client.

### Step 2 — visual identity (required, once per entity)

```bash
curl -X POST "$BASE/v1/characters/char_mira/visual-identity" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"owner_type":"character","owner_id":"char_mira","world_id":"world_poc",
       "display_name":"Captain Mira","style_profile_id":"sty_17c36e08344742e1",
       "canonical_visual_traits":{"hair":"short silver","eyes":"grey","build":"tall"}}'
```

```json
{"id":"vi_c40c1fc21b057d27","owner_type":"character","owner_id":"char_mira",
 "world_id":"world_poc","display_name":"Captain Mira",
 "style_profile_id":"sty_17c36e08344742e1","current_version":1,"status":"active",
 "canonical_visual_traits":{"build":"tall","eyes":"grey","hair":"short silver"}}
```

**Every one of these six body fields is required.** Verified responses:

| Field | Missing → | Notes |
|---|---|---|
| `owner_type` | `400 invalid_request` | must equal the route's type (`character` / `place`) |
| `owner_id` | `400 invalid_request` | must equal the `{character_id}` path parameter |
| `world_id` | `400 invalid_request` | part of the upsert key; **not** derived from the token |
| `display_name` | `400 invalid_request` | also seeds the render description |
| `style_profile_id` | `400 invalid_request` | invalid/foreign id → `422 invalid_style_profile` |
| `canonical_visual_traits` | `400 invalid_request` — `"canonical_visual_traits is required"` | see below |

`canonical_visual_traits` is the one that trips clients, because it is easy to miss
in a request builder and its rule is presence, not content: **the key must be
present, but `{}` is accepted** (verified — an empty object returns `200`). So a
client can satisfy the validator while supplying nothing meaningful. Don't: these
traits are the durable description of the subject and feed the render, so an empty
map means every portrait for that entity is generated from the display name alone.
Send the real traits.

Note the mixed status codes — missing fields are `400 invalid_request`, while a
style id that doesn't belong to your tenant is `422 invalid_style_profile`. Handle
both.

`owner_type` must match the route and `owner_id` must equal the path parameter.
Upsert is keyed on `(tenant_id, world_id, owner_type, owner_id)`, so replaying this
is safe. **Store the returned `id`** — it is the `subject.identity_id` for every
later generation. Reading it back requires `?world_id=`.

### Step 3 — request the image

```bash
curl -X POST "$BASE/v1/generations" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: poc-mira-portrait-0001' \
  -d '{"governance":{"schema_version":"1.0","classification_id":"cls_poc_default",
        "visibility":"private","content_class":"character_portrait",
        "authorized_by":"svc_world_backend","issued_at":"2026-08-08T16:33:23Z",
        "signature":"stub-unsigned-v1"},
       "subject":{"identity_id":"vi_c40c1fc21b057d27"},
       "render":{"intent":"commit"}}'
```

```json
{"job_id":"job_041c843f24940ad4","status":"queued",
 "estimated_cost_usd":"0.0100","currency":"USD",
 "cost_reservation_id":"resv_79aac0e00fd870ad"}
```

Notes:

- `Idempotency-Key` header is **mandatory** and canonical. Neither header nor body
  key → `422`.
- `world_id` is **not accepted** here — it is derived from the identity.
- `render.intent` must be `draft` or `commit`.
- Unknown fields are rejected with `422` (strict decoding).
- `render.transform_only: true` → `501`; `grid.enabled: true` → `501`.

> **Idempotency trap (hit during verification).** The key is bound to a hash of the
> whole body, and `issued_at` changes every time you build an envelope. Re-sending
> "the same" request with a fresh timestamp yields:
>
> ```json
> {"code":"idempotency_conflict","message":"idempotency key reused with a different body or endpoint"}
> ```
> (`409`). Pin one `issued_at` per logical request and store it with the key, or
> derive a new key whenever the body changes. Do not regenerate the envelope on retry.

### Step 4 — poll

```bash
curl "$BASE/v1/jobs/job_041c843f24940ad4" -H "Authorization: Bearer $TOKEN"
```

```json
{"id":"job_041c843f24940ad4","job_type":"generation","status":"completed",
 "visual_identity_id":"vi_c40c1fc21b057d27",
 "final_asset_ids":["asset_cf63b1d2e6150906"],
 "cost_estimate_usd":"0.0100","actual_cost_usd":"0.0100",
 "created_at":"2026-08-08T13:33:23.591857-03:00",
 "updated_at":"2026-08-08T13:33:24.288595-03:00"}
```

`status` ∈ `queued | running | preview_ready | completed | failed | cancelled`.
Terminal set: `completed | failed | cancelled`. `final_asset_ids` and
`preview_asset_ids` are **omitted entirely** while empty — do not expect `[]`.

On `failed`, read `error_code` + `error_message`. `retryable` tells you whether a
fresh request could plausibly succeed.

### Step 5 — fetch the assets

```bash
curl "$BASE/v1/jobs/job_041c843f24940ad4/assets" -H "Authorization: Bearer $TOKEN"
```

```json
{"assets":[{"id":"asset_cf63b1d2e6150906","asset_type":"artifact","variant_key":"default",
  "status":"ready","version":1,"world_id":"world_poc","visual_identity_id":null,
  "prompt_hash":"6a01171474f3335a...","provider_id":"mock","model_id":"pm_mock_v1",
  "thumbnail_download_url":"http://.../thumb.png?X-Amz-Algorithm=...",
  "preview_download_url":"http://.../low.png?X-Amz-Algorithm=...",
  "final_download_url":"http://.../high.png?X-Amz-Algorithm=...",
  "url_expires_at":"2026-08-08T16:48:37.73886Z"}]}
```

Fetching `final_download_url` returns `HTTP 200`, `image/png`, a real PNG. Three
tiers are always available; measured from this same asset: `thumbnail_download_url`
= 256×256, `preview_download_url` = 768×768, `final_download_url` = 1024×1024 (the
provider output, never upscaled).

> **Storage requirement.** Note `visual_identity_id` is **`null` on the asset** for
> this path (confirmed in the DB, not just the API view). The *job* carries the
> identity; the *asset* does not. There is no "give me the current asset for
> identity X" endpoint. **You must persist the `identity_id → asset_id` mapping
> yourself.** Suggested split:
>
> - **Platform** stores assets, provenance, cost, tiers.
> - **World backend** stores `entity → visual_identity_id` and the last known
>   `asset_id` per slot, so it can answer the frontend without calling us, plus
>   `job_id` while in flight.
> - **Frontend** stores nothing durable; it receives `{asset_id | null}` and treats
>   any URL as expiring.

### Step 6 — reuse (verified)

Re-issuing the same request with a **new** idempotency key is a cache hit:

```json
{"job_id":"job_efbb6db33397f3b9","status":"queued","estimated_cost_usd":"0.0000"}
```

and that job resolves to the **same asset at zero cost**:

```json
{"id":"job_efbb6db33397f3b9","status":"completed","actual_cost_usd":"0",
 "cost_estimate_usd":"0","final_asset_ids":["asset_cf63b1d2e6150906"],
 "visual_identity_id":"vi_c40c1fc21b057d27"}
```

This is the mechanism behind "a portrait is never re-requested on a perception
change": what changes with understanding is the text, not the face. If you *do*
call again, you get the same face for free — you never need to guard against it.

## 4. Polling — bounded, jittered backoff is REQUIRED

Rate limiting is per token, **counted on reads exactly like writes** (the limiter
sits on the whole `/v1` group after auth). Defaults: **60 requests/minute, 1000
requests/hour**. Live headers on a plain job read:

```
X-RateLimit-Requests-Per-Minute: 60
X-RateLimit-Requests-Per-Minute-Remaining: 55
X-RateLimit-Requests-Per-Minute-Reset: 1786207200
X-RateLimit-Requests-Per-Hour: 1000
X-RateLimit-Requests-Per-Hour-Remaining: 983
X-RateLimit-Requests-Per-Hour-Reset: 1786208400
```

**A denied request still increments the counter.** Naive fixed-interval polling is
therefore self-harming: once you are limited, continuing to poll keeps your own
window pinned and extends the outage. Required client behaviour:

1. **Bounded, jittered exponential backoff.** Start ~1s, multiply ~1.5–2×, cap at
   ~15s, and apply full jitter (`sleep = random(0, computed)`) so concurrent
   pollers do not synchronise into bursts.
2. **Honour `Retry-After`** on `429 rate_limit_exceeded`; it is authoritative and
   present on that code (it is deliberately *absent* on
   `429 concurrent_jobs_exceeded`, which clears when a job reaches a terminal
   state, not on a clock).
3. **Cap total attempts** and surface a timeout rather than polling forever. There
   is **no** list-jobs endpoint, no status filter, no `since` cursor, and no
   ETag/`If-None-Match` — every poll is one request per job and a full body.
4. **Stop at a terminal status.** Never poll a `completed`/`failed`/`cancelled` job.
5. **Budget your fan-out.** At a 5s interval one token sustains roughly 5 in-flight
   jobs before the minute window saturates; the hourly cap works out to ~16.7
   req/min sustained. This aligns with the default `max_concurrent_jobs = 5`, so
   respecting the concurrency cap naturally keeps polling inside budget.
6. **Watch the two 429s separately.** `rate_limit_exceeded` = slow down.
   `concurrent_jobs_exceeded` = too many live jobs for this token; wait for one to
   finish rather than retrying faster.

Request-rate limiting **fails open** if Redis is unavailable (headers omitted); the
concurrency cap is Postgres-backed and always holds.

## 5. Error codes you should handle

| Status | Code | Meaning / action |
|---|---|---|
| 422 | `invalid_request` | missing/unknown field; message names it. Fix and resend |
| 422 | `route_capability_mismatch` | no identity-capable provider configured — see §1.1 |
| 422 | `no_route` / `unsupported_capability` | no route matches this request shape |
| 422 | `no_price_entry` | resolved model has no active price |
| 422 | `budget_exceeded` | cost budget exhausted. **Never** silently downgraded |
| 403 | `governance_blocked` | only under `enforce`; envelope invalid |
| 403 | `forbidden` | token lacks a required scope |
| 409 | `idempotency_conflict` | same key, different body — see the §3 trap |
| 429 | `rate_limit_exceeded` | back off; honour `Retry-After` |
| 429 | `concurrent_jobs_exceeded` | too many live jobs; wait for a terminal state |
| 501 | `transform_only_not_supported` / `grid_not_supported` | deferred features |

Cross-tenant access is indistinguishable from absence: `404 not_found`.

## 6. Local stack ports

`make start` (`scripts/dev.sh`) is the local stack: it brings up Postgres, Redis
and MinIO as containers and runs the **API and worker as host processes**. It
deliberately stops the compose `image-platform-api`/`image-platform-worker`
containers first — two copies would double-consume the asynq queue and fight for
the API port. If you ever run `docker compose up` for the full stack, do not leave
`make start` running alongside it.

Published host ports:

| Service | Host port | Why |
|---|---|---|
| API | **8081** | `dreamchat-world-backend` owns 8080 in this environment |
| Postgres | **5433** | the world backend's Postgres owns 5432 |
| Redis | 6379 | default |
| MinIO | 9000 / 9001 | default; presigned URLs are signed for `S3_PUBLIC_ENDPOINT` |

Because SigV4 signs the `Host` header, a presigned URL cannot be rewritten after
signing — `S3_PUBLIC_ENDPOINT` must be the origin **you** will call
(`http://localhost:9000` locally), not the Docker-internal `minio:9000`.

## 7. Open item — tenant granularity

Today `tenant` = **the deployment**: one token → one tenant, and a token maps to no
particular world. `world_id` is a separate scoping column, carried on
`visual_identities`, `visual_assets` and `asset_packs` (`NOT NULL`) and nullable on
`generation_jobs`.

If a per-customer split is needed later, note that **RLS keys on `tenant_id`
alone**. Sizing on this side: **no schema change is required to add tenants.**
Tenant ids are opaque `TEXT`, and the enforcement layer is already in place —
measured on a migrated database at head 19: **20 tables** run under
`ENABLE` + `FORCE ROW LEVEL SECURITY` with a deny-by-default `tenant_isolation`
policy, of which **14 carry `tenant_id` directly** and the remainder are policed
through parent-join `EXISTS` policies.

The work is therefore a **data migration, not a structural one**: re-key existing
rows from the single deployment tenant onto per-customer tenant ids across those
tables (children follow their parents), mint per-tenant tokens, and re-point
`world_id` groupings. Because `world_id` already rides on every asset, identity and
pack row, **nothing needs regenerating** — no image is reproduced. Cost is
dominated by the re-key and its verification (cross-tenant isolation must be
re-proven under the `image_platform_api` role afterwards), not by schema design.
The one genuine design question to settle first is whether a "customer" maps to one
tenant with many worlds or one tenant per world; the former is a straight re-key,
the latter multiplies token management.
