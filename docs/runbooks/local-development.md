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
