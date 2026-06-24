package governance

import "context"

// SignatureVerifier checks the envelope signature. The canonicalization +
// crypto is a cross-system contract with core that is NOT YET DESIGNED.
type SignatureVerifier interface {
	VerifySignature(ctx context.Context, env Envelope) (bool, error)
}

// StubSignatureVerifier is an explicit no-op that PASSES every signature.
//
// TODO(core-signing): replace with real canonicalization + signature
// verification once core ships envelope signing and the on-the-wire format is
// pinned. Do NOT invent the format here — it is a cross-system contract.
type StubSignatureVerifier struct{}

func (StubSignatureVerifier) VerifySignature(ctx context.Context, env Envelope) (bool, error) {
	return true, nil
}

// IsStub reports whether the active verifier is the no-op stub. Wiring uses this
// to emit a startup WARN when enforce mode runs against the stub (Task 9).
func IsStub(s SignatureVerifier) bool {
	_, ok := s.(StubSignatureVerifier)
	return ok
}
