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

1. **Connection** — base URL + tenant/admin bearer tokens (saved to
   localStorage), `GET /health`, and the OpenAPI version from `GET /openapi.json`.
2. **Styles** — `POST /v1/styles`, `GET /v1/styles`, and an "active style"
   selector reused by the generation panels.
3. **Artifact generation** — `POST /v1/artifacts/{artifact_id}/generate`.
4. **Pack generation** — `POST /v1/characters/{id}/generate-pack` and
   `POST /v1/places/{id}/generate-pack`.
5. **Job monitor** — polls `GET /v1/jobs/{job_id}`, shows a status timeline and
   error fields, fetches `GET /v1/jobs/{job_id}/assets`, and renders the
   returned thumbnail/preview/final URLs in a gallery.
6. **Asset search** — `POST /v1/assets/search` with match type, compatibility
   score, generation-recommended flag, and an image gallery.
7. **Webhook endpoint** — `PUT`/`GET /v1/admin/webhook-endpoint` (admin token);
   shows the signing secret only when PUT returns it.
8. **Admin job controls** — `POST /v1/admin/jobs/{job_id}/retry` and
   `.../cancel` (admin token).
9. **Request log** — every request the playground made (method, URL, status,
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
```

## Typical flow

1. **Connection** → paste tenant + admin tokens → `GET /health` (expect
   `200 {"status":"ok"}`) → `GET /openapi.json` (shows the version).
2. **Styles** → create a style → it becomes the active style.
3. **Artifact generation** → submit → copy the returned `job_id`.
4. **Job monitor** → paste the `job_id` → **Poll (2s)** until `completed` →
   **GET .../assets** to render the image tiers.
5. **Pack generation** → generate a character or place pack → monitor its job.
6. **Asset search** → search the world you generated into.
7. **Webhook endpoint** → set a `webhook.site` URL (admin token) → note the
   one-time signing secret.
8. **Admin job controls** → retry a failed job or cancel a live one (admin
   token).
9. **Request log** → expand any call and **Copy curl** to replay from a shell.

## Sample assets without generating

To populate searchable/renderable assets without running a generation job, see
[`docs/runbooks/playground-fixtures.md`](../docs/runbooks/playground-fixtures.md)
and the optional `scripts/seed_visual_fixtures.sh`.

## Non-goals (intentionally absent)

No provider/route management, audit-event entry, webhook replay/DLQ/rotation,
signature rotation, cost dashboards, or user/token management UI. This is a
testing console, not an admin backoffice — those surfaces are out of scope.
