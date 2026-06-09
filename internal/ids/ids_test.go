package ids

import (
	"regexp"
	"testing"
)

var (
	stylePattern    = regexp.MustCompile(`^sty_[0-9a-f]{16}$`)
	identityPattern = regexp.MustCompile(`^vi_[0-9a-f]{16}$`)
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
