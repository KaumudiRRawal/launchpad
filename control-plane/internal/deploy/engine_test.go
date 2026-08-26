package deploy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/KaumudiRRawal/launchpad/control-plane/internal/domain"
)

// fakeStore records the transitions the engine performs so a test can assert
// on the sequence, which is the part that must not regress.
type fakeStore struct {
	mu sync.Mutex

	jobs        []domain.DeploymentJob
	transitions []string
	logs        []string
	failure     string
	imageRef    string
	liveURL     string
	internalURL string
	superseded  bool

	transitionErr error
}

func (f *fakeStore) ClaimNextDeployment(context.Context) (domain.DeploymentJob, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if len(f.jobs) == 0 {
		return domain.DeploymentJob{}, domain.ErrNotFound
	}
	job := f.jobs[0]
	f.jobs = f.jobs[1:]
	f.transitions = append(f.transitions, "queued->building")
	return job, nil
}

func (f *fakeStore) TransitionDeployment(_ context.Context, _ string, from, to domain.DeploymentStatus) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.transitionErr != nil {
		return f.transitionErr
	}
	f.transitions = append(f.transitions, fmt.Sprintf("%s->%s", from, to))
	return nil
}

func (f *fakeStore) MarkDeploymentLive(_ context.Context, _, imageRef, publicURL, internalURL string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.transitions = append(f.transitions, "deploying->live")
	f.imageRef = imageRef
	f.liveURL = publicURL
	f.internalURL = internalURL
	return nil
}

func (f *fakeStore) MarkDeploymentFailed(_ context.Context, _, reason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.transitions = append(f.transitions, "->failed")
	f.failure = reason
	return nil
}

func (f *fakeStore) SupersedePriorDeployments(context.Context, string, string, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.superseded = true
	return nil
}

func (f *fakeStore) AppendDeploymentLog(_ context.Context, _ string, _ int, stream, message string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.logs = append(f.logs, stream+": "+message)
	return nil
}

func (f *fakeStore) snapshot() ([]string, []string, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.transitions...), append([]string(nil), f.logs...), f.failure
}

// fakeFetcher writes a source tree instead of talking to a git remote.
type fakeFetcher struct {
	files map[string]string
	err   error
}

func (f *fakeFetcher) Fetch(_ context.Context, _, _, destDir string) error {
	if f.err != nil {
		return f.err
	}
	for name, content := range f.files {
		full := filepath.Join(destDir, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			return err
		}
	}
	return nil
}

type fakeDriver struct {
	buildErr   error
	releaseErr error

	builtTag   string
	releaseReq ReleaseRequest
}

func (f *fakeDriver) Name() string { return "fake" }

func (f *fakeDriver) Build(_ context.Context, req BuildRequest, logs LogWriter) (BuildResult, error) {
	if f.buildErr != nil {
		return BuildResult{}, f.buildErr
	}
	f.builtTag = req.Tag
	logs.WriteLine("stdout", "built "+req.Tag)
	// Mimics a registry-backed driver: the engine must release and record what
	// the build reported, not the tag it asked for.
	return BuildResult{Image: "registry.example/" + req.Tag}, nil
}

func (f *fakeDriver) Release(_ context.Context, req ReleaseRequest, _ LogWriter) (ReleaseResult, error) {
	if f.releaseErr != nil {
		return ReleaseResult{}, f.releaseErr
	}
	f.releaseReq = req
	return ReleaseResult{URL: "http://localhost:32770", WorkloadID: req.Name}, nil
}

func testJob() domain.DeploymentJob {
	return domain.DeploymentJob{
		DeploymentID:  "11111111-2222-3333-4444-555555555555",
		ServiceID:     "service-1",
		EnvironmentID: "env-1",
		CommitSHA:     "0123456789abcdef0123456789abcdef01234567",
		RepoURL:       "https://github.com/example/demo",
		SourcePath:    ".",
		Port:          8080,
		ServiceName:   "api",
		Subdomain:     "production-demo",
	}
}

func newTestEngine(store Store, fetcher Fetcher, driver Driver) *Engine {
	return &Engine{
		Store:      store,
		Driver:     driver,
		Fetcher:    fetcher,
		Log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		WorkDir:    os.TempDir(),
		BaseDomain: "localhost",
		ProxyPort:  8081,
	}
}

func TestEngineHappyPath(t *testing.T) {
	store := &fakeStore{jobs: []domain.DeploymentJob{testJob()}}
	driver := &fakeDriver{}
	engine := newTestEngine(store, &fakeFetcher{files: map[string]string{"go.mod": "module demo\n"}}, driver)

	claimed, err := engine.processNext(context.Background())
	if err != nil {
		t.Fatalf("processNext() error = %v", err)
	}
	if !claimed {
		t.Fatal("processNext() claimed = false, want true")
	}

	transitions, logs, _ := store.snapshot()
	want := []string{"queued->building", "building->deploying", "deploying->live"}
	if len(transitions) != len(want) {
		t.Fatalf("transitions = %v, want %v", transitions, want)
	}
	for i, w := range want {
		if transitions[i] != w {
			t.Errorf("transition[%d] = %q, want %q", i, transitions[i], w)
		}
	}

	// The public address is the environment's stable subdomain and survives
	// redeployment; the driver's address changes with every release and is only
	// ever read by the proxy.
	if store.liveURL != "http://production-demo.localhost:8081" {
		t.Errorf("liveURL = %q, want the environment's public address", store.liveURL)
	}
	if store.internalURL != "http://localhost:32770" {
		t.Errorf("internalURL = %q, want the driver's address", store.internalURL)
	}
	if !store.superseded {
		t.Error("prior deployments were not superseded")
	}

	// The image tag must trace back to the deployment that produced it.
	if !strings.Contains(driver.builtTag, "11111111") {
		t.Errorf("built tag %q does not identify the deployment", driver.builtTag)
	}

	// What gets released and recorded is the reference the build reported. A
	// driver that pushed the image somewhere else released from there, and a
	// deployment row naming the tag the engine asked for would name an image
	// that exists nowhere.
	if driver.releaseReq.Image != "registry.example/"+driver.builtTag {
		t.Errorf("released image = %q, want the reference the build reported", driver.releaseReq.Image)
	}
	if store.imageRef != "registry.example/"+driver.builtTag {
		t.Errorf("recorded image = %q, want the reference the build reported", store.imageRef)
	}

	// The workload name must be stable so a release replaces its predecessor
	// rather than starting a second container beside it.
	if driver.releaseReq.Name != "launchpad-production-demo" {
		t.Errorf("workload name = %q, want launchpad-production-demo", driver.releaseReq.Name)
	}

	if len(logs) == 0 {
		t.Error("no build logs were persisted")
	}
}

func TestEngineRecordsFailures(t *testing.T) {
	tests := []struct {
		name        string
		fetcher     Fetcher
		driver      Driver
		wantInError string
	}{
		{
			name:        "fetch fails",
			fetcher:     &fakeFetcher{err: errors.New("repository not found")},
			driver:      &fakeDriver{},
			wantInError: "repository not found",
		},
		{
			name:        "no build strategy matches",
			fetcher:     &fakeFetcher{files: map[string]string{"README.md": "hello"}},
			driver:      &fakeDriver{},
			wantInError: "no build strategy matched",
		},
		{
			name:        "build fails",
			fetcher:     &fakeFetcher{files: map[string]string{"go.mod": "module demo\n"}},
			driver:      &fakeDriver{buildErr: errors.New("compile error on line 12")},
			wantInError: "compile error on line 12",
		},
		{
			name:        "release fails",
			fetcher:     &fakeFetcher{files: map[string]string{"go.mod": "module demo\n"}},
			driver:      &fakeDriver{releaseErr: errors.New("port already allocated")},
			wantInError: "port already allocated",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeStore{jobs: []domain.DeploymentJob{testJob()}}
			engine := newTestEngine(store, tt.fetcher, tt.driver)

			// A failed deployment is not an engine error: the worker records
			// it and carries on to the next one.
			claimed, err := engine.processNext(context.Background())
			if err != nil {
				t.Fatalf("processNext() error = %v, want nil", err)
			}
			if !claimed {
				t.Fatal("processNext() claimed = false, want true")
			}

			transitions, _, failure := store.snapshot()
			if !strings.Contains(failure, tt.wantInError) {
				t.Errorf("recorded failure = %q, want it to mention %q", failure, tt.wantInError)
			}

			last := transitions[len(transitions)-1]
			if last != "->failed" {
				t.Errorf("final transition = %q, want ->failed", last)
			}
			// A deployment that never built must not have claimed to.
			for _, tr := range transitions {
				if tr == "deploying->live" {
					t.Error("a failed deployment was marked live")
				}
			}
		})
	}
}

// TestEngineFailureMessagesAreStorable exists because the failure message is
// assembled from a subprocess's own output — the fetcher puts the remote's
// bytes straight into its error — and PostgreSQL refuses a text value holding
// invalid UTF-8 or a NUL. Here that refusal would land on the write recording
// the failure, so the deployment would stay in building forever with nothing
// saying why.
func TestEngineFailureMessagesAreStorable(t *testing.T) {
	// Latin-1 bytes, a NUL, and enough length that the cap also has to cut
	// somewhere — which is its own way of producing invalid UTF-8.
	remote := "remote: caf\xe9\x00 " + strings.Repeat("x", 2000) + "→ aborting"

	store := &fakeStore{jobs: []domain.DeploymentJob{testJob()}}
	engine := newTestEngine(store, &fakeFetcher{err: errors.New(remote)}, &fakeDriver{})

	if _, err := engine.processNext(context.Background()); err != nil {
		t.Fatalf("processNext() error = %v, want nil", err)
	}

	_, logs, failure := store.snapshot()
	if !utf8.ValidString(failure) {
		t.Errorf("recorded failure is not valid UTF-8: %q", failure)
	}
	if strings.ContainsRune(failure, 0) {
		t.Errorf("recorded failure contains a NUL byte: %q", failure)
	}
	// The message still has to say what went wrong.
	if !strings.Contains(failure, "remote: caf") {
		t.Errorf("recorded failure = %q, want it to keep the remote's message", failure)
	}
	for i, line := range logs {
		if !utf8.ValidString(line) || strings.ContainsRune(line, 0) {
			t.Errorf("log[%d] = %q, want valid UTF-8 with no NUL", i, line)
		}
	}
}

func TestEngineIdleQueue(t *testing.T) {
	store := &fakeStore{}
	engine := newTestEngine(store, &fakeFetcher{}, &fakeDriver{})

	claimed, err := engine.processNext(context.Background())
	if err != nil {
		t.Fatalf("processNext() on an empty queue error = %v, want nil", err)
	}
	if claimed {
		t.Error("processNext() claimed = true on an empty queue")
	}
}

func TestEngineRunStopsOnContextCancel(t *testing.T) {
	store := &fakeStore{}
	engine := newTestEngine(store, &fakeFetcher{}, &fakeDriver{})
	engine.PollInterval = 10 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { engine.Run(ctx); close(done) }()

	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run() did not return within 2s of cancellation")
	}
}

func TestLogRecorderSplitsAndOrders(t *testing.T) {
	store := &fakeStore{}
	recorder := newLogRecorder(store, "deployment-1")

	recorder.WriteLine("stdout", "first\nsecond\nthird")
	recorder.Flush()

	_, logs, _ := store.snapshot()
	want := []string{"stdout: first", "stdout: second", "stdout: third"}
	if len(logs) != len(want) {
		t.Fatalf("logs = %v, want %v", logs, want)
	}
	for i, w := range want {
		if logs[i] != w {
			t.Errorf("log[%d] = %q, want %q", i, logs[i], w)
		}
	}
}

func TestLogRecorderFlushesWhenBufferFills(t *testing.T) {
	// A verbose build must not hold thousands of lines in memory waiting for
	// the deployment to end.
	store := &fakeStore{}
	recorder := newLogRecorder(store, "deployment-1")

	for i := range logFlushThreshold + 5 {
		recorder.WriteLine("stdout", fmt.Sprintf("line %d", i))
	}

	_, logs, _ := store.snapshot()
	if len(logs) < logFlushThreshold {
		t.Errorf("only %d lines persisted before an explicit flush, want at least %d",
			len(logs), logFlushThreshold)
	}
}

func TestLogRecorderSanitizesBuildOutput(t *testing.T) {
	// Build output is whatever bytes the toolchain wrote. A line PostgreSQL
	// refuses is a line the follower never sees, and the insert is best-effort
	// so nothing would report that it went missing.
	store := &fakeStore{}
	recorder := newLogRecorder(store, "deployment-1")

	recorder.WriteLine("stderr", "warning: caf\xe9\x00 not found")
	recorder.Flush()

	_, logs, _ := store.snapshot()
	if len(logs) != 1 {
		t.Fatalf("logs = %v, want 1 line", logs)
	}
	if want := "stderr: warning: caf\uFFFD not found"; logs[0] != want {
		t.Errorf("log = %q, want %q", logs[0], want)
	}
}

func TestSanitize(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "plain text is untouched", in: "compile error on line 12", want: "compile error on line 12"},
		{name: "valid multi-byte text is untouched", in: "erreur: café → 2", want: "erreur: café → 2"},
		{name: "latin-1 output becomes a replacement character", in: "caf\xe9", want: "caf\uFFFD"},
		{name: "a truncated multi-byte sequence is replaced", in: "ok\xc3(more", want: "ok\uFFFD(more"},
		// NUL is valid UTF-8 and still rejected by PostgreSQL, so it is dropped
		// rather than replaced: it carries no meaning worth a marker.
		{name: "NUL is dropped", in: "before\x00after", want: "beforeafter"},
		{name: "empty stays empty", in: "", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitize(tt.in)
			if got != tt.want {
				t.Errorf("sanitize(%q) = %q, want %q", tt.in, got, tt.want)
			}
			if !utf8.ValidString(got) {
				t.Errorf("sanitize(%q) = %q, which is not valid UTF-8", tt.in, got)
			}
			if strings.ContainsRune(got, 0) {
				t.Errorf("sanitize(%q) = %q, which still contains a NUL", tt.in, got)
			}
		})
	}
}

func TestTruncateCutsOnRuneBoundary(t *testing.T) {
	tests := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{name: "shorter than the cap is returned whole", in: "short", max: 100, want: "short"},
		{name: "exactly the cap is returned whole", in: "abcde", max: 5, want: "abcde"},
		{name: "ascii is cut at the cap", in: "abcdef", max: 3, want: "abc…"},
		// "abéc" is five bytes, so a cap of three lands inside the é. Cutting
		// there would leave half a character behind.
		{name: "a rune straddling the cap is dropped whole", in: "abéc", max: 3, want: "ab…"},
		{name: "a cap on a rune boundary keeps the rune before it", in: "abéc", max: 4, want: "abé…"},
		// Degenerate, but the loop must terminate rather than run off the front.
		{name: "nothing but continuation bytes", in: "\xa9\xa9\xa9", max: 1, want: "…"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncate(tt.in, tt.max)
			if got != tt.want {
				t.Errorf("truncate(%q, %d) = %q, want %q", tt.in, tt.max, got, tt.want)
			}
		})
	}
}
