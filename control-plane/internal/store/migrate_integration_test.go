package store_test

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/KaumudiRRawal/launchpad/control-plane/internal/store"
	"github.com/KaumudiRRawal/launchpad/control-plane/migrations"
)

// TestMigrateAgainstPostgres runs the real migrations against a real database.
// It is skipped unless LAUNCHPAD_TEST_DATABASE_URL is set, so `go test ./...`
// stays fast and dependency-free for anyone without Docker running. CI sets
// the variable, so the schema is verified on every push.
func TestMigrateAgainstPostgres(t *testing.T) {
	databaseURL := os.Getenv("LAUNCHPAD_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("LAUNCHPAD_TEST_DATABASE_URL not set; skipping database integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	pool, err := store.Connect(ctx, databaseURL, log)
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	defer pool.Close()

	// Start from a clean slate so the test is repeatable against a persistent
	// database, not just a throwaway CI container.
	if _, err := pool.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public;`); err != nil {
		t.Fatalf("reset schema: %v", err)
	}

	if err := store.Migrate(ctx, pool, migrations.FS, log); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}

	// Running twice must be a no-op; this is what makes redeploys safe.
	if err := store.Migrate(ctx, pool, migrations.FS, log); err != nil {
		t.Fatalf("Migrate() second run error = %v, want nil (migrations must be idempotent)", err)
	}

	wantTables := []string{
		"accounts", "projects", "services", "environments",
		"provisioned_databases", "deployments", "deployment_logs",
		"schema_migrations",
	}
	for _, table := range wantTables {
		var exists bool
		err := pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM information_schema.tables
				WHERE table_schema = 'public' AND table_name = $1
			)`, table).Scan(&exists)
		if err != nil {
			t.Fatalf("check table %q: %v", table, err)
		}
		if !exists {
			t.Errorf("table %q was not created by the migrations", table)
		}
	}
}

// TestProductionEnvironmentIsUnique guards the partial unique index that keeps
// a project from ending up with two production environments, which is the
// invariant the whole isolation model rests on.
func TestProductionEnvironmentIsUnique(t *testing.T) {
	databaseURL := os.Getenv("LAUNCHPAD_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("LAUNCHPAD_TEST_DATABASE_URL not set; skipping database integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	pool, err := store.Connect(ctx, databaseURL, log)
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	defer pool.Close()

	if err := store.Migrate(ctx, pool, migrations.FS, log); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}

	var accountID, projectID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO accounts (email, name) VALUES ($1, 'Test') RETURNING id`,
		"unique-prod-"+time.Now().Format("150405.000000")+"@example.com",
	).Scan(&accountID); err != nil {
		t.Fatalf("insert account: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM accounts WHERE id = $1`, accountID)
	})

	if err := pool.QueryRow(ctx,
		`INSERT INTO projects (account_id, slug, name, repo_url)
		 VALUES ($1, 'demo', 'Demo', 'https://github.com/example/demo') RETURNING id`,
		accountID,
	).Scan(&projectID); err != nil {
		t.Fatalf("insert project: %v", err)
	}

	insertProd := func(subdomain string) error {
		_, err := pool.Exec(ctx,
			`INSERT INTO environments (project_id, kind, name, subdomain)
			 VALUES ($1, 'production', $2, $3)`,
			projectID, "production-"+subdomain, subdomain)
		return err
	}

	if err := insertProd("demo-prod-a"); err != nil {
		t.Fatalf("first production environment should insert cleanly: %v", err)
	}
	if err := insertProd("demo-prod-b"); err == nil {
		t.Error("second production environment inserted, want a unique-violation error")
	}

	// Previews are unbounded, so several must coexist.
	for _, sub := range []string{"demo-pr-1", "demo-pr-2"} {
		if _, err := pool.Exec(ctx,
			`INSERT INTO environments (project_id, kind, name, subdomain)
			 VALUES ($1, 'preview', $2, $3)`,
			projectID, sub, sub); err != nil {
			t.Errorf("preview environment %q should insert cleanly: %v", sub, err)
		}
	}
}
