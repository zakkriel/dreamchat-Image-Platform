// Package handlers wires Phase 2 visual-identity CRUD on top of the existing
// auth + DB pipeline. Handlers depend only on domain repositories; sqlc
// types stay inside the repository layer.
package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/zakkriel/drchat-image-platform/internal/httperr"
)

const maxRequestBodyBytes = 1 << 20 // 1 MiB; identity payloads are small JSON

var errBodyTenantID = errors.New("decode: body must not include tenant_id")

// readJSONBody reads the request body into memory, rejects bodies that try
// to set top-level tenant_id, and decodes the result into v.
func readJSONBody(w http.ResponseWriter, r *http.Request, v any) bool {
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBodyBytes))
	if err != nil {
		httperr.Write(w, r, http.StatusBadRequest, httperr.CodeInvalidRequest, "could not read request body")
		return false
	}
	if err := rejectBodyTenantID(raw); err != nil {
		httperr.Write(w, r, http.StatusBadRequest, httperr.CodeInvalidRequest, "tenant_id must not be set in request body")
		return false
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		httperr.Write(w, r, http.StatusBadRequest, httperr.CodeInvalidRequest, "request body required")
		return false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := dec.Decode(v); err != nil {
		httperr.Write(w, r, http.StatusBadRequest, httperr.CodeInvalidRequest, "could not decode request body")
		return false
	}
	return true
}

func rejectBodyTenantID(raw []byte) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		// Body isn't a JSON object; subsequent decode handles the error.
		return nil
	}
	if _, found := top["tenant_id"]; found {
		return errBodyTenantID
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
