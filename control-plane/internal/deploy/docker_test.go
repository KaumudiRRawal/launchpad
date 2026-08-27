package deploy

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// TestExcludeGitMetadata covers the rule the Docker driver writes into a build
// context. The integration test proves the exclusion works against a real
// daemon; this one covers what happens to a repository's own rules, which is
// where the damage would be done and which needs no daemon to check.
func TestExcludeGitMetadata(t *testing.T) {
	tests := []struct {
		name     string
		hasGit   bool
		existing string // "" means no .dockerignore in the context
		want     string // "" means the file should not exist afterwards
	}{
		{
			// A service built from a subdirectory of a monorepo: the metadata
			// sits above the context, so there is nothing to exclude and no
			// reason to leave a file behind saying so.
			name:   "no metadata leaves the context untouched",
			hasGit: false,
			want:   "",
		},
		{
			name:   "metadata and no rules writes just the one rule",
			hasGit: true,
			want:   ".git\n",
		},
		{
			name:     "existing rules are kept",
			hasGit:   true,
			existing: "node_modules\n*.log\n",
			want:     "node_modules\n*.log\n.git\n",
		},
		{
			// Appending to a file that does not end in a newline would
			// otherwise produce "*.log.git", excluding neither.
			name:     "a missing trailing newline is supplied",
			hasGit:   true,
			existing: "*.log",
			want:     "*.log\n.git\n",
		},
		{
			// Later patterns win in a .dockerignore, so appending is also what
			// stops a repository re-including the metadata under us.
			name:     "our rule goes last",
			hasGit:   true,
			existing: ".git\n!.git\n",
			want:     ".git\n!.git\n.git\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if tt.hasGit {
				if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
					t.Fatalf("create .git: %v", err)
				}
			}
			if tt.existing != "" {
				writeFile(t, dir, ".dockerignore", tt.existing)
			}

			if err := excludeGitMetadata(dir); err != nil {
				t.Fatalf("excludeGitMetadata() error = %v", err)
			}

			got, err := os.ReadFile(filepath.Join(dir, ".dockerignore"))
			switch {
			case tt.want == "":
				if !errors.Is(err, fs.ErrNotExist) {
					t.Errorf("read .dockerignore = %q, %v; want it not to exist", got, err)
				}
			case err != nil:
				t.Fatalf("read .dockerignore: %v", err)
			case string(got) != tt.want:
				t.Errorf("\n got %q\nwant %q", got, tt.want)
			}
		})
	}
}
