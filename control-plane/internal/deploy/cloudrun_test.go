package deploy

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// fakeGcloud stands in for the CLI and records what it was asked to do.
//
// The Cloud Run driver's whole job is turning a deployment into the right
// gcloud invocation, so that is what the tests assert on. Every argument is
// recorded on its own line, which keeps a flag and its value distinguishable —
// a test that matched on the joined command line could not tell --labels from
// a label whose value happened to contain the word.
type fakeGcloud struct {
	path string
	log  string
}

func newFakeGcloud(t *testing.T, serviceURL string, exitCode int) *fakeGcloud {
	t.Helper()

	dir := t.TempDir()
	f := &fakeGcloud{
		path: filepath.Join(dir, "gcloud"),
		log:  filepath.Join(dir, "invocations"),
	}

	script := `#!/bin/sh
{ for arg in "$@"; do printf '%s\n' "$arg"; done; printf '=== end ===\n'; } >> "` + f.log + `"
if [ "$1 $2 $3" = "run services describe" ]; then
	printf '%s\n' '` + serviceURL + `'
	exit 0
fi
echo "step 1/3: pulling base image"
echo "note: written to stderr" >&2
exit ` + strconv.Itoa(exitCode) + `
`
	if err := os.WriteFile(f.path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake gcloud: %v", err)
	}
	return f
}

// invocations returns one slice of arguments per call the driver made.
func (f *fakeGcloud) invocations(t *testing.T) [][]string {
	t.Helper()

	raw, err := os.ReadFile(f.log)
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

// flagValue returns the argument following flag.
func flagValue(t *testing.T, args []string, flag string) string {
	t.Helper()

	i := slices.Index(args, flag)
	if i < 0 || i+1 >= len(args) {
		t.Fatalf("%s missing from %v", flag, args)
	}
	return args[i+1]
}

func testCloudRunDriver(t *testing.T, fake *fakeGcloud) *CloudRunDriver {
	return &CloudRunDriver{
		Project:              "launchpad-prod",
		Region:               "europe-west1",
		Repository:           "images",
		ServiceAccount:       "workload@launchpad-prod.iam.gserviceaccount.com",
		AllowUnauthenticated: true,
		Binary:               fake.path,
	}
}

func TestCloudRunDriverBuild(t *testing.T) {
	fake := newFakeGcloud(t, "", 0)
	driver := testCloudRunDriver(t, fake)

	contextDir := t.TempDir()
	writeFile(t, contextDir, "go.mod", "module demo\n")

	logs := &collectLogs{}
	built, err := driver.Build(context.Background(), BuildRequest{
		ContextDir: contextDir,
		Dockerfile: "FROM scratch\n",
		Tag:        "launchpad/api:1a2b3c4d",
	}, logs)
	if err != nil {
		t.Fatalf("Build() error = %v\nlogs:\n%s", err, logs)
	}

	// Cloud Run pulls from a registry, so the reported reference has to be the
	// pushed one rather than the tag the engine asked for.
	const want = "europe-west1-docker.pkg.dev/launchpad-prod/images/api:1a2b3c4d"
	if built.Image != want {
		t.Errorf("Build() image = %q, want %q", built.Image, want)
	}

	// Cloud Build reads the Dockerfile out of the uploaded context; there is no
	// way to pipe one in, so a generated one has to land on disk first.
	generated, err := os.ReadFile(filepath.Join(contextDir, "Dockerfile"))
	if err != nil {
		t.Fatalf("generated Dockerfile was not written: %v", err)
	}
	if string(generated) != "FROM scratch\n" {
		t.Errorf("generated Dockerfile = %q, want the request's", generated)
	}

	calls := fake.invocations(t)
	if len(calls) != 1 {
		t.Fatalf("made %d gcloud calls, want 1: %v", len(calls), calls)
	}
	args := calls[0]

	if got := strings.Join(args[:2], " "); got != "builds submit" {
		t.Errorf("command = %q, want builds submit", got)
	}
	if got := flagValue(t, args, "--tag"); got != want {
		t.Errorf("--tag = %q, want %q", got, want)
	}
	if got := flagValue(t, args, "--project"); got != "launchpad-prod" {
		t.Errorf("--project = %q, want launchpad-prod", got)
	}
	// The image is built where it will run, so a cold start does not pull it
	// across regions.
	if got := flagValue(t, args, "--region"); got != "europe-west1" {
		t.Errorf("--region = %q, want europe-west1", got)
	}
	if args[len(args)-1] != contextDir {
		t.Errorf("build context = %q, want %q", args[len(args)-1], contextDir)
	}

	// Both output streams have to reach the log: gcloud reports progress on
	// stderr, so a driver reading only stdout would show an empty build.
	if out := logs.String(); !strings.Contains(out, "pulling base image") ||
		!strings.Contains(out, "written to stderr") {
		t.Errorf("build log missing driver output:\n%s", out)
	}
}

func TestCloudRunDriverBuildLeavesAnExistingDockerfileAlone(t *testing.T) {
	fake := newFakeGcloud(t, "", 0)
	driver := testCloudRunDriver(t, fake)

	contextDir := t.TempDir()
	writeFile(t, contextDir, "Dockerfile", "FROM the-repository-said-so\n")

	// An empty Dockerfile in the request means detection found the repository's
	// own, and overwriting it would build something the author did not write.
	if _, err := driver.Build(context.Background(), BuildRequest{
		ContextDir: contextDir,
		Tag:        "launchpad/api:1a2b3c4d",
	}, &collectLogs{}); err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	kept, err := os.ReadFile(filepath.Join(contextDir, "Dockerfile"))
	if err != nil {
		t.Fatalf("read Dockerfile: %v", err)
	}
	if string(kept) != "FROM the-repository-said-so\n" {
		t.Errorf("Dockerfile = %q, want the repository's own", kept)
	}
}

func TestCloudRunDriverRelease(t *testing.T) {
	fake := newFakeGcloud(t, "https://launchpad-production-demo-abc123-ew.a.run.app", 0)
	driver := testCloudRunDriver(t, fake)

	logs := &collectLogs{}
	result, err := driver.Release(context.Background(), ReleaseRequest{
		Name:  "launchpad-production-demo",
		Image: "europe-west1-docker.pkg.dev/launchpad-prod/images/api:1a2b3c4d",
		Port:  8080,
		Env: map[string]string{
			"PORT":                  "8080",
			"LAUNCHPAD_ENVIRONMENT": "production-demo",
		},
		Labels: map[string]string{
			"launchpad.deployment":  "11111111-2222-3333-4444-555555555555",
			"launchpad.environment": "env-1",
		},
	}, logs)
	if err != nil {
		t.Fatalf("Release() error = %v\nlogs:\n%s", err, logs)
	}

	if result.URL != "https://launchpad-production-demo-abc123-ew.a.run.app" {
		t.Errorf("URL = %q, want the address Cloud Run reported", result.URL)
	}
	if result.WorkloadID != "launchpad-production-demo" {
		t.Errorf("WorkloadID = %q, want launchpad-production-demo", result.WorkloadID)
	}

	calls := fake.invocations(t)
	if len(calls) != 2 {
		t.Fatalf("made %d gcloud calls, want deploy then describe: %v", len(calls), calls)
	}

	deploy := calls[0]
	if got := strings.Join(deploy[:3], " "); got != "run deploy launchpad-production-demo" {
		t.Errorf("command = %q, want run deploy launchpad-production-demo", got)
	}
	if got := flagValue(t, deploy, "--port"); got != "8080" {
		t.Errorf("--port = %q, want 8080", got)
	}
	if got := flagValue(t, deploy, "--service-account"); got != "workload@launchpad-prod.iam.gserviceaccount.com" {
		t.Errorf("--service-account = %q, want the configured identity", got)
	}
	if !slices.Contains(deploy, "--allow-unauthenticated") {
		t.Error("--allow-unauthenticated missing; the proxy sends no identity token")
	}

	// PORT is reserved: Cloud Run sets it from --port and rejects a deploy that
	// also passes it as an environment variable.
	if got := flagValue(t, deploy, "--set-env-vars"); got != "^|^LAUNCHPAD_ENVIRONMENT=production-demo" {
		t.Errorf("--set-env-vars = %q, want the delimited pairs without PORT", got)
	}

	// Google Cloud rejects a dot in a label key, so the engine's are translated.
	wantLabels := "launchpad_deployment=11111111-2222-3333-4444-555555555555,launchpad_environment=env-1"
	if got := flagValue(t, deploy, "--labels"); got != wantLabels {
		t.Errorf("--labels = %q, want %q", got, wantLabels)
	}

	describe := calls[1]
	if got := strings.Join(describe[:3], " "); got != "run services describe" {
		t.Errorf("second call = %q, want run services describe", got)
	}
	if got := flagValue(t, describe, "--format"); got != "value(status.url)" {
		t.Errorf("--format = %q, want value(status.url)", got)
	}
}

func TestCloudRunDriverReleaseWithoutPublicAccess(t *testing.T) {
	fake := newFakeGcloud(t, "https://private-abc123-ew.a.run.app", 0)
	driver := testCloudRunDriver(t, fake)
	driver.AllowUnauthenticated = false
	driver.ServiceAccount = ""

	if _, err := driver.Release(context.Background(), ReleaseRequest{
		Name: "launchpad-preview-demo", Image: "img", Port: 3000,
	}, &collectLogs{}); err != nil {
		t.Fatalf("Release() error = %v", err)
	}

	deploy := fake.invocations(t)[0]

	// Stated rather than left out: gcloud prompts when neither is given, and a
	// prompt in a deploy worker is a build that hangs until it times out.
	if !slices.Contains(deploy, "--no-allow-unauthenticated") {
		t.Errorf("--no-allow-unauthenticated missing from %v", deploy)
	}
	if slices.Contains(deploy, "--service-account") {
		t.Error("--service-account passed with no identity configured")
	}
}

func TestCloudRunDriverRedactsEnvironmentInTheEchoedCommand(t *testing.T) {
	fake := newFakeGcloud(t, "https://svc-abc123-ew.a.run.app", 0)
	driver := testCloudRunDriver(t, fake)

	logs := &collectLogs{}
	if _, err := driver.Release(context.Background(), ReleaseRequest{
		Name:  "launchpad-production-demo",
		Image: "img",
		Port:  8080,
		Env:   map[string]string{"DATABASE_URL": "postgres://user:hunter2@db/app"},
	}, logs); err != nil {
		t.Fatalf("Release() error = %v", err)
	}

	// The command is echoed so a remote failure can be reproduced by hand, and
	// build logs are readable by anyone who can read the deployment.
	out := logs.String()
	if strings.Contains(out, "hunter2") {
		t.Errorf("environment value leaked into the build log:\n%s", out)
	}
	if !strings.Contains(out, "[redacted]") {
		t.Errorf("echoed command did not mark the environment as redacted:\n%s", out)
	}

	// Redacting the log must not change what gcloud is actually given.
	if got := flagValue(t, fake.invocations(t)[0], "--set-env-vars"); !strings.Contains(got, "hunter2") {
		t.Errorf("--set-env-vars = %q, want the real value", got)
	}
}

func TestCloudRunDriverReportsFailure(t *testing.T) {
	fake := newFakeGcloud(t, "", 1)
	driver := testCloudRunDriver(t, fake)

	_, err := driver.Build(context.Background(), BuildRequest{
		ContextDir: t.TempDir(),
		Tag:        "launchpad/api:1a2b3c4d",
	}, &collectLogs{})
	if err == nil {
		t.Fatal("Build() error = nil, want the failure gcloud reported")
	}
	// The engine records this string against the deployment, so it has to say
	// which command failed rather than only that one did.
	if !strings.Contains(err.Error(), "builds submit") {
		t.Errorf("error = %q, want it to name the command", err)
	}
}

func TestCloudRunDriverRequiresConfiguration(t *testing.T) {
	tests := []struct {
		name   string
		driver CloudRunDriver
		want   string
	}{
		{
			name:   "no project",
			driver: CloudRunDriver{Region: "europe-west1", Repository: "images"},
			want:   "project",
		},
		{
			name:   "no region",
			driver: CloudRunDriver{Project: "p", Repository: "images"},
			want:   "region",
		},
		{
			name:   "no repository",
			driver: CloudRunDriver{Project: "p", Region: "europe-west1"},
			want:   "repository",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Caught before gcloud runs: its own complaint about a missing
			// --project arrives partway through a build log and reads like a
			// transient failure rather than a misconfiguration.
			_, err := tt.driver.Build(context.Background(), BuildRequest{}, &collectLogs{})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Build() error = %v, want it to name the missing %s", err, tt.want)
			}
			if _, err := tt.driver.Release(context.Background(), ReleaseRequest{}, &collectLogs{}); err == nil {
				t.Error("Release() error = nil, want a configuration error")
			}
		})
	}
}

func TestCloudRunServiceName(t *testing.T) {
	// 63 characters is a legal subdomain, and "launchpad-" in front of it is
	// not a legal Cloud Run service name.
	long := "launchpad-" + strings.Repeat("a", 63)
	// The truncation lands exactly on the hyphen at index 39.
	hyphenAtTheCut := "launchpad-" + strings.Repeat("b", 29) + "-" + strings.Repeat("c", 34)

	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "short enough is left alone",
			in:   "launchpad-production-demo",
			want: "launchpad-production-demo",
		},
		{
			name: "exactly at the limit is left alone",
			in:   "launchpad-" + strings.Repeat("a", 39),
			want: "launchpad-" + strings.Repeat("a", 39),
		},
		{
			name: "too long is truncated and given a digest",
			in:   long,
			want: "launchpad-" + strings.Repeat("a", 30) + "-326de3a9",
		},
		{
			name: "truncation never leaves a trailing hyphen",
			in:   hyphenAtTheCut,
			want: "launchpad-" + strings.Repeat("b", 29) + "-308866b9",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cloudRunServiceName(tt.in)
			if got != tt.want {
				t.Errorf("cloudRunServiceName(%q) = %q, want %q", tt.in, got, tt.want)
			}
			if len(got) > maxCloudRunService {
				t.Errorf("name is %d characters, over the %d Cloud Run allows", len(got), maxCloudRunService)
			}
			if strings.HasSuffix(got, "-") {
				t.Errorf("name %q ends in a hyphen, which Cloud Run rejects", got)
			}
			// Stability is what makes a release replace its predecessor.
			if again := cloudRunServiceName(tt.in); again != got {
				t.Errorf("cloudRunServiceName is not deterministic: %q then %q", got, again)
			}
		})
	}

	t.Run("names sharing a truncated prefix stay distinct", func(t *testing.T) {
		a := cloudRunServiceName("launchpad-" + strings.Repeat("a", 50) + "-one")
		b := cloudRunServiceName("launchpad-" + strings.Repeat("a", 50) + "-two")
		if a == b {
			t.Errorf("two environments collapsed onto one service: %q", a)
		}
	})
}

func TestCloudRunEnvFlag(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{
			name: "nothing to set",
			env:  nil,
			want: "",
		},
		{
			name: "only reserved variables leaves nothing to set",
			env:  map[string]string{"PORT": "8080"},
			want: "",
		},
		{
			name: "sorted so the same input renders the same command",
			env:  map[string]string{"B": "2", "A": "1", "C": "3"},
			want: "^|^A=1|B=2|C=3",
		},
		{
			name: "a value containing a comma survives the delimiter",
			env:  map[string]string{"HOSTS": "a.example,b.example"},
			want: "^|^HOSTS=a.example,b.example",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cloudRunEnvFlag(tt.env); got != tt.want {
				t.Errorf("cloudRunEnvFlag() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCloudRunLabel(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "already legal", in: "launchpad-service", want: "launchpad-service"},
		{name: "dots become underscores", in: "launchpad.deployment", want: "launchpad_deployment"},
		{name: "uppercase is folded", in: "Preview-Demo", want: "preview-demo"},
		{name: "commas cannot survive to split the flag", in: "a,b", want: "a_b"},
		{
			name: "over-long is truncated to what Google stores",
			in:   strings.Repeat("x", 100),
			want: strings.Repeat("x", 63),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cloudRunLabel(tt.in); got != tt.want {
				t.Errorf("cloudRunLabel(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
