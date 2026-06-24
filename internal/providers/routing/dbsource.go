package routing

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zakkriel/drchat-image-platform/internal/db/dbgen"
)

// DBRouteSource is the Postgres-backed RouteSource. It returns the joined
// provider_routes / provider_models rows for an operation; the resolver applies
// the active/availability/tier/capability filtering and tie-break.
type DBRouteSource struct {
	q *dbgen.Queries
}

// NewDBRouteSource builds a route source over the connection pool.
func NewDBRouteSource(pool *pgxpool.Pool) *DBRouteSource {
	return &DBRouteSource{q: dbgen.New(pool)}
}

// ListRoutes loads every route for the operation joined to its model status and
// the active unit price (if any) from provider_model_prices.
func (s *DBRouteSource) ListRoutes(ctx context.Context, operationType string) ([]Route, error) {
	rows, err := s.q.ListProviderRoutesForOperation(ctx, operationType)
	if err != nil {
		return nil, err
	}
	out := make([]Route, 0, len(rows))
	for _, row := range rows {
		rt := Route{
			RouteID:            row.RouteID,
			ProviderID:         row.ProviderID,
			ModelID:            row.ModelID,
			OperationType:      row.OperationType,
			RequiredCapability: row.RequiredCapability,
			PreviewCapability:  row.PreviewCapability,
			QualityTier:        row.QualityTier,
			LatencyTier:        row.LatencyTier,
			Priority:           row.Priority,
			Enabled:            row.IsEnabled,
			ModelActive:        row.ModelStatus == "active",
		}
		// Convert the LEFT-JOIN price to *float64 (nil when no active price row).
		if row.PricePerUnit.Valid && !row.PricePerUnit.NaN {
			if f8, err := row.PricePerUnit.Float64Value(); err == nil && f8.Valid {
				f := f8.Float64
				rt.Price = &f
			}
		}
		out = append(out, rt)
	}
	return out, nil
}
