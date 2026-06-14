package webhooks

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

// TestSignKnownVector pins the signature format and value against an
// independently computed HMAC-SHA256 so a future refactor cannot silently
// change the wire contract receivers verify against.
func TestSignKnownVector(t *testing.T) {
	secret := "whsec_test_secret"
	body := []byte(`{"event":"generation_job.completed"}`)

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	want := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	got := Sign(secret, body)
	if got != want {
		t.Fatalf("Sign mismatch:\n got=%s\nwant=%s", got, want)
	}
	if !strings.HasPrefix(got, "sha256=") {
		t.Fatalf("signature must carry the sha256= prefix, got %q", got)
	}
}

// TestSignDiffersBySecretAndBody verifies the signature is sensitive to both
// inputs: a different secret or a different body yields a different signature.
func TestSignDiffersBySecretAndBody(t *testing.T) {
	body := []byte(`{"a":1}`)
	if Sign("secret-a", body) == Sign("secret-b", body) {
		t.Fatal("different secrets must produce different signatures")
	}
	if Sign("secret-a", body) == Sign("secret-a", []byte(`{"a":2}`)) {
		t.Fatal("different bodies must produce different signatures")
	}
}
