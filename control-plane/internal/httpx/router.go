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
	Store   Store
	Log     *slog.Logger
	Version string
}

// Routes returns the fully wired handler, middleware included.
func (a *API) Routes() http.Handler {
	mux := http.NewServeMux()

	// Unauthenticated. An orchestrator probing health should not need a
	// credential, and the version is not a secret.
	mux.HandleFunc("GET /healthz", a.handleHealth)
	mux.HandleFunc("GET /readyz", a.handleReady)
	mux.HandleFunc("GET /v1/version", a.handleVersion)

	// Everything else under /v1 requires a bearer token. Registering these on
	// their own mux means a new route is authenticated by default: forgetting
	// to opt in is impossible, where forgetting to opt out would not be.
	authed := http.NewServeMux()

	authed.HandleFunc("POST /v1/projects", a.handleCreateProject)
	authed.HandleFunc("GET /v1/projects", a.handleListProjects)
	authed.HandleFunc("GET /v1/projects/{projectID}", a.handleGetProject)
	authed.HandleFunc("DELETE /v1/projects/{projectID}", a.handleDeleteProject)

	authed.HandleFunc("POST /v1/projects/{projectID}/services", a.handleCreateService)
	authed.HandleFunc("GET /v1/projects/{projectID}/services", a.handleListServices)

	authed.HandleFunc("POST /v1/projects/{projectID}/environments", a.handleCreateEnvironment)
	authed.HandleFunc("GET /v1/projects/{projectID}/environments", a.handleListEnvironments)

	authed.HandleFunc("GET /v1/projects/{projectID}/deployments", a.handleListDeployments)
	authed.HandleFunc("POST /v1/deployments", a.handleCreateDeployment)
	authed.HandleFunc("GET /v1/deployments/{deploymentID}", a.handleGetDeployment)
	authed.HandleFunc("GET /v1/deployments/{deploymentID}/logs", a.handleDeploymentLogs)

	authed.HandleFunc("POST /v1/api-keys", a.handleCreateAPIKey)
	authed.HandleFunc("GET /v1/api-keys", a.handleListAPIKeys)
	authed.HandleFunc("DELETE /v1/api-keys/{keyID}", a.handleRevokeAPIKey)

	// "GET /v1/version" is a more specific pattern than "/v1/", so it still
	// wins and stays public.
	mux.Handle("/v1/", RequireAuth(a.Store)(authed))

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
