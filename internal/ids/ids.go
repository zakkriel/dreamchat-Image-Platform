// Package ids generates opaque, slug-style identifiers for platform
// entities. IDs are minted at the API layer (never by the database) so that
// repository inserts can use a known ID before the row exists.
package ids

import (
	"crypto/rand"
	"encoding/hex"
)

const (
	PrefixStyleProfile    = "sty"
	PrefixVisualIdentity  = "vi"
	PrefixGenerationJob   = "job"
	PrefixVisualAsset     = "asset"
	PrefixProviderAttempt = "att"
	PrefixCostEvent       = "ce"
	PrefixCostReservation = "resv"
	PrefixIdempotencyKey  = "idem"
)

// New returns an opaque ID of the form "<prefix>_<16 hex chars>".
func New(prefix string) string {
	return prefix + "_" + randomHex(8)
}

func NewStyleProfileID() string    { return New(PrefixStyleProfile) }
func NewVisualIdentityID() string  { return New(PrefixVisualIdentity) }
func NewGenerationJobID() string   { return New(PrefixGenerationJob) }
func NewVisualAssetID() string     { return New(PrefixVisualAsset) }
func NewProviderAttemptID() string { return New(PrefixProviderAttempt) }
func NewCostEventID() string       { return New(PrefixCostEvent) }
func NewCostReservationID() string { return New(PrefixCostReservation) }
func NewIdempotencyKeyID() string  { return New(PrefixIdempotencyKey) }

func randomHex(byteLen int) string {
	b := make([]byte, byteLen)
	if _, err := rand.Read(b); err != nil {
		panic("ids: crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(b)
}
