# Component Boundaries

## API Layer

Owns:

- HTTP routing
- OpenAPI request/response validation
- Bearer-token authentication
- Scope checks
- Idempotency handling
- Request ID creation
- Response formatting

Must not own:

- Provider-specific logic
- Prompt assembly details
- Storage implementation details
- Business decision logic

## Auth Service

Owns:

- API token lookup
- Token hash verification
- Scope checks
- Rate-limit identity extraction
- Audit event for token usage

Must never log raw bearer tokens.

## Visual Identity Service

Owns:

- Creation and update of visual identity records
- Canonical visual traits
- Consistency keys
- Anchor assets
- Current version
- Owner binding to character/place/artifact IDs

It does not own DreamChat canon. It receives known/perceived/canonical visual descriptors from the client.

## Asset Service

Owns:

- Asset metadata
- Asset search
- Asset retrieval
- Asset lifecycle
- Asset versioning
- Variant classification
- Low-res/high-res URLs

## Generation Job Service

Owns:

- Job creation
- Job status transitions
- Retry rules
- Idempotency connection
- Worker enqueueing
- Job result mapping

Status lifecycle:

```txt
queued -> running -> preview_ready -> completed
queued -> running -> failed
queued -> cancelled
```

## Prompt Compiler

Owns:

- Turning structured visual identity + style + variant intent into provider-neutral prompt packages
- Negative prompt policy
- Style prompt expansion
- Prompt hashing
- Prompt versioning

It must produce a deterministic `prompt_hash` from the normalized prompt package.

## Provider Router

Owns:

- Selecting provider/model based on quality tier, latency tier, asset type, cost policy, and availability
- Fallback rules
- Provider circuit-breaker state

## Provider Adapters

Own:

- Provider-specific HTTP calls
- Provider payload transformation
- Provider error normalization
- Provider job polling if needed

Must not write directly to domain tables.

## Storage Service

Owns:

- Uploading images to object storage
- Generating stable object keys
- Creating thumbnails or derivative records
- Signed URL generation if needed

## Telemetry Service

Owns:

- Cost events
- Provider latency events
- Cache hit/miss events
- Asset reuse events
- Failure classification

## Repository Layer

Owns database access.

Recommended repositories:

```txt
VisualIdentityRepository
VisualAssetRepository
GenerationJobRepository
StyleProfileRepository
ApiTokenRepository
ProviderModelRepository
CostEventRepository
```

---

## Confidence to Implement

**Score: 90/100 — Very High**

The boundaries are sharp and they avoid the most common drift problems: provider-specific logic stays in adapters, business logic doesn't reach into Postgres directly, the prompt compiler is isolated so it can be golden-tested. The `prompt_hash` requirement is a nice forcing function for determinism. The Go package layout in `docs/guidelines/go-service-guidelines.md` mirrors these boundaries. Easy to enforce with a linting rule (`go-import-restrictions` or a `tool` check on architectural layers).
