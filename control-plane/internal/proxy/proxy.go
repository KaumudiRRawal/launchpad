// Package proxy routes incoming requests to the workload serving the
// environment named in the request's hostname.
//
// This is where environment isolation becomes visible from outside: a preview
// and a production deployment of the same service answer on different
// hostnames and are reached through different upstreams, so no request can
// arrive at the wrong one by accident.
package proxy

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/KaumudiRRawal/launchpad/control-plane/internal/domain"
)

// Resolver maps a subdomain to the workload currently serving it.
type Resolver interface {
	ResolveUpstream(ctx context.Context, subdomain string) (domain.Upstream, error)
}

// Proxy forwards requests to deployed workloads.
type Proxy struct {
	Resolver Resolver
	Log      *slog.Logger
	// BaseDomain is the suffix stripped from a Host header to recover the
	// environment's subdomain, e.g. "localhost" or "launchpad.dev".
	BaseDomain string
	// CacheTTL bounds how long a resolved upstream is reused. Short, because a
	// deployment replaces its predecessor and traffic must follow within a
	// deploy's worth of time rather than a cache's.
	CacheTTL time.Duration

	mu    sync.RWMutex
	cache map[string]cacheEntry
}

type cacheEntry struct {
	upstream domain.Upstream
	proxy    *httputil.ReverseProxy
	expires  time.Time
}

const defaultCacheTTL = 5 * time.Second

func (p *Proxy) ttl() time.Duration {
	if p.CacheTTL > 0 {
		return p.CacheTTL
	}
	return defaultCacheTTL
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	subdomain, ok := p.subdomainFor(r.Host)
	if !ok {
		http.Error(w, "no environment in the request hostname", http.StatusNotFound)
		return
	}

	target, err := p.upstreamFor(r.Context(), subdomain)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		http.Error(w, "no such environment", http.StatusNotFound)
		return
	case errors.Is(err, domain.ErrNoLiveDeployment):
		// The environment exists but nothing is serving it yet. This is
		// temporary by nature, so it must not be cached as a 404 and the
		// client is told it is worth retrying.
		w.Header().Set("Retry-After", "5")
		http.Error(w, "no deployment is live for this environment yet", http.StatusServiceUnavailable)
		return
	case err != nil:
		p.Log.Error("resolve upstream",
			slog.String("subdomain", subdomain),
			slog.String("error", err.Error()))
		http.Error(w, "could not route this request", http.StatusBadGateway)
		return
	}

	target.ServeHTTP(w, r)
}

// subdomainFor extracts the environment label from a Host header.
func (p *Proxy) subdomainFor(host string) (string, bool) {
	// Host carries a port when the proxy is not on 80; the routing decision
	// never depends on it.
	if h, _, found := strings.Cut(host, ":"); found {
		host = h
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))

	base := strings.ToLower(p.BaseDomain)
	suffix := "." + base
	if !strings.HasSuffix(host, suffix) {
		return "", false
	}

	subdomain := strings.TrimSuffix(host, suffix)
	// Only a single label is a valid environment subdomain. Anything deeper is
	// refused rather than guessed at, so "a.b.localhost" cannot silently route
	// to environment "b".
	if subdomain == "" || strings.Contains(subdomain, ".") {
		return "", false
	}
	return subdomain, true
}

// upstreamFor returns a reverse proxy for a subdomain, reusing a cached one
// while it is fresh. Resolving on every request would put a database query in
// front of every byte of proxied traffic.
func (p *Proxy) upstreamFor(ctx context.Context, subdomain string) (*httputil.ReverseProxy, error) {
	p.mu.RLock()
	entry, found := p.cache[subdomain]
	p.mu.RUnlock()

	if found && time.Now().Before(entry.expires) {
		return entry.proxy, nil
	}

	upstream, err := p.Resolver.ResolveUpstream(ctx, subdomain)
	if err != nil {
		return nil, err
	}

	target, err := url.Parse(upstream.URL)
	if err != nil {
		return nil, err
	}

	reverse := httputil.NewSingleHostReverseProxy(target)

	// The workload is another tenant's code, so a failure inside it must
	// surface as a gateway error rather than a panic in the platform.
	reverse.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		p.Log.Warn("upstream request failed",
			slog.String("subdomain", subdomain),
			slog.String("upstream", upstream.URL),
			slog.String("error", err.Error()))
		http.Error(w, "the deployed application did not respond", http.StatusBadGateway)
	}

	// Identify the deployment actually serving a response. When two
	// deployments differ only in behaviour, this is what tells you which one
	// you reached.
	reverse.ModifyResponse = func(resp *http.Response) error {
		resp.Header.Set("X-Launchpad-Deployment", upstream.DeploymentID)
		return nil
	}

	p.mu.Lock()
	if p.cache == nil {
		p.cache = make(map[string]cacheEntry)
	}
	p.cache[subdomain] = cacheEntry{
		upstream: upstream,
		proxy:    reverse,
		expires:  time.Now().Add(p.ttl()),
	}
	p.mu.Unlock()

	return reverse, nil
}

// Invalidate drops a cached route, so a freshly released deployment takes
// traffic immediately instead of after the cache expires.
func (p *Proxy) Invalidate(subdomain string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.cache, subdomain)
}
