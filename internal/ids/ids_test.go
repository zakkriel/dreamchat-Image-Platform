package ids

import (
	"regexp"
	"testing"
)

var (
	stylePattern     = regexp.MustCompile(`^sty_[0-9a-f]{16}$`)
	identityPattern  = regexp.MustCompile(`^vi_[0-9a-f]{16}$`)
	jobPattern       = regexp.MustCompile(`^job_[0-9a-f]{16}$`)
	assetPattern     = regexp.MustCompile(`^asset_[0-9a-f]{16}$`)
	attemptPattern   = regexp.MustCompile(`^att_[0-9a-f]{16}$`)
	costEventPattern = regexp.MustCompile(`^ce_[0-9a-f]{16}$`)
	idemPattern      = regexp.MustCompile(`^idem_[0-9a-f]{16}$`)
)

func TestNewStyleProfileIDPrefix(t *testing.T) {
	id := NewStyleProfileID()
	if !stylePattern.MatchString(id) {
		t.Fatalf("style id %q does not match sty_<16 hex>", id)
	}
}

func TestNewVisualIdentityIDPrefix(t *testing.T) {
	id := NewVisualIdentityID()
	if !identityPattern.MatchString(id) {
		t.Fatalf("visual identity id %q does not match vi_<16 hex>", id)
	}
}

func TestNewGenerationJobIDPrefix(t *testing.T) {
	id := NewGenerationJobID()
	if !jobPattern.MatchString(id) {
		t.Fatalf("job id %q does not match job_<16 hex>", id)
	}
}

func TestNewVisualAssetIDPrefix(t *testing.T) {
	id := NewVisualAssetID()
	if !assetPattern.MatchString(id) {
		t.Fatalf("asset id %q does not match asset_<16 hex>", id)
	}
}

func TestNewProviderAttemptIDPrefix(t *testing.T) {
	id := NewProviderAttemptID()
	if !attemptPattern.MatchString(id) {
		t.Fatalf("attempt id %q does not match att_<16 hex>", id)
	}
}

func TestNewCostEventIDPrefix(t *testing.T) {
	id := NewCostEventID()
	if !costEventPattern.MatchString(id) {
		t.Fatalf("cost event id %q does not match ce_<16 hex>", id)
	}
}

func TestNewIdempotencyKeyIDPrefix(t *testing.T) {
	id := NewIdempotencyKeyID()
	if !idemPattern.MatchString(id) {
		t.Fatalf("idempotency key id %q does not match idem_<16 hex>", id)
	}
}

func TestNewIDsAreUnique(t *testing.T) {
	seen := make(map[string]struct{}, 1024)
	for i := 0; i < 1024; i++ {
		id := NewStyleProfileID()
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate id at iter %d: %q", i, id)
		}
		seen[id] = struct{}{}
	}
}
