package deploy

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
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
	}
	args = append(args, req.ContextDir)

	if err := d.run(ctx, args, stdin, logs); err != nil {
		return BuildResult{}, err
	}
	return BuildResult{Image: req.Tag}, nil
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

	// Output looks like "0.0.0.0:32770" and may list several bindings.
	first, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	_, port, found := strings.Cut(strings.TrimSpace(first), ":")
	if !found || port == "" {
		return "", fmt.Errorf("could not parse published port from %q", first)
	}
	return port, nil
}

// run executes a docker command, streaming both output streams to logs as
// they arrive rather than buffering until the command finishes. A build that
// takes minutes should show progress while it runs.
func (d *DockerDriver) run(ctx context.Context, args []string, stdin io.Reader, logs LogWriter) error {
	cmd := exec.CommandContext(ctx, d.binary(), args...)
	cmd.Stdin = stdin

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("pipe stdout: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("pipe stderr: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start docker %s: %w", args[0], err)
	}

	// Both pipes must be drained concurrently: a command that fills one while
	// the reader is blocked on the other would deadlock.
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); scan(stdout, "stdout", logs) }()
	go func() { defer wg.Done(); scan(stderr, "stderr", logs) }()
	wg.Wait()

	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("docker %s: %w", args[0], err)
	}
	return nil
}

func scan(r io.Reader, stream string, logs LogWriter) {
	scanner := bufio.NewScanner(r)
	// Build output can carry very long lines; the default 64 KiB limit would
	// abort the scan and silently truncate the log.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		logs.WriteLine(stream, scanner.Text())
	}
}
