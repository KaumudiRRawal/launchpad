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
	"time"

	"github.com/KaumudiRRawal/launchpad/control-plane/internal/analyze"
	"github.com/KaumudiRRawal/launchpad/control-plane/internal/domain"
	"github.com/KaumudiRRawal/launchpad/control-plane/internal/metrics"
)

const (
	testToken     = "lp_deadbeef_secret"
	testAccountID = "account-1"
	// The address the proxy answers on, so a rendered environment URL in a
	// test looks like the one a caller is given.
	testBaseDomain = "localhost"
	testProxyPort  = 8080
)

// fakeStore is an in-memory Store. It models the behaviours the handlers
// actually depend on — ownership scoping, slug uniqueness, and the difference
// between a query that reads through a join and one that writes through it —
// so the tests exercise real decisions rather than a mock that always
// succeeds. Where the real query's shape decides the answer a caller gets,
// the method here says which shape it is reproducing.
type fakeStore struct {
	projects       []domain.Project
	services       []domain.Service
	environments   []domain.Environment
	deployments    []domain.Deployment
	apiKeys        []domain.APIKey
	metricBuckets  []domain.MetricBucket
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

func (f *fakeStore) CreateService(_ context.Context, accountID, projectID string, in domain.CreateServiceInput) (domain.Service, error) {
	// The INSERT ... SELECT FROM projects finds no row to attach to when the
	// project is not the caller's, which reaches the handler as ErrNotFound.
	if !f.ownsProject(accountID, projectID) {
		return domain.Service{}, domain.ErrNotFound
	}

	f.nextID++
	s := domain.Service{
		ID:         "service-" + strconv.Itoa(f.nextID),
		ProjectID:  projectID,
		Name:       in.Name,
		SourcePath: in.SourcePath,
		Port:       in.Port,
	}
	f.services = append(f.services, s)
	return s, nil
}

func (f *fakeStore) ListServices(_ context.Context, accountID, projectID string) ([]domain.Service, error) {
	// Reading is a join rather than a lookup, so another account's project is
	// an empty result and not an error.
	out := []domain.Service{}
	if !f.ownsProject(accountID, projectID) {
		return out, nil
	}
	for _, s := range f.services {
		if s.ProjectID == projectID {
			out = append(out, s)
		}
	}
	return out, nil
}

func (f *fakeStore) CreateEnvironment(_ context.Context, _, projectID string, in domain.CreateEnvironmentInput, subdomain string) (domain.Environment, error) {
	e := domain.Environment{ID: "env-1", ProjectID: projectID, Kind: in.Kind, Name: in.Name, Subdomain: subdomain}
	f.environments = append(f.environments, e)
	return e, nil
}

func (f *fakeStore) ListEnvironments(_ context.Context, accountID, projectID string) ([]domain.Environment, error) {
	out := []domain.Environment{}
	if !f.ownsProject(accountID, projectID) {
		return out, nil
	}
	for _, e := range f.environments {
		if e.ProjectID == projectID {
			out = append(out, e)
		}
	}
	return out, nil
}

// ownsProject is the join every project-scoped query starts from.
func (f *fakeStore) ownsProject(accountID, projectID string) bool {
	for _, p := range f.projects {
		if p.ID == projectID && p.AccountID == accountID {
			return true
		}
	}
	return false
}

// owns mirrors what the real queries do in SQL: reach the environment through
// the project, so one account's ID never resolves another account's row.
func (f *fakeStore) owns(accountID, environmentID string) bool {
	for _, e := range f.environments {
		if e.ID != environmentID {
			continue
		}
		for _, p := range f.projects {
			if p.ID == e.ProjectID && p.AccountID == accountID {
				return true
			}
		}
	}
	return false
}

func (f *fakeStore) GetEnvironment(_ context.Context, accountID, id string) (domain.Environment, error) {
	if !f.owns(accountID, id) {
		return domain.Environment{}, domain.ErrNotFound
	}
	for _, e := range f.environments {
		if e.ID == id {
			return e, nil
		}
	}
	return domain.Environment{}, domain.ErrNotFound
}

func (f *fakeStore) EnvironmentMetrics(_ context.Context, accountID, environmentID string, since time.Time) ([]domain.MetricBucket, error) {
	if !f.owns(accountID, environmentID) {
		return nil, domain.ErrNotFound
	}
	out := []domain.MetricBucket{}
	for _, b := range f.metricBuckets {
		if b.EnvironmentID == environmentID && !b.Bucket.Before(since) {
			out = append(out, b)
		}
	}
	return out, nil
}

func (f *fakeStore) CreateDeployment(_ context.Context, _ string, in domain.CreateDeploymentInput) (domain.Deployment, error) {
	return domain.Deployment{
		ID: "deploy-1", ServiceID: in.ServiceID, EnvironmentID: in.EnvironmentID,
		CommitSHA: in.CommitSHA, Status: domain.DeploymentQueued,
	}, nil
}

// deploymentProject resolves a deployment to the project that owns it the way
// the real queries do, through its service.
func (f *fakeStore) deploymentProject(deploymentID string) (string, bool) {
	for _, d := range f.deployments {
		if d.ID != deploymentID {
			continue
		}
		for _, s := range f.services {
			if s.ID == d.ServiceID {
				return s.ProjectID, true
			}
		}
	}
	return "", false
}

func (f *fakeStore) GetDeployment(_ context.Context, accountID, id string) (domain.Deployment, error) {
	projectID, ok := f.deploymentProject(id)
	if !ok || !f.ownsProject(accountID, projectID) {
		return domain.Deployment{}, domain.ErrNotFound
	}
	for _, d := range f.deployments {
		if d.ID == id {
			return d, nil
		}
	}
	return domain.Deployment{}, domain.ErrNotFound
}

func (f *fakeStore) ListDeployments(_ context.Context, accountID, projectID string) ([]domain.Deployment, error) {
	out := []domain.Deployment{}
	if !f.ownsProject(accountID, projectID) {
		return out, nil
	}
	for _, d := range f.deployments {
		if p, ok := f.deploymentProject(d.ID); ok && p == projectID {
			out = append(out, d)
		}
	}
	return out, nil
}

func (f *fakeStore) CreateAPIKey(_ context.Context, accountID, name string) (domain.APIKey, string, error) {
	f.nextID++
	k := domain.APIKey{
		ID:          "key-" + strconv.Itoa(f.nextID),
		AccountID:   accountID,
		Name:        name,
		TokenPrefix: "lp_deadbeef",
	}
	f.apiKeys = append(f.apiKeys, k)
	return k, testToken, nil
}

func (f *fakeStore) ListAPIKeys(_ context.Context, accountID string) ([]domain.APIKey, error) {
	out := []domain.APIKey{}
	for _, k := range f.apiKeys {
		if k.AccountID == accountID {
			out = append(out, k)
		}
	}
	return out, nil
}

// RevokeAPIKey reproduces the real UPDATE's WHERE clause, revoked_at IS NULL
// included, so revoking twice reports not found here as well.
func (f *fakeStore) RevokeAPIKey(_ context.Context, accountID, id string) error {
	for i, k := range f.apiKeys {
		if k.ID != id || k.AccountID != accountID || k.RevokedAt != nil {
			continue
		}
		now := time.Now()
		f.apiKeys[i].RevokedAt = &now
		return nil
	}
	return domain.ErrNotFound
}

func newAPI(store Store) http.Handler {
	api := &API{
		DB:         stubPinger{},
		Store:      store,
		Log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version:    "test",
		BaseDomain: testBaseDomain,
		ProxyPort:  testProxyPort,
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

// seedEnvironment gives the fake store an environment the test account owns,
// reached through a project exactly as the real queries reach it.
func seedEnvironment(f *fakeStore) domain.Environment {
	project := domain.Project{ID: "project-metrics", AccountID: testAccountID, Slug: "demo", Name: "Demo"}
	f.projects = append(f.projects, project)

	environment := domain.Environment{
		ID: "env-metrics", ProjectID: project.ID,
		Kind: domain.EnvironmentProduction, Name: "production", Subdomain: "production-demo",
	}
	f.environments = append(f.environments, environment)
	return environment
}

// seedTraffic adds one minute of measurements, `minutesAgo` minutes back.
func seedTraffic(f *fakeStore, environmentID string, minutesAgo int, requests, failures int, latencyMS float64) {
	histogram := metrics.NewHistogram()
	for range requests {
		histogram.Observe(latencyMS)
	}

	f.metricBuckets = append(f.metricBuckets, domain.MetricBucket{
		DeploymentID:  "deploy-1",
		EnvironmentID: environmentID,
		CommitSHA:     strings.Repeat("a", 40),
		Bucket:        time.Now().UTC().Add(-time.Duration(minutesAgo) * time.Minute).Truncate(time.Minute),
		Requests:      int64(requests),
		Failures:      int64(failures),
		LatencySumMS:  latencyMS * float64(requests),
		LatencyMaxMS:  latencyMS,
		Histogram:     histogram,
	})
}

func TestEnvironmentMetricsSummarisesTheWindow(t *testing.T) {
	store := newFakeStore()
	environment := seedEnvironment(store)
	seedTraffic(store, environment.ID, 2, 100, 1, 30)
	seedTraffic(store, environment.ID, 1, 100, 1, 30)
	// Outside a one-hour window, so it must not reach the summary.
	seedTraffic(store, environment.ID, 200, 500, 400, 4000)

	srv := newAPI(store)
	rec := request(t, srv, http.MethodGet, "/v1/environments/"+environment.ID+"/metrics", testToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body)
	}

	var body domain.EnvironmentMetrics
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if body.EnvironmentID != environment.ID {
		t.Errorf("environment_id = %q, want %q", body.EnvironmentID, environment.ID)
	}
	if body.Window.Summary.Requests != 200 || body.Window.Summary.Failures != 2 {
		t.Errorf("summary = %d requests / %d failures, want 200/2 — the older minute leaked in",
			body.Window.Summary.Requests, body.Window.Summary.Failures)
	}
	if got := body.Window.Summary.Availability; got < 0.98 || got > 0.99 {
		t.Errorf("Availability = %v, want 0.99", got)
	}
	if got := body.Window.Summary.LatencyP95MS; got < 20 || got > 30 {
		t.Errorf("LatencyP95MS = %v, want it inside the 20–30ms bucket the traffic was in", got)
	}
	if len(body.Series) != 2 {
		t.Errorf("series holds %d points, want 2", len(body.Series))
	}
}

func TestEnvironmentMetricsOfAQuietEnvironment(t *testing.T) {
	store := newFakeStore()
	environment := seedEnvironment(store)

	srv := newAPI(store)
	rec := request(t, srv, http.MethodGet, "/v1/environments/"+environment.ID+"/metrics", testToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body)
	}

	// An environment with no traffic is a 200 with nothing in it, not a 404:
	// the environment exists, and "nobody has called it" is the answer.
	if !strings.Contains(rec.Body.String(), `"series":[]`) {
		t.Errorf("body = %s, want an empty series rather than null", rec.Body)
	}
}

func TestMeasurementIsScopedToTheOwner(t *testing.T) {
	store := newFakeStore()
	// An environment under a project belonging to somebody else. It has to be
	// indistinguishable from one that does not exist.
	store.projects = append(store.projects, domain.Project{ID: "project-theirs", AccountID: "account-2"})
	store.environments = append(store.environments,
		domain.Environment{ID: "env-theirs", ProjectID: "project-theirs"})

	srv := newAPI(store)
	for _, path := range []string{
		"/v1/environments/env-theirs/metrics",
		"/v1/environments/env-theirs/analysis",
		"/v1/environments/does-not-exist/metrics",
		"/v1/environments/does-not-exist/analysis",
	} {
		t.Run(path, func(t *testing.T) {
			rec := request(t, srv, http.MethodGet, path, testToken, nil)
			if rec.Code != http.StatusNotFound {
				t.Errorf("status = %d, want 404 (body: %s)", rec.Code, rec.Body)
			}
		})
	}
}

func TestWindowParameterIsBounded(t *testing.T) {
	store := newFakeStore()
	environment := seedEnvironment(store)
	srv := newAPI(store)

	tests := []struct {
		name       string
		query      string
		wantStatus int
	}{
		{name: "absent uses the default", query: "", wantStatus: http.StatusOK},
		{name: "a duration in range", query: "?window=30m", wantStatus: http.StatusOK},
		{name: "not a duration", query: "?window=lastweek", wantStatus: http.StatusBadRequest},
		// Below the floor a percentile is one or two requests.
		{name: "shorter than a minute", query: "?window=10s", wantStatus: http.StatusBadRequest},
		// Above the ceiling the answer holds more minutes than a caller can use.
		{name: "longer than a day", query: "?window=72h", wantStatus: http.StatusBadRequest},
		{name: "negative", query: "?window=-5m", wantStatus: http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := request(t, srv, http.MethodGet,
				"/v1/environments/"+environment.ID+"/metrics"+tt.query, testToken, nil)
			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d (body: %s)", rec.Code, tt.wantStatus, rec.Body)
			}
		})
	}
}

func TestEnvironmentAnalysisReadsBothWindows(t *testing.T) {
	store := newFakeStore()
	environment := seedEnvironment(store)

	// A slow release: an hour of fast traffic, then fifteen minutes of slow
	// traffic. The analysis has to reach back past its own window to see it.
	for minutesAgo := 16; minutesAgo <= 75; minutesAgo++ {
		seedTraffic(store, environment.ID, minutesAgo, 20, 0, 20)
	}
	for minutesAgo := 1; minutesAgo <= 15; minutesAgo++ {
		seedTraffic(store, environment.ID, minutesAgo, 20, 0, 200)
	}

	srv := newAPI(store)
	rec := request(t, srv, http.MethodGet, "/v1/environments/"+environment.ID+"/analysis", testToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body)
	}

	var report analyze.Report
	if err := json.NewDecoder(rec.Body).Decode(&report); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if report.Verdict != analyze.VerdictFailing {
		t.Errorf("Verdict = %q, want %q (detail: %s)", report.Verdict, analyze.VerdictFailing, report.Detail)
	}
	if report.Baseline.Summary.Requests == 0 {
		t.Error("the baseline window is empty; the handler did not read back far enough")
	}
	if len(report.Remediations) == 0 {
		t.Error("a failing verdict with nothing to do about it")
	}
}

func TestEnvironmentAnalysisOfAQuietEnvironment(t *testing.T) {
	store := newFakeStore()
	environment := seedEnvironment(store)

	srv := newAPI(store)
	rec := request(t, srv, http.MethodGet, "/v1/environments/"+environment.ID+"/analysis", testToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body)
	}

	var report analyze.Report
	if err := json.NewDecoder(rec.Body).Decode(&report); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Not "healthy": an environment nobody called has not been shown to work.
	if report.Verdict != analyze.VerdictInsufficientData {
		t.Errorf("Verdict = %q, want %q", report.Verdict, analyze.VerdictInsufficientData)
	}
}
