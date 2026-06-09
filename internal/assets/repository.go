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
	ID                string
	TenantID          string
	WorldID           string
	VisualIdentityID  *string
	AssetType         string
	VariantKey        string
	VariantFamily     *string
	Version           int32
	StateVersion      int32
	Status            string
	CompatibilityTags []string
	FallbackAllowed   bool
	FallbackRank      *int32
	IsIdentityAnchor  bool
	LowResUrl         *string
	HighResUrl        *string
	ThumbnailUrl      *string
	ProviderID        *string
	ModelID           *string
	PromptHash        *string
	Seed              *string
	Metadata          map[string]any
}

type Repository interface {
	GetByIDForTenant(ctx context.Context, id, tenantID string) (VisualAsset, error)
}

type pgRepository struct {
	q *dbgen.Queries
}

func NewRepository(pool *pgxpool.Pool) Repository {
	return &pgRepository{q: dbgen.New(pool)}
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
		ID:                row.ID,
		TenantID:          row.TenantID,
		WorldID:           row.WorldID,
		VisualIdentityID:  row.VisualIdentityID,
		AssetType:         row.AssetType,
		VariantKey:        row.VariantKey,
		VariantFamily:     row.VariantFamily,
		Version:           row.Version,
		StateVersion:      row.StateVersion,
		Status:            row.Status,
		CompatibilityTags: row.CompatibilityTags,
		FallbackAllowed:   row.FallbackAllowed,
		FallbackRank:      row.FallbackRank,
		IsIdentityAnchor:  row.IsIdentityAnchor,
		LowResUrl:         row.LowResUrl,
		HighResUrl:        row.HighResUrl,
		ThumbnailUrl:      row.ThumbnailUrl,
		ProviderID:        row.ProviderID,
		ModelID:           row.ModelID,
		PromptHash:        row.PromptHash,
		Seed:              row.Seed,
		Metadata:          meta,
	}
}
