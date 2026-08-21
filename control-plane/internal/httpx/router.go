package httpx

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/KaumudiRRawal/launchpad/control-plane/openapi"
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
	// BaseDomain and ProxyPort render an environment's public address. It is
	// derived at render time rather than stored, because the platform's domain
	// is configuration: a column would keep serving the old address after that
	// configuration changed.
	BaseDomain string
	ProxyPort  int
}

// route is one endpoint the API serves. The set is declared as data rather
// than as a sequence of registration calls so that the conformance test can
// walk it and hold the OpenAPI document to the same list the router uses,
// instead of a second list that has to be kept in step by hand.
type route struct {
	method  string
	path    string
	handler http.HandlerFunc
	// public marks a route served without a bearer token. Everything else is
	// mounted behind RequireAuth, so a route added without saying anything
	// here is authenticated: forgetting to opt in is impossible, where
	// forgetting to opt out would not be.
	public bool
}

// routes lists every endpoint. An orchestrator probing health should not need
// a credential, the version is not a secret, and neither is the specification:
// a client generating itself against the API should not have to authenticate
// to learn its shape.
func (a *API) routes() []route {
	return []route{
		{method: http.MethodGet, path: "/healthz", handler: a.handleHealth, public: true},
		{method: http.MethodGet, path: "/readyz", handler: a.handleReady, public: true},
		{method: http.MethodGet, path: "/v1/version", handler: a.handleVersion, public: true},
		{method: http.MethodGet, path: "/v1/openapi.yaml", handler: a.handleOpenAPI, public: true},

		{method: http.MethodPost, path: "/v1/projects", handler: a.handleCreateProject},
		{method: http.MethodGet, path: "/v1/projects", handler: a.handleListProjects},
		{method: http.MethodGet, path: "/v1/projects/{projectID}", handler: a.handleGetProject},
		{method: http.MethodDelete, path: "/v1/projects/{projectID}", handler: a.handleDeleteProject},

		{method: http.MethodPost, path: "/v1/projects/{projectID}/services", handler: a.handleCreateService},
		{method: http.MethodGet, path: "/v1/projects/{projectID}/services", handler: a.handleListServices},

		{method: http.MethodPost, path: "/v1/projects/{projectID}/environments", handler: a.handleCreateEnvironment},
		{method: http.MethodGet, path: "/v1/projects/{projectID}/environments", handler: a.handleListEnvironments},

		{method: http.MethodGet, path: "/v1/environments/{environmentID}/metrics", handler: a.handleEnvironmentMetrics},
		{method: http.MethodGet, path: "/v1/environments/{environmentID}/analysis", handler: a.handleEnvironmentAnalysis},

		{method: http.MethodGet, path: "/v1/projects/{projectID}/deployments", handler: a.handleListDeployments},
		{method: http.MethodPost, path: "/v1/deployments", handler: a.handleCreateDeployment},
		{method: http.MethodGet, path: "/v1/deployments/{deploymentID}", handler: a.handleGetDeployment},
		{method: http.MethodGet, path: "/v1/deployments/{deploymentID}/logs", handler: a.handleDeploymentLogs},

		{method: http.MethodPost, path: "/v1/api-keys", handler: a.handleCreateAPIKey},
		{method: http.MethodGet, path: "/v1/api-keys", handler: a.handleListAPIKeys},
		{method: http.MethodDelete, path: "/v1/api-keys/{keyID}", handler: a.handleRevokeAPIKey},
	}
}

// Routes returns the fully wired handler, middleware included.
func (a *API) Routes() http.Handler {
	mux := http.NewServeMux()
	authed := http.NewServeMux()

	for _, rt := range a.routes() {
		pattern := rt.method + " " + rt.path
		if rt.public {
			mux.HandleFunc(pattern, rt.handler)
			continue
		}
		authed.HandleFunc(pattern, rt.handler)
	}

	// The public /v1 patterns registered above are more specific than "/v1/",
	// so they still win and stay unauthenticated.
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

// handleOpenAPI serves the specification this build implements. The document
// is embedded rather than read from disk so it cannot go missing from a
// container image, and it is the same file the dashboard's client is generated
// from, so what a caller reads here is what the client was built against.
func (a *API) handleOpenAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	if _, err := w.Write(openapi.Document); err != nil {
		LoggerFrom(r.Context()).Warn("write openapi document",
			slog.String("error", err.Error()))
	}
}
