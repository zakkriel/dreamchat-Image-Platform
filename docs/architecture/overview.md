# Architecture Overview

## System Purpose

The DreamChat Image Platform is a standalone service responsible for visual assets used by DreamChat.

It provides an API that can be used by:

- DreamChat web app
- Internal test playground
- Admin tools
- Future creator tools
- Future partner or external clients

The platform should be independently testable without the web app.

## High-Level Architecture

```txt
DreamChat Web App / Playground / Admin Tool
        |
        v
Go Image Platform API
        |
        +-- Auth Middleware
        +-- Visual Identity Service
        +-- Asset Service
        +-- Generation Job Service
        +-- Prompt Compiler
        +-- Provider Router
        +-- Storage Service
        +-- Telemetry Service
        |
        +-- Postgres
        +-- Redis
        +-- S3-compatible object storage
        +-- External Image Providers
```

## Deployment Shape

First implementation should be a modular monolith.

One deployable service can contain:

- HTTP API
- Worker process
- Provider adapters
- Repository layer
- Swagger/OpenAPI docs

The API and workers may run as two processes from the same codebase:

```txt
image-platform api
image-platform worker
```

## Core Principle

The platform is asset-state-first, not prompt-first.

Bad model:

```txt
prompt -> image
```

Correct model:

```txt
visual identity -> generation intent -> prompt package -> job -> asset -> reusable variant
```

## Responsibilities

The service owns:

- Visual identities
- Character/place/artifact visual profiles
- Image generation jobs
- Asset storage metadata
- Low-res and high-res derivative URLs
- Provider routing
- Cost and latency telemetry
- Asset retrieval and reuse
- Style profiles
- Asset versioning

The service does not own:

- DreamChat world canon
- In-world truth
- NPC memory
- Narration logic
- Relationship logic
- Backstage updates

## Required Quality Attributes

### Performance

- API requests should be fast.
- Generation should be async.
- Preview assets should be available before final high-res assets when possible.

### Consistency

- Character assets must remain recognizable across new variants.
- Place assets must remain recognizable across new variants.
- Visual identity records must carry persistent anchors and canonical visual traits.

### Cost Control

- Retrieve before generating.
- Cache aggressively.
- Track costs per token/client/world/asset type/provider.

### Portability

- Providers must be behind adapters.
- Business logic must not depend directly on provider payloads.

### Operability

- Every request has a request ID.
- Every generation has a job ID.
- Every provider call has provider metadata.
- Every generated asset has model, provider, prompt hash, seed, and cost metadata.
