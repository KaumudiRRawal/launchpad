package deploy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// DockerDriver builds and runs workloads with the local Docker daemon.
//
// It shells out to the docker CLI rather than using the Docker SDK. The SDK
// pulls in a large dependency tree to reproduce commands that are stable,
// documented, and identical to what a developer would run by hand — which
// also makes a failed build reproducible from the log.
type DockerDriver struct {
	// HostPort assigns the published port. Zero lets Docker choose a free one.
	HostPort int
	// Binary overrides the docker executable, for testing.
	Binary string
}

func (d *DockerDriver) Name() string { return "docker" }

func (d *DockerDriver) binary() string {
	if d.Binary != "" {
		return d.Binary
	}
	return "docker"
}

// Build produces an image from the request's context directory.
//
// The image stays in the local daemon: there is no registry to push to, which
// is most of why the development loop is fast, so the reference it reports back
// is the tag it was asked for.
func (d *DockerDriver) Build(ctx context.Context, req BuildRequest, logs LogWriter) (BuildResult, error) {
	args := []string{"build", "--tag", req.Tag}

	var stdin io.Reader
	if req.Dockerfile != "" {
		// The repository has no Dockerfile of its own, so the generated one is
		// piped in and the context directory supplies the source.
		args = append(args, "--file", "-")
		stdin = strings.NewReader(req.Dockerfile)

		// Safe here and only here: these instructions are ours, so we know
		// what they read.
		if err := excludeGitMetadata(req.ContextDir); err != nil {
			return BuildResult{}, err
		}
	}
	args = append(args, req.ContextDir)

	if err := d.run(ctx, args, stdin, logs); err != nil {
		return BuildResult{}, err
	}
	return BuildResult{Image: req.Tag}, nil
}

// excludeGitMetadata keeps the checkout's .git directory out of the build
// context.
//
// The Python and Node strategies copy the whole source tree into the image they
// deploy, so without this a deployed container ships the repository's git
// metadata inside itself: .git/config naming the remote, and the object store
// holding the fetched commit. A platform that builds other people's code should
// not put their repository inside the thing it exposes to the internet. It also
// keeps a repository's entire .git from being uploaded to the daemon on every
// single build.
//
// What it does not do is repair the layer cache, which is why it was originally
// wanted. A second checkout of the same commit still misses on COPY with the
// metadata gone, so .git was not what was invalidating it; docs/architecture.md
// carries that measurement.
//
// It runs only for a generated Dockerfile, which is the only case where dropping
// the metadata is known to be safe. A repository that wrote its own build may
// stamp a version out of git, and breaking that build would be trading someone
// else's correctness for the platform's tidiness.
//
// A .dockerignore in the context root is the only way to exclude a path from a
// docker build, so the rule is appended to whatever the repository already had
// rather than replacing it. Those rules were written for a build of this same
// source and may be excluding something for a reason; appending also puts ours
// last, where a pattern above it cannot re-include the metadata.
func excludeGitMetadata(contextDir string) error {
	if _, err := os.Stat(filepath.Join(contextDir, ".git")); err != nil {
		// Nothing to exclude. A service built from a subdirectory of a
		// monorepo never had the metadata in its context to begin with.
		return nil
	}

	path := filepath.Join(contextDir, ".dockerignore")
	rules, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("read .dockerignore: %w", err)
	}

	if len(rules) > 0 && !strings.HasSuffix(string(rules), "\n") {
		rules = append(rules, '\n')
	}
	rules = append(rules, ".git\n"...)

	if err := os.WriteFile(path, rules, 0o644); err != nil {
		return fmt.Errorf("write .dockerignore: %w", err)
	}
	return nil
}

// Release starts the image, replacing any workload already using the name.
func (d *DockerDriver) Release(ctx context.Context, req ReleaseRequest, logs LogWriter) (ReleaseResult, error) {
	// Removing the predecessor first makes releasing idempotent: a retried
	// deployment replaces its own half-finished attempt instead of colliding
	// with it. Failure is ignored because "no such container" is the normal
	// case on a first release.
	_ = exec.CommandContext(ctx, d.binary(), "rm", "--force", req.Name).Run()

	publish := fmt.Sprintf("%d:%d", d.HostPort, req.Port)
	if d.HostPort == 0 {
		// An empty host port asks Docker for any free one.
		publish = fmt.Sprintf("::%d", req.Port)
	}

	args := []string{
		"run", "--detach",
		"--name", req.Name,
		"--publish", publish,
		"--restart", "unless-stopped",
	}
	for key, value := range req.Env {
		args = append(args, "--env", key+"="+value)
	}
	for key, value := range req.Labels {
		args = append(args, "--label", key+"="+value)
	}
	args = append(args, req.Image)

	if err := d.run(ctx, args, nil, logs); err != nil {
		return ReleaseResult{}, err
	}

	port, err := d.publishedPort(ctx, req.Name, req.Port)
	if err != nil {
		return ReleaseResult{}, err
	}

	return ReleaseResult{
		URL:        fmt.Sprintf("http://localhost:%s", port),
		WorkloadID: req.Name,
	}, nil
}

// publishedPort asks Docker which host port it bound, which is the only way to
// know when the port was chosen dynamically.
func (d *DockerDriver) publishedPort(ctx context.Context, name string, containerPort int) (string, error) {
	out, err := exec.CommandContext(ctx, d.binary(), "port", name, fmt.Sprint(containerPort)).Output()
	if err != nil {
		return "", fmt.Errorf("read published port: %w", err)
	}

	// Output is one binding per line, "0.0.0.0:32770". A daemon with IPv6
	// enabled prints "[::]:32770" beside it, and prints only that one when the
	// binding is IPv6-only. The port is therefore whatever follows the *last*
	// colon: cutting at the first reads "[::]:32770" as a port of ":]:32770".
	first, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	first = strings.TrimSpace(first)

	// LastIndex is -1 when the line carries no colon at all, which leaves the
	// whole of it to fail the check below.
	port := first[strings.LastIndex(first, ":")+1:]
	if _, err := strconv.Atoi(port); err != nil {
		// Refusing a line we cannot read beats releasing on a made-up port.
		// The environment's URL is built from this, so guessing here produces
		// an address that never answers and never says why.
		return "", fmt.Errorf("could not parse published port from %q", first)
	}
	return port, nil
}

// run executes a docker command, streaming its output to logs as it arrives.
func (d *DockerDriver) run(ctx context.Context, args []string, stdin io.Reader, logs LogWriter) error {
	cmd := exec.CommandContext(ctx, d.binary(), args...)
	cmd.Stdin = stdin

	if err := streamCommand(cmd, logs); err != nil {
		return fmt.Errorf("docker %s: %w", args[0], err)
	}
	return nil
}
