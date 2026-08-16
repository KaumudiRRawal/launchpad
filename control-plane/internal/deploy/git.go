package deploy

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// GitFetcher checks out a repository at a specific commit using the git CLI.
type GitFetcher struct {
	// Binary overrides the git executable, for testing.
	Binary string
}

func (g *GitFetcher) binary() string {
	if g.Binary != "" {
		return g.Binary
	}
	return "git"
}

// Fetch retrieves exactly the requested commit into destDir.
//
// It does not clone and then check out. A full clone of a large repository
// costs minutes and bandwidth to obtain history the build will never read, so
// this initialises an empty repository and fetches the single commit at depth
// 1 — the difference between a deployment that takes seconds and one that
// takes minutes.
func (g *GitFetcher) Fetch(ctx context.Context, repoURL, commitSHA, destDir string) error {
	// A URL that begins with a dash would be read by git as an option.
	if strings.HasPrefix(repoURL, "-") || strings.HasPrefix(commitSHA, "-") {
		return fmt.Errorf("refusing repository %q at commit %q: leading dash", repoURL, commitSHA)
	}

	steps := [][]string{
		{"init", "--quiet"},
		{"remote", "add", "origin", repoURL},
		{"fetch", "--quiet", "--depth", "1", "origin", commitSHA},
		{"checkout", "--quiet", "FETCH_HEAD"},
	}

	for _, args := range steps {
		cmd := exec.CommandContext(ctx, g.binary(), args...)
		cmd.Dir = destDir

		if out, err := cmd.CombinedOutput(); err != nil {
			// git writes the useful part of a failure to stderr, so it is
			// included rather than just the exit status.
			return fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}
