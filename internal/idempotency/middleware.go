package idempotency

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/zakkriel/drchat-image-platform/internal/auth"
	"github.com/zakkriel/drchat-image-platform/internal/httperr"
	"github.com/zakkriel/drchat-image-platform/internal/ids"
)

// HeaderKey is the request header callers send to opt in to idempotent
// replay.
const HeaderKey = "Idempotency-Key"

// MaxBodyBytes caps the request body the middleware reads for hashing. Big
// enough for any Phase 3 generation request; matches the handler limit.
const MaxBodyBytes = 1 << 20

type ctxKey int

const (
	// reservedJobIDKey carries the job id the middleware reserved when the
	// caller supplied an idempotency header. Handlers use it instead of
	// minting a new id so the row + the idempotency_keys row agree.
	reservedJobIDKey ctxKey = iota
	replayJobIDKey
)

// Deps bundles the runtime dependencies the middleware needs.
type Deps struct {
	Repo Repository
	Now  func() time.Time
}

// Middleware enforces idempotency for the wrapped handler. Behavior matches
// docs/api/idempotency.md:
//   - No header → pass through unchanged.
//   - Header + same body + same endpoint → replay the prior 202 inline; the
//     downstream handler is never called.
//   - Header + different body OR different endpoint → 409 idempotency_conflict.
//   - Otherwise → reserve a job id, attach it to the context, run the
//     handler, and post-write the idempotency row so future replays succeed.
func Middleware(deps Deps) func(http.Handler) http.Handler {
	now := deps.Now
	if now == nil {
		now = time.Now
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := r.Header.Get(HeaderKey)
			if key == "" {
				next.ServeHTTP(w, r)
				return
			}
			principal := auth.PrincipalFromContext(r.Context())
			if principal == nil {
				httperr.Write(w, r, http.StatusInternalServerError, httperr.CodeInternalError, "missing principal for idempotency check")
				return
			}

			raw, err := io.ReadAll(io.LimitReader(r.Body, MaxBodyBytes))
			if err != nil {
				httperr.Write(w, r, http.StatusBadRequest, httperr.CodeInvalidRequest, "could not read request body")
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(raw))

			hash := hashBody(raw)
			endpoint := endpointKey(r)

			existing, err := deps.Repo.Get(r.Context(), principal.TokenID, key)
			switch {
			case err == nil:
				if existing.Endpoint != endpoint {
					httperr.Write(w, r, http.StatusConflict, httperr.CodeIdempotencyConflict, "idempotency key reused with a different endpoint")
					return
				}
				if existing.RequestHash != hash {
					httperr.Write(w, r, http.StatusConflict, httperr.CodeIdempotencyConflict, "idempotency key reused with a different body")
					return
				}
				if existing.GenerationJobID == nil {
					httperr.Write(w, r, http.StatusInternalServerError, httperr.CodeInternalError, "idempotency record missing job id")
					return
				}
				writeReplay(w, r, *existing.GenerationJobID)
				return
			case errors.Is(err, ErrNotFound):
				// Fall through to reservation.
			default:
				httperr.Write(w, r, http.StatusInternalServerError, httperr.CodeInternalError, "could not check idempotency key")
				return
			}

			reservedJobID := ids.NewGenerationJobID()
			ctx := context.WithValue(r.Context(), reservedJobIDKey, reservedJobID)
			recorder := &writeRecorder{ResponseWriter: w}
			next.ServeHTTP(recorder, r.WithContext(ctx))

			if recorder.status != http.StatusAccepted {
				return
			}
			rec := Record{
				ID:              ids.NewIdempotencyKeyID(),
				TokenID:         principal.TokenID,
				Key:             key,
				Endpoint:        endpoint,
				RequestHash:     hash,
				GenerationJobID: &reservedJobID,
				ExpiresAt:       now().Add(TTL),
			}
			if _, _, err := deps.Repo.Insert(r.Context(), rec); err != nil {
				// Best-effort: the handler already responded 202; if we fail
				// to record the key, replays will fall through and create a
				// new job. Log via the access-log layer; do not rewrite the
				// already-sent response.
				_ = err
			}
		})
	}
}

// ReservedJobIDFromContext returns the job id the middleware reserved for
// the caller, or "" when no Idempotency-Key was supplied (handlers must
// fall back to minting a new id in that case).
func ReservedJobIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(reservedJobIDKey).(string); ok {
		return v
	}
	return ""
}

// replayResponse is the 202 body shape the middleware reconstructs when
// replaying.
type replayResponse struct {
	JobID  string `json:"job_id"`
	Status string `json:"status"`
}

func writeReplay(w http.ResponseWriter, r *http.Request, jobID string) {
	body := replayResponse{JobID: jobID, Status: "queued"}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(body)
	_ = r
}

type writeRecorder struct {
	http.ResponseWriter
	status int
}

func (rec *writeRecorder) WriteHeader(code int) {
	rec.status = code
	rec.ResponseWriter.WriteHeader(code)
}

func (rec *writeRecorder) Write(b []byte) (int, error) {
	if rec.status == 0 {
		rec.status = http.StatusOK
	}
	return rec.ResponseWriter.Write(b)
}

func endpointKey(r *http.Request) string {
	return r.Method + " " + r.URL.Path
}

func hashBody(raw []byte) string {
	// Round-trip through json.Marshal so insignificant whitespace differences
	// don't collapse two semantically-identical bodies into different hashes.
	normalized := normalizeJSON(raw)
	sum := sha256.Sum256(normalized)
	return hex.EncodeToString(sum[:])
}

func normalizeJSON(raw []byte) []byte {
	if len(bytes.TrimSpace(raw)) == 0 {
		return []byte{}
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return raw
	}
	out, err := json.Marshal(v)
	if err != nil {
		return raw
	}
	return out
}
