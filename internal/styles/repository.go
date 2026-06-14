// Package styles owns style profiles: positive/negative prompts, LoRAs,
// reference assets, default quality tier. Phase 2 implements tenant-wide
// CRUD; world-scoped styles land via an explicit contract patch later.
package styles

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	appdb "github.com/zakkriel/drchat-image-platform/internal/db"
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
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) Repository {
	return &pgRepository{pool: pool}
}

// All three methods run inside a tenant executor (Phase 7C-3) so style_profiles
// reads/writes are scoped by app.current_tenant under RLS, in addition to the
// app-level tenant predicates that remain. On a BYPASSRLS/superuser pool the GUC
// is harmless.

func (r *pgRepository) ListActiveByTenant(ctx context.Context, tenantID string) ([]StyleProfile, error) {
	var out []StyleProfile
	err := appdb.WithTenant(ctx, r.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := dbgen.New(tx).ListStyleProfilesByTenant(ctx, tenantID)
		if err != nil {
			return err
		}
		out = make([]StyleProfile, 0, len(rows))
		for _, row := range rows {
			out = append(out, fromRow(row))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (r *pgRepository) Create(ctx context.Context, params CreateParams) (StyleProfile, error) {
	var out StyleProfile
	err := appdb.WithTenant(ctx, r.pool, params.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		row, err := dbgen.New(tx).CreateStyleProfile(ctx, dbgen.CreateStyleProfileParams{
			ID:                 params.ID,
			TenantID:           params.TenantID,
			Name:               params.Name,
			StyleMode:          params.StyleMode,
			PositivePrompt:     params.PositivePrompt,
			NegativePrompt:     params.NegativePrompt,
			DefaultQualityTier: params.DefaultQualityTier,
		})
		if err != nil {
			return err
		}
		out = fromRow(row)
		return nil
	})
	if err != nil {
		return StyleProfile{}, err
	}
	return out, nil
}

func (r *pgRepository) GetByIDForTenant(ctx context.Context, id, tenantID string) (StyleProfile, error) {
	var out StyleProfile
	err := appdb.WithTenant(ctx, r.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		row, err := dbgen.New(tx).GetStyleProfileByID(ctx, dbgen.GetStyleProfileByIDParams{
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
		return StyleProfile{}, err
	}
	return out, nil
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
