# Image Platform Playground

A **dev-only** local testing console for the DreamChat Image Platform API.

It exists to make the Image Platform usable and testable on its own —
independently, before DreamChat integration — by driving the **existing** API
from a browser. It is deliberately **not** a productized admin dashboard and
adds **no** backend features: every button maps 1:1 to an endpoint already in
[`api/openapi.yaml`](../api/openapi.yaml).

> Local development tool. Do not deploy it, and do not point it at a shared or
> production environment. Tokens are stored in your browser's `localStorage`.

## What it does

A single page with stacked panels:

0. **Scenario import** — load a JSON scenario (file upload or pasted text) to
   pre-fill the panels below. Dev/local-only; nothing is uploaded, stored
   server-side, or auto-submitted. See [Scenario import](#scenario-import).
1. **Connection** — base URL + tenant/admin bearer tokens (saved to
   localStorage), `GET /health`, and the OpenAPI version from `GET /openapi.json`.
2. **Styles** — `POST /v1/styles`, `GET /v1/styles`, and an "active style"
   selector reused by the other panels.
3. **Visual identity** — `POST`/`GET /v1/characters/{id}/visual-identity` and
   `.../places/{id}/visual-identity`. Captures world_id, owner_type, owner_id,
   display_name, `canonical_visual_traits` (JSON), style_profile_id, and an
   optional consistency_key. The created identity becomes the "active visual
   identity" reused by the Pack-generation and Asset-search panels.
4. **Artifact generation** — `POST /v1/artifacts/{artifact_id}/generate`.
5. **Pack generation** — `POST /v1/characters/{id}/generate-pack` and
   `POST /v1/places/{id}/generate-pack`. Packs require an **existing visual
   identity** for the owner (panel 3); a "Use active visual identity" button
   fills the id/world.
6. **Job monitor** — polls `GET /v1/jobs/{job_id}`, shows a status timeline and
   error fields, fetches `GET /v1/jobs/{job_id}/assets`, and renders the
   returned thumbnail/preview/final URLs in a gallery.
7. **Asset search** — `POST /v1/assets/search`. The backend requires world_id,
   visual_identity_id, owner_type (**character or place** — artifact is
   rejected), variant_key, style_profile_id, and state_version (default 1).
   Shows match type, compatibility score, generation-recommended flag, and an
   image gallery.
8. **Webhook endpoint** — `PUT`/`GET /v1/admin/webhook-endpoint` (admin token);
   shows the signing secret only when PUT returns it.
9. **Admin job controls** — `POST /v1/admin/jobs/{job_id}/retry` and
   `.../cancel` (admin token).
10. **Request log** — every request the playground made (method, URL, status,
    duration, request/response JSON) with a **copy-as-curl** button.

## Prerequisites

- Node 20+ and npm.
- The Image Platform backend running locally. From the repo root:

  ```bash
  make dev        # postgres + redis + minio + api + worker, then seeds a token
  make seed-admin # prints a second token carrying admin:costs, admin:jobs
  ```

  `make dev` prints a tenant token (`dci_dev_*`); `make seed-admin` prints an
  admin token (`dci_admin_*`). Paste both into the Connection panel.

## Run

```bash
cd playground
npm install
npm run dev        # http://localhost:5173
```

### How it reaches the API (CORS)

The backend ships no CORS middleware, so a browser cannot call it cross-origin
directly. The Vite dev server therefore **proxies** `/api/*` to the API. The
Connection panel's base URL defaults to `/api`, which is proxied to
`http://localhost:8080`.

- To point at a different local API, copy `.env.example` to `.env` and set
  `VITE_API_TARGET`, then restart `npm run dev`.
- If your API *does* serve CORS, you can instead set the base URL field to a
  full origin (e.g. `http://localhost:8080`) and bypass the proxy.

## Validate

```bash
npm install
npm run build     # tsc --noEmit && vite build
npm run lint      # eslint .
npm run test      # vitest run (scenario import validation)
```

## Typical flow

1. **Connection** → paste tenant + admin tokens → `GET /health` (expect
   `200 {"status":"ok"}`) → `GET /openapi.json` (shows the version).
2. **Styles** → create a style → it becomes the active style.
3. **Visual identity** → create a character (or place) identity → it becomes
   the active visual identity. **Do this before generating a pack** — packs
   resolve an existing identity for the owner.
4. **Artifact generation** → submit → copy the returned `job_id`. (Artifacts do
   not need a visual identity.)
5. **Job monitor** → paste the `job_id` → **Poll (2s)** until `completed` →
   **GET .../assets** to render the image tiers.
6. **Pack generation** → **Use active visual identity** to fill the id/world →
   generate a character or place pack → monitor its job.
7. **Asset search** → **Use active visual identity**, set `variant_key` and
   `state_version` (default 1), then search. All of world_id,
   visual_identity_id, owner_type (character|place), variant_key,
   style_profile_id and state_version are required by the backend.
8. **Webhook endpoint** → set a `webhook.site` URL (admin token) → note the
   one-time signing secret.
9. **Admin job controls** → retry a failed job or cancel a live one (admin
   token).
10. **Request log** → expand any call and **Copy curl** to replay from a shell.

## Scenario import

The **Scenario import** panel (panel 0) loads a JSON document that pre-fills the
other panels' form fields so a predefined test case can be repeated without
retyping everything. It is strictly a dev/local convenience:

- Import only **fills form fields and active config** — it never submits an API
  call. You still press each panel's button to actually call the backend.
- Nothing is uploaded or stored server-side; the file is read in-browser only.
- Manual editing still works afterward — change any field before calling the API.
- Re-importing re-applies the scenario; fields not present in the scenario, and
  any section you omit, are left untouched.

You can either **choose a `.json` file** or **paste JSON** into the textarea,
then press **Import scenario**. Malformed JSON, unknown sections/fields, and
badly typed values are reported as validation errors and abort the import. On
success the panel lists which panels were filled.

### Format

The top level may contain optional `version` / `name` metadata plus any of these
**optional** sections. Every field within a section is optional — only the
fields you include are applied.

| Section | Fills panel | Notable fields |
| --- | --- | --- |
| `connection` | Connection | `baseUrl`; `token` / `adminToken` only when explicitly present |
| `style` | Styles | `name`, `styleMode`, `positivePrompt`, `negativePrompt`, `defaultQualityTier` |
| `visualIdentity` | Visual identity | `ownerType` (`character`\|`place`), `worldId`, `ownerId`, `displayName`, `canonicalVisualTraits` (object), `styleProfileId`, `consistencyKey` |
| `artifact` | Artifact generation | `artifactId`, `worldId`, `styleProfileId`, `description`, `qualityTier`, `latencyTier`, `deliveryMode`, `providerId`, `forceRegenerate`, `idempotencyKey` |
| `pack` | Pack generation | `character` / `place` objects with `entityId`, `worldId`, `styleProfileId`, `packTemplate`, `qualityTier`, `providerId`, `forceRegenerate` |
| `assetSearch` | Asset search | `worldId`, `ownerType`, `visualIdentityId`, `variantKey`, `styleProfileId`, `stateVersion` (int), `qualityTier`, `fallbackPolicy` |
| `webhook` | Webhook endpoint | `url` |
| `admin` | Admin job controls | `jobId` |

> **Never commit raw bearer tokens** in a scenario file. The example below fills
> only the base URL; paste tenant/admin tokens into the Connection panel by hand.

### Example scenario

This canonical sample is also committed at
[`examples/example-scenario.json`](examples/example-scenario.json) so you can
upload it directly via **Choose .json file…**.

```json
{
  "version": 1,
  "name": "Playground smoke test",
  "connection": {
    "baseUrl": "/api"
  },
  "style": {
    "name": "Storybook Soft",
    "styleMode": "open_prompt",
    "positivePrompt": "clean flat illustration, soft lighting, storybook",
    "negativePrompt": "harsh shadows, photorealistic",
    "defaultQualityTier": "standard"
  },
  "visualIdentity": {
    "ownerType": "character",
    "worldId": "world_dev",
    "ownerId": "character_play_1",
    "displayName": "Playground Hero",
    "canonicalVisualTraits": {
      "hair": "black",
      "outfit": "blue cloak"
    },
    "consistencyKey": ""
  },
  "artifact": {
    "artifactId": "artifact_play_1",
    "worldId": "world_dev",
    "description": "a brass compass resting on an old map",
    "qualityTier": "standard",
    "latencyTier": "balanced",
    "deliveryMode": "final_only",
    "forceRegenerate": false
  },
  "pack": {
    "character": {
      "entityId": "character_play_1",
      "worldId": "world_dev",
      "packTemplate": "character_minimal_portrait_pack",
      "qualityTier": "standard"
    },
    "place": {
      "entityId": "place_play_1",
      "worldId": "world_dev",
      "packTemplate": "place_minimal_scene_pack",
      "qualityTier": "standard"
    }
  },
  "assetSearch": {
    "worldId": "world_dev",
    "ownerType": "character",
    "variantKey": "neutral",
    "stateVersion": 1
  },
  "webhook": {
    "url": "https://webhook.site/your-id"
  },
  "admin": {
    "jobId": "job_play_1"
  }
}
```

After importing this scenario, the Connection panel shows base URL `/api` (token
fields untouched), and the Styles, Visual identity, Artifact generation, Pack
generation, Asset search, Webhook endpoint, and Admin job controls panels are
pre-filled. Press each panel's button to drive the API as usual.

### Per-request provider preference (`providerId`)

Both the Artifact and Pack panels expose an optional **`provider_id` (preference)**
select. It maps to the request body's `provider_id` field, which pins route
resolution to a single provider **for that one request** instead of the
deployment-wide `IMAGE_PROVIDER` default. This is what lets one stable
deployment generate a **BFL** anchor portrait and then a **fal** character pack
without touching any Railway variable.

- It is a **hard** preference, validated before cost reservation:
  - an unconfigured provider → `422 provider_preference_unavailable`;
  - a provider with no route for the operation/capability → `422 no_route` /
    `unsupported_capability`.
- It never silently falls back. Pinning a scene-only provider (e.g. `bfl`) to a
  pack request fails closed rather than quietly resolving fal/mock.
- Leave it unset (`(unset)`) to keep the existing default route resolution.

The committed [`examples/seren-recurring-character.json`](examples/seren-recurring-character.json)
sample demonstrates the recurring-character flow end to end: the **artifact**
section pins `providerId: "bfl"` (the Seren anchor portrait) and the
**character pack** section pins `providerId: "fal"` (the reference-conditioned
pack). Import it, generate the anchor, attach it as the character's anchor asset
(Visual identity panel), then generate the pack — all against a deployment whose
`IMAGE_PROVIDER` never changes. See
[`docs/runbooks/railway-real-image-smoke-test.md`](../docs/runbooks/railway-real-image-smoke-test.md) §9.

## Sample assets without generating

To populate searchable/renderable assets without running a generation job, see
[`docs/runbooks/playground-fixtures.md`](../docs/runbooks/playground-fixtures.md)
and the optional `scripts/seed_visual_fixtures.sh`.

## Non-goals (intentionally absent)

No provider/route management, audit-event entry, webhook replay/DLQ/rotation,
signature rotation, cost dashboards, or user/token management UI. This is a
testing console, not an admin backoffice — those surfaces are out of scope.
