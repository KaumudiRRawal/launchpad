package deploy

import (
	"context"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// collectLogs captures driver output so a failing test can show what Docker
// actually said.
type collectLogs struct {
	mu    sync.Mutex
	lines []string
}

func (c *collectLogs) WriteLine(stream, message string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lines = append(c.lines, stream+": "+message)
}

func (c *collectLogs) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return strings.Join(c.lines, "\n")
}

// requireDocker skips unless a Docker daemon is actually reachable. Set
// LAUNCHPAD_TEST_DOCKER=1 to opt in; these tests build and run real
// containers, so they are slow and are kept out of the default run.
func requireDocker(t *testing.T) {
	t.Helper()

	if os.Getenv("LAUNCHPAD_TEST_DOCKER") == "" {
		t.Skip("LAUNCHPAD_TEST_DOCKER not set; skipping Docker integration test")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skipf("docker daemon not reachable: %v", err)
	}
}

// TestDockerDriverBuildAndRelease proves the driver can turn a source tree
// into a container that actually answers HTTP. The engine tests cover the
// orchestration with fakes; this covers the part that talks to Docker, which
// fakes can never verify.
func TestDockerDriverBuildAndRelease(t *testing.T) {
	requireDocker(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// A source tree with no Dockerfile, so the generated Go one is exercised.
	dir := t.TempDir()
	writeFile(t, dir, "go.mod", "module hello\n\ngo 1.26\n")
	writeFile(t, dir, "main.go", `package main

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
		fmt.Fprint(w, "hello from launchpad")
	})
	http.ListenAndServe(":"+port, nil)
}
`)

	driver := &DockerDriver{}
	logs := &collectLogs{}

	strategy, err := Detect(os.DirFS(dir), ".")
	if err != nil {
		t.Fatalf("Detect() error = %v", err)
	}
	if strategy != StrategyGo {
		t.Fatalf("Detect() = %q, want go", strategy)
	}

	const tag = "launchpad-test/hello:integration"
	const name = "launchpad-test-hello"

	// Clean up whatever the test creates, whether or not it succeeds.
	t.Cleanup(func() {
		_ = exec.Command("docker", "rm", "--force", name).Run()
		_ = exec.Command("docker", "rmi", "--force", tag).Run()
	})

	built, err := driver.Build(ctx, BuildRequest{
		ContextDir: dir,
		Dockerfile: strategy.Dockerfile(8080),
		Tag:        tag,
	}, logs)
	if err != nil {
		t.Fatalf("Build() error = %v\nlogs:\n%s", err, logs)
	}
	// Nothing is pushed anywhere, so the local tag is the whole reference.
	if built.Image != tag {
		t.Errorf("Build() image = %q, want %q", built.Image, tag)
	}

	result, err := driver.Release(ctx, ReleaseRequest{
		Name:   name,
		Image:  built.Image,
		Port:   8080,
		Env:    map[string]string{"PORT": "8080"},
		Labels: map[string]string{"launchpad.test": "true"},
	}, logs)
	if err != nil {
		t.Fatalf("Release() error = %v\nlogs:\n%s", err, logs)
	}
	if result.URL == "" {
		t.Fatal("Release() returned an empty URL")
	}

	body := getWithRetry(t, ctx, result.URL)
	if !strings.Contains(body, "hello from launchpad") {
		t.Errorf("deployed service returned %q, want it to contain the expected greeting", body)
	}

	t.Run("releasing again replaces rather than duplicates", func(t *testing.T) {
		// A stable workload name is what makes a redeploy replace its
		// predecessor instead of colliding with it on the container name.
		second, err := driver.Release(ctx, ReleaseRequest{
			Name: name, Image: tag, Port: 8080,
			Env: map[string]string{"PORT": "8080"},
		}, logs)
		if err != nil {
			t.Fatalf("second Release() error = %v\nlogs:\n%s", err, logs)
		}
		if second.WorkloadID != name {
			t.Errorf("WorkloadID = %q, want %q", second.WorkloadID, name)
		}

		out, err := exec.Command("docker", "ps", "--filter", "name="+name, "--format", "{{.Names}}").Output()
		if err != nil {
			t.Fatalf("docker ps: %v", err)
		}
		if got := strings.Count(strings.TrimSpace(string(out)), name); got != 1 {
			t.Errorf("found %d containers named %q, want exactly 1", got, name)
		}
	})
}

// TestGitFetcherFetchesSingleCommit verifies the shallow fetch against a real
// remote, which is the behaviour that keeps a deployment from downloading a
// repository's entire history.
func TestGitFetcherFetchesSingleCommit(t *testing.T) {
	if os.Getenv("LAUNCHPAD_TEST_DOCKER") == "" {
		t.Skip("LAUNCHPAD_TEST_DOCKER not set; skipping network-dependent test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// This repository, at a commit that exists in its own history.
	sha, err := exec.Command("git", "rev-parse", "HEAD").Output()
	if err != nil {
		t.Skipf("could not resolve HEAD: %v", err)
	}
	commit := strings.TrimSpace(string(sha))

	dir := t.TempDir()
	fetcher := &GitFetcher{}

	if err := fetcher.Fetch(ctx, "https://github.com/KaumudiRRawal/launchpad", commit, dir); err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "control-plane", "go.mod")); err != nil {
		t.Errorf("expected file missing from checkout: %v", err)
	}

	// Shallow: exactly one commit of history, not the whole repository.
	out, err := exec.Command("git", "-C", dir, "rev-list", "--count", "HEAD").Output()
	if err != nil {
		t.Fatalf("rev-list: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != "1" {
		t.Errorf("checkout has %s commits, want 1 (fetch was not shallow)", got)
	}

	t.Run("unknown commit fails clearly", func(t *testing.T) {
		err := fetcher.Fetch(ctx, "https://github.com/KaumudiRRawal/launchpad",
			"0000000000000000000000000000000000000000", t.TempDir())
		if err == nil {
			t.Error("Fetch() with an unknown commit = nil, want an error")
		}
	})
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// getWithRetry polls until the container is accepting connections. A container
// is running before the process inside it has bound its port.
func getWithRetry(t *testing.T, ctx context.Context, url string) string {
	t.Helper()

	deadline := time.Now().Add(30 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			t.Fatalf("build request: %v", err)
		}

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			lastErr = err
			time.Sleep(500 * time.Millisecond)
			continue
		}
		defer resp.Body.Close()

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		return string(body)
	}

	t.Fatalf("service at %s never became reachable: %v", url, lastErr)
	return ""
}
