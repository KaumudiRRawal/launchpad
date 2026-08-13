package store

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// migrationLockID namespaces the Postgres advisory lock the migrator holds.
// Any arbitrary constant works as long as nothing else in the database uses it.
const migrationLockID int64 = 8675309

type migration struct {
	version int
	name    string
	sql     string
}

// Migrate applies every migration in fsys that has not run yet, in version
// order, each inside its own transaction. It takes a session-level advisory
// lock first so that several control-plane replicas booting at once cannot
// apply the same migration twice.
func Migrate(ctx context.Context, pool *pgxpool.Pool, fsys fs.FS, log *slog.Logger) error {
	migrations, err := loadMigrations(fsys)
	if err != nil {
		return err
	}

	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire migration connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, migrationLockID); err != nil {
		return fmt.Errorf("acquire migration lock: %w", err)
	}
	defer func() {
		// Uses context.WithoutCancel so the lock is still released when the
		// caller's context was cancelled mid-migration.
		if _, err := conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, migrationLockID); err != nil {
			log.Error("release migration lock", slog.String("error", err.Error()))
		}
	}()

	if _, err := conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    INTEGER     PRIMARY KEY,
			name       TEXT        NOT NULL,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	applied, err := appliedVersions(ctx, conn)
	if err != nil {
		return err
	}

	count := 0
	for _, m := range migrations {
		if applied[m.version] {
			continue
		}

		tx, err := conn.Begin(ctx)
		if err != nil {
			return fmt.Errorf("begin migration %d: %w", m.version, err)
		}
		if _, err := tx.Exec(ctx, m.sql); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("apply migration %04d_%s: %w", m.version, m.name, err)
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO schema_migrations (version, name) VALUES ($1, $2)`,
			m.version, m.name); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("record migration %d: %w", m.version, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit migration %d: %w", m.version, err)
		}

		log.Info("applied migration", slog.Int("version", m.version), slog.String("name", m.name))
		count++
	}

	if count == 0 {
		log.Info("schema up to date", slog.Int("version", highestVersion(migrations)))
	}
	return nil
}

// loadMigrations reads and validates every *.sql file, which must be named
// <version>_<name>.sql, e.g. 0001_init.sql.
func loadMigrations(fsys fs.FS) ([]migration, error) {
	entries, err := fs.Glob(fsys, "*.sql")
	if err != nil {
		return nil, fmt.Errorf("list migrations: %w", err)
	}

	seen := make(map[int]string, len(entries))
	out := make([]migration, 0, len(entries))
	for _, entry := range entries {
		base := strings.TrimSuffix(path.Base(entry), ".sql")
		version, name, ok := strings.Cut(base, "_")
		if !ok {
			return nil, fmt.Errorf("migration %q must be named <version>_<name>.sql", entry)
		}
		v, err := strconv.Atoi(version)
		if err != nil {
			return nil, fmt.Errorf("migration %q has a non-numeric version: %w", entry, err)
		}
		if prior, dup := seen[v]; dup {
			return nil, fmt.Errorf("migrations %q and %q share version %d", prior, entry, v)
		}
		seen[v] = entry

		body, err := fs.ReadFile(fsys, entry)
		if err != nil {
			return nil, fmt.Errorf("read migration %q: %w", entry, err)
		}
		out = append(out, migration{version: v, name: name, sql: string(body)})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	return out, nil
}

func appliedVersions(ctx context.Context, conn *pgxpool.Conn) (map[int]bool, error) {
	rows, err := conn.Query(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("read applied migrations: %w", err)
	}
	defer rows.Close()

	applied := make(map[int]bool)
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, fmt.Errorf("scan applied migration: %w", err)
		}
		applied[v] = true
	}
	return applied, rows.Err()
}

func highestVersion(migrations []migration) int {
	if len(migrations) == 0 {
		return 0
	}
	return migrations[len(migrations)-1].version
}
