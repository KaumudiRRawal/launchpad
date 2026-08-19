package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/KaumudiRRawal/launchpad/control-plane/internal/domain"
)

// TestResolveUpstream covers the three answers the proxy has to tell apart: a
// hostname that was never minted, an environment with nothing serving yet, and
// a live deployment.
func TestResolveUpstream(t *testing.T) {
	repo, pool := newTestRepo(t)
	ctx := context.Background()

	account := newAccount(t, repo, "upstream")
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM accounts WHERE id = $1`, account.ID)
	})

	project, err := repo.CreateProject(ctx, account.ID, domain.CreateProjectInput{
		Slug: "routing", Name: "Routing",
		RepoURL: "https://github.com/example/routing", DefaultBranch: "main",
	})
	if err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}

	service, err := repo.CreateService(ctx, account.ID, project.ID,
		domain.CreateServiceInput{Name: "api", SourcePath: ".", Port: 8080})
	if err != nil {
		t.Fatalf("CreateService() error = %v", err)
	}

	env, err := repo.CreateEnvironment(ctx, account.ID, project.ID,
		domain.CreateEnvironmentInput{Kind: domain.EnvironmentProduction, Name: "production"},
		"production-routing")
	if err != nil {
		t.Fatalf("CreateEnvironment() error = %v", err)
	}

	t.Run("unknown subdomain is not found", func(t *testing.T) {
		_, err := repo.ResolveUpstream(ctx, "nobody-minted-this")
		if !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("ResolveUpstream() error = %v, want ErrNotFound", err)
		}
	})

	t.Run("environment with nothing live is distinguishable", func(t *testing.T) {
		// The proxy answers 503 here and 404 above, so these must not collapse
		// into the same error.
		_, err := repo.ResolveUpstream(ctx, "production-routing")
		if !errors.Is(err, domain.ErrNoLiveDeployment) {
			t.Errorf("ResolveUpstream() error = %v, want ErrNoLiveDeployment", err)
		}
	})

	const sha = "0123456789abcdef0123456789abcdef01234567"

	deploy := func(t *testing.T, internalURL string) string {
		t.Helper()

		d, err := repo.CreateDeployment(ctx, account.ID, domain.CreateDeploymentInput{
			ServiceID: service.ID, EnvironmentID: env.ID, CommitSHA: sha,
		})
		if err != nil {
			t.Fatalf("CreateDeployment() error = %v", err)
		}
		for _, step := range []struct{ from, to domain.DeploymentStatus }{
			{domain.DeploymentQueued, domain.DeploymentBuilding},
			{domain.DeploymentBuilding, domain.DeploymentDeploying},
		} {
			if err := repo.TransitionDeployment(ctx, d.ID, step.from, step.to); err != nil {
				t.Fatalf("TransitionDeployment(%s->%s) error = %v", step.from, step.to, err)
			}
		}
		if err := repo.MarkDeploymentLive(ctx, d.ID, "image:tag",
			"http://production-routing.localhost:8081", internalURL); err != nil {
			t.Fatalf("MarkDeploymentLive() error = %v", err)
		}
		return d.ID
	}

	firstID := deploy(t, "http://localhost:31000")

	t.Run("live deployment resolves to its internal address", func(t *testing.T) {
		upstream, err := repo.ResolveUpstream(ctx, "production-routing")
		if err != nil {
			t.Fatalf("ResolveUpstream() error = %v", err)
		}
		if upstream.DeploymentID != firstID {
			t.Errorf("DeploymentID = %q, want %q", upstream.DeploymentID, firstID)
		}
		if upstream.URL != "http://localhost:31000" {
			t.Errorf("URL = %q, want the internal address", upstream.URL)
		}
		if upstream.EnvironmentID != env.ID {
			t.Errorf("EnvironmentID = %q, want %q", upstream.EnvironmentID, env.ID)
		}
	})

	t.Run("a redeploy moves traffic to the newest live deployment", func(t *testing.T) {
		secondID := deploy(t, "http://localhost:31001")
		if err := repo.SupersedePriorDeployments(ctx, service.ID, env.ID, secondID); err != nil {
			t.Fatalf("SupersedePriorDeployments() error = %v", err)
		}

		upstream, err := repo.ResolveUpstream(ctx, "production-routing")
		if err != nil {
			t.Fatalf("ResolveUpstream() error = %v", err)
		}
		if upstream.DeploymentID != secondID {
			t.Errorf("DeploymentID = %q, want the newest deployment %q", upstream.DeploymentID, secondID)
		}
		if upstream.URL != "http://localhost:31001" {
			t.Errorf("URL = %q, want the new internal address", upstream.URL)
		}
	})
}

// TestResolveUpstreamIgnoresOtherEnvironments is the routing half of the
// isolation promise: a subdomain must never resolve to another environment's
// workload, even within one project.
func TestResolveUpstreamIgnoresOtherEnvironments(t *testing.T) {
	repo, pool := newTestRepo(t)
	ctx := context.Background()

	account := newAccount(t, repo, "isolation-routing")
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM accounts WHERE id = $1`, account.ID)
	})

	project, err := repo.CreateProject(ctx, account.ID, domain.CreateProjectInput{
		Slug: "iso", Name: "Iso",
		RepoURL: "https://github.com/example/iso", DefaultBranch: "main",
	})
	if err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}

	service, err := repo.CreateService(ctx, account.ID, project.ID,
		domain.CreateServiceInput{Name: "api", SourcePath: ".", Port: 8080})
	if err != nil {
		t.Fatalf("CreateService() error = %v", err)
	}

	newEnv := func(kind domain.EnvironmentKind, name, subdomain string) domain.Environment {
		t.Helper()
		e, err := repo.CreateEnvironment(ctx, account.ID, project.ID,
			domain.CreateEnvironmentInput{Kind: kind, Name: name}, subdomain)
		if err != nil {
			t.Fatalf("CreateEnvironment(%q) error = %v", name, err)
		}
		return e
	}

	prod := newEnv(domain.EnvironmentProduction, "production", "production-iso")
	preview := newEnv(domain.EnvironmentPreview, "pr-42", "pr-42-iso")

	releaseInto := func(t *testing.T, environmentID, internalURL string) {
		t.Helper()
		d, err := repo.CreateDeployment(ctx, account.ID, domain.CreateDeploymentInput{
			ServiceID: service.ID, EnvironmentID: environmentID,
			CommitSHA: "0123456789abcdef0123456789abcdef01234567",
		})
		if err != nil {
			t.Fatalf("CreateDeployment() error = %v", err)
		}
		_ = repo.TransitionDeployment(ctx, d.ID, domain.DeploymentQueued, domain.DeploymentBuilding)
		_ = repo.TransitionDeployment(ctx, d.ID, domain.DeploymentBuilding, domain.DeploymentDeploying)
		if err := repo.MarkDeploymentLive(ctx, d.ID, "image:tag", "http://public", internalURL); err != nil {
			t.Fatalf("MarkDeploymentLive() error = %v", err)
		}
	}

	releaseInto(t, prod.ID, "http://localhost:31100")
	releaseInto(t, preview.ID, "http://localhost:31101")

	for subdomain, wantURL := range map[string]string{
		"production-iso": "http://localhost:31100",
		"pr-42-iso":      "http://localhost:31101",
	} {
		upstream, err := repo.ResolveUpstream(ctx, subdomain)
		if err != nil {
			t.Fatalf("ResolveUpstream(%q) error = %v", subdomain, err)
		}
		if upstream.URL != wantURL {
			t.Errorf("%s resolved to %q, want %q — traffic crossed environments",
				subdomain, upstream.URL, wantURL)
		}
	}
}
