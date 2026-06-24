// Package audit emits rows to the audit_events table. It is a thin, shared
// wrapper over the sqlc InsertAuditEvent query so any service can record a
// security-relevant event without duplicating the marshal+insert. Event types
// follow the dotted <domain>.<resource>.<action> convention.
package audit

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/zakkriel/drchat-image-platform/internal/db/dbgen"
	"github.com/zakkriel/drchat-image-platform/internal/ids"
)

// Event is one audit row. Optional string fields ("" → SQL NULL): TenantID,
// ActorTokenID, ResourceType, ResourceID. Metadata is marshalled to JSONB.
type Event struct {
	EventType    string
	TenantID     string
	ActorTokenID string
	ResourceType string
	ResourceID   string
	Metadata     map[string]any
}

// Emit inserts the audit event using the supplied queries handle (built on a
// pool or tx). Audit rows are tenant-scoped (RLS), so q must run under the
// correct tenant context (or the system/bypass role).
func Emit(ctx context.Context, q *dbgen.Queries, ev Event) error {
	meta := ev.Metadata
	if meta == nil {
		meta = map[string]any{}
	}
	raw, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("audit: marshal metadata: %w", err)
	}
	return q.InsertAuditEvent(ctx, dbgen.InsertAuditEventParams{
		ID:           ids.NewAuditEventID(),
		EventType:    ev.EventType,
		TenantID:     strPtr(ev.TenantID),
		ActorTokenID: strPtr(ev.ActorTokenID),
		ResourceType: strPtr(ev.ResourceType),
		ResourceID:   strPtr(ev.ResourceID),
		Metadata:     raw,
	})
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
