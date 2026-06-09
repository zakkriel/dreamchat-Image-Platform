// Package idempotency owns the Idempotency-Key middleware backed by the
// idempotency_keys table. First-writer-wins via INSERT ... ON CONFLICT DO
// NOTHING; replay reconstructs the original 202 body from the recorded
// generation_job_id.
package idempotency

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zakkriel/drchat-image-platform/internal/db/dbgen"
)

// TTL is the storage retention for an idempotency record. Matches the
// `docs/api/idempotency.md` recommendation for generation requests.
const TTL = 24 * time.Hour

var ErrNotFound = errors.New("idempotency: key not found")

type Record struct {
	ID              string
	TokenID         string
	Key             string
	Endpoint        string
	RequestHash     string
	GenerationJobID *string
	ExpiresAt       time.Time
}

type Repository interface {
	Get(ctx context.Context, tokenID, key string) (Record, error)
	Insert(ctx context.Context, rec Record) (Record, bool, error)
}

type pgRepository struct {
	q *dbgen.Queries
}

func NewRepository(pool *pgxpool.Pool) Repository {
	return &pgRepository{q: dbgen.New(pool)}
}

func (r *pgRepository) Get(ctx context.Context, tokenID, key string) (Record, error) {
	row, err := r.q.GetIdempotencyKey(ctx, dbgen.GetIdempotencyKeyParams{
		TokenID: tokenID,
		Key:     key,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Record{}, ErrNotFound
		}
		return Record{}, err
	}
	return rowToRecord(row), nil
}

// Insert returns (record, inserted, error). When a row with the same
// (token_id, key) already exists, ON CONFLICT DO NOTHING yields no row and
// we fall back to a GET so the caller sees the existing record.
func (r *pgRepository) Insert(ctx context.Context, rec Record) (Record, bool, error) {
	row, err := r.q.InsertIdempotencyKey(ctx, dbgen.InsertIdempotencyKeyParams{
		ID:              rec.ID,
		TokenID:         rec.TokenID,
		Key:             rec.Key,
		Endpoint:        rec.Endpoint,
		RequestHash:     rec.RequestHash,
		GenerationJobID: rec.GenerationJobID,
		ExpiresAt:       pgtype.Timestamptz{Time: rec.ExpiresAt, Valid: true},
	})
	if err == nil {
		return rowToRecord(row), true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Record{}, false, err
	}
	existing, err := r.Get(ctx, rec.TokenID, rec.Key)
	if err != nil {
		return Record{}, false, err
	}
	return existing, false, nil
}

func rowToRecord(row dbgen.IdempotencyKey) Record {
	rec := Record{
		ID:              row.ID,
		TokenID:         row.TokenID,
		Key:             row.Key,
		Endpoint:        row.Endpoint,
		RequestHash:     row.RequestHash,
		GenerationJobID: row.GenerationJobID,
	}
	if row.ExpiresAt.Valid {
		rec.ExpiresAt = row.ExpiresAt.Time
	}
	return rec
}
