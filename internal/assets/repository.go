// Package assets owns visual asset metadata, search, retrieval, lifecycle,
// versioning, and variant classification. Phase 2 only exposes single-asset
// reads scoped by tenant. Retrieval matrix and search land in later phases.
package assets

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zakkriel/drchat-image-platform/internal/db/dbgen"
)

var ErrNotFound = errors.New("assets: visual asset not found")

type VisualAsset struct {
	ID                  string
	TenantID            string
	WorldID             string
	VisualIdentityID    *string
	AssetType           string
	VariantKey          string
	VariantFamily       *string
	Version             int32
	StateVersion        int32
	StyleProfileID      *string
	StyleProfileVersion *int32
	QualityTier         string
	Status              string
	CompatibilityTags   []string
	FallbackAllowed     bool
	FallbackRank        *int32
	IsIdentityAnchor    bool
	LowResUrl           *string
	HighResUrl          *string
	ThumbnailUrl        *string
	ProviderID          *string
	ModelID             *string
	PromptHash          *string
	Seed                *string
	Metadata            map[string]any
}

// InsertParams captures what the worker writes when a generation succeeds.
// The compatibility fields (Phase 5B) carry the deterministic variant
// classification; the single-artifact path leaves them at safe defaults.
type InsertParams struct {
	ID                  string
	TenantID            string
	WorldID             string
	VisualIdentityID    *string
	AssetType           string
	VariantKey          string
	VariantFamily       *string
	CompatibilityTags   []string
	FallbackAllowed     bool
	FallbackRank        *int32
	Metadata            map[string]any
	StyleProfileID      *string
	StyleProfileVersion *int32
	QualityTier         string
	LowResUrl           *string
	HighResUrl          *string
	ThumbnailUrl        *string
	ProviderID          *string
	ModelID             *string
	PromptHash          *string
	Seed                *string
	GenerationJobID     *string
}

// ArtifactLookup is the narrow exact-reuse query for the single-artifact
// generation path (Phase 6A2). Artifacts have no visual identity / variant /
// state in the generation path, so the lookup is keyed on owner (tenant/world)
// + style + quality + the deterministic artifact render hash (stored in
// prompt_hash). StyleProfileVersion is optional and only narrows when set.
type ArtifactLookup struct {
	TenantID            string
	WorldID             string
	StyleProfileID      string
	StyleProfileVersion *int32
	QualityTier         string
	PromptHash          string
}

type Repository interface {
	GetByIDForTenant(ctx context.Context, id, tenantID string) (VisualAsset, error)
	Insert(ctx context.Context, params InsertParams) (VisualAsset, error)

	// FindReadyArtifactByPromptHash returns the single reusable (status =
	// 'ready') artifact asset whose owner + style + quality + render hash match
	// the lookup exactly, or ErrNotFound. This is the SQL half of Phase 6A2
	// single-artifact exact reuse; it deliberately uses no matrix/compatibility
	// logic (artifacts reuse on exact hash only).
	FindReadyArtifactByPromptHash(ctx context.Context, q ArtifactLookup) (VisualAsset, error)

	// FindExact returns the single reusable (status = 'ready') asset that
	// matches the query's owner + variant + state + style exactly, or
	// ErrNotFound. It is the SQL half of RetrievalResult.exact_match; the
	// matrix decision lives in the retrieval layer, not here.
	FindExact(ctx context.Context, q RetrievalQuery) (VisualAsset, error)
	// ListRetrievalCandidates returns ready, non-anchor assets for the same
	// owner / visual identity / state / style that the compatibility matrix
	// may approve as a substitute. The repository performs no matrix logic.
	ListRetrievalCandidates(ctx context.Context, q RetrievalQuery) ([]VisualAsset, error)
	// ListRetrievalCandidatesByCompatTag narrows ListRetrievalCandidates to
	// assets whose compatibility_tags overlap the supplied set (GIN-indexed).
	ListRetrievalCandidatesByCompatTag(ctx context.Context, q RetrievalQuery, tags []string) ([]VisualAsset, error)
}

type pgRepository struct {
	q *dbgen.Queries
}

func NewRepository(pool *pgxpool.Pool) Repository {
	return &pgRepository{q: dbgen.New(pool)}
}

func (r *pgRepository) Insert(ctx context.Context, params InsertParams) (VisualAsset, error) {
	return InsertWithQueries(ctx, r.q, params)
}

// InsertWithQueries runs the visual_assets insert against the supplied
// queries object, so callers that need the insert inside their own
// transaction (e.g. the pack worker's atomic asset + pack-item write) can
// pass dbgen.New(tx) without duplicating the column mapping.
func InsertWithQueries(ctx context.Context, q *dbgen.Queries, params InsertParams) (VisualAsset, error) {
	// compatibility_tags and metadata are NOT NULL DEFAULT '{}' in the schema;
	// an explicit insert must therefore supply non-null values.
	tags := params.CompatibilityTags
	if tags == nil {
		tags = []string{}
	}
	metadata, err := marshalMetadata(params.Metadata)
	if err != nil {
		return VisualAsset{}, err
	}
	row, err := q.InsertVisualAsset(ctx, dbgen.InsertVisualAssetParams{
		ID:                  params.ID,
		TenantID:            params.TenantID,
		WorldID:             params.WorldID,
		VisualIdentityID:    params.VisualIdentityID,
		AssetType:           params.AssetType,
		VariantKey:          params.VariantKey,
		VariantFamily:       params.VariantFamily,
		CompatibilityTags:   tags,
		FallbackAllowed:     params.FallbackAllowed,
		FallbackRank:        params.FallbackRank,
		StyleProfileID:      params.StyleProfileID,
		StyleProfileVersion: params.StyleProfileVersion,
		QualityTier:         params.QualityTier,
		LowResUrl:           params.LowResUrl,
		HighResUrl:          params.HighResUrl,
		ThumbnailUrl:        params.ThumbnailUrl,
		ProviderID:          params.ProviderID,
		ModelID:             params.ModelID,
		PromptHash:          params.PromptHash,
		Seed:                params.Seed,
		GenerationJobID:     params.GenerationJobID,
		Metadata:            metadata,
	})
	if err != nil {
		return VisualAsset{}, err
	}
	return fromRow(row), nil
}

// marshalMetadata serializes the asset metadata map, defaulting an empty/nil
// map to the JSON object literal so the NOT NULL JSONB column is satisfied.
func marshalMetadata(meta map[string]any) ([]byte, error) {
	if len(meta) == 0 {
		return []byte("{}"), nil
	}
	return json.Marshal(meta)
}

func (r *pgRepository) FindExact(ctx context.Context, q RetrievalQuery) (VisualAsset, error) {
	row, err := r.q.FindExactVisualAsset(ctx, dbgen.FindExactVisualAssetParams{
		TenantID:            q.TenantID,
		WorldID:             q.WorldID,
		VisualIdentityID:    strPtr(q.VisualIdentityID),
		VariantKey:          q.VariantKey,
		StateVersion:        q.StateVersion,
		StyleProfileID:      strPtr(q.StyleProfileID),
		StyleProfileVersion: q.StyleProfileVersion,
		QualityTier:         strPtr(q.QualityTier),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return VisualAsset{}, ErrNotFound
		}
		return VisualAsset{}, err
	}
	return fromRow(row), nil
}

func (r *pgRepository) FindReadyArtifactByPromptHash(ctx context.Context, q ArtifactLookup) (VisualAsset, error) {
	row, err := r.q.FindReadyArtifactByPromptHash(ctx, dbgen.FindReadyArtifactByPromptHashParams{
		TenantID:            q.TenantID,
		WorldID:             q.WorldID,
		StyleProfileID:      strPtr(q.StyleProfileID),
		QualityTier:         q.QualityTier,
		PromptHash:          strPtr(q.PromptHash),
		StyleProfileVersion: q.StyleProfileVersion,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return VisualAsset{}, ErrNotFound
		}
		return VisualAsset{}, err
	}
	return fromRow(row), nil
}

func (r *pgRepository) ListRetrievalCandidates(ctx context.Context, q RetrievalQuery) ([]VisualAsset, error) {
	rows, err := r.q.ListVisualAssetCandidates(ctx, dbgen.ListVisualAssetCandidatesParams{
		TenantID:         q.TenantID,
		WorldID:          q.WorldID,
		VisualIdentityID: strPtr(q.VisualIdentityID),
		StateVersion:     q.StateVersion,
		StyleProfileID:   strPtr(q.StyleProfileID),
	})
	if err != nil {
		return nil, err
	}
	return fromRows(rows), nil
}

func (r *pgRepository) ListRetrievalCandidatesByCompatTag(ctx context.Context, q RetrievalQuery, tags []string) ([]VisualAsset, error) {
	if tags == nil {
		tags = []string{}
	}
	rows, err := r.q.ListVisualAssetCandidatesByCompatTag(ctx, dbgen.ListVisualAssetCandidatesByCompatTagParams{
		TenantID:          q.TenantID,
		WorldID:           q.WorldID,
		VisualIdentityID:  strPtr(q.VisualIdentityID),
		StateVersion:      q.StateVersion,
		StyleProfileID:    strPtr(q.StyleProfileID),
		CompatibilityTags: tags,
	})
	if err != nil {
		return nil, err
	}
	return fromRows(rows), nil
}

// strPtr returns nil for the empty string so an unset RetrievalQuery field maps
// to a SQL NULL parameter rather than matching rows with an empty value.
func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func fromRows(rows []dbgen.VisualAsset) []VisualAsset {
	out := make([]VisualAsset, 0, len(rows))
	for _, row := range rows {
		out = append(out, fromRow(row))
	}
	return out
}

func (r *pgRepository) GetByIDForTenant(ctx context.Context, id, tenantID string) (VisualAsset, error) {
	row, err := r.q.GetVisualAssetByID(ctx, dbgen.GetVisualAssetByIDParams{
		ID:       id,
		TenantID: tenantID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return VisualAsset{}, ErrNotFound
		}
		return VisualAsset{}, err
	}
	return fromRow(row), nil
}

func fromRow(row dbgen.VisualAsset) VisualAsset {
	meta := map[string]any{}
	if len(row.Metadata) > 0 {
		_ = json.Unmarshal(row.Metadata, &meta)
	}
	return VisualAsset{
		ID:                  row.ID,
		TenantID:            row.TenantID,
		WorldID:             row.WorldID,
		VisualIdentityID:    row.VisualIdentityID,
		AssetType:           row.AssetType,
		VariantKey:          row.VariantKey,
		VariantFamily:       row.VariantFamily,
		Version:             row.Version,
		StateVersion:        row.StateVersion,
		StyleProfileID:      row.StyleProfileID,
		StyleProfileVersion: row.StyleProfileVersion,
		QualityTier:         row.QualityTier,
		Status:              row.Status,
		CompatibilityTags:   row.CompatibilityTags,
		FallbackAllowed:     row.FallbackAllowed,
		FallbackRank:        row.FallbackRank,
		IsIdentityAnchor:    row.IsIdentityAnchor,
		LowResUrl:           row.LowResUrl,
		HighResUrl:          row.HighResUrl,
		ThumbnailUrl:        row.ThumbnailUrl,
		ProviderID:          row.ProviderID,
		ModelID:             row.ModelID,
		PromptHash:          row.PromptHash,
		Seed:                row.Seed,
		Metadata:            meta,
	}
}
