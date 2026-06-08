package http

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
)

type HealthResponse struct {
	Status string `json:"status"`
}

func NewRouter(logger *slog.Logger) *chi.Mux {
	r := chi.NewRouter()
	r.Use(RequestID)
	r.Use(AccessLog(logger))

	r.Get("/health", healthHandler)
	return r
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(HealthResponse{Status: "ok"})
}
