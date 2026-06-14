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

	appdb "github.com/zakkriel/drchat-image-platform/internal/db"
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
	SupersededByAssetID *string
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
	ProviderRouteID     *string
	PromptHash          *string
	Seed                *string
	GenerationJobID     *string
	// Version is the per-asset version written to visual_assets.version. Zero
	// means "use the default" (1) — the normal generate path leaves it 0. A
	// forced regeneration (Phase 6A4) sets it to prior_max_version + 1 so
	// versions stay monotonic across regenerations of a slot.
	Version int32
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

	// InsertPreview is the Phase 7B two-phase preview-tier write: identical to
	// Insert but the new asset lands status='preview_ready' (not 'ready'). It is
	// the lighter, earlier output of a preview_first job — committed and readable
	// before final generation runs — and is never a reuse target (the artifact
	// reuse lookup matches status='ready' only).
	InsertPreview(ctx context.Context, params InsertParams) (VisualAsset, error)

	// SupersedeAndInsertArtifact is the Phase 6A4 forced-regeneration artifact
	// write: in one transaction, under a slot advisory lock, it inserts the new
	// asset as the single ready row for the artifact slot (version =
	// prior_max_version + 1) and archives every prior ready row of the exact same
	// slot, linking each forward to the new asset. Committed readers therefore
	// flip atomically from the old ready row to the regenerated one — never
	// observing zero or multiple ready rows.
	SupersedeAndInsertArtifact(ctx context.Context, params InsertParams, slot ArtifactSlot) (VisualAsset, error)

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
	q    *dbgen.Queries
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) Repository {
	return &pgRepository{q: dbgen.New(pool), pool: pool}
}

func (r *pgRepository) Insert(ctx context.Context, params InsertParams) (VisualAsset, error) {
	return InsertWithQueries(ctx, r.q, params)
}

func (r *pgRepository) InsertPreview(ctx context.Context, params InsertParams) (VisualAsset, error) {
	return InsertPreviewWithQueries(ctx, r.q, params)
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
	// version defaults to 1 (the prior schema DEFAULT) when the caller leaves it
	// unset; a forced regeneration supplies prior_max_version + 1.
	version := params.Version
	if version == 0 {
		version = 1
	}
	row, err := q.InsertVisualAsset(ctx, dbgen.InsertVisualAssetParams{
		ID:                  params.ID,
		TenantID:            params.TenantID,
		WorldID:             params.WorldID,
		Version:             version,
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
		ProviderRouteID:     params.ProviderRouteID,
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

// InsertPreviewWithQueries is the Phase 7B preview-tier analogue of
// InsertWithQueries: same column mapping, but it routes to InsertPreviewVisualAsset
// so the row lands status='preview_ready'. Exposed for callers that need the
// insert inside their own transaction.
func InsertPreviewWithQueries(ctx context.Context, q *dbgen.Queries, params InsertParams) (VisualAsset, error) {
	tags := params.CompatibilityTags
	if tags == nil {
		tags = []string{}
	}
	metadata, err := marshalMetadata(params.Metadata)
	if err != nil {
		return VisualAsset{}, err
	}
	version := params.Version
	if version == 0 {
		version = 1
	}
	row, err := q.InsertPreviewVisualAsset(ctx, dbgen.InsertPreviewVisualAssetParams{
		ID:                  params.ID,
		TenantID:            params.TenantID,
		WorldID:             params.WorldID,
		Version:             version,
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
		ProviderRouteID:     params.ProviderRouteID,
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

// The retrieval/reuse reads below are request-path only (handlers, via the
// reuse + retriever interfaces). Each runs inside a tenant executor (Phase
// 7C-3) so visual_assets reads are scoped by app.current_tenant under RLS, on
// top of the existing tenant predicate. The query structs always carry TenantID.

func (r *pgRepository) FindExact(ctx context.Context, q RetrievalQuery) (VisualAsset, error) {
	var out VisualAsset
	err := appdb.WithTenant(ctx, r.pool, q.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		row, err := dbgen.New(tx).FindExactVisualAsset(ctx, dbgen.FindExactVisualAssetParams{
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
				return ErrNotFound
			}
			return err
		}
		out = fromRow(row)
		return nil
	})
	if err != nil {
		return VisualAsset{}, err
	}
	return out, nil
}

func (r *pgRepository) FindReadyArtifactByPromptHash(ctx context.Context, q ArtifactLookup) (VisualAsset, error) {
	var out VisualAsset
	err := appdb.WithTenant(ctx, r.pool, q.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		row, err := dbgen.New(tx).FindReadyArtifactByPromptHash(ctx, dbgen.FindReadyArtifactByPromptHashParams{
			TenantID:            q.TenantID,
			WorldID:             q.WorldID,
			StyleProfileID:      strPtr(q.StyleProfileID),
			QualityTier:         q.QualityTier,
			PromptHash:          strPtr(q.PromptHash),
			StyleProfileVersion: q.StyleProfileVersion,
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		out = fromRow(row)
		return nil
	})
	if err != nil {
		return VisualAsset{}, err
	}
	return out, nil
}

func (r *pgRepository) ListRetrievalCandidates(ctx context.Context, q RetrievalQuery) ([]VisualAsset, error) {
	var out []VisualAsset
	err := appdb.WithTenant(ctx, r.pool, q.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := dbgen.New(tx).ListVisualAssetCandidates(ctx, dbgen.ListVisualAssetCandidatesParams{
			TenantID:         q.TenantID,
			WorldID:          q.WorldID,
			VisualIdentityID: strPtr(q.VisualIdentityID),
			StateVersion:     q.StateVersion,
			StyleProfileID:   strPtr(q.StyleProfileID),
		})
		if err != nil {
			return err
		}
		out = fromRows(rows)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (r *pgRepository) ListRetrievalCandidatesByCompatTag(ctx context.Context, q RetrievalQuery, tags []string) ([]VisualAsset, error) {
	if tags == nil {
		tags = []string{}
	}
	var out []VisualAsset
	err := appdb.WithTenant(ctx, r.pool, q.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := dbgen.New(tx).ListVisualAssetCandidatesByCompatTag(ctx, dbgen.ListVisualAssetCandidatesByCompatTagParams{
			TenantID:          q.TenantID,
			WorldID:           q.WorldID,
			VisualIdentityID:  strPtr(q.VisualIdentityID),
			StateVersion:      q.StateVersion,
			StyleProfileID:    strPtr(q.StyleProfileID),
			CompatibilityTags: tags,
		})
		if err != nil {
			return err
		}
		out = fromRows(rows)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
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
	var out VisualAsset
	err := appdb.WithTenant(ctx, r.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		row, err := dbgen.New(tx).GetVisualAssetByID(ctx, dbgen.GetVisualAssetByIDParams{
			ID:       id,
			TenantID: tenantID,
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		out = fromRow(row)
		return nil
	})
	if err != nil {
		return VisualAsset{}, err
	}
	return out, nil
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
		SupersededByAssetID: row.SupersededByAssetID,
	}
}
