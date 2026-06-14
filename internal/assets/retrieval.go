package assets

// Retrieval substrate (Phase 6A1). This is the deterministic decision layer
// for "retrieval before generation" (ADR-009): given a RetrievalQuery it
// resolves to exactly one of four typed outcomes — exact_match,
// compatible_match, preview_fallback, generated_required — by consulting the
// Phase 5B classifier (ClassifyVariant) and the Phase 5B compatibility matrix
// (CompareVariants). It does NOT reimplement the matrix and it holds NO HTTP
// concerns; SQL lives in the repository, the matrix verdict lives here.
//
// Phase 6A1 builds and exposes this layer only. Generation paths are NOT
// wired to it yet (that is 6A2/6A3). The single consumer in this PR is
// POST /v1/assets/search.
//
// Determinism: the same inputs always produce the same outcome and the same
// asset, so generation decisions are reproducible and cache-hit telemetry is
// honest. When several candidates qualify the winner is chosen by, in order:
//
//	1. highest outcome tier        (compatible_match > preview_fallback)
//	2. highest compatibility_score (the matrix rule's confidence)
//	3. lowest fallback_rank        (lower = preferred fallback)
//	4. lowest id                   (the single final tie-break)
//
// "lowest id" is the chosen final tie-break (not generated_at): it is total,
// stable, and free of clock/null ambiguity. The repository's ORDER BY mirrors
// it so DB-ordered and in-memory-ordered results agree.

import (
	"context"
	"errors"
	"fmt"
	"sort"
)

// Match types mirror AssetSearchResponse.match_type (OpenAPI MatchType) and
// the matrix outcomes. The first three reuse the compatibility.go outcome
// constants; OutcomeGeneratedRequired is retrieval-only (the matrix never
// emits it — it is the "no usable candidate" terminal state).
const OutcomeGeneratedRequired = "generated_required"

// Fallback policies (OpenAPI FallbackPolicy). They gate which non-exact
// outcomes count as a usable hit. See variant-compatibility-matrix.md §5.
const (
	FallbackPolicyNone           = "none"
	FallbackPolicyCompatibleOnly = "compatible_only"
	FallbackPolicyPreviewAllowed = "preview_allowed"
	FallbackPolicyAnyExisting    = "any_existing"
)

// DefaultFallbackPolicy matches the OpenAPI default.
const DefaultFallbackPolicy = FallbackPolicyCompatibleOnly

// ErrInvalidFallbackPolicy is returned by Retrieve (and surfaced as
// 400 invalid_request by the handler) for an unrecognized fallback_policy.
type ErrInvalidFallbackPolicy struct{ Value string }

func (e ErrInvalidFallbackPolicy) Error() string {
	return fmt.Sprintf("assets: invalid fallback_policy %q", e.Value)
}

// ValidFallbackPolicy reports whether p is one of the four allowed policies.
func ValidFallbackPolicy(p string) bool {
	switch p {
	case FallbackPolicyNone, FallbackPolicyCompatibleOnly,
		FallbackPolicyPreviewAllowed, FallbackPolicyAnyExisting:
		return true
	default:
		return false
	}
}

// RetrievalQuery is the well-typed retrieval request. TenantID is always set
// from the auth principal by the caller, never from a request body.
type RetrievalQuery struct {
	TenantID            string
	WorldID             string
	VisualIdentityID    string
	EntityType          string // character | place
	VariantKey          string
	StyleProfileID      string
	StyleProfileVersion *int32
	StateVersion        int32
	QualityTier         string
	FallbackPolicy      string // none | compatible_only | preview_allowed | any_existing
}

// RetrievalResult is the decision. Asset is nil for generated_required.
type RetrievalResult struct {
	MatchType             string // exact_match | compatible_match | preview_fallback | generated_required
	Asset                 *VisualAsset
	CompatibilityScore    float64
	FallbackReason        string
	GenerationRecommended bool
}

// CandidateSource is the SQL-facing dependency the retriever needs: an exact
// lookup and a candidate list. *pgRepository satisfies it, and so does any
// in-memory test double. Keeping it an interface keeps the decision layer
// pure and unit-testable without a database.
type CandidateSource interface {
	FindExact(ctx context.Context, q RetrievalQuery) (VisualAsset, error)
	ListRetrievalCandidates(ctx context.Context, q RetrievalQuery) ([]VisualAsset, error)
}

// Retriever runs the §4 retrieval algorithm against a CandidateSource.
type Retriever struct {
	src CandidateSource
}

// NewRetriever builds a Retriever over the given source.
func NewRetriever(src CandidateSource) *Retriever {
	return &Retriever{src: src}
}

// Retrieve resolves q to a single typed outcome. Order: exact → compatible →
// preview → generated_required, gated by q.FallbackPolicy.
func (rt *Retriever) Retrieve(ctx context.Context, q RetrievalQuery) (RetrievalResult, error) {
	policy := q.FallbackPolicy
	if policy == "" {
		policy = DefaultFallbackPolicy
	}
	if !ValidFallbackPolicy(policy) {
		return RetrievalResult{}, ErrInvalidFallbackPolicy{Value: q.FallbackPolicy}
	}
	q.FallbackPolicy = policy

	// 1. Exact match. status = 'ready' is enforced by the query; an exact key
	//    + owner + state + style match is reusable directly.
	exact, err := rt.src.FindExact(ctx, q)
	switch {
	case err == nil:
		asset := exact
		return RetrievalResult{
			MatchType:             OutcomeExactMatch,
			Asset:                 &asset,
			CompatibilityScore:    1.0,
			GenerationRecommended: false,
		}, nil
	case errors.Is(err, ErrNotFound):
		// fall through to fallback handling
	default:
		return RetrievalResult{}, err
	}

	// policy=none short-circuits: exact only, otherwise generate.
	if policy == FallbackPolicyNone {
		return generatedRequired(), nil
	}

	candidates, err := rt.src.ListRetrievalCandidates(ctx, q)
	if err != nil {
		return RetrievalResult{}, err
	}

	requested := ClassifyVariant(q.EntityType, q.VariantKey)
	if best, ok := rt.bestMatch(q, requested, candidates); ok {
		return best, nil
	}

	// 2b. any_existing (admin/debug): if the matrix produced no normal match,
	//     return the best existing ready candidate anyway — but never a
	//     failed/archived asset (the candidate query is ready-only) and never
	//     an identity anchor (excluded by the query and the guard below).
	if policy == FallbackPolicyAnyExisting {
		if best, ok := rt.bestAnyExisting(q, requested, candidates); ok {
			return best, nil
		}
	}

	return generatedRequired(), nil
}

func generatedRequired() RetrievalResult {
	return RetrievalResult{
		MatchType:             OutcomeGeneratedRequired,
		Asset:                 nil,
		CompatibilityScore:    0.0,
		GenerationRecommended: true,
	}
}

// scoredCandidate pairs an asset with the matrix verdict for it.
type scoredCandidate struct {
	asset   VisualAsset
	outcome string
	score   float64
	reason  string
}

// bestMatch evaluates every candidate through the matrix, keeps only those the
// policy and safety rules allow, and returns the deterministic winner.
func (rt *Retriever) bestMatch(q RetrievalQuery, requested ClassifiedVariant, candidates []VisualAsset) (RetrievalResult, bool) {
	qualifying := make([]scoredCandidate, 0, len(candidates))
	for _, c := range candidates {
		if !candidateReusable(c) {
			continue
		}
		candidate := ClassifyVariant(q.EntityType, c.VariantKey)
		verdict := CompareVariants(q.EntityType, requested, candidate)

		// Same-key (exact) candidates are handled by FindExact under stricter
		// constraints (style version / quality); skip them here so a same-key
		// asset that failed the exact filter cannot sneak in as "compatible".
		if verdict.Outcome == OutcomeExactMatch {
			continue
		}
		if !outcomeAllowedByPolicy(verdict.Outcome, q.FallbackPolicy) {
			continue
		}
		// Matrix §2 world-state safety OVERRIDE (deliberate stub; see func
		// doc). Matrix safety itself — invalid_match exclusion above and the
		// outcome/policy gating — is already active; this override is the only
		// deferred part.
		if !passesWorldStateSafetyFilter(q, c, requested, candidate) {
			continue
		}
		qualifying = append(qualifying, scoredCandidate{
			asset:   c,
			outcome: verdict.Outcome,
			score:   verdict.Score,
			reason:  fallbackReason(q.EntityType, requested, candidate, verdict.Outcome),
		})
	}
	if len(qualifying) == 0 {
		return RetrievalResult{}, false
	}
	best := pickBest(qualifying)
	asset := best.asset
	return RetrievalResult{
		MatchType:             best.outcome,
		Asset:                 &asset,
		CompatibilityScore:    best.score,
		FallbackReason:        best.reason,
		GenerationRecommended: true, // a non-exact hit still warrants the real variant
	}, true
}

// bestAnyExisting implements the admin/debug any_existing tail: return the best
// ready, non-anchor candidate even when the matrix approved nothing. The result
// is labelled preview_fallback (provisional) because it is, by definition, not
// a matrix-approved compatible match.
func (rt *Retriever) bestAnyExisting(q RetrievalQuery, requested ClassifiedVariant, candidates []VisualAsset) (RetrievalResult, bool) {
	pool := make([]scoredCandidate, 0, len(candidates))
	for _, c := range candidates {
		if !candidateReusable(c) {
			continue
		}
		candidate := ClassifyVariant(q.EntityType, c.VariantKey)
		verdict := CompareVariants(q.EntityType, requested, candidate)
		if verdict.Outcome == OutcomeExactMatch {
			continue // would have been an exact hit; not "non-exact existing"
		}
		pool = append(pool, scoredCandidate{
			asset:   c,
			outcome: OutcomePreviewFallback,
			score:   verdict.Score, // 0.0 for matrix-invalid candidates
			reason:  "any_existing.debug: best existing candidate (matrix produced no normal match)",
		})
	}
	if len(pool) == 0 {
		return RetrievalResult{}, false
	}
	best := pickBest(pool)
	asset := best.asset
	return RetrievalResult{
		MatchType:             OutcomePreviewFallback,
		Asset:                 &asset,
		CompatibilityScore:    best.score,
		FallbackReason:        best.reason,
		GenerationRecommended: true,
	}, true
}

// pickBest applies the deterministic ordering and returns the winner.
func pickBest(in []scoredCandidate) scoredCandidate {
	sorted := make([]scoredCandidate, len(in))
	copy(sorted, in)
	sort.SliceStable(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		if ta, tb := outcomeTier(a.outcome), outcomeTier(b.outcome); ta != tb {
			return ta > tb // higher tier first
		}
		if a.score != b.score {
			return a.score > b.score // higher score first
		}
		if ra, rb := fallbackRankOf(a.asset), fallbackRankOf(b.asset); ra != rb {
			return ra < rb // lower fallback_rank first
		}
		return a.asset.ID < b.asset.ID // final tie-break: lowest id
	})
	return sorted[0]
}

// candidateReusable enforces the retrieval safety rules that hold regardless
// of the matrix: never a non-ready asset, never an identity anchor as a
// substitute. (The SQL already filters both; this guards the in-memory path
// and documents the invariant at the decision layer.)
func candidateReusable(c VisualAsset) bool {
	if c.Status != "ready" {
		return false
	}
	if c.IsIdentityAnchor {
		return false
	}
	return true
}

func outcomeAllowedByPolicy(outcome, policy string) bool {
	switch policy {
	case FallbackPolicyNone:
		return false // exact only; handled before candidates are loaded
	case FallbackPolicyCompatibleOnly:
		return outcome == OutcomeCompatibleMatch
	case FallbackPolicyPreviewAllowed, FallbackPolicyAnyExisting:
		return outcome == OutcomeCompatibleMatch || outcome == OutcomePreviewFallback
	default:
		return false
	}
}

func outcomeTier(outcome string) int {
	switch outcome {
	case OutcomeExactMatch:
		return 3
	case OutcomeCompatibleMatch:
		return 2
	case OutcomePreviewFallback:
		return 1
	default:
		return 0
	}
}

func fallbackRankOf(a VisualAsset) int32 {
	if a.FallbackRank == nil {
		return fallbackRankNever
	}
	return *a.FallbackRank
}

// fallbackReason names the matrix rule that allowed a substitution, for audit
// and debugging (matrix §10.3). Deterministic and human-readable, e.g.
// "character.warm→warm.compatible_match" or
// "place.time_of_day→establishing.preview_fallback".
func fallbackReason(entityType string, requested, candidate ClassifiedVariant, outcome string) string {
	return fmt.Sprintf("%s.%s→%s.%s", entityType, requested.Family, candidate.Family, outcome)
}

// passesWorldStateSafetyFilter is the matrix §2 product-safety gate: a
// candidate that would visually contradict known world state must be rejected
// even when the matrix says "compatible".
//
// This is the WORLD-STATE-AWARE override specifically, and it is DEFERRED, not a
// silent no-op of the whole safety system. Matrix safety is active and
// conservative today: invalid_match candidates are excluded, and reuse is gated
// by outcome + fallback policy before this function is consulted. Only the
// world-state override is deferred — it depends on world-state hints (scene_mood,
// recent canonical events) that the retrieval call does not yet carry, so for now
// this is a deliberate, documented stub that always returns true. Do not wire new
// generation decisions to it as if it were enforcing world-state contradictions
// yet. Tracked under "Post-7C known residue" in IMPLEMENTATION_STATUS.md.
func passesWorldStateSafetyFilter(_ RetrievalQuery, _ VisualAsset, _ ClassifiedVariant, _ ClassifiedVariant) bool {
	return true
}
