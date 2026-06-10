package assets

// Artifact render hash (Phase 6A2). This is the deterministic cache key for
// single-artifact exact reuse (retrieval-before-generation, ADR-009): given the
// request inputs that determine what an artifact render *is*, it produces a
// stable hash that two identical requests share and two materially-different
// requests do not. The generation path stores it in visual_assets.prompt_hash
// and looks an existing ready artifact up by it before reserving cost or
// enqueuing provider work.
//
// Why artifacts hash instead of using identity/matrix retrieval: artifacts have
// no durable visual_identity_id in the generation path — they are produced as
// asset_type = 'artifact', variant_key = 'default'. Identity/matrix retrieval
// (Phase 6A1) keys on visual_identity_id + variant family, which artifacts do
// not carry, so it cannot distinguish two different logical artifacts. The
// render hash therefore includes artifact_id: without it, two unrelated
// artifacts with similar descriptions could collide and reuse each other's
// visual. Until artifacts gain real visual identities, artifact_id IS part of
// the cache key.
//
// Provider/model identity is deliberately NOT folded into the hash. Real
// provider/model routing does not exist yet (it is a Phase 7 concern); the
// current artifact path resolves to a single fixed mock route as a placeholder,
// not a deterministic routing decision. Baking a placeholder model id into a
// durable cache key would either (a) silently invalidate every cached artifact
// the day routing lands, or (b) be a guessed id we were told not to include.
// When deterministic routing exists, a hash-version bump (the "v" field below)
// can fold it in without colliding with today's keys.

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
)

// artifactHashVersion namespaces the hash so the input set can evolve (e.g.
// folding in a real model id once routing is deterministic) without colliding
// with keys minted by an earlier definition.
const artifactHashVersion = "1"

// artifactDefaultVariantKey is the only variant artifacts are generated as in
// this phase; it is part of the key so a future non-default artifact variant
// cannot reuse a default render.
const artifactDefaultVariantKey = "default"

// ArtifactHashInput is the well-typed set of request fields that determine an
// artifact render. TenantID/WorldID/ArtifactID/StyleProfileID come from the
// authenticated request (tenant from the principal, never the body); the
// remaining fields mirror the generate request payload.
type ArtifactHashInput struct {
	TenantID            string
	WorldID             string
	ArtifactID          string
	Description         string
	StyleProfileID      string
	StyleProfileVersion *int32
	QualityTier         string
	VariantKey          string // defaults to "default" when empty
}

// ArtifactRenderHash returns the deterministic hex render hash for the input.
// The same inputs always produce the same hash; any material change (different
// artifact_id, description, style profile, style version, or quality tier)
// produces a different hash.
func ArtifactRenderHash(in ArtifactHashInput) string {
	variantKey := in.VariantKey
	if variantKey == "" {
		variantKey = artifactDefaultVariantKey
	}
	version := ""
	if in.StyleProfileVersion != nil {
		version = strconv.FormatInt(int64(*in.StyleProfileVersion), 10)
	}

	// Labeled, newline-delimited canonical form. Labels + a delimiter the field
	// values cannot contain (the description is whitespace-collapsed to a single
	// line, ids carry no newlines) prevent field-boundary ambiguity, so e.g.
	// artifact_id="ab" + description="c" cannot collide with artifact_id="a" +
	// description="bc".
	var b strings.Builder
	writeHashField(&b, "v", artifactHashVersion)
	writeHashField(&b, "tenant_id", in.TenantID)
	writeHashField(&b, "world_id", in.WorldID)
	writeHashField(&b, "artifact_id", in.ArtifactID)
	writeHashField(&b, "description", NormalizeArtifactDescription(in.Description))
	writeHashField(&b, "style_profile_id", in.StyleProfileID)
	writeHashField(&b, "style_profile_version", version)
	writeHashField(&b, "quality_tier", in.QualityTier)
	writeHashField(&b, "variant_key", variantKey)

	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

// NormalizeArtifactDescription canonicalizes a free-text artifact description
// so that differences that don't change the prompt's meaning (leading/trailing
// whitespace, runs of spaces/tabs/newlines) don't produce a different hash.
// strings.Fields splits on any Unicode whitespace and drops empties, so
// "  bronze   key\n" and "bronze key" normalize identically. Case is preserved
// — "Bronze Key" and "bronze key" are treated as distinct prompts.
func NormalizeArtifactDescription(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func writeHashField(b *strings.Builder, label, value string) {
	b.WriteString(label)
	b.WriteByte('=')
	b.WriteString(value)
	b.WriteByte('\n')
}
