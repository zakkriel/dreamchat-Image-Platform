// Package styles owns style profiles: positive/negative prompts, LoRAs,
// reference assets, default quality tier. Phase 2 implements tenant-wide
// CRUD; world-scoped styles land via an explicit contract patch later.
package styles

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zakkriel/drchat-image-platform/internal/db/dbgen"
)

var ErrNotFound = errors.New("styles: style profile not found")

type StyleProfile struct {
	ID                 string
	TenantID           string
	Name               string
	StyleMode          string
	PositivePrompt     string
	NegativePrompt     *string
	DefaultQualityTier string
	Status             string
}

type CreateParams struct {
	ID                 string
	TenantID           string
	Name               string
	StyleMode          string
	PositivePrompt     string
	NegativePrompt     *string
	DefaultQualityTier string
}

type Repository interface {
	ListActiveByTenant(ctx context.Context, tenantID string) ([]StyleProfile, error)
	Create(ctx context.Context, params CreateParams) (StyleProfile, error)
	GetByIDForTenant(ctx context.Context, id, tenantID string) (StyleProfile, error)
}

type pgRepository struct {
	q *dbgen.Queries
}

func NewRepository(pool *pgxpool.Pool) Repository {
	return &pgRepository{q: dbgen.New(pool)}
}

func (r *pgRepository) ListActiveByTenant(ctx context.Context, tenantID string) ([]StyleProfile, error) {
	rows, err := r.q.ListStyleProfilesByTenant(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	out := make([]StyleProfile, 0, len(rows))
	for _, row := range rows {
		out = append(out, fromRow(row))
	}
	return out, nil
}

func (r *pgRepository) Create(ctx context.Context, params CreateParams) (StyleProfile, error) {
	row, err := r.q.CreateStyleProfile(ctx, dbgen.CreateStyleProfileParams{
		ID:                 params.ID,
		TenantID:           params.TenantID,
		Name:               params.Name,
		StyleMode:          params.StyleMode,
		PositivePrompt:     params.PositivePrompt,
		NegativePrompt:     params.NegativePrompt,
		DefaultQualityTier: params.DefaultQualityTier,
	})
	if err != nil {
		return StyleProfile{}, err
	}
	return fromRow(row), nil
}

func (r *pgRepository) GetByIDForTenant(ctx context.Context, id, tenantID string) (StyleProfile, error) {
	row, err := r.q.GetStyleProfileByID(ctx, dbgen.GetStyleProfileByIDParams{
		ID:       id,
		TenantID: tenantID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return StyleProfile{}, ErrNotFound
		}
		return StyleProfile{}, err
	}
	return fromRow(row), nil
}

func fromRow(row dbgen.StyleProfile) StyleProfile {
	return StyleProfile{
		ID:                 row.ID,
		TenantID:           row.TenantID,
		Name:               row.Name,
		StyleMode:          row.StyleMode,
		PositivePrompt:     row.PositivePrompt,
		NegativePrompt:     row.NegativePrompt,
		DefaultQualityTier: row.DefaultQualityTier,
		Status:             row.Status,
	}
}
