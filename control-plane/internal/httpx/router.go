package httpx

import (
	"context"
	"log/slog"
	"net/http"
	"time"
)

// Pinger is the slice of the database pool the readiness check needs. Keeping
// it narrow lets the handler be tested without a live PostgreSQL.
type Pinger interface {
	Ping(ctx context.Context) error
}

// API wires the control-plane HTTP handlers to their dependencies.
type API struct {
	DB      Pinger
	Log     *slog.Logger
	Version string
}

// Routes returns the fully wired handler, middleware included.
func (a *API) Routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", a.handleHealth)
	mux.HandleFunc("GET /readyz", a.handleReady)
	mux.HandleFunc("GET /v1/version", a.handleVersion)

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		Error(w, r, http.StatusNotFound, "not_found", "no route matches "+r.URL.Path)
	})

	return WithObservability(a.Log)(mux)
}

// handleHealth reports process liveness. It deliberately touches no
// dependencies: a failing database should not cause the orchestrator to
// restart an otherwise healthy process.
func (a *API) handleHealth(w http.ResponseWriter, r *http.Request) {
	JSON(w, r, http.StatusOK, map[string]string{"status": "ok"})
}

// handleReady reports whether this instance can serve traffic, which requires
// a reachable database.
func (a *API) handleReady(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	if err := a.DB.Ping(ctx); err != nil {
		LoggerFrom(r.Context()).Warn("readiness check failed",
			slog.String("error", err.Error()))
		Error(w, r, http.StatusServiceUnavailable, "database_unavailable", "database is not reachable")
		return
	}

	JSON(w, r, http.StatusOK, map[string]string{
		"status":   "ready",
		"database": "ok",
	})
}

func (a *API) handleVersion(w http.ResponseWriter, r *http.Request) {
	JSON(w, r, http.StatusOK, map[string]string{
		"service": "launchpad-control-plane",
		"version": a.Version,
	})
}
