package auth

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zakkriel/drchat-image-platform/internal/db/dbgen"
)

var ErrTokenNotFound = errors.New("auth: api token not found")

type Token struct {
	ID          string
	TenantID    string
	TokenHash   string
	Scopes      []string
	Environment string
	Status      string
	ExpiresAt   *time.Time
}

type Repository interface {
	GetActiveAPITokenByPrefix(ctx context.Context, prefix string) (Token, error)
	TouchAPITokenLastUsed(ctx context.Context, id string) error
}

type pgRepository struct {
	q *dbgen.Queries
}

func NewRepository(pool *pgxpool.Pool) Repository {
	return &pgRepository{q: dbgen.New(pool)}
}

func (r *pgRepository) GetActiveAPITokenByPrefix(ctx context.Context, prefix string) (Token, error) {
	row, err := r.q.GetActiveAPITokenByPrefix(ctx, prefix)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Token{}, ErrTokenNotFound
		}
		return Token{}, err
	}
	t := Token{
		ID:          row.ID,
		TenantID:    row.TenantID,
		TokenHash:   row.TokenHash,
		Scopes:      row.Scopes,
		Environment: row.Environment,
		Status:      row.Status,
	}
	if row.ExpiresAt.Valid {
		expires := row.ExpiresAt.Time
		t.ExpiresAt = &expires
	}
	return t, nil
}

func (r *pgRepository) TouchAPITokenLastUsed(ctx context.Context, id string) error {
	return r.q.TouchAPITokenLastUsed(ctx, id)
}
