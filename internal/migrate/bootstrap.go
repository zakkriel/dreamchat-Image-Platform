package migrate

import (
	"database/sql"
	"fmt"
	"strings"
)

// baselineTables are the 20 tables created by baseline migrations 1..11. Their
// presence count (all database-local) is the footprint signal: 0 → fresh DB,
// all → fully migrated, in-between → partial/unknown. Roles created by 0009 are
// intentionally excluded — they are cluster-global and would misclassify a
// fresh database on a cluster where the roles already exist.
var baselineTables = []string{
	"api_tokens", "asset_pack_items", "asset_packs", "audit_events",
	"cost_budgets", "cost_reservation_budget_holds", "cost_reservations",
	"generation_cost_events", "generation_jobs", "idempotency_keys",
	"provider_attempts", "provider_model_prices", "provider_models",
	"provider_routes", "style_profiles", "visual_assets", "visual_identities",
	"visual_identity_versions", "webhook_deliveries", "webhook_endpoints",
}

// Bootstrap converges any database onto the goose version table without
// destructive re-application. See docs/adr/ADR-P001-migration-tooling.md and the
// design spec §4.
//
//   - goose_db_version already present  -> delegate to Up (apply pending).
//   - zero baseline tables              -> fresh DB, apply everything via Up.
//   - full footprint (20 tables + 0011  -> stamp versions 1..BaselineVersion as
//     fal seed)                            applied without running them, then Up.
//   - anything in between               -> REFUSE; stamp nothing.
func Bootstrap(db *sql.DB) error {
	tracked, err := tableExists(db, "goose_db_version")
	if err != nil {
		return fmt.Errorf("probe version table: %w", err)
	}
	if tracked {
		return Up(db)
	}

	present, err := countBaselineTables(db)
	if err != nil {
		return fmt.Errorf("probe baseline footprint: %w", err)
	}

	switch {
	case present == 0:
		// Fresh database: apply everything normally.
		return Up(db)
	case present == len(baselineTables):
		seed, err := falSeedPresent(db)
		if err != nil {
			return fmt.Errorf("probe fal seed: %w", err)
		}
		if !seed {
			return fmt.Errorf(
				"bootstrap refused: all %d baseline tables present but the 0011 fal seed is missing — "+
					"database is at an incomplete/unknown state; a human must resolve it (nothing stamped)",
				len(baselineTables))
		}
		if err := stampBaseline(db); err != nil {
			return err
		}
		// Apply any post-baseline (Chunk 1+) migrations.
		return Up(db)
	default:
		return fmt.Errorf(
			"bootstrap refused: %d of %d baseline tables present — database is partially migrated to an "+
				"unknown state; a human must resolve it (nothing stamped)",
			present, len(baselineTables))
	}
}

// stampBaseline marks versions 0..BaselineVersion applied WITHOUT running them,
// so a subsequent Up is a no-op for the baseline. It writes goose's own version
// table schema explicitly (stable across goose v3), which is deterministic and
// does not depend on goose internals.
func stampBaseline(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS goose_db_version (
		id serial NOT NULL,
		version_id bigint NOT NULL,
		is_applied boolean NOT NULL,
		tstamp timestamp NULL DEFAULT now(),
		PRIMARY KEY(id)
	)`); err != nil {
		return fmt.Errorf("create version table: %w", err)
	}
	for v := int64(0); v <= BaselineVersion; v++ {
		if _, err := db.Exec(
			`INSERT INTO goose_db_version (version_id, is_applied) VALUES ($1, true)`, v,
		); err != nil {
			return fmt.Errorf("stamp version %d: %w", v, err)
		}
	}
	return nil
}

func tableExists(db *sql.DB, name string) (bool, error) {
	var n int
	err := db.QueryRow(
		`SELECT count(*) FROM information_schema.tables
		 WHERE table_schema='public' AND table_name=$1`, name).Scan(&n)
	return n > 0, err
}

func countBaselineTables(db *sql.DB) (int, error) {
	placeholders := make([]string, len(baselineTables))
	args := make([]any, len(baselineTables))
	for i, name := range baselineTables {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
		args[i] = name
	}
	q := `SELECT count(*) FROM information_schema.tables
	      WHERE table_schema='public' AND table_name IN (` + strings.Join(placeholders, ",") + `)`
	var n int
	err := db.QueryRow(q, args...).Scan(&n)
	return n, err
}

// falSeedPresent reports whether migration 0011's seed ran. 0011 adds no table,
// so the table footprint alone cannot distinguish version 10 from 11.
func falSeedPresent(db *sql.DB) (bool, error) {
	var n int
	err := db.QueryRow(
		`SELECT count(*) FROM provider_models WHERE id='pm_fal_flux_kontext_multi'`).Scan(&n)
	return n > 0, err
}
