package assets

// Generation render hash (combined contract, /v1/generations). This is the
// deterministic cache key for combined-contract exact reuse
// (retrieval-before-generation, ADR-009): given the request fields that
// determine what a generation render *is*, it produces a stable hash two
// identical requests share and two materially-different requests do not. The
// generations handler stamps it into the job payload as prompt_hash (the
// worker persists it onto the produced visual_asset) and looks an existing
// ready asset up by it before resolving a route or reserving cost.
//
// Why a separate hash from ArtifactRenderHash: the combined contract carries
// no world/style/quality inputs — its render-determining fields are the
// subject (identity + anchors + derive_from), the identity's display name
// (the current prompt source, ADR-P002 Decision 6), the intent (draft/commit
// selects a different route class and therefore different output quality),
// and the transform block. Folding those into the artifact hash's field set
// would collide two unrelated key vocabularies; a versioned sibling keeps
// each evolvable independently.
//
// Provider/model identity is deliberately NOT part of the key: reuse is
// asset-state-first (ADR-008) — the same logical content is reusable
// regardless of which provider produced it. max_megapixels and lazy are also
// excluded: neither changes the produced pixels this chunk (MP is clamped but
// unenforced, lazy is stored-not-acted); when either becomes behavioral the
// hash version below MUST be bumped.

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// generationHashVersion namespaces the hash so the input set can evolve
// (e.g. folding in max_megapixels once the worker enforces it) without
// colliding with keys minted by an earlier definition.
const generationHashVersion = "1"

// GenerationHashInput is the well-typed set of combined-contract fields that
// determine a generation render. TenantID comes from the principal; the rest
// mirror the validated request plus the fetched identity's display name.
type GenerationHashInput struct {
	TenantID      string
	IdentityID    string
	DisplayName   string
	AnchorAssetID string
	DeriveFrom    string
	Intent        string
	// TransformJSON is the raw transform block (marshalled request JSON), or
	// empty when absent. A present-but-inert transform still keys separately:
	// once transform execution lands, an untransformed cached render must not
	// satisfy a transformed request.
	TransformJSON string
}

// GenerationRenderHash returns the deterministic hex render hash for the
// input. The same inputs always produce the same hash; any material change
// (different identity, display name, anchor, derive source, intent, or
// transform) produces a different hash.
func GenerationRenderHash(in GenerationHashInput) string {
	var b strings.Builder
	writeHashField(&b, "gv", generationHashVersion)
	writeHashField(&b, "tenant_id", in.TenantID)
	writeHashField(&b, "identity_id", in.IdentityID)
	writeHashField(&b, "display_name", NormalizeArtifactDescription(in.DisplayName))
	writeHashField(&b, "anchor_asset_id", in.AnchorAssetID)
	writeHashField(&b, "derive_from", in.DeriveFrom)
	writeHashField(&b, "intent", in.Intent)
	writeHashField(&b, "transform", NormalizeArtifactDescription(in.TransformJSON))

	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}
