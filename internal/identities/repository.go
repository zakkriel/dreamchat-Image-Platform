// Package identities owns visual identity records — canonical traits,
// consistency keys, anchor assets, and owner binding. The upsert flow runs
// inside a single transaction so version bumps and version-history inserts
// stay atomic.
package identities

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	appdb "github.com/zakkriel/drchat-image-platform/internal/db"
	"github.com/zakkriel/drchat-image-platform/internal/db/dbgen"
)

var (
	ErrNotFound       = errors.New("identities: visual identity not found")
	ErrInvalidStyle   = errors.New("identities: style profile invalid for tenant")
	ErrInvalidTraits  = errors.New("identities: canonical_visual_traits must be a JSON object")
	ErrInvalidVersion = errors.New("identities: invalid version reason")
)

type VisualIdentity struct {
	ID                    string
	TenantID              string
	WorldID               string
	OwnerType             string
	OwnerID               string
	DisplayName           string
	CanonicalVisualTraits map[string]any
	StyleProfileID        string
	ConsistencyKey        *string
	AnchorAssetIds        []string
	CurrentVersion        int32
	Status                string
}

type UpsertParams struct {
	NewID                 string
	TenantID              string
	WorldID               string
	OwnerType             string
	OwnerID               string
	DisplayName           string
	CanonicalVisualTraits map[string]any
	StyleProfileID        string
	ConsistencyKey        *string
}

// IDGenerator is supplied by the handler so the repository never depends on
// the ids package directly. The function only fires when an insert is needed.
type IDGenerator func() string

type Repository interface {
	Upsert(ctx context.Context, params UpsertParams) (VisualIdentity, error)
	GetByOwner(ctx context.Context, tenantID, worldID, ownerType, ownerID string) (VisualIdentity, error)
	GetByIDForTenant(ctx context.Context, id, tenantID string) (VisualIdentity, error)
}

type pgRepository struct {
	pool *pgxpool.Pool
	// styleCheck validates that the referenced style profile belongs to the
	// tenant and is active. It runs inside the upsert transaction, so it must
	// accept a queries object backed by the transaction.
	styleCheck func(ctx context.Context, q *dbgen.Queries, styleID, tenantID string) error
}

func NewRepository(pool *pgxpool.Pool) Repository {
	return &pgRepository{
		pool:       pool,
		styleCheck: defaultStyleCheck,
	}
}

func defaultStyleCheck(ctx context.Context, q *dbgen.Queries, styleID, tenantID string) error {
	_, err := q.GetStyleProfileByID(ctx, dbgen.GetStyleProfileByIDParams{
		ID:       styleID,
		TenantID: tenantID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrInvalidStyle
		}
		return err
	}
	return nil
}

func (r *pgRepository) Upsert(ctx context.Context, params UpsertParams) (VisualIdentity, error) {
	traitsJSON, err := marshalTraits(params.CanonicalVisualTraits)
	if err != nil {
		return VisualIdentity{}, err
	}

	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return VisualIdentity{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Phase 7C-3: this service-owned upsert transaction writes tenant-owned
	// visual_identities and (child) visual_identity_versions rows, so scope it to
	// the tenant for RLS before any query.
	if err := appdb.SetTenantLocal(ctx, tx, params.TenantID); err != nil {
		return VisualIdentity{}, err
	}

	q := dbgen.New(tx)

	if err := r.styleCheck(ctx, q, params.StyleProfileID, params.TenantID); err != nil {
		return VisualIdentity{}, err
	}

	existing, err := q.GetVisualIdentityByOwnerForUpdate(ctx, dbgen.GetVisualIdentityByOwnerForUpdateParams{
		TenantID:  params.TenantID,
		WorldID:   params.WorldID,
		OwnerType: params.OwnerType,
		OwnerID:   params.OwnerID,
	})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		row, err := q.InsertVisualIdentity(ctx, dbgen.InsertVisualIdentityParams{
			ID:                    params.NewID,
			TenantID:              params.TenantID,
			WorldID:               params.WorldID,
			OwnerType:             params.OwnerType,
			OwnerID:               params.OwnerID,
			DisplayName:           params.DisplayName,
			CanonicalVisualTraits: traitsJSON,
			StyleProfileID:        params.StyleProfileID,
			ConsistencyKey:        params.ConsistencyKey,
		})
		if err != nil {
			return VisualIdentity{}, err
		}
		if err := q.InsertVisualIdentityVersion(ctx, dbgen.InsertVisualIdentityVersionParams{
			VisualIdentityID:        row.ID,
			Version:                 1,
			Reason:                  "initial",
			CanonicalTraitsSnapshot: traitsJSON,
		}); err != nil {
			return VisualIdentity{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return VisualIdentity{}, err
		}
		return rowToDomain(row), nil
	case err != nil:
		return VisualIdentity{}, err
	}

	if identityUnchanged(existing, params, traitsJSON) {
		if err := tx.Commit(ctx); err != nil {
			return VisualIdentity{}, err
		}
		return rowToDomain(existing), nil
	}

	row, err := q.UpdateVisualIdentityWithVersionBump(ctx, dbgen.UpdateVisualIdentityWithVersionBumpParams{
		ID:                    existing.ID,
		DisplayName:           params.DisplayName,
		CanonicalVisualTraits: traitsJSON,
		StyleProfileID:        params.StyleProfileID,
		ConsistencyKey:        params.ConsistencyKey,
	})
	if err != nil {
		return VisualIdentity{}, err
	}
	if err := q.InsertVisualIdentityVersion(ctx, dbgen.InsertVisualIdentityVersionParams{
		VisualIdentityID:        row.ID,
		Version:                 row.CurrentVersion,
		Reason:                  "canonical_change",
		CanonicalTraitsSnapshot: traitsJSON,
	}); err != nil {
		return VisualIdentity{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return VisualIdentity{}, err
	}
	return rowToDomain(row), nil
}

func (r *pgRepository) GetByOwner(ctx context.Context, tenantID, worldID, ownerType, ownerID string) (VisualIdentity, error) {
	var out VisualIdentity
	err := appdb.WithTenant(ctx, r.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		row, err := dbgen.New(tx).GetVisualIdentityByOwner(ctx, dbgen.GetVisualIdentityByOwnerParams{
			TenantID:  tenantID,
			WorldID:   worldID,
			OwnerType: ownerType,
			OwnerID:   ownerID,
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		out = rowToDomain(row)
		return nil
	})
	if err != nil {
		return VisualIdentity{}, err
	}
	return out, nil
}

func (r *pgRepository) GetByIDForTenant(ctx context.Context, id, tenantID string) (VisualIdentity, error) {
	var out VisualIdentity
	err := appdb.WithTenant(ctx, r.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		row, err := dbgen.New(tx).GetVisualIdentityByID(ctx, dbgen.GetVisualIdentityByIDParams{
			ID:       id,
			TenantID: tenantID,
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		out = rowToDomain(row)
		return nil
	})
	if err != nil {
		return VisualIdentity{}, err
	}
	return out, nil
}

func identityUnchanged(existing dbgen.VisualIdentity, params UpsertParams, traitsJSON []byte) bool {
	if existing.StyleProfileID != params.StyleProfileID {
		return false
	}
	if !ptrEqual(existing.ConsistencyKey, params.ConsistencyKey) {
		return false
	}
	return jsonEqual(existing.CanonicalVisualTraits, traitsJSON)
}

func ptrEqual(a, b *string) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

func jsonEqual(a, b []byte) bool {
	var av, bv any
	if err := json.Unmarshal(a, &av); err != nil {
		return bytes.Equal(a, b)
	}
	if err := json.Unmarshal(b, &bv); err != nil {
		return bytes.Equal(a, b)
	}
	an, err := json.Marshal(av)
	if err != nil {
		return false
	}
	bn, err := json.Marshal(bv)
	if err != nil {
		return false
	}
	return bytes.Equal(an, bn)
}

func marshalTraits(m map[string]any) ([]byte, error) {
	if m == nil {
		m = map[string]any{}
	}
	return json.Marshal(m)
}

func rowToDomain(row dbgen.VisualIdentity) VisualIdentity {
	traits := map[string]any{}
	if len(row.CanonicalVisualTraits) > 0 {
		_ = json.Unmarshal(row.CanonicalVisualTraits, &traits)
	}
	return VisualIdentity{
		ID:                    row.ID,
		TenantID:              row.TenantID,
		WorldID:               row.WorldID,
		OwnerType:             row.OwnerType,
		OwnerID:               row.OwnerID,
		DisplayName:           row.DisplayName,
		CanonicalVisualTraits: traits,
		StyleProfileID:        row.StyleProfileID,
		ConsistencyKey:        row.ConsistencyKey,
		AnchorAssetIds:        row.AnchorAssetIds,
		CurrentVersion:        row.CurrentVersion,
		Status:                row.Status,
	}
}
