# Runbook — Local Development

## Requirements

- Go
- Docker
- Postgres
- Redis
- MinIO or local S3-compatible storage

## Local Services

Recommended docker-compose services:

```txt
postgres
redis
minio
```

## Start API

```bash
go run ./cmd/api
```

## Start Worker

```bash
go run ./cmd/worker
```

## Open Docs

```txt
http://localhost:8080/docs
```

## Mock Provider

Use mock provider by default locally.

```txt
IMAGE_PROVIDER=mock
```

## Create Dev Token

Use local admin CLI or seed migration to create a dev token.

The raw token should only be shown once.

---

## Confidence to Implement

**Score: 80/100 — High**

The shape is right (Docker compose with Postgres/Redis/MinIO, `go run ./cmd/api` and `./cmd/worker`, mock provider env var, Swagger at `:8080/docs`). To actually be runnable on day one this needs: an actual `docker-compose.yml`, a `Makefile` (or `task`/`just` recipes) for `make up` / `make seed` / `make dev`, a seed migration that creates a `dci_dev_*` token, and a one-line bootstrap that pre-loads a `mock` row into `provider_models`. None of that is hard, just absent.
