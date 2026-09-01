package store_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/KaumudiRRawal/launchpad/control-plane/internal/domain"
	"github.com/KaumudiRRawal/launchpad/control-plane/internal/store"
	"github.com/KaumudiRRawal/launchpad/control-plane/migrations"
)

// newTestRepo returns a repository against the test database, skipping the
// test when one is not configured.
func newTestRepo(t *testing.T) (*store.Repository, *pgxpool.Pool) {
	t.Helper()

	databaseURL := os.Getenv("LAUNCHPAD_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("LAUNCHPAD_TEST_DATABASE_URL not set; skipping database integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	pool, err := store.Connect(ctx, databaseURL, log)
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	t.Cleanup(pool.Close)

	if err := store.Migrate(ctx, pool, migrations.FS, log); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	return store.NewRepository(pool), pool
}

// uniqueEmail keeps parallel and repeated runs from colliding on the accounts
// unique index.
func uniqueEmail(prefix string) string {
	return fmt.Sprintf("%s-%d@example.com", prefix, time.Now().UnixNano())
}

func newAccount(t *testing.T, repo *store.Repository, prefix string) domain.Account {
	t.Helper()

	account, err := repo.CreateAccount(context.Background(), uniqueEmail(prefix), "Test Account")
	if err != nil {
		t.Fatalf("CreateAccount() error = %v", err)
	}
	return account
}

func TestProjectOwnershipIsEnforced(t *testing.T) {
	repo, pool := newTestRepo(t)
	ctx := context.Background()

	owner := newAccount(t, repo, "owner")
	intruder := newAccount(t, repo, "intruder")
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM accounts WHERE id = ANY($1)`,
			[]string{owner.ID, intruder.ID})
	})

	project, err := repo.CreateProject(ctx, owner.ID, domain.CreateProjectInput{
		Slug: "demo", Name: "Demo",
		RepoURL: "https://github.com/example/demo", DefaultBranch: "main",
	})
	if err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}

	t.Run("owner can read it", func(t *testing.T) {
		if _, err := repo.GetProject(ctx, owner.ID, project.ID); err != nil {
			t.Errorf("GetProject() as owner error = %v, want nil", err)
		}
	})

	t.Run("another account cannot read it even knowing the ID", func(t *testing.T) {
		_, err := repo.GetProject(ctx, intruder.ID, project.ID)
		if !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("GetProject() as intruder error = %v, want ErrNotFound", err)
		}
	})

	t.Run("another account cannot delete it", func(t *testing.T) {
		if err := repo.DeleteProject(ctx, intruder.ID, project.ID); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("DeleteProject() as intruder error = %v, want ErrNotFound", err)
		}
		// And it is genuinely still there.
		if _, err := repo.GetProject(ctx, owner.ID, project.ID); err != nil {
			t.Errorf("project was deleted by a non-owner: %v", err)
		}
	})

	t.Run("listing is scoped to the account", func(t *testing.T) {
		projects, err := repo.ListProjects(ctx, intruder.ID)
		if err != nil {
			t.Fatalf("ListProjects() error = %v", err)
		}
		if len(projects) != 0 {
			t.Errorf("intruder sees %d projects, want 0", len(projects))
		}
	})

	t.Run("duplicate slug within an account conflicts", func(t *testing.T) {
		_, err := repo.CreateProject(ctx, owner.ID, domain.CreateProjectInput{
			Slug: "demo", Name: "Demo Again",
			RepoURL: "https://github.com/example/other", DefaultBranch: "main",
		})
		if !errors.Is(err, domain.ErrConflict) {
			t.Errorf("CreateProject() with duplicate slug error = %v, want ErrConflict", err)
		}
	})

	t.Run("the same slug is free for a different account", func(t *testing.T) {
		created, err := repo.CreateProject(ctx, intruder.ID, domain.CreateProjectInput{
			Slug: "demo", Name: "Their Demo",
			RepoURL: "https://github.com/example/theirs", DefaultBranch: "main",
		})
		if err != nil {
			t.Fatalf("CreateProject() for second account error = %v, want nil", err)
		}
		if created.AccountID != intruder.ID {
			t.Errorf("project belongs to %s, want %s", created.AccountID, intruder.ID)
		}
	})
}

// TestDeploymentCannotCrossProjects is the test behind the platform's
// isolation promise: a service must not be deployable into an environment
// belonging to a different project, even when the caller owns both.
func TestDeploymentCannotCrossProjects(t *testing.T) {
	repo, pool := newTestRepo(t)
	ctx := context.Background()

	account := newAccount(t, repo, "isolation")
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM accounts WHERE id = $1`, account.ID)
	})

	newProject := func(slug string) domain.Project {
		t.Helper()
		p, err := repo.CreateProject(ctx, account.ID, domain.CreateProjectInput{
			Slug: slug, Name: slug,
			RepoURL: "https://github.com/example/" + slug, DefaultBranch: "main",
		})
		if err != nil {
			t.Fatalf("CreateProject(%q) error = %v", slug, err)
		}
		return p
	}

	alpha := newProject("alpha")
	beta := newProject("beta")

	alphaService, err := repo.CreateService(ctx, account.ID, alpha.ID,
		domain.CreateServiceInput{Name: "api", SourcePath: ".", Port: 8080})
	if err != nil {
		t.Fatalf("CreateService() error = %v", err)
	}

	alphaEnv, err := repo.CreateEnvironment(ctx, account.ID, alpha.ID,
		domain.CreateEnvironmentInput{Kind: domain.EnvironmentProduction, Name: "production"},
		"production-alpha")
	if err != nil {
		t.Fatalf("CreateEnvironment(alpha) error = %v", err)
	}

	betaEnv, err := repo.CreateEnvironment(ctx, account.ID, beta.ID,
		domain.CreateEnvironmentInput{Kind: domain.EnvironmentProduction, Name: "production"},
		"production-beta")
	if err != nil {
		t.Fatalf("CreateEnvironment(beta) error = %v", err)
	}

	const commitSHA = "0123456789abcdef0123456789abcdef01234567"

	t.Run("same project succeeds", func(t *testing.T) {
		deployment, err := repo.CreateDeployment(ctx, account.ID, domain.CreateDeploymentInput{
			ServiceID: alphaService.ID, EnvironmentID: alphaEnv.ID, CommitSHA: commitSHA,
		})
		if err != nil {
			t.Fatalf("CreateDeployment() within one project error = %v, want nil", err)
		}
		if deployment.Status != domain.DeploymentQueued {
			t.Errorf("status = %q, want queued", deployment.Status)
		}
	})

	t.Run("crossing projects is refused", func(t *testing.T) {
		_, err := repo.CreateDeployment(ctx, account.ID, domain.CreateDeploymentInput{
			ServiceID: alphaService.ID, EnvironmentID: betaEnv.ID, CommitSHA: commitSHA,
		})
		if !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("CreateDeployment() across projects error = %v, want ErrNotFound", err)
		}
	})

	t.Run("another account cannot deploy at all", func(t *testing.T) {
		outsider := newAccount(t, repo, "outsider")
		t.Cleanup(func() {
			_, _ = pool.Exec(context.Background(), `DELETE FROM accounts WHERE id = $1`, outsider.ID)
		})

		_, err := repo.CreateDeployment(ctx, outsider.ID, domain.CreateDeploymentInput{
			ServiceID: alphaService.ID, EnvironmentID: alphaEnv.ID, CommitSHA: commitSHA,
		})
		if !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("CreateDeployment() as outsider error = %v, want ErrNotFound", err)
		}
	})
}

func TestAPIKeyLifecycle(t *testing.T) {
	repo, pool := newTestRepo(t)
	ctx := context.Background()

	account := newAccount(t, repo, "keys")
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM accounts WHERE id = $1`, account.ID)
	})

	key, token, err := repo.CreateAPIKey(ctx, account.ID, "CI")
	if err != nil {
		t.Fatalf("CreateAPIKey() error = %v", err)
	}

	t.Run("plaintext is never stored", func(t *testing.T) {
		var count int
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM api_keys WHERE token_hash = $1 OR token_prefix = $1`,
			token).Scan(&count); err != nil {
			t.Fatalf("query: %v", err)
		}
		if count != 0 {
			t.Error("the plaintext token appears in the api_keys table")
		}
	})

	t.Run("the token authenticates", func(t *testing.T) {
		got, err := repo.Authenticate(ctx, token)
		if err != nil {
			t.Fatalf("Authenticate() error = %v", err)
		}
		if got.ID != account.ID {
			t.Errorf("authenticated as %s, want %s", got.ID, account.ID)
		}
	})

	t.Run("a wrong token does not", func(t *testing.T) {
		if _, err := repo.Authenticate(ctx, token+"x"); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("Authenticate() with a bad token error = %v, want ErrNotFound", err)
		}
		if _, err := repo.Authenticate(ctx, ""); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("Authenticate() with an empty token error = %v, want ErrNotFound", err)
		}
	})

	t.Run("revoking stops authentication immediately", func(t *testing.T) {
		if err := repo.RevokeAPIKey(ctx, account.ID, key.ID); err != nil {
			t.Fatalf("RevokeAPIKey() error = %v", err)
		}
		if _, err := repo.Authenticate(ctx, token); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("Authenticate() after revocation error = %v, want ErrNotFound", err)
		}
	})

	t.Run("revoking twice reports not found", func(t *testing.T) {
		if err := repo.RevokeAPIKey(ctx, account.ID, key.ID); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("second RevokeAPIKey() error = %v, want ErrNotFound", err)
		}
	})

	t.Run("another account cannot revoke a key it does not own", func(t *testing.T) {
		other := newAccount(t, repo, "other")
		t.Cleanup(func() {
			_, _ = pool.Exec(context.Background(), `DELETE FROM accounts WHERE id = $1`, other.ID)
		})

		fresh, freshToken, err := repo.CreateAPIKey(ctx, account.ID, "second")
		if err != nil {
			t.Fatalf("CreateAPIKey() error = %v", err)
		}

		if err := repo.RevokeAPIKey(ctx, other.ID, fresh.ID); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("RevokeAPIKey() as another account error = %v, want ErrNotFound", err)
		}
		if _, err := repo.Authenticate(ctx, freshToken); err != nil {
			t.Errorf("key was revoked by a non-owner: %v", err)
		}
	})
}

// TestProjectContentsAreScopedToTheAccount covers the queries that read what
// is inside a project. Each of them reaches the account by joining through
// projects and filtering there, so losing that join would hand another
// account's services, environments and deployments to anyone who learned a
// project ID — and every handler test would still pass, because the handlers
// are exercised against a fake store and never run this SQL.
func TestProjectContentsAreScopedToTheAccount(t *testing.T) {
	repo, pool := newTestRepo(t)
	ctx := context.Background()

	owner := newAccount(t, repo, "contents-owner")
	intruder := newAccount(t, repo, "contents-intruder")
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM accounts WHERE id = ANY($1)`,
			[]string{owner.ID, intruder.ID})
	})

	project, err := repo.CreateProject(ctx, owner.ID, domain.CreateProjectInput{
		Slug: "contents", Name: "Contents",
		RepoURL: "https://github.com/example/contents", DefaultBranch: "main",
	})
	if err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}

	service, err := repo.CreateService(ctx, owner.ID, project.ID,
		domain.CreateServiceInput{Name: "api", SourcePath: ".", Port: 8080})
	if err != nil {
		t.Fatalf("CreateService() error = %v", err)
	}

	// Subdomains are unique across the whole platform, not per project, so a
	// literal here would collide with whatever an earlier run left behind.
	environment, err := repo.CreateEnvironment(ctx, owner.ID, project.ID,
		domain.CreateEnvironmentInput{Kind: domain.EnvironmentProduction, Name: "production"},
		fmt.Sprintf("production-contents-%d", time.Now().UnixNano()))
	if err != nil {
		t.Fatalf("CreateEnvironment() error = %v", err)
	}

	if _, err := repo.CreateDeployment(ctx, owner.ID, domain.CreateDeploymentInput{
		ServiceID:     service.ID,
		EnvironmentID: environment.ID,
		CommitSHA:     "0123456789abcdef0123456789abcdef01234567",
	}); err != nil {
		t.Fatalf("CreateDeployment() error = %v", err)
	}

	// Each list takes the same two identifiers and, for a project the caller
	// does not own, must come back empty rather than not found: the project ID
	// is the caller's own input, and answering "no such project" would confirm
	// that someone else's project exists.
	lists := []struct {
		name  string
		count func(accountID string) (int, error)
	}{
		{"services", func(accountID string) (int, error) {
			got, err := repo.ListServices(ctx, accountID, project.ID)
			return len(got), err
		}},
		{"environments", func(accountID string) (int, error) {
			got, err := repo.ListEnvironments(ctx, accountID, project.ID)
			return len(got), err
		}},
		{"deployments", func(accountID string) (int, error) {
			got, err := repo.ListDeployments(ctx, accountID, project.ID)
			return len(got), err
		}},
	}

	for _, tt := range lists {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.count(owner.ID)
			if err != nil {
				t.Fatalf("listing %s as owner error = %v", tt.name, err)
			}
			if got != 1 {
				t.Errorf("owner sees %d %s, want 1", got, tt.name)
			}

			got, err = tt.count(intruder.ID)
			if err != nil {
				t.Fatalf("listing %s as intruder error = %v, want nil", tt.name, err)
			}
			if got != 0 {
				t.Errorf("intruder sees %d %s, want 0", got, tt.name)
			}
		})
	}

	// GetEnvironment is the lookup behind the metrics and analysis endpoints,
	// which are addressed by environment ID alone and so never pass through a
	// project the caller was shown.
	t.Run("environment by id", func(t *testing.T) {
		if _, err := repo.GetEnvironment(ctx, owner.ID, environment.ID); err != nil {
			t.Errorf("GetEnvironment() as owner error = %v, want nil", err)
		}
		if _, err := repo.GetEnvironment(ctx, intruder.ID, environment.ID); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("GetEnvironment() as intruder error = %v, want ErrNotFound", err)
		}
	})
}
