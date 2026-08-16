package httpx

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/KaumudiRRawal/launchpad/control-plane/internal/domain"
)

// Store is the slice of the repository the HTTP layer uses. Declaring it here,
// at the point of use, keeps handler tests free of a database.
type Store interface {
	Authenticator

	CreateProject(ctx context.Context, accountID string, in domain.CreateProjectInput) (domain.Project, error)
	GetProject(ctx context.Context, accountID, id string) (domain.Project, error)
	ListProjects(ctx context.Context, accountID string) ([]domain.Project, error)
	DeleteProject(ctx context.Context, accountID, id string) error

	CreateService(ctx context.Context, accountID, projectID string, in domain.CreateServiceInput) (domain.Service, error)
	ListServices(ctx context.Context, accountID, projectID string) ([]domain.Service, error)

	CreateEnvironment(ctx context.Context, accountID, projectID string, in domain.CreateEnvironmentInput, subdomain string) (domain.Environment, error)
	ListEnvironments(ctx context.Context, accountID, projectID string) ([]domain.Environment, error)

	CreateDeployment(ctx context.Context, accountID string, in domain.CreateDeploymentInput) (domain.Deployment, error)
	GetDeployment(ctx context.Context, accountID, id string) (domain.Deployment, error)
	ListDeployments(ctx context.Context, accountID, projectID string) ([]domain.Deployment, error)
	ListDeploymentLogs(ctx context.Context, accountID, deploymentID string, afterSeq int) ([]domain.DeploymentLog, error)

	CreateAPIKey(ctx context.Context, accountID, name string) (domain.APIKey, string, error)
	ListAPIKeys(ctx context.Context, accountID string) ([]domain.APIKey, error)
	RevokeAPIKey(ctx context.Context, accountID, id string) error
}

// maxRequestBody caps how much a handler will read. Every body the API accepts
// is a small JSON object, so a generous limit still refuses a client that
// tries to exhaust memory.
const maxRequestBody = 1 << 20 // 1 MiB

// decodeJSON reads and validates a request body, writing the appropriate error
// response itself. It reports whether the handler should continue.
func decodeJSON[T interface{ Validate() error }](w http.ResponseWriter, r *http.Request, dst T) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)

	dec := json.NewDecoder(r.Body)
	// Reject unknown fields so a typo in a field name fails loudly instead of
	// being silently dropped and leaving the caller wondering why nothing
	// changed.
	dec.DisallowUnknownFields()

	if err := dec.Decode(dst); err != nil {
		var maxBytesErr *http.MaxBytesError
		switch {
		case errors.As(err, &maxBytesErr):
			Error(w, r, http.StatusRequestEntityTooLarge, "request_too_large",
				"request body exceeds 1 MiB")
		case errors.Is(err, io.EOF):
			Error(w, r, http.StatusBadRequest, "invalid_body", "a JSON body is required")
		default:
			Error(w, r, http.StatusBadRequest, "invalid_body", err.Error())
		}
		return false
	}

	if err := dst.Validate(); err != nil {
		writeValidationError(w, r, err)
		return false
	}
	return true
}

// writeValidationError renders field-level problems so a caller can correct
// each one without guessing.
func writeValidationError(w http.ResponseWriter, r *http.Request, err error) {
	// Accepts either a collection of problems or a single one, so a caller
	// always gets the same field-level shape back.
	var problems domain.ValidationErrors
	var single domain.ValidationError
	switch {
	case errors.As(err, &problems):
	case errors.As(err, &single):
		problems = domain.ValidationErrors{single}
	default:
		Error(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	fields := make([]map[string]string, len(problems))
	for i, p := range problems {
		fields[i] = map[string]string{"field": p.Field, "message": p.Message}
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusUnprocessableEntity)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{
			"code":       "validation_failed",
			"message":    "one or more fields are invalid",
			"request_id": RequestIDFrom(r.Context()),
			"fields":     fields,
		},
	})
}

// writeStoreError maps a repository error onto a status code.
func writeStoreError(w http.ResponseWriter, r *http.Request, err error, notFoundMessage string) {
	switch {
	case errors.Is(err, domain.ErrNotFound):
		Error(w, r, http.StatusNotFound, "not_found", notFoundMessage)
	case errors.Is(err, domain.ErrConflict):
		Error(w, r, http.StatusConflict, "conflict", "a resource with those details already exists")
	default:
		LoggerFrom(r.Context()).Error("store operation failed", "error", err.Error())
		Error(w, r, http.StatusInternalServerError, "internal_error", "an unexpected error occurred")
	}
}

// --- Projects ---

func (a *API) handleCreateProject(w http.ResponseWriter, r *http.Request) {
	var in domain.CreateProjectInput
	if !decodeJSON(w, r, &in) {
		return
	}

	account := MustAccountFrom(r.Context())
	project, err := a.Store.CreateProject(r.Context(), account.ID, in)
	if err != nil {
		writeStoreError(w, r, err, "project not found")
		return
	}

	w.Header().Set("Location", "/v1/projects/"+project.ID)
	JSON(w, r, http.StatusCreated, project)
}

func (a *API) handleListProjects(w http.ResponseWriter, r *http.Request) {
	account := MustAccountFrom(r.Context())

	projects, err := a.Store.ListProjects(r.Context(), account.ID)
	if err != nil {
		writeStoreError(w, r, err, "project not found")
		return
	}
	JSON(w, r, http.StatusOK, map[string]any{"projects": projects})
}

func (a *API) handleGetProject(w http.ResponseWriter, r *http.Request) {
	account := MustAccountFrom(r.Context())

	project, err := a.Store.GetProject(r.Context(), account.ID, r.PathValue("projectID"))
	if err != nil {
		writeStoreError(w, r, err, "project not found")
		return
	}
	JSON(w, r, http.StatusOK, project)
}

func (a *API) handleDeleteProject(w http.ResponseWriter, r *http.Request) {
	account := MustAccountFrom(r.Context())

	if err := a.Store.DeleteProject(r.Context(), account.ID, r.PathValue("projectID")); err != nil {
		writeStoreError(w, r, err, "project not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- Services ---

func (a *API) handleCreateService(w http.ResponseWriter, r *http.Request) {
	var in domain.CreateServiceInput
	if !decodeJSON(w, r, &in) {
		return
	}

	account := MustAccountFrom(r.Context())
	service, err := a.Store.CreateService(r.Context(), account.ID, r.PathValue("projectID"), in)
	if err != nil {
		writeStoreError(w, r, err, "project not found")
		return
	}
	JSON(w, r, http.StatusCreated, service)
}

func (a *API) handleListServices(w http.ResponseWriter, r *http.Request) {
	account := MustAccountFrom(r.Context())

	services, err := a.Store.ListServices(r.Context(), account.ID, r.PathValue("projectID"))
	if err != nil {
		writeStoreError(w, r, err, "project not found")
		return
	}
	JSON(w, r, http.StatusOK, map[string]any{"services": services})
}

// --- Environments ---

func (a *API) handleCreateEnvironment(w http.ResponseWriter, r *http.Request) {
	var in domain.CreateEnvironmentInput
	if !decodeJSON(w, r, &in) {
		return
	}

	account := MustAccountFrom(r.Context())
	projectID := r.PathValue("projectID")

	// The subdomain is derived from the project slug, so the project has to be
	// read before the environment can be created.
	project, err := a.Store.GetProject(r.Context(), account.ID, projectID)
	if err != nil {
		writeStoreError(w, r, err, "project not found")
		return
	}

	subdomain, err := domain.Subdomain(project.Slug, in.Name)
	if err != nil {
		writeValidationError(w, r, err)
		return
	}

	environment, err := a.Store.CreateEnvironment(r.Context(), account.ID, projectID, in, subdomain)
	if err != nil {
		writeStoreError(w, r, err, "project not found")
		return
	}
	JSON(w, r, http.StatusCreated, environment)
}

func (a *API) handleListEnvironments(w http.ResponseWriter, r *http.Request) {
	account := MustAccountFrom(r.Context())

	environments, err := a.Store.ListEnvironments(r.Context(), account.ID, r.PathValue("projectID"))
	if err != nil {
		writeStoreError(w, r, err, "project not found")
		return
	}
	JSON(w, r, http.StatusOK, map[string]any{"environments": environments})
}

// --- Deployments ---

func (a *API) handleCreateDeployment(w http.ResponseWriter, r *http.Request) {
	var in domain.CreateDeploymentInput
	if !decodeJSON(w, r, &in) {
		return
	}

	account := MustAccountFrom(r.Context())
	deployment, err := a.Store.CreateDeployment(r.Context(), account.ID, in)
	if err != nil {
		writeStoreError(w, r, err,
			"the service and environment must both exist and belong to the same project")
		return
	}

	w.Header().Set("Location", "/v1/deployments/"+deployment.ID)
	JSON(w, r, http.StatusCreated, deployment)
}

func (a *API) handleGetDeployment(w http.ResponseWriter, r *http.Request) {
	account := MustAccountFrom(r.Context())

	deployment, err := a.Store.GetDeployment(r.Context(), account.ID, r.PathValue("deploymentID"))
	if err != nil {
		writeStoreError(w, r, err, "deployment not found")
		return
	}
	JSON(w, r, http.StatusOK, deployment)
}

func (a *API) handleListDeployments(w http.ResponseWriter, r *http.Request) {
	account := MustAccountFrom(r.Context())

	deployments, err := a.Store.ListDeployments(r.Context(), account.ID, r.PathValue("projectID"))
	if err != nil {
		writeStoreError(w, r, err, "project not found")
		return
	}
	JSON(w, r, http.StatusOK, map[string]any{"deployments": deployments})
}

// handleDeploymentLogs returns build output. The `after` query parameter lets
// a client poll for only what it has not seen, so following a running build
// does not mean re-fetching the whole log each time.
func (a *API) handleDeploymentLogs(w http.ResponseWriter, r *http.Request) {
	account := MustAccountFrom(r.Context())

	afterSeq := 0
	if raw := r.URL.Query().Get("after"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 {
			Error(w, r, http.StatusBadRequest, "invalid_parameter",
				"after must be a non-negative integer")
			return
		}
		afterSeq = parsed
	}

	logs, err := a.Store.ListDeploymentLogs(r.Context(), account.ID, r.PathValue("deploymentID"), afterSeq)
	if err != nil {
		writeStoreError(w, r, err, "deployment not found")
		return
	}

	// next_after tells the client where to resume, so it never has to reason
	// about sequence numbers itself.
	nextAfter := afterSeq
	if len(logs) > 0 {
		nextAfter = logs[len(logs)-1].Seq
	}

	JSON(w, r, http.StatusOK, map[string]any{
		"logs":       logs,
		"next_after": nextAfter,
	})
}

// --- API keys ---

func (a *API) handleCreateAPIKey(w http.ResponseWriter, r *http.Request) {
	var in domain.CreateAPIKeyInput
	if !decodeJSON(w, r, &in) {
		return
	}

	account := MustAccountFrom(r.Context())
	key, token, err := a.Store.CreateAPIKey(r.Context(), account.ID, in.Name)
	if err != nil {
		writeStoreError(w, r, err, "account not found")
		return
	}

	// The only response that ever carries the plaintext token.
	JSON(w, r, http.StatusCreated, map[string]any{
		"api_key": key,
		"token":   token,
		"warning": "store this token now; it cannot be retrieved again",
	})
}

func (a *API) handleListAPIKeys(w http.ResponseWriter, r *http.Request) {
	account := MustAccountFrom(r.Context())

	keys, err := a.Store.ListAPIKeys(r.Context(), account.ID)
	if err != nil {
		writeStoreError(w, r, err, "account not found")
		return
	}
	JSON(w, r, http.StatusOK, map[string]any{"api_keys": keys})
}

func (a *API) handleRevokeAPIKey(w http.ResponseWriter, r *http.Request) {
	account := MustAccountFrom(r.Context())

	if err := a.Store.RevokeAPIKey(r.Context(), account.ID, r.PathValue("keyID")); err != nil {
		writeStoreError(w, r, err, "api key not found or already revoked")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
