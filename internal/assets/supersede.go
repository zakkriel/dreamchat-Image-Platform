package assets

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/zakkriel/drchat-image-platform/internal/db/dbgen"
)

// ArtifactSlot identifies the exact artifact reuse slot a forced regeneration
// supersedes (Phase 6A4). It is the FindReadyArtifactByPromptHash predicate:
// tenant + world + asset_type='artifact' + variant_key='default' + style +
// quality + the deterministic render hash. Supersede is slot-scoped and exact —
// never matrix-based — so archiving the predecessor uses exactly this predicate
// and can never touch a compatible/preview neighbor.
type ArtifactSlot struct {
	TenantID       string
	WorldID        string
	StyleProfileID string
	QualityTier    string
	PromptHash     string
}

// VariantSlot identifies the exact pack-role reuse slot a forced regeneration
// supersedes (Phase 6A4). It is the FindExactVisualAsset predicate: tenant +
// world + visual identity + variant + state + style + quality. Like the artifact
// slot it is exact, never matrix-based.
type VariantSlot struct {
	TenantID         string
	WorldID          string
	VisualIdentityID string
	VariantKey       string
	StateVersion     int32
	StyleProfileID   string
	QualityTier      string
}

// slotKeySep is an in-band separator unlikely to appear in an id/hash/key. The
// slot-key strings only feed a Postgres advisory-lock hash, so the requirement
// is determinism + low collision risk, not reversibility.
const slotKeySep = "\x1f"

// LockKey is the deterministic advisory-lock key for an artifact slot. Two
// forced regenerations of the same slot derive the same key and therefore
// serialize on pg_advisory_xact_lock.
func (s ArtifactSlot) LockKey() string {
	return strings.Join([]string{
		"artifact", s.TenantID, s.WorldID, "default",
		s.StyleProfileID, s.QualityTier, s.PromptHash,
	}, slotKeySep)
}

// LockKey is the deterministic advisory-lock key for a pack-role slot.
func (s VariantSlot) LockKey() string {
	return strings.Join([]string{
		"packrole", s.TenantID, s.WorldID, s.VisualIdentityID,
		s.VariantKey, itoa(s.StateVersion), s.StyleProfileID, s.QualityTier,
	}, slotKeySep)
}

// itoa keeps the slot-key builder dependency-free for a single int32.
func itoa(n int32) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	var buf [12]byte
	i := len(buf)
	v := int64(n)
	if neg {
		v = -v
	}
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// supersedeArtifactSlot runs the Phase 6A4 supersede + insert for an artifact
// slot inside an already-open transaction's queries object: lock the slot,
// compute the next version, insert the new ready asset, then archive every prior
// ready row of the slot and link it forward to the new asset. Insert precedes
// archive because superseded_by_asset_id FKs visual_assets(id) — the new row
// must exist before predecessors can point at it. The whole thing runs under the
// slot lock so concurrent regenerations serialize and versions never collide.
func supersedeArtifactSlot(ctx context.Context, q *dbgen.Queries, params InsertParams, slot ArtifactSlot) (VisualAsset, error) {
	if err := q.AcquireSupersedeLock(ctx, slot.LockKey()); err != nil {
		return VisualAsset{}, err
	}
	maxVersion, err := q.MaxVersionForArtifactSlot(ctx, dbgen.MaxVersionForArtifactSlotParams{
		TenantID:       slot.TenantID,
		WorldID:        slot.WorldID,
		StyleProfileID: strPtr(slot.StyleProfileID),
		QualityTier:    slot.QualityTier,
		PromptHash:     strPtr(slot.PromptHash),
	})
	if err != nil {
		return VisualAsset{}, err
	}
	params.Version = maxVersion + 1
	asset, err := InsertWithQueries(ctx, q, params)
	if err != nil {
		return VisualAsset{}, err
	}
	newID := asset.ID
	if err := q.ArchivePriorReadyArtifactSlot(ctx, dbgen.ArchivePriorReadyArtifactSlotParams{
		NewAssetID:     &newID,
		TenantID:       slot.TenantID,
		WorldID:        slot.WorldID,
		StyleProfileID: strPtr(slot.StyleProfileID),
		QualityTier:    slot.QualityTier,
		PromptHash:     strPtr(slot.PromptHash),
	}); err != nil {
		return VisualAsset{}, err
	}
	return asset, nil
}

// SupersedeVariantSlotWithQueries runs the Phase 6A4 supersede + insert for a
// pack-role slot inside the caller's transaction (the pack worker reuses
// InsertPackItemWithAsset's transaction so the asset, its archive, and the
// asset_pack_items row all commit together). It mirrors supersedeArtifactSlot:
// lock → next version → insert ready → archive prior ready linked forward.
func SupersedeVariantSlotWithQueries(ctx context.Context, q *dbgen.Queries, params InsertParams, slot VariantSlot) (VisualAsset, error) {
	if err := q.AcquireSupersedeLock(ctx, slot.LockKey()); err != nil {
		return VisualAsset{}, err
	}
	maxVersion, err := q.MaxVersionForVariantSlot(ctx, dbgen.MaxVersionForVariantSlotParams{
		TenantID:         slot.TenantID,
		WorldID:          slot.WorldID,
		VisualIdentityID: strPtr(slot.VisualIdentityID),
		VariantKey:       slot.VariantKey,
		StateVersion:     slot.StateVersion,
		StyleProfileID:   strPtr(slot.StyleProfileID),
		QualityTier:      slot.QualityTier,
	})
	if err != nil {
		return VisualAsset{}, err
	}
	params.Version = maxVersion + 1
	asset, err := InsertWithQueries(ctx, q, params)
	if err != nil {
		return VisualAsset{}, err
	}
	newID := asset.ID
	if err := q.ArchivePriorReadyVariantSlot(ctx, dbgen.ArchivePriorReadyVariantSlotParams{
		NewAssetID:       &newID,
		TenantID:         slot.TenantID,
		WorldID:          slot.WorldID,
		VisualIdentityID: strPtr(slot.VisualIdentityID),
		VariantKey:       slot.VariantKey,
		StateVersion:     slot.StateVersion,
		StyleProfileID:   strPtr(slot.StyleProfileID),
		QualityTier:      slot.QualityTier,
	}); err != nil {
		return VisualAsset{}, err
	}
	return asset, nil
}

func (r *pgRepository) SupersedeAndInsertArtifact(ctx context.Context, params InsertParams, slot ArtifactSlot) (VisualAsset, error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return VisualAsset{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	q := dbgen.New(tx)
	asset, err := supersedeArtifactSlot(ctx, q, params, slot)
	if err != nil {
		return VisualAsset{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return VisualAsset{}, err
	}
	committed = true
	return asset, nil
}
