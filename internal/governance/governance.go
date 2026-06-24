// Package governance verifies the media-eligibility envelope at the generation
// chokepoint (D-3/E-1): it VERIFIES and stores, it never interprets policy and
// never reads prompt/description text. SubjectMeta carries only identity refs —
// there is intentionally NO prompt field, so the gate cannot inspect the prompt.
// content_class is opaque: stored/logged, never parsed for meaning.
package governance

import (
	"context"
	"time"
)

// Mode mirrors config.GovernanceMode without importing it (avoids a cycle).
type Mode string

const (
	ModeLogOnly Mode = "log_only"
	ModeEnforce Mode = "enforce"
)

const (
	EventVerified = "media.eligibility_verified"
	EventBlocked  = "media.eligibility_blocked"
)

// Block reasons (also used as audit metadata).
const (
	ReasonMissingField         = "missing_field"
	ReasonMissingSchemaVersion = "missing_schema_version"
	ReasonStale                = "stale"
	ReasonUnknownIssuer        = "unknown_issuer"
	ReasonBadSignature         = "bad_signature"
)

// Envelope is the governance object to verify. Persisted JSONB carries
// SchemaVersion (D-4).
type Envelope struct {
	SchemaVersion    string
	ClassificationID string
	Visibility       string
	ContentClass     string // OPAQUE — never parsed
	AuthorizedBy     string
	IssuedAt         time.Time
	Signature        string
}

// SubjectMeta is the only non-envelope context the gate sees. No prompt field.
type SubjectMeta struct {
	IdentityID    string
	AnchorAssetID string
	DeriveFrom    string
}

type Result struct {
	OK     bool
	Reason string
}

type Verifier interface {
	Verify(ctx context.Context, env Envelope, subj SubjectMeta) Result
}

// clockSkew tolerates small future issued_at values.
const clockSkew = 2 * time.Minute

type RealVerifier struct {
	sig     SignatureVerifier
	maxAge  time.Duration
	issuers map[string]struct{}
}

func NewVerifier(sig SignatureVerifier, maxAge time.Duration, issuers []string) *RealVerifier {
	set := make(map[string]struct{}, len(issuers))
	for _, i := range issuers {
		set[i] = struct{}{}
	}
	return &RealVerifier{sig: sig, maxAge: maxAge, issuers: set}
}

func (v *RealVerifier) Verify(ctx context.Context, env Envelope, _ SubjectMeta) Result {
	// (a) required field presence + schema_version (D-4).
	if env.SchemaVersion == "" {
		return Result{Reason: ReasonMissingSchemaVersion}
	}
	if env.ClassificationID == "" || env.Visibility == "" || env.ContentClass == "" ||
		env.AuthorizedBy == "" || env.Signature == "" || env.IssuedAt.IsZero() {
		return Result{Reason: ReasonMissingField}
	}
	// (b) freshness: not older than maxAge, not in the future beyond skew.
	now := time.Now()
	if env.IssuedAt.Before(now.Add(-v.maxAge)) || env.IssuedAt.After(now.Add(clockSkew)) {
		return Result{Reason: ReasonStale}
	}
	// (c) authorized_by allowlist.
	if _, ok := v.issuers[env.AuthorizedBy]; !ok {
		return Result{Reason: ReasonUnknownIssuer}
	}
	// (d) signature — crypto is stubbed (see signature.go).
	ok, err := v.sig.VerifySignature(ctx, env)
	if err != nil || !ok {
		return Result{Reason: ReasonBadSignature}
	}
	return Result{OK: true}
}

// EnforceWithStubWarning returns a non-empty warning when enforce mode is active
// against a stubbed signature verifier (signatures are NOT actually verified).
func EnforceWithStubWarning(mode Mode, sig SignatureVerifier) string {
	if mode == ModeEnforce && IsStub(sig) {
		return "GOVERNANCE_ENFORCEMENT=enforce but signature verification is STUBBED — signatures are not verified; do not trust enforce for signature integrity (TODO core-signing)"
	}
	return ""
}

// Decide maps (mode, result) to (proceed, auditEventType). proceed is false only
// when enforcing AND the result is a block. The audit event always reflects the
// verdict (verified vs blocked), in BOTH modes.
func Decide(mode Mode, res Result) (proceed bool, eventType string) {
	if res.OK {
		return true, EventVerified
	}
	if mode == ModeEnforce {
		return false, EventBlocked
	}
	return true, EventBlocked
}
