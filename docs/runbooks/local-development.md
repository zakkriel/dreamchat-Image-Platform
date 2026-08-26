# Runbook — Local Development

## Requirements

- Go (see `go.mod`)
- Docker (Compose v2)
- Node 20+ (only for the `playground/` console)

Postgres, Redis and MinIO all run in the compose stack — nothing to install.

## Start everything

```bash
make start
```

`scripts/dev.sh`. One command, no manual steps, ~10s warm:

| Step | What it does |
| --- | --- |
| infra | `docker compose up -d postgres redis minio` |
| tunnel | public MinIO origin, **only** when `FAL_KEY`/`BFL_API_KEY` is set |
| migrate | goose, idempotent |
| tokens | reused from `.dev/tokens.env` while they still authenticate, else seeded |
| api + worker | host `go run`, so a Go change costs a ~5s restart, not an image rebuild |
| playground | vite on `:5174` with both tokens pre-filled, opened in a browser |

Ctrl-C stops every process it started, including the tunnel. Infra containers
stay up (`make down` removes them and their volumes).

Because the API and worker run on the host, the script stops their container
counterparts — two workers would double-consume the job queue.

Nothing is prompted for and nothing is pasted: a raw token is unrecoverable
once printed, so the script caches it in `.dev/tokens.env` (gitignored) and
writes `playground/.env.local`, which the Connection panel reads as its default.
A token you type yourself is kept in `localStorage` and always wins.

`make dev` remains the all-in-docker variant: same stack, API and worker as
containers, rebuilt with `docker compose build` after every Go change.

```bash
curl -i http://localhost:8081/health     # {"status":"ok"}
```

## Published host ports

Two ports are deliberately non-default so this stack can run alongside its
siblings in the shared dev environment:

| Service | Host | Container | Why not the default |
| --- | --- | --- | --- |
| Postgres | `5433` | `5432` | `dreamchat-world-backend` owns host `5432` |
| API | `8081` | `8080` | `dreamchat-world-backend/core/api` owns host `8080` |
| Redis | `6379` | `6379` | — |
| MinIO | `9000` / `9001` | same | — |
| Playground UI | `5174` | — | historically `5173` was taken by `dreamchat-frontend`; that repo is archived and `5173` retired, but 5174 stays so the playground never collides with the live frontend on `5273` |

OpenAPI docs: <http://localhost:8081/docs>.

## Playground console

```bash
cd playground && npm install && npm run dev    # http://localhost:5174
```

It proxies `/api/*` to `http://localhost:8081` (the backend ships no CORS).
Paste the tenant and admin tokens into the Connection panel.

## Presigned URLs: two origins, two audiences

SigV4 signs the `Host` header, so a presigned URL can never be rewritten after
signing — it must be signed for the host that will actually fetch it. There are
two different fetchers, so there are two knobs:

| Variable | Fetched by | Local value |
|---|---|---|
| `S3_PUBLIC_ENDPOINT` | the **caller** — browser, playground, world backend | `http://localhost:9000` (compose/dev.sh default) |
| `S3_REFERENCE_ENDPOINT` | the **provider's servers** — fal downloads `image_urls` itself | the cloudflared tunnel URL, set automatically by `make start` |

Containers write to MinIO at `http://minio:9000`, a Docker network name no
browser can resolve, which is why delivery is signed for `localhost:9000`.

**Delivery no longer depends on the tunnel.** `make start` points
`S3_PUBLIC_ENDPOINT` at `localhost:9000` unconditionally and gives the tunnel URL
to `S3_REFERENCE_ENDPOINT` only. A dead tunnel now breaks *new reference-
conditioned generation* (loudly, at job level) instead of silhouetting every
image already delivered to the frontend — which it did three times.

`S3_REFERENCE_ENDPOINT` falls back to `S3_PUBLIC_ENDPOINT`, so a deployment whose
single origin is publicly reachable (R2/CDN) sets neither. `DEV_TUNNEL=off` skips
the tunnel entirely when you are not exercising a real provider.

## Providers

`IMAGE_PROVIDER=mock` is the default and needs no key. Real keys come from your
gitignored `./.env` (compose interpolates it) — never hardcode them in
`docker-compose.yml`:

```bash
FAL_KEY=...        # fal.ai — real, identity/pack-capable (reference-conditioned)
BFL_API_KEY=...    # Black Forest Labs — real, scene-only
```

Both are read by the API *and* the worker; the worker is what actually calls the
provider, so a key set on only one resolves a route that cannot execute.

Two gotchas:

- **Mock is synthetic.** It may not satisfy identity-axis routes
  (identity/pack/production) unless `ALLOW_SYNTHETIC_PROVIDERS=true`, which
  compose sets for local dev. Without it, every character/place pack request
  fails closed with `422`. Production must leave it off.
- **Route priority is lower-is-preferred**: mock `100`, fal `200`. With
  synthetic identity allowed, an unpinned request keeps resolving mock even when
  `FAL_KEY` is set. Pin `provider_id: "fal"` per request, set
  `IMAGE_PROVIDER=fal`, or turn `ALLOW_SYNTHETIC_PROVIDERS` off.

Confirm what registered:

```bash
docker compose logs image-platform-api | grep readiness
# real_identity_capable_provider=true real_identity_providers=["fal"]
```

## Running fal locally (reference images must be public)

fal is reference-conditioned: it downloads `image_urls` **from fal's servers**.
A presigned `localhost` URL fails with
`file_download_error: Failed to download the file`. The identity must also have
anchor assets attached, or the job fails closed with `missing_reference_assets`
before any provider call.

`make start` does this for you: it opens the tunnel and exports it as
`S3_REFERENCE_ENDPOINT`, leaving delivery on `localhost:9000`. To do it by hand
against compose:

```bash
cloudflared tunnel --url http://localhost:9000     # prints https://<name>.trycloudflare.com
echo "S3_REFERENCE_ENDPOINT=https://<name>.trycloudflare.com" >> .env
docker compose up -d image-platform-api image-platform-worker
```

> Set `S3_REFERENCE_ENDPOINT`, **not** `S3_PUBLIC_ENDPOINT`. Pointing delivery at
> the tunnel is what made every already-delivered image 404 each time the tunnel
> rotated.

> The compose bucket has anonymous download enabled, so while the tunnel is up
> every object in it is world-readable to anyone holding a URL. Stop the tunnel
> and drop the `.env` override as soon as you are done.

## Common failures

| Symptom | Cause |
| --- | --- |
| `422 provider_preference_unavailable` | request pinned a `provider_id` whose key is unset, so the provider is not registered |
| `422 route_capability_mismatch` / `no_route` | mock is synthetic and `ALLOW_SYNTHETIC_PROVIDERS` is off, so it backs no route on any axis |
| pack fails `missing_reference_assets` | the visual identity has no anchor assets and the route is reference-conditioned |
| `fal: … file_download_error` | `S3_REFERENCE_ENDPOINT` is unset or not reachable from the public internet |
| broken image thumbnails in the playground | `S3_PUBLIC_ENDPOINT` not set, so URLs point at `minio:9000` |
| `compile: signal: killed` during build | Docker ran out of memory building both binaries at once — build them serially |

## Other commands

```bash
make migrate-status
make down          # stops the stack and DELETES its volumes
go test ./...
```
