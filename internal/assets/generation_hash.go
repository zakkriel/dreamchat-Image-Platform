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
// (the current prompt source, ADR-I002 Decision 6), the intent (draft/commit
// selects a different route class and therefore different output quality),
// and the transform block. Folding those into the artifact hash's field set
// would collide two unrelated key vocabularies; a versioned sibling keeps
// each evolvable independently.
//
// Provider/model identity is deliberately NOT part of the key: reuse is
// asset-state-first (ADR-008) — the same logical content is reusable
// regardless of which provider produced it. max_megapixels is included because
// the worker now enforces it and a lower pixel budget is a different render
// contract. lazy remains excluded because it controls scheduling, not pixels.

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
)

// generationHashVersion namespaces the hash so the input set can evolve
// MaxMegapixels became behavioral in Wave 3 and style_profile_id now affects
// the worker prompt, so the version is bumped from prior definitions. Version 4
// folds in the identity's CURRENT anchor set, which the worker conditions on but
// the hash previously ignored - see IdentityAnchorAssetIDs.
//
// Version 5 (Wave 3.5) quantizes max_megapixels to the two decimals the column
// stores, so requests the database cannot tell apart stop producing two keys and
// two paid renders. A bump invalidates every existing cached key, which is free
// only pre-traffic — that timing is why the correction lands now.
const generationHashVersion = "5"

// GenerationHashInput is the well-typed set of combined-contract fields that
// determine a generation render. TenantID comes from the principal; the rest
// mirror the validated request plus the fetched identity prompt source.
type GenerationHashInput struct {
	TenantID       string
	IdentityID     string
	DisplayName    string
	StyleProfileID string
	AnchorAssetID  string
	// IdentityAnchorAssetIDs is the identity's CURRENT anchor set - the
	// reference images a reference-conditioned provider actually renders from
	// (internal/jobs/worker.go gathers identity.AnchorAssetIds into
	// ProviderGenerateRequest.ReferenceURLs).
	//
	// It has to be in the hash. AnchorAssetID above is only the caller-supplied
	// request field, which is a deferred contract and is normally empty, so
	// without this a character whose anchors were replaced kept the same render
	// hash and the reuse path returned an image of the OLD appearance. Anchors
	// are deliberately not versioned on the identity row (they are reference
	// provenance, not a canonical-trait change), which is exactly why the
	// reuse key has to carry them.
	//
	// Order is preserved rather than sorted: it is passed through to the
	// provider as an ordered reference list, so a reordering may legitimately
	// render differently and must not silently reuse.
	IdentityAnchorAssetIDs []string
	DeriveFrom             string
	Intent                 string
	// MaxMegapixels is the effective validated pixel budget persisted by the
	// handler. It is hashed quantized to the two decimals the max_megapixels
	// column stores, so two requests the store cannot distinguish share one key.
	MaxMegapixels float64
	TransformJSON string
}

// GenerationRenderHash returns the deterministic hex render hash for the
// input. The same inputs always produce the same hash; any material change
// (different identity, prompt source, style profile, request anchor, identity
// anchor set, derive source, intent, pixel budget, or transform) produces a
// different hash.
func GenerationRenderHash(in GenerationHashInput) string {
	var b strings.Builder
	writeHashField(&b, "gv", generationHashVersion)
	writeHashField(&b, "tenant_id", in.TenantID)
	writeHashField(&b, "identity_id", in.IdentityID)
	writeHashField(&b, "display_name", NormalizeArtifactDescription(in.DisplayName))
	writeHashField(&b, "style_profile_id", in.StyleProfileID)
	writeHashField(&b, "anchor_asset_id", in.AnchorAssetID)
	// Each anchor is its own labelled field, plus an explicit count. Joining
	// them into one string would let ["a","b"] collide with ["a,b"] on whatever
	// separator was chosen; labelled fields cannot.
	writeHashField(&b, "identity_anchor_count", strconv.Itoa(len(in.IdentityAnchorAssetIDs)))
	for i, anchorID := range in.IdentityAnchorAssetIDs {
		writeHashField(&b, "identity_anchor_"+strconv.Itoa(i), anchorID)
	}
	writeHashField(&b, "derive_from", in.DeriveFrom)
	writeHashField(&b, "intent", in.Intent)
	maxMegapixels := in.MaxMegapixels
	if maxMegapixels <= 0 {
		// Direct callers that predate the field use the same effective default as
		// the HTTP handler, keeping old helper fixtures semantically equivalent.
		maxMegapixels = 4.0
	}
	// Quantized to the two decimals the STORE keeps: max_megapixels is
	// NUMERIC(6, 2) (migrations/0013_cost_routing.sql). Shortest-round-trip
	// formatting ('g'/-1) kept apart values the column cannot tell apart — a
	// client sending a float32-widened 2.0999999046325684 and one sending 2.1
	// both persist as 2.10 and render identically, yet produced two different
	// cache keys and a second full-price render. Precision the store discards
	// cannot be a render difference, so keeping it only forfeits real cache hits.
	writeHashField(&b, "max_megapixels", strconv.FormatFloat(maxMegapixels, 'f', 2, 64))
	writeHashField(&b, "transform", NormalizeArtifactDescription(in.TransformJSON))

	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}
