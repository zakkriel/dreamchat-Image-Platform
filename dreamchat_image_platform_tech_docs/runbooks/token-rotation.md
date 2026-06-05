# Runbook — Token Rotation

## Purpose

Rotate API tokens without breaking clients.

## Steps

1. Create new token with same scopes.
2. Give token to client securely.
3. Wait for client to deploy new token.
4. Check `last_used_at` for old and new tokens.
5. Revoke old token.
6. Confirm old token returns 401.

## Emergency Revocation

If a token leaks:

1. Revoke token immediately.
2. Check recent usage.
3. Identify generated jobs/assets from that token.
4. Disable suspicious jobs if needed.
5. Issue replacement token.

## Logging

Never paste raw tokens into tickets or logs.
