package httpx

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/KaumudiRRawal/launchpad/control-plane/internal/domain"
)

const (
	testToken     = "lp_deadbeef_secret"
	testAccountID = "account-1"
)

// fakeStore is an in-memory Store. It models the two behaviours the handlers
// actually depend on — ownership scoping and slug uniqueness — so the tests
// exercise real decisions rather than a mock that always succeeds.
type fakeStore struct {
	projects       []domain.Project
	deploymentLogs []domain.DeploymentLog
	nextID         int

	// Overrides for cases that are awkward to reach naturally.
	authErr   error
	createErr error
}

func newFakeStore() *fakeStore {
	return &fakeStore{nextID: 1}
}

func (f *fakeStore) ListDeploymentLogs(_ context.Context, _, _ string, afterSeq int) ([]domain.DeploymentLog, error) {
	out := []domain.DeploymentLog{}
	for _, l := range f.deploymentLogs {
		if l.Seq > afterSeq {
			out = append(out, l)
		}
	}
	return out, nil
}

func (f *fakeStore) Authenticate(_ context.Context, token string) (domain.Account, error) {
	if f.authErr != nil {
		return domain.Account{}, f.authErr
	}
	if token != testToken {
		return domain.Account{}, domain.ErrNotFound
	}
	return domain.Account{ID: testAccountID, Email: "dev@example.com", Name: "Dev"}, nil
}

func (f *fakeStore) CreateProject(_ context.Context, accountID string, in domain.CreateProjectInput) (domain.Project, error) {
	if f.createErr != nil {
		return domain.Project{}, f.createErr
	}
	for _, p := range f.projects {
		if p.AccountID == accountID && p.Slug == in.Slug {
			return domain.Project{}, domain.ErrConflict
		}
	}

	f.nextID++
	p := domain.Project{
		ID:            "project-" + strconv.Itoa(f.nextID),
		AccountID:     accountID,
		Slug:          in.Slug,
		Name:          in.Name,
		RepoURL:       in.RepoURL,
		DefaultBranch: in.DefaultBranch,
	}
	f.projects = append(f.projects, p)
	return p, nil
}

func (f *fakeStore) GetProject(_ context.Context, accountID, id string) (domain.Project, error) {
	for _, p := range f.projects {
		if p.ID == id && p.AccountID == accountID {
			return p, nil
		}
	}
	return domain.Project{}, domain.ErrNotFound
}

func (f *fakeStore) ListProjects(_ context.Context, accountID string) ([]domain.Project, error) {
	out := []domain.Project{}
	for _, p := range f.projects {
		if p.AccountID == accountID {
			out = append(out, p)
		}
	}
	return out, nil
}

func (f *fakeStore) DeleteProject(_ context.Context, accountID, id string) error {
	for i, p := range f.projects {
		if p.ID == id && p.AccountID == accountID {
			f.projects = append(f.projects[:i], f.projects[i+1:]...)
			return nil
		}
	}
	return domain.ErrNotFound
}

func (f *fakeStore) CreateService(_ context.Context, _, projectID string, in domain.CreateServiceInput) (domain.Service, error) {
	return domain.Service{ID: "service-1", ProjectID: projectID, Name: in.Name, SourcePath: in.SourcePath, Port: in.Port}, nil
}

func (f *fakeStore) ListServices(context.Context, string, string) ([]domain.Service, error) {
	return []domain.Service{}, nil
}

func (f *fakeStore) CreateEnvironment(_ context.Context, _, projectID string, in domain.CreateEnvironmentInput, subdomain string) (domain.Environment, error) {
	return domain.Environment{ID: "env-1", ProjectID: projectID, Kind: in.Kind, Name: in.Name, Subdomain: subdomain}, nil
}

func (f *fakeStore) ListEnvironments(context.Context, string, string) ([]domain.Environment, error) {
	return []domain.Environment{}, nil
}

func (f *fakeStore) CreateDeployment(_ context.Context, _ string, in domain.CreateDeploymentInput) (domain.Deployment, error) {
	return domain.Deployment{
		ID: "deploy-1", ServiceID: in.ServiceID, EnvironmentID: in.EnvironmentID,
		CommitSHA: in.CommitSHA, Status: domain.DeploymentQueued,
	}, nil
}

func (f *fakeStore) GetDeployment(context.Context, string, string) (domain.Deployment, error) {
	return domain.Deployment{}, domain.ErrNotFound
}

func (f *fakeStore) ListDeployments(context.Context, string, string) ([]domain.Deployment, error) {
	return []domain.Deployment{}, nil
}

func (f *fakeStore) CreateAPIKey(_ context.Context, accountID, name string) (domain.APIKey, string, error) {
	return domain.APIKey{ID: "key-1", AccountID: accountID, Name: name, TokenPrefix: "lp_deadbeef"}, testToken, nil
}

func (f *fakeStore) ListAPIKeys(context.Context, string) ([]domain.APIKey, error) {
	return []domain.APIKey{}, nil
}

func (f *fakeStore) RevokeAPIKey(context.Context, string, string) error { return nil }

func newAPI(store Store) http.Handler {
	api := &API{
		DB:      stubPinger{},
		Store:   store,
		Log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version: "test",
	}
	return api.Routes()
}

// request builds an authenticated request unless token is empty.
func request(t *testing.T, srv http.Handler, method, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()

	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(encoded)
	}

	req := httptest.NewRequest(method, path, reader)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

func TestAuthenticationIsRequired(t *testing.T) {
	srv := newAPI(newFakeStore())

	tests := []struct {
		name       string
		authHeader string
		wantStatus int
	}{
		{name: "no header", authHeader: "", wantStatus: http.StatusUnauthorized},
		{name: "wrong scheme", authHeader: "Basic abc123", wantStatus: http.StatusUnauthorized},
		{name: "empty credential", authHeader: "Bearer ", wantStatus: http.StatusUnauthorized},
		{name: "unknown token", authHeader: "Bearer lp_nope_nope", wantStatus: http.StatusUnauthorized},
		{name: "valid token", authHeader: "Bearer " + testToken, wantStatus: http.StatusOK},
		// RFC 7235 defines the scheme as case-insensitive and real clients
		// send it lowercase.
		{name: "lowercase scheme", authHeader: "bearer " + testToken, wantStatus: http.StatusOK},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/v1/projects", nil)
			if tt.authHeader != "" {
				req.Header.Set("Authorization", tt.authHeader)
			}

			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d (body: %s)", rec.Code, tt.wantStatus, rec.Body)
			}
			if rec.Code == http.StatusUnauthorized && rec.Header().Get("WWW-Authenticate") == "" {
				t.Error("401 response is missing the WWW-Authenticate header")
			}
		})
	}
}

func TestPublicRoutesNeedNoToken(t *testing.T) {
	srv := newAPI(newFakeStore())

	for _, path := range []string{"/healthz", "/readyz", "/v1/version"} {
		t.Run(path, func(t *testing.T) {
			rec := request(t, srv, http.MethodGet, path, "", nil)
			if rec.Code != http.StatusOK {
				t.Errorf("status = %d, want 200", rec.Code)
			}
		})
	}
}

func TestCreateProject(t *testing.T) {
	t.Run("creates and returns a Location header", func(t *testing.T) {
		srv := newAPI(newFakeStore())

		rec := request(t, srv, http.MethodPost, "/v1/projects", testToken, map[string]any{
			"slug":     "demo",
			"name":     "Demo",
			"repo_url": "https://github.com/example/demo",
		})

		if rec.Code != http.StatusCreated {
			t.Fatalf("status = %d, want 201 (body: %s)", rec.Code, rec.Body)
		}

		var project domain.Project
		if err := json.NewDecoder(rec.Body).Decode(&project); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if project.Slug != "demo" {
			t.Errorf("slug = %q, want demo", project.Slug)
		}
		if project.DefaultBranch != "main" {
			t.Errorf("default_branch = %q, want main", project.DefaultBranch)
		}
		if loc := rec.Header().Get("Location"); loc != "/v1/projects/"+project.ID {
			t.Errorf("Location = %q, want /v1/projects/%s", loc, project.ID)
		}
	})

	t.Run("reports field-level validation problems", func(t *testing.T) {
		srv := newAPI(newFakeStore())

		rec := request(t, srv, http.MethodPost, "/v1/projects", testToken, map[string]any{
			"slug":     "Not A Slug",
			"name":     "",
			"repo_url": "git@github.com:example/demo.git",
		})

		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("status = %d, want 422 (body: %s)", rec.Code, rec.Body)
		}

		var body struct {
			Error struct {
				Code   string              `json:"code"`
				Fields []map[string]string `json:"fields"`
			} `json:"error"`
		}
		if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if body.Error.Code != "validation_failed" {
			t.Errorf("code = %q, want validation_failed", body.Error.Code)
		}
		if len(body.Error.Fields) != 3 {
			t.Errorf("got %d field errors, want 3: %v", len(body.Error.Fields), body.Error.Fields)
		}
	})

	t.Run("rejects unknown fields", func(t *testing.T) {
		// A typo in a field name should fail loudly rather than be silently
		// dropped, leaving the caller wondering why nothing changed.
		srv := newAPI(newFakeStore())

		rec := request(t, srv, http.MethodPost, "/v1/projects", testToken, map[string]any{
			"slug":      "demo",
			"name":      "Demo",
			"repo_url":  "https://github.com/example/demo",
			"repo_urll": "typo",
		})

		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400 (body: %s)", rec.Code, rec.Body)
		}
	})

	t.Run("requires a body", func(t *testing.T) {
		srv := newAPI(newFakeStore())

		rec := request(t, srv, http.MethodPost, "/v1/projects", testToken, nil)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rec.Code)
		}
	})

	t.Run("refuses an oversized body", func(t *testing.T) {
		srv := newAPI(newFakeStore())

		req := httptest.NewRequest(http.MethodPost, "/v1/projects",
			strings.NewReader(`{"name":"`+strings.Repeat("a", 2<<20)+`"}`))
		req.Header.Set("Authorization", "Bearer "+testToken)

		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)

		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("status = %d, want 413", rec.Code)
		}
	})

	t.Run("maps a duplicate slug to 409", func(t *testing.T) {
		srv := newAPI(newFakeStore())
		body := map[string]any{
			"slug":     "demo",
			"name":     "Demo",
			"repo_url": "https://github.com/example/demo",
		}

		if rec := request(t, srv, http.MethodPost, "/v1/projects", testToken, body); rec.Code != http.StatusCreated {
			t.Fatalf("first create: status = %d, want 201", rec.Code)
		}
		if rec := request(t, srv, http.MethodPost, "/v1/projects", testToken, body); rec.Code != http.StatusConflict {
			t.Errorf("second create: status = %d, want 409", rec.Code)
		}
	})
}

func TestProjectsAreScopedToTheAccount(t *testing.T) {
	// A project belonging to another account is reported as not found rather
	// than forbidden, so a caller cannot confirm that the ID exists at all.
	store := newFakeStore()
	store.projects = append(store.projects, domain.Project{
		ID:        "someone-elses",
		AccountID: "another-account",
		Slug:      "secret",
	})
	srv := newAPI(store)

	rec := request(t, srv, http.MethodGet, "/v1/projects/someone-elses", testToken, nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("GET status = %d, want 404", rec.Code)
	}

	rec = request(t, srv, http.MethodDelete, "/v1/projects/someone-elses", testToken, nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("DELETE status = %d, want 404", rec.Code)
	}

	rec = request(t, srv, http.MethodGet, "/v1/projects", testToken, nil)
	if !strings.Contains(rec.Body.String(), `"projects":[]`) {
		t.Errorf("list leaked another account's project: %s", rec.Body)
	}
}

func TestDeleteProject(t *testing.T) {
	store := newFakeStore()
	store.projects = append(store.projects, domain.Project{ID: "mine", AccountID: testAccountID, Slug: "mine"})
	srv := newAPI(store)

	rec := request(t, srv, http.MethodDelete, "/v1/projects/mine", testToken, nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}

	// Deleting twice must not report success the second time.
	rec = request(t, srv, http.MethodDelete, "/v1/projects/mine", testToken, nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("second delete: status = %d, want 404", rec.Code)
	}
}

func TestCreateEnvironmentDerivesSubdomain(t *testing.T) {
	store := newFakeStore()
	store.projects = append(store.projects, domain.Project{
		ID: "p1", AccountID: testAccountID, Slug: "demo",
	})
	srv := newAPI(store)

	rec := request(t, srv, http.MethodPost, "/v1/projects/p1/environments", testToken, map[string]any{
		"kind": "preview",
		"name": "pr-42",
	})

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body: %s)", rec.Code, rec.Body)
	}

	var env domain.Environment
	if err := json.NewDecoder(rec.Body).Decode(&env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Subdomain != "pr-42-demo" {
		t.Errorf("subdomain = %q, want pr-42-demo", env.Subdomain)
	}
}

func TestCreateDeploymentValidatesCommitSHA(t *testing.T) {
	srv := newAPI(newFakeStore())

	rec := request(t, srv, http.MethodPost, "/v1/deployments", testToken, map[string]any{
		"service_id":     "svc",
		"environment_id": "env",
		"commit_sha":     "abc1234",
	})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("short SHA: status = %d, want 422", rec.Code)
	}

	rec = request(t, srv, http.MethodPost, "/v1/deployments", testToken, map[string]any{
		"service_id":     "svc",
		"environment_id": "env",
		"commit_sha":     "0123456789abcdef0123456789abcdef01234567",
	})
	if rec.Code != http.StatusCreated {
		t.Errorf("full SHA: status = %d, want 201 (body: %s)", rec.Code, rec.Body)
	}
}

func TestCreateAPIKeyReturnsPlaintextOnce(t *testing.T) {
	srv := newAPI(newFakeStore())

	rec := request(t, srv, http.MethodPost, "/v1/api-keys", testToken, map[string]any{"name": "CI"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body: %s)", rec.Code, rec.Body)
	}

	var body struct {
		Token  string        `json:"token"`
		APIKey domain.APIKey `json:"api_key"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Token == "" {
		t.Error("token is empty; creation is the only chance to return it")
	}

	// The listing must never expose the secret.
	rec = request(t, srv, http.MethodGet, "/v1/api-keys", testToken, nil)
	if strings.Contains(rec.Body.String(), body.Token) {
		t.Error("list response leaked the plaintext token")
	}
}

func TestDeploymentLogsResumeFromAfterCursor(t *testing.T) {
	// Following a running build means polling, so the endpoint must return
	// only what the client has not already seen.
	store := newFakeStore()
	store.deploymentLogs = []domain.DeploymentLog{
		{Seq: 1, Stream: "stdout", Message: "fetching"},
		{Seq: 2, Stream: "stdout", Message: "building"},
		{Seq: 3, Stream: "stdout", Message: "live"},
	}
	srv := newAPI(store)

	var first struct {
		Logs      []domain.DeploymentLog `json:"logs"`
		NextAfter int                    `json:"next_after"`
	}
	rec := request(t, srv, http.MethodGet, "/v1/deployments/d1/logs", testToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body)
	}
	if err := json.NewDecoder(rec.Body).Decode(&first); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(first.Logs) != 3 {
		t.Errorf("got %d lines, want 3", len(first.Logs))
	}
	if first.NextAfter != 3 {
		t.Errorf("next_after = %d, want 3", first.NextAfter)
	}

	var second struct {
		Logs []domain.DeploymentLog `json:"logs"`
	}
	rec = request(t, srv, http.MethodGet, "/v1/deployments/d1/logs?after=2", testToken, nil)
	if err := json.NewDecoder(rec.Body).Decode(&second); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(second.Logs) != 1 || second.Logs[0].Seq != 3 {
		t.Errorf("after=2 returned %v, want only seq 3", second.Logs)
	}
}

func TestDeploymentLogsRejectsBadCursor(t *testing.T) {
	srv := newAPI(newFakeStore())

	for _, cursor := range []string{"abc", "-1"} {
		rec := request(t, srv, http.MethodGet, "/v1/deployments/d1/logs?after="+cursor, testToken, nil)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("after=%q: status = %d, want 400", cursor, rec.Code)
		}
	}
}
