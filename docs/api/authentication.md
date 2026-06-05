# API Authentication

## Authentication Method

The API uses bearer tokens.

```txt
Authorization: Bearer dci_test_xxxxxxxxx
Authorization: Bearer dci_live_xxxxxxxxx
```

## Token Environments

```txt
dci_test_ = test token
dci_live_ = live token
dci_dev_ = local development token
```

## Scopes

Endpoints require scopes.

Initial scope list:

```txt
images:read
images:write
jobs:read
styles:read
styles:write
models:read
admin:tokens
admin:costs
admin:providers
```

## Example Request

```bash
curl -X POST "https://image-api.dreamchat.ai/v1/assets/search" \
  -H "Authorization: Bearer dci_test_xxxxx" \
  -H "Content-Type: application/json" \
  -d '{"owner_type":"character","owner_id":"char_123"}'
```

## Token Storage

Tokens are shown only once.

The service stores only token hashes.

## Token Revocation

Admin users can revoke tokens.

A revoked token must immediately stop working.
