package deploy

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// fakeGit stands in for the git CLI and records what it was asked to do.
//
// The fetcher's contract is the sequence of commands it issues — an empty
// repository and one commit at depth 1, never a clone — so that sequence is
// what these tests assert on. The integration test proves a real fetch is
// shallow, but it needs a network and is kept out of the default run, which
// left the argument handling here with no cover at all.
//
// Every argument is recorded on its own line, so a flag stays distinguishable
// from a value that happens to look like one.
type fakeGit struct {
	path string
	log  string
}

// newFakeGit writes a stub git that fails on the subcommand named by failOn.
// Passing "" fails nothing, since a real invocation always has a subcommand.
func newFakeGit(t *testing.T, failOn string) *fakeGit {
	t.Helper()

	dir := t.TempDir()
	f := &fakeGit{
		path: filepath.Join(dir, "git"),
		log:  filepath.Join(dir, "invocations"),
	}

	// The marker lands in whatever directory the fetcher ran the command in,
	// which is how the test checks it ran there: comparing paths would fail on
	// macOS, where a temporary directory is reached through a symlink.
	script := `#!/bin/sh
: > ./git-ran-here
{ for arg in "$@"; do printf '%s\n' "$arg"; done; printf '=== end ===\n'; } >> "` + f.log + `"
if [ "$1" = "` + failOn + `" ]; then
	echo "fatal: could not read from remote repository" >&2
	exit 128
fi
exit 0
`
	if err := os.WriteFile(f.path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake git: %v", err)
	}
	return f
}

// invocations returns one slice of arguments per call the fetcher made, or nil
// if it never reached git.
func (f *fakeGit) invocations(t *testing.T) [][]string {
	t.Helper()

	raw, err := os.ReadFile(f.log)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("read invocations: %v", err)
	}

	var calls [][]string
	var current []string
	for _, line := range strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n") {
		if line == "=== end ===" {
			calls = append(calls, current)
			current = nil
			continue
		}
		current = append(current, line)
	}
	return calls
}

const (
	testRepoURL = "https://github.com/example/demo"
	testCommit  = "1a2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d"
)

func TestGitFetcherIssuesAShallowFetch(t *testing.T) {
	fake := newFakeGit(t, "")
	fetcher := &GitFetcher{Binary: fake.path}
	dir := t.TempDir()

	if err := fetcher.Fetch(context.Background(), testRepoURL, testCommit, dir); err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}

	// Asserted as the whole sequence rather than as individual flags, because
	// the absence of a clone is as much the point as the presence of --depth:
	// an extra step that fetched more history would still pass a test that only
	// looked for the depth flag.
	want := [][]string{
		{"init", "--quiet"},
		{"remote", "add", "origin", testRepoURL},
		{"fetch", "--quiet", "--depth", "1", "origin", testCommit},
		{"checkout", "--quiet", "FETCH_HEAD"},
	}
	got := fake.invocations(t)
	if len(got) != len(want) {
		t.Fatalf("made %d git calls, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if !slices.Equal(got[i], want[i]) {
			t.Errorf("call %d = %v, want %v", i, got[i], want[i])
		}
	}

	// Every step has to run inside the destination. A git run one directory up
	// would initialise a repository somewhere else and hand the build an empty
	// tree without failing.
	if _, err := os.Stat(filepath.Join(dir, "git-ran-here")); err != nil {
		t.Errorf("git did not run in the destination directory: %v", err)
	}
}

func TestGitFetcherRefusesArgumentsThatLookLikeOptions(t *testing.T) {
	tests := []struct {
		name    string
		repoURL string
		commit  string
	}{
		{
			// git would read this as an option to fetch rather than as a
			// remote, and --upload-pack names a command to run on the far end.
			name:    "dashed repository url",
			repoURL: "--upload-pack=touch /tmp/pwned",
			commit:  testCommit,
		},
		{
			name:    "dashed commit",
			repoURL: testRepoURL,
			commit:  "--upload-pack=touch /tmp/pwned",
		},
		{
			name:    "both dashed",
			repoURL: "-x",
			commit:  "-y",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := newFakeGit(t, "")
			fetcher := &GitFetcher{Binary: fake.path}

			if err := fetcher.Fetch(context.Background(), tt.repoURL, tt.commit, t.TempDir()); err == nil {
				t.Fatal("Fetch() = nil, want an error")
			}

			// The rejection is only worth anything if it happens before the
			// process starts: git validating its own arguments is exactly what
			// this guard exists not to rely on.
			if calls := fake.invocations(t); calls != nil {
				t.Errorf("git was invoked with a rejected argument: %v", calls)
			}
		})
	}
}

func TestGitFetcherStopsAtTheFirstFailingStep(t *testing.T) {
	tests := []struct {
		name   string
		failOn string
		// calls counts the invocations expected including the failing one.
		calls int
	}{
		{name: "init", failOn: "init", calls: 1},
		{name: "remote add", failOn: "remote", calls: 2},
		{name: "unknown commit", failOn: "fetch", calls: 3},
		{name: "checkout", failOn: "checkout", calls: 4},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := newFakeGit(t, tt.failOn)
			fetcher := &GitFetcher{Binary: fake.path}

			err := fetcher.Fetch(context.Background(), testRepoURL, testCommit, t.TempDir())
			if err == nil {
				t.Fatal("Fetch() = nil, want an error")
			}

			// The subcommand and git's own words both have to survive into the
			// error: it is the only account of the failure the deployment gets,
			// and "exit status 128" alone does not distinguish a commit that
			// does not exist from a remote that could not be reached.
			if !strings.Contains(err.Error(), "git "+tt.failOn) {
				t.Errorf("error = %v, want it to name the %s step", err, tt.failOn)
			}
			if !strings.Contains(err.Error(), "could not read from remote repository") {
				t.Errorf("error = %v, want it to carry git's output", err)
			}

			// A step that failed leaves the checkout in a state the next step
			// would misread — a failed fetch followed by a checkout would
			// resolve FETCH_HEAD to whatever was there before.
			if got := len(fake.invocations(t)); got != tt.calls {
				t.Errorf("made %d git calls, want %d", got, tt.calls)
			}
		})
	}
}
