package proxy

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KaumudiRRawal/launchpad/control-plane/internal/domain"
)

// stubResolver serves a fixed routing table and counts lookups, so a test can
// tell a cache hit from a miss.
type stubResolver struct {
	upstreams map[string]domain.Upstream
	err       error
	calls     atomic.Int32
}

func (s *stubResolver) ResolveUpstream(_ context.Context, subdomain string) (domain.Upstream, error) {
	s.calls.Add(1)
	if s.err != nil {
		return domain.Upstream{}, s.err
	}
	upstream, ok := s.upstreams[subdomain]
	if !ok {
		return domain.Upstream{}, domain.ErrNotFound
	}
	return upstream, nil
}

func newProxy(resolver Resolver, baseDomain string) *Proxy {
	return &Proxy{
		Resolver:   resolver,
		Log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		BaseDomain: baseDomain,
	}
}

func TestSubdomainFor(t *testing.T) {
	p := newProxy(&stubResolver{}, "localhost")

	tests := []struct {
		name string
		host string
		want string
		ok   bool
	}{
		{name: "simple", host: "pr-42-demo.localhost", want: "pr-42-demo", ok: true},
		{name: "with port", host: "pr-42-demo.localhost:8081", want: "pr-42-demo", ok: true},
		{name: "uppercase is normalised", host: "PR-42-Demo.LOCALHOST", want: "pr-42-demo", ok: true},
		{name: "trailing dot is tolerated", host: "demo.localhost.", want: "demo", ok: true},

		{name: "base domain alone", host: "localhost", ok: false},
		{name: "empty label", host: ".localhost", ok: false},
		{name: "different domain", host: "example.com", ok: false},
		{name: "no host", host: "", ok: false},
		// Refused rather than guessed at: "a.b.localhost" must not silently
		// route to environment "b".
		{name: "nested label", host: "a.b.localhost", ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := p.subdomainFor(tt.host)
			if ok != tt.ok {
				t.Fatalf("subdomainFor(%q) ok = %v, want %v", tt.host, ok, tt.ok)
			}
			if ok && got != tt.want {
				t.Errorf("subdomainFor(%q) = %q, want %q", tt.host, got, tt.want)
			}
		})
	}
}

func TestProxyRoutesToTheRightEnvironment(t *testing.T) {
	// Two environments of the same service. Each must receive only its own
	// traffic: this is the isolation promise, observable from outside.
	production := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "production")
	}))
	defer production.Close()

	preview := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "preview")
	}))
	defer preview.Close()

	resolver := &stubResolver{upstreams: map[string]domain.Upstream{
		"production-demo": {EnvironmentID: "env-prod", DeploymentID: "deploy-prod", URL: production.URL},
		"pr-42-demo":      {EnvironmentID: "env-prev", DeploymentID: "deploy-prev", URL: preview.URL},
	}}
	p := newProxy(resolver, "localhost")

	for host, want := range map[string]string{
		"production-demo.localhost": "production",
		"pr-42-demo.localhost":      "preview",
	} {
		req := httptest.NewRequest(http.MethodGet, "http://"+host+"/", nil)
		req.Host = host
		rec := httptest.NewRecorder()

		p.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200 (body: %s)", host, rec.Code, rec.Body)
		}
		if got := rec.Body.String(); got != want {
			t.Errorf("%s served %q, want %q", host, got, want)
		}
		if got := rec.Header().Get("X-Launchpad-Deployment"); got == "" {
			t.Errorf("%s: response does not identify the serving deployment", host)
		}
	}
}

func TestProxyErrorMapping(t *testing.T) {
	tests := []struct {
		name       string
		host       string
		resolver   *stubResolver
		wantStatus int
	}{
		{
			name:       "hostname carries no environment",
			host:       "localhost",
			resolver:   &stubResolver{},
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "environment does not exist",
			host:       "nope.localhost",
			resolver:   &stubResolver{upstreams: map[string]domain.Upstream{}},
			wantStatus: http.StatusNotFound,
		},
		{
			// Distinct from 404 on purpose: this environment exists and will
			// have an upstream shortly, so the caller should retry.
			name:       "environment has nothing live yet",
			host:       "demo.localhost",
			resolver:   &stubResolver{err: domain.ErrNoLiveDeployment},
			wantStatus: http.StatusServiceUnavailable,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := newProxy(tt.resolver, "localhost")

			req := httptest.NewRequest(http.MethodGet, "http://"+tt.host+"/", nil)
			req.Host = tt.host
			rec := httptest.NewRecorder()

			p.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if tt.wantStatus == http.StatusServiceUnavailable && rec.Header().Get("Retry-After") == "" {
				t.Error("503 response is missing Retry-After")
			}
		})
	}
}

func TestProxyReturnsBadGatewayWhenWorkloadIsDown(t *testing.T) {
	// The workload is another tenant's code. It failing must surface as a
	// gateway error, not as a panic in the platform.
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadURL := dead.URL
	dead.Close()

	resolver := &stubResolver{upstreams: map[string]domain.Upstream{
		"demo": {EnvironmentID: "env-1", DeploymentID: "deploy-1", URL: deadURL},
	}}
	p := newProxy(resolver, "localhost")

	req := httptest.NewRequest(http.MethodGet, "http://demo.localhost/", nil)
	req.Host = "demo.localhost"
	rec := httptest.NewRecorder()

	p.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", rec.Code)
	}
}

func TestProxyCachesRoutes(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer backend.Close()

	resolver := &stubResolver{upstreams: map[string]domain.Upstream{
		"demo": {EnvironmentID: "env-1", DeploymentID: "deploy-1", URL: backend.URL},
	}}
	p := newProxy(resolver, "localhost")
	p.CacheTTL = time.Minute

	send := func() {
		req := httptest.NewRequest(http.MethodGet, "http://demo.localhost/", nil)
		req.Host = "demo.localhost"
		p.ServeHTTP(httptest.NewRecorder(), req)
	}

	for range 5 {
		send()
	}
	if got := resolver.calls.Load(); got != 1 {
		t.Errorf("resolver called %d times for 5 requests, want 1", got)
	}

	// A new release must take traffic immediately rather than waiting out the
	// cache, which is what Invalidate is for.
	p.Invalidate("demo")
	send()
	if got := resolver.calls.Load(); got != 2 {
		t.Errorf("resolver called %d times after Invalidate, want 2", got)
	}
}

func TestProxyDoesNotCacheFailures(t *testing.T) {
	// An environment with nothing live yet is a temporary state. Caching it
	// would keep refusing traffic after the deployment came up.
	resolver := &stubResolver{err: domain.ErrNoLiveDeployment}
	p := newProxy(resolver, "localhost")
	p.CacheTTL = time.Minute

	for range 3 {
		req := httptest.NewRequest(http.MethodGet, "http://demo.localhost/", nil)
		req.Host = "demo.localhost"
		p.ServeHTTP(httptest.NewRecorder(), req)
	}

	if got := resolver.calls.Load(); got != 3 {
		t.Errorf("resolver called %d times, want 3: a failed lookup must not be cached", got)
	}
}
