// Package migrations embeds the ordered SQL migration files so the
// cmd/migrate runner is a self-contained binary. Embedding lets migrations be
// applied on Railway (or any host) straight from the built image, without a
// repo checkout, Docker Compose, or a local psql.
//
// Only the *.up.sql files are embedded. They are applied in filename order, so
// the zero-padded numeric prefixes (0001_, 0002_, ...) define apply order.
package migrations

import "embed"

// FS holds every up-migration in filename order.
//
//go:embed 0*.up.sql
var FS embed.FS
