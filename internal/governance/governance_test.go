package governance_test

import (
	"context"
	"testing"
	"time"

	"github.com/zakkriel/drchat-image-platform/internal/governance"
)

func freshEnvelope() governance.Envelope {
	return governance.Envelope{
		SchemaVersion:    "1",
		ClassificationID: "class-1",
		Visibility:       "private",
		ContentClass:     "anything-opaque",
		AuthorizedBy:     "core-signer-1",
		IssuedAt:         time.Now().Add(-1 * time.Minute),
		Signature:        "sig-bytes",
	}
}

func newV() governance.Verifier {
	return governance.NewVerifier(governance.StubSignatureVerifier{}, 24*time.Hour, []string{"core-signer-1"})
}

func TestVerifyOK(t *testing.T) {
	res := newV().Verify(context.Background(), freshEnvelope(), governance.SubjectMeta{IdentityID: "id1"})
	if !res.OK {
		t.Fatalf("want OK, got reason %q", res.Reason)
	}
}

func TestVerifyMissingField(t *testing.T) {
	env := freshEnvelope()
	env.ClassificationID = ""
	res := newV().Verify(context.Background(), env, governance.SubjectMeta{IdentityID: "id1"})
	if res.OK || res.Reason != governance.ReasonMissingField {
		t.Fatalf("want missing_field block, got OK=%v reason=%q", res.OK, res.Reason)
	}
}

func TestVerifyMissingSchemaVersion(t *testing.T) {
	env := freshEnvelope()
	env.SchemaVersion = ""
	res := newV().Verify(context.Background(), env, governance.SubjectMeta{IdentityID: "id1"})
	if res.OK || res.Reason != governance.ReasonMissingSchemaVersion {
		t.Fatalf("want missing_schema_version, got OK=%v reason=%q", res.OK, res.Reason)
	}
}

func TestVerifyStale(t *testing.T) {
	env := freshEnvelope()
	env.IssuedAt = time.Now().Add(-48 * time.Hour)
	res := newV().Verify(context.Background(), env, governance.SubjectMeta{IdentityID: "id1"})
	if res.OK || res.Reason != governance.ReasonStale {
		t.Fatalf("want stale, got OK=%v reason=%q", res.OK, res.Reason)
	}
}

func TestVerifyFutureIssuedAt(t *testing.T) {
	env := freshEnvelope()
	env.IssuedAt = time.Now().Add(1 * time.Hour)
	res := newV().Verify(context.Background(), env, governance.SubjectMeta{IdentityID: "id1"})
	if res.OK || res.Reason != governance.ReasonStale {
		t.Fatalf("want stale (future), got OK=%v reason=%q", res.OK, res.Reason)
	}
}

func TestVerifyUnknownIssuer(t *testing.T) {
	env := freshEnvelope()
	env.AuthorizedBy = "stranger"
	res := newV().Verify(context.Background(), env, governance.SubjectMeta{IdentityID: "id1"})
	if res.OK || res.Reason != governance.ReasonUnknownIssuer {
		t.Fatalf("want unknown_issuer, got OK=%v reason=%q", res.OK, res.Reason)
	}
}

// content_class is opaque: the verdict must not depend on its value (D-3/E-1).
func TestContentClassOpaque(t *testing.T) {
	v := newV()
	a := freshEnvelope()
	a.ContentClass = "benign"
	b := freshEnvelope()
	b.ContentClass = "../../etc/passwd; DROP TABLE; nsfw?maybe"
	if got := v.Verify(context.Background(), a, governance.SubjectMeta{IdentityID: "id1"}); !got.OK {
		t.Fatalf("a should be OK")
	}
	if got := v.Verify(context.Background(), b, governance.SubjectMeta{IdentityID: "id1"}); !got.OK {
		t.Fatalf("verdict changed with content_class value — not opaque")
	}
}

func TestDecide(t *testing.T) {
	blocked := governance.Result{OK: false, Reason: governance.ReasonStale}
	ok := governance.Result{OK: true}
	// log_only: always proceed; event reflects verdict.
	if proceed, ev := governance.Decide(governance.ModeLogOnly, blocked); !proceed || ev != governance.EventBlocked {
		t.Fatalf("log_only blocked: proceed=%v ev=%q", proceed, ev)
	}
	if proceed, ev := governance.Decide(governance.ModeLogOnly, ok); !proceed || ev != governance.EventVerified {
		t.Fatalf("log_only ok: proceed=%v ev=%q", proceed, ev)
	}
	// enforce: block stops; ok proceeds.
	if proceed, ev := governance.Decide(governance.ModeEnforce, blocked); proceed || ev != governance.EventBlocked {
		t.Fatalf("enforce blocked: proceed=%v ev=%q", proceed, ev)
	}
	if proceed, _ := governance.Decide(governance.ModeEnforce, ok); !proceed {
		t.Fatalf("enforce ok should proceed")
	}
}

func TestIsStub(t *testing.T) {
	if !governance.IsStub(governance.StubSignatureVerifier{}) {
		t.Fatal("StubSignatureVerifier must report IsStub true")
	}
}

func TestEnforceWithStubWarning(t *testing.T) {
	if governance.EnforceWithStubWarning(governance.ModeEnforce, governance.StubSignatureVerifier{}) == "" {
		t.Fatal("expected warning for enforce+stub")
	}
	if governance.EnforceWithStubWarning(governance.ModeLogOnly, governance.StubSignatureVerifier{}) != "" {
		t.Fatal("no warning expected in log_only")
	}
}
