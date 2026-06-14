// Package webhooks implements Phase 7C-4 (2/2) outbound webhooks: a single
// signed endpoint per tenant, HMAC-SHA256 request signing, three
// generation-job lifecycle events, an asynq-backed deliverer with bounded
// retry/backoff, and a per-event delivery-attempt log.
//
// MVP scope (deliberately small): one active endpoint per tenant, no
// subscription management, no dead-letter queue, no event filtering, no
// multiple endpoints, no signature rotation endpoint.
package webhooks

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
)

// Sign computes the value of the X-DreamChat-Signature header for a request
// body: the lowercase hex HMAC-SHA256 of the exact bytes that will be sent,
// keyed by the endpoint's server-generated secret, prefixed with "sha256=".
//
// Receivers verify a delivery by recomputing this over the raw request body
// with their copy of the secret and comparing in constant time. The signature
// is over the EXACT bytes posted, so callers must sign and send the same
// []byte (no re-marshaling between sign and send).
func Sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}
