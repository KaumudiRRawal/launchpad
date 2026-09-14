package deploy

import (
	"context"
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

// newFakeDocker writes a stub docker that prints output on stdout and exits 0.
// The driver's only way to learn a dynamically chosen host port is to read the
// CLI back, so the parsing below is the contract, and a real daemon is not
// needed to hold it to one.
func newFakeDocker(t *testing.T, output string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "docker")
	script := "#!/bin/sh\ncat <<'EOF'\n" + output + "\nEOF\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake docker: %v", err)
	}
	return path
}

func TestPublishedPort(t *testing.T) {
	tests := []struct {
		name    string
		output  string
		want    string
		wantErr bool
	}{
		{
			name:   "a single IPv4 binding",
			output: "0.0.0.0:32770",
			want:   "32770",
		},
		{
			// What a stock dual-stack daemon prints: the port is the same on
			// both lines, so reading the first is right for the wrong reason.
			name:   "both bindings of a dual-stack daemon",
			output: "0.0.0.0:32770\n[::]:32770",
			want:   "32770",
		},
		{
			// An IPv6-only binding leaves this line as the only one there is.
			// Cutting at the first colon used to make the port ":]:32770" and
			// report no error, which became a URL of "http://localhost::]:32770".
			name:   "an IPv6-only binding",
			output: "[::]:32770",
			want:   "32770",
		},
		{
			name:   "an explicit IPv6 host address",
			output: "[fe80::1]:32770",
			want:   "32770",
		},
		{
			name:    "output with no binding at all",
			output:  "",
			wantErr: true,
		},
		{
			// Anything unrecognised has to stop the release rather than become
			// part of an address, however port-shaped the line looks.
			name:    "a line that is not a binding",
			output:  "no such container: demo",
			wantErr: true,
		},
		{
			name:    "an address with no port",
			output:  "0.0.0.0:",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			driver := &DockerDriver{Binary: newFakeDocker(t, tt.output)}

			got, err := driver.publishedPort(context.Background(), "demo", 8080)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("publishedPort() = %q, want an error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("publishedPort() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("publishedPort() = %q, want %q", got, tt.want)
			}
		})
	}
}
