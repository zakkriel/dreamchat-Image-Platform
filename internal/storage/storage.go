// Package storage owns S3-compatible object upload, key generation, the
// canonical s3:// URL form persisted on visual_assets, and (Phase 6B)
// time-limited presigned read URLs minted from the deterministic object keys.
package storage

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Storage writes object bytes to the configured bucket and mints time-limited
// authenticated GET URLs for reading them back.
//
// Put returns a canonical s3:// URL the caller persists as durable provenance.
// Presign computes an ephemeral https GET URL from the object key at read time
// — it is never persisted (it embeds an expiry and a signature), per the
// Phase 6B delivery design.
type Storage interface {
	Put(ctx context.Context, key string, body []byte, contentType string) (string, error)
	Presign(ctx context.Context, key string, ttl time.Duration) (string, error)
}

// CanonicalURL formats the s3:// URL recorded on visual_assets rows.
func CanonicalURL(bucket, key string) string {
	return "s3://" + bucket + "/" + strings.TrimPrefix(key, "/")
}

// KeyFromCanonicalURL extracts the object key from an s3://<bucket>/<key> URL as
// produced by CanonicalURL. It returns ok=false for any URL not in that form
// (empty, a presigned https URL, or missing a key segment), so callers presign
// the ACTUAL stored object rather than a guessed key — or fail clearly.
func KeyFromCanonicalURL(canonical string) (string, bool) {
	const scheme = "s3://"
	if !strings.HasPrefix(canonical, scheme) {
		return "", false
	}
	rest := canonical[len(scheme):]
	slash := strings.IndexByte(rest, '/')
	if slash < 0 {
		return "", false
	}
	key := rest[slash+1:]
	if key == "" {
		return "", false
	}
	return key, true
}

// AssetVariant identifies a single output for an asset's S3 object key.
type AssetVariant string

const (
	VariantHigh  AssetVariant = "high"
	VariantLow   AssetVariant = "low"
	VariantThumb AssetVariant = "thumb"
)

// ObjectKey is the deterministic S3 key for an asset variant. Phase 3 stores
// PNGs at <prefix>assets/<asset_id>/<variant>.<ext>.
func ObjectKey(assetID string, variant AssetVariant, ext string) string {
	return fmt.Sprintf("assets/%s/%s.%s", assetID, variant, strings.TrimPrefix(ext, "."))
}
