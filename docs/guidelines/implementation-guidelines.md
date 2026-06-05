# Implementation Guidelines

## Non-Negotiable Rules

1. The service must be standalone.
2. The web app must not call image providers directly.
3. OpenAPI contract must be maintained.
4. All `/v1` endpoints require bearer token auth unless explicitly public.
5. Store only token hashes.
6. Generation must be async.
7. Retrieve before generating.
8. Store all asset metadata.
9. Use provider adapters.
10. Include request IDs in logs and responses.

## First Milestone

A local developer should be able to:

```txt
1. Start API and worker locally.
2. Open Swagger docs.
3. Create a dev token.
4. Create a character visual identity.
5. Generate a character pack using mock provider.
6. Poll job status.
7. Retrieve generated assets.
```

## Mock Provider

The mock provider must produce deterministic placeholder image files or URLs.

This allows testing without provider costs.

## Real Provider

Add exactly one real provider first.

Do not implement multiple real providers before the platform contract is proven.

## No Python / LangGraph for MVP

Do not use Python or LangGraph in the core MVP.

The platform is a Go asset/job/storage/retrieval service.

Python may be considered later only for:

- self-hosted inference
- LoRA training
- offline image evaluation
- ML experiments
