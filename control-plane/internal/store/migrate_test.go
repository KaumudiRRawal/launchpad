package store

import (
	"testing"
	"testing/fstest"
)

func TestLoadMigrations(t *testing.T) {
	t.Run("sorts by numeric version, not filename", func(t *testing.T) {
		// Lexical sorting would put 0010 before 0002; version order must win.
		fsys := fstest.MapFS{
			"0010_add_logs.sql": {Data: []byte("SELECT 10")},
			"0002_add_envs.sql": {Data: []byte("SELECT 2")},
			"0001_init.sql":     {Data: []byte("SELECT 1")},
		}

		got, err := loadMigrations(fsys)
		if err != nil {
			t.Fatalf("loadMigrations() error = %v", err)
		}
		if len(got) != 3 {
			t.Fatalf("got %d migrations, want 3", len(got))
		}

		wantVersions := []int{1, 2, 10}
		for i, want := range wantVersions {
			if got[i].version != want {
				t.Errorf("migration[%d].version = %d, want %d", i, got[i].version, want)
			}
		}
		if got[0].name != "init" {
			t.Errorf("migration[0].name = %q, want init", got[0].name)
		}
		if got[2].sql != "SELECT 10" {
			t.Errorf("migration[2].sql = %q, want SELECT 10", got[2].sql)
		}
	})

	t.Run("keeps underscores in the name", func(t *testing.T) {
		fsys := fstest.MapFS{"0003_add_deployment_logs.sql": {Data: []byte("SELECT 1")}}

		got, err := loadMigrations(fsys)
		if err != nil {
			t.Fatalf("loadMigrations() error = %v", err)
		}
		if got[0].name != "add_deployment_logs" {
			t.Errorf("name = %q, want add_deployment_logs", got[0].name)
		}
	})

	t.Run("rejects duplicate versions", func(t *testing.T) {
		fsys := fstest.MapFS{
			"0001_init.sql":  {Data: []byte("SELECT 1")},
			"0001_other.sql": {Data: []byte("SELECT 2")},
		}

		if _, err := loadMigrations(fsys); err == nil {
			t.Fatal("loadMigrations() error = nil, want a duplicate-version error")
		}
	})

	t.Run("rejects malformed filenames", func(t *testing.T) {
		for _, name := range []string{"init.sql", "abc_init.sql"} {
			fsys := fstest.MapFS{name: {Data: []byte("SELECT 1")}}
			if _, err := loadMigrations(fsys); err == nil {
				t.Errorf("loadMigrations(%q) error = nil, want an error", name)
			}
		}
	})

	t.Run("empty directory is not an error", func(t *testing.T) {
		got, err := loadMigrations(fstest.MapFS{})
		if err != nil {
			t.Fatalf("loadMigrations() error = %v", err)
		}
		if len(got) != 0 {
			t.Errorf("got %d migrations, want 0", len(got))
		}
	})
}

func TestHighestVersion(t *testing.T) {
	if got := highestVersion(nil); got != 0 {
		t.Errorf("highestVersion(nil) = %d, want 0", got)
	}
	sorted := []migration{{version: 1}, {version: 7}}
	if got := highestVersion(sorted); got != 7 {
		t.Errorf("highestVersion() = %d, want 7", got)
	}
}
