// Package storage owns S3-compatible object upload, key generation, and the
// canonical s3:// URL form persisted on visual_assets. Phase 3 only writes;
// reads and presigned URLs land later.
package storage

import (
	"context"
	"fmt"
	"strings"
)

// Storage writes object bytes to the configured bucket and returns a
// canonical s3:// URL the caller can persist.
type Storage interface {
	Put(ctx context.Context, key string, body []byte, contentType string) (string, error)
}

// CanonicalURL formats the s3:// URL recorded on visual_assets rows.
func CanonicalURL(bucket, key string) string {
	return "s3://" + bucket + "/" + strings.TrimPrefix(key, "/")
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
