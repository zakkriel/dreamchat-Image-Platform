package governance_test

import (
	"testing"

	"github.com/zakkriel/drchat-image-platform/internal/governance"
)

// EnforceWithStubError: enforce + stub is refused only in the live
// environment; dev/test keep the WARN-only posture, and a real (non-stub)
// verifier or log_only mode never refuses.
func TestEnforceWithStubError(t *testing.T) {
	stub := governance.StubSignatureVerifier{}
	if err := governance.EnforceWithStubError(true, governance.ModeEnforce, stub); err == nil {
		t.Fatal("live + enforce + stub must refuse startup")
	}
	if err := governance.EnforceWithStubError(false, governance.ModeEnforce, stub); err != nil {
		t.Fatalf("dev/test + enforce + stub must only WARN, got %v", err)
	}
	if err := governance.EnforceWithStubError(true, governance.ModeLogOnly, stub); err != nil {
		t.Fatalf("live + log_only + stub must not refuse, got %v", err)
	}
}
