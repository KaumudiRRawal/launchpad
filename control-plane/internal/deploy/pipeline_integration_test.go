package deploy

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/KaumudiRRawal/launchpad/control-plane/internal/domain"
)

// TestPipelineEndToEnd runs a commit all the way to a container answering HTTP,
// with nothing faked but the database.
//
// The engine tests cover the orchestration against fakes and the driver test
// covers the part that talks to Docker. Neither covers the seam: that a real
// shallow fetch produces a tree real detection recognises, that the Dockerfile
// detection generated actually builds, and that the container the release
// started serves the port the engine told it to. Every one of those is a
// contract between two components that pass their own tests.
//
// It also reports where the time went, which is the only honest way to say how
// long a deployment takes. It asserts nothing about the durations: this runs on
// whatever machine happens to be free, and a test that fails because a laptop
// was busy teaches nobody anything.
func TestPipelineEndToEnd(t *testing.T) {
	requireDocker(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	repoURL, commit := gitRemote(t, map[string]string{
		"go.mod": "module timing\n\ngo 1.26\n",
		"main.go": `package main

import (
	"fmt"
	"net/http"
	"os"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "deployed by launchpad into %s", os.Getenv("LAUNCHPAD_ENVIRONMENT"))
	})
	http.ListenAndServe(":"+port, nil)
}
`,
	})

	job := testJob()
	job.RepoURL = repoURL
	job.CommitSHA = commit
	job.ServiceName = "timing"
	job.Subdomain = "e2e-timing"

	fetcher := &timedFetcher{inner: &GitFetcher{}}
	driver := &timedDriver{inner: &DockerDriver{}}

	store := &fakeStore{jobs: []domain.DeploymentJob{job}}
	engine := newTestEngine(store, fetcher, driver)
	engine.WorkDir = t.TempDir()

	// Whatever happens, do not leave a container and an image behind.
	t.Cleanup(func() {
		_ = exec.Command("docker", "rm", "--force", "launchpad-"+job.Subdomain).Run()
		_ = exec.Command("docker", "rmi", "--force",
			fmt.Sprintf("launchpad/%s:%s", job.ServiceName, job.DeploymentID[:8])).Run()
	})

	started := time.Now()
	claimed, err := engine.processNext(ctx)
	total := time.Since(started)

	if err != nil {
		t.Fatalf("processNext() error = %v", err)
	}
	if !claimed {
		t.Fatal("processNext() claimed = false, want the queued deployment")
	}

	transitions, logs, failure := store.snapshot()
	if failure != "" {
		t.Fatalf("deployment failed: %s\nlogs:\n%s", failure, strings.Join(logs, "\n"))
	}

	want := []string{"queued->building", "building->deploying", "deploying->live"}
	if strings.Join(transitions, ",") != strings.Join(want, ",") {
		t.Errorf("transitions = %v, want %v", transitions, want)
	}

	// The environment's address is derived and stable; the driver's is where the
	// workload actually landed, and it is the one a test can reach without the
	// proxy in front.
	if store.internalURL == "" {
		t.Fatal("deployment recorded no internal URL")
	}
	body := getWithRetry(t, ctx, store.internalURL)
	if !strings.Contains(body, "deployed by launchpad into e2e-timing") {
		t.Errorf("deployed service returned %q, want the greeting naming its environment", body)
	}

	// The environment reaches the workload only because the engine passed it
	// through, so a wrong answer here is the release request, not the app.
	if !strings.Contains(body, job.Subdomain) {
		t.Errorf("service did not receive LAUNCHPAD_ENVIRONMENT: %q", body)
	}

	// Fetch, build and release are the three phases that talk to something
	// outside the process. What is left is detection, the state transitions and
	// the engine's own bookkeeping.
	overhead := total - fetcher.took - driver.build - driver.release

	t.Log("end-to-end deploy, no Dockerfile in the repository (Go strategy):")
	t.Logf("  fetch     %9s", fetcher.took.Round(time.Millisecond))
	t.Logf("  build     %9s", driver.build.Round(time.Millisecond))
	t.Logf("  release   %9s", driver.release.Round(time.Millisecond))
	t.Logf("  other     %9s", overhead.Round(time.Millisecond))
	t.Logf("  total     %9s", total.Round(time.Millisecond))
}

// gitRemote builds a repository the fetcher can pull a single commit out of, and
// returns a URL for it with the commit it is at.
//
// A local path would make git use its local-clone optimisation and ignore
// --depth, which is the behaviour under test, so the URL is file:// to force the
// same smart protocol a hosted remote speaks. allowAnySHA1InWant is set for the
// same reason: fetching a bare commit rather than a branch is off by default in
// git and on at every forge, and it is what lets a deployment name exactly one
// commit forever.
func gitRemote(t *testing.T, files map[string]string) (url, commit string) {
	t.Helper()

	dir := t.TempDir()
	for name, content := range files {
		writeFile(t, dir, name, content)
	}

	run := func(args ...string) string {
		t.Helper()

		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v: %s", args[0], err, strings.TrimSpace(string(out)))
		}
		return strings.TrimSpace(string(out))
	}

	run("init", "--quiet", "--initial-branch", "main")
	run("config", "uploadpack.allowAnySHA1InWant", "true")
	run("add", ".")
	// Identity on the command line rather than in config, so the test does not
	// depend on the machine having one set.
	run("-c", "user.email=test@launchpad.invalid", "-c", "user.name=Launchpad Test",
		"commit", "--quiet", "--message", "initial commit")

	return "file://" + dir, run("rev-parse", "HEAD")
}

// timedFetcher and timedDriver record how long each phase took. The engine logs
// a deployment's total duration but not its shape, and "a deploy takes forty
// seconds" is not something anyone can act on until they know which forty.
type timedFetcher struct {
	inner Fetcher
	took  time.Duration
}

func (f *timedFetcher) Fetch(ctx context.Context, repoURL, commitSHA, destDir string) error {
	started := time.Now()
	defer func() { f.took = time.Since(started) }()

	return f.inner.Fetch(ctx, repoURL, commitSHA, destDir)
}

type timedDriver struct {
	inner   Driver
	build   time.Duration
	release time.Duration
}

func (d *timedDriver) Name() string { return d.inner.Name() }

func (d *timedDriver) Build(ctx context.Context, req BuildRequest, logs LogWriter) (BuildResult, error) {
	started := time.Now()
	defer func() { d.build = time.Since(started) }()

	return d.inner.Build(ctx, req, logs)
}

func (d *timedDriver) Release(ctx context.Context, req ReleaseRequest, logs LogWriter) (ReleaseResult, error) {
	started := time.Now()
	defer func() { d.release = time.Since(started) }()

	return d.inner.Release(ctx, req, logs)
}
