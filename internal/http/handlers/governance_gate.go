package handlers

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/zakkriel/drchat-image-platform/internal/audit"
	"github.com/zakkriel/drchat-image-platform/internal/governance"
	"github.com/zakkriel/drchat-image-platform/internal/http/apigen"
	"github.com/zakkriel/drchat-image-platform/internal/httperr"
	"github.com/zakkriel/drchat-image-platform/internal/jobs"
)

// GovernanceGate is the media-eligibility gate shared by the legacy generation
// endpoints (artifact, pack, style preview) — ADR-P002 Follow-up 1: every
// generation path runs the SAME verification as POST /v1/generations. The gate
// verifies integrity/authorization only (presence, freshness, issuer,
// signature seam); it never inspects prompt/content and content_class stays
// opaque (D-3/E-1).
//
// The envelope is OPTIONAL on the legacy contracts (v0.13.0, additive): an
// absent envelope flows through the verifier as a zero envelope and fails its
// presence checks, so under the default log_only posture the verdict is
// audited and the request proceeds (existing callers unaffected), while
// enforce rejects it 403 governance_blocked — exactly the phased-enforcement
// model the combined endpoint shipped with.
//
// A zero-valued gate (nil Verifier) is a no-op, mirroring the generations
// handler's nil-safe wiring.
type GovernanceGate struct {
	Verifier governance.Verifier
	Mode     governance.Mode
	Audit    AuditSink
}

// gateOutcome carries what the gate decided so the handler can stamp the
// governance columns onto the job it eventually creates. Zero value = nothing
// to stamp (gate unwired, or envelope absent and not verified).
type gateOutcome struct {
	verifiedAt *time.Time
	envelope   *apigen.GovernanceEnvelope
}

// apply stamps the outcome onto the create params: the raw envelope JSON +
// scalar columns when an envelope was supplied, and governance_verified_at
// when verification passed.
func (o gateOutcome) apply(params *jobs.CreateAndEnqueueParams) {
	if o.envelope != nil {
		if b, err := json.Marshal(o.envelope); err == nil {
			params.GovernanceEnvelope = b
		}
		cls := o.envelope.ClassificationId
		params.ClassificationID = &cls
		vis := o.envelope.Visibility
		params.Visibility = &vis
		cc := o.envelope.ContentClass
		params.ContentClass = &cc
		ab := o.envelope.AuthorizedBy
		params.AuthorizedBy = &ab
	}
	params.GovernanceVerifiedAt = o.verifiedAt
}

// run verifies the (possibly absent) envelope, emits the audit event, and
// writes the 403 governance_blocked response when enforcement blocks. It
// returns ok=false when the response has been written and the handler must
// stop. Runs AFTER the idempotency replay pre-check and BEFORE reuse, route
// resolution, and cost reservation — a block leaves no side effects.
func (g GovernanceGate) run(w http.ResponseWriter, r *http.Request, tenantID, tokenID string, env *apigen.GovernanceEnvelope) (gateOutcome, bool) {
	if g.Verifier == nil {
		return gateOutcome{envelope: env}, true
	}
	var genv governance.Envelope
	if env != nil {
		genv = governance.Envelope{
			SchemaVersion:    env.SchemaVersion,
			ClassificationID: env.ClassificationId,
			Visibility:       env.Visibility,
			ContentClass:     env.ContentClass,
			AuthorizedBy:     env.AuthorizedBy,
			IssuedAt:         env.IssuedAt,
			Signature:        env.Signature,
		}
	}
	// SubjectMeta stays empty on the legacy contracts: they carry no
	// subject/anchor refs, and the gate structurally never sees prompt text.
	res := g.Verifier.Verify(r.Context(), genv, governance.SubjectMeta{})
	proceed, eventType := governance.Decide(g.Mode, res)
	if g.Audit != nil {
		meta := map[string]any{
			"reason":           res.Reason,
			"mode":             string(g.Mode),
			"envelope_present": env != nil,
			"endpoint":         r.Method + " " + r.URL.Path,
		}
		if env != nil {
			meta["classification_id"] = env.ClassificationId
			meta["content_class"] = env.ContentClass // opaque: stored/logged, never parsed
		}
		_ = g.Audit.Emit(r.Context(), tenantID, audit.Event{
			EventType:    eventType,
			TenantID:     tenantID,
			ActorTokenID: tokenID,
			ResourceType: "generation",
			Metadata:     meta,
		})
	}
	if !proceed {
		httperr.Write(w, r, http.StatusForbidden, httperr.CodeGovernanceBlocked, "governance verification failed: "+res.Reason)
		return gateOutcome{}, false
	}
	out := gateOutcome{envelope: env}
	if res.OK {
		now := time.Now()
		out.verifiedAt = &now
	}
	return out, true
}

// requireAdminForAnyExisting gates the any_existing fallback policy behind the
// admin:read scope: it deliberately bypasses the compatibility matrix and can
// return matrix-invalid assets, so it is a debug facility, never a normal
// retrieval mode. Returns false after writing the 403.
func requireAdminForAnyExisting(w http.ResponseWriter, r *http.Request, hasAdmin bool, policy string) bool {
	if policy != string(apigen.AnyExisting) || hasAdmin {
		return true
	}
	httperr.Write(w, r, http.StatusForbidden, httperr.CodeForbidden, "fallback_policy any_existing requires the admin:read scope (debug facility: it may return matrix-invalid assets)")
	return false
}
