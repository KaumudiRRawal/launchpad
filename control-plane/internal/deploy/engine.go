package deploy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/KaumudiRRawal/launchpad/control-plane/internal/domain"
)

// Store is the slice of the repository the engine needs. Declared here at the
// point of use so the engine can be tested without a database.
type Store interface {
	ClaimNextDeployment(ctx context.Context) (domain.DeploymentJob, error)
	TransitionDeployment(ctx context.Context, id string, from, to domain.DeploymentStatus) error
	MarkDeploymentLive(ctx context.Context, id, imageRef, url string) error
	MarkDeploymentFailed(ctx context.Context, id, reason string) error
	SupersedePriorDeployments(ctx context.Context, serviceID, environmentID, keepID string) error
	AppendDeploymentLog(ctx context.Context, deploymentID string, seq int, stream, message string) error
}

// Engine runs queued deployments to completion.
type Engine struct {
	Store   Store
	Driver  Driver
	Fetcher Fetcher
	Log     *slog.Logger

	// WorkDir is where sources are checked out. Defaults to the system
	// temporary directory.
	WorkDir string
	// PollInterval is how often an idle worker looks for new work.
	PollInterval time.Duration
	// BuildTimeout caps a single deployment, so one wedged build cannot
	// occupy a worker forever.
	BuildTimeout time.Duration
}

// Run processes deployments until ctx is cancelled.
//
// It polls rather than listening for a notification because the claim query
// already handles concurrency safely, and polling keeps a restarted worker
// from missing deployments queued while it was down.
func (e *Engine) Run(ctx context.Context) {
	interval := e.PollInterval
	if interval <= 0 {
		interval = 2 * time.Second
	}

	e.Log.Info("deploy engine started",
		slog.String("driver", e.Driver.Name()),
		slog.Duration("poll_interval", interval))

	for {
		claimed, err := e.processNext(ctx)
		switch {
		case ctx.Err() != nil:
			e.Log.Info("deploy engine stopped")
			return
		case err != nil:
			e.Log.Error("deploy engine iteration failed", slog.String("error", err.Error()))
		case claimed:
			// More work may be waiting; look again without pausing.
			continue
		}

		select {
		case <-ctx.Done():
			e.Log.Info("deploy engine stopped")
			return
		case <-time.After(interval):
		}
	}
}

// processNext claims one deployment and runs it. It reports whether anything
// was claimed, so a busy worker does not sleep between deployments.
func (e *Engine) processNext(ctx context.Context) (bool, error) {
	job, err := e.Store.ClaimNextDeployment(ctx)
	if errors.Is(err, domain.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("claim deployment: %w", err)
	}

	timeout := e.BuildTimeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	buildCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	logs := newLogRecorder(e.Store, job.DeploymentID)
	defer logs.Flush()

	if err := e.deploy(buildCtx, job, logs); err != nil {
		logs.WriteLine("stderr", "deployment failed: "+err.Error())
		logs.Flush()

		// Uses the parent context: the build context may already be expired,
		// and a failure that cannot be recorded leaves a deployment stuck in
		// building forever.
		if markErr := e.Store.MarkDeploymentFailed(context.WithoutCancel(ctx),
			job.DeploymentID, truncate(err.Error(), 2000)); markErr != nil {
			e.Log.Error("could not record deployment failure",
				slog.String("deployment_id", job.DeploymentID),
				slog.String("error", markErr.Error()))
		}
		e.Log.Warn("deployment failed",
			slog.String("deployment_id", job.DeploymentID),
			slog.String("error", err.Error()))
		return true, nil
	}

	return true, nil
}

// deploy runs the pipeline for one already-claimed job. The caller owns
// recording failure, so every error here simply returns.
func (e *Engine) deploy(ctx context.Context, job domain.DeploymentJob, logs *logRecorder) error {
	started := time.Now()
	logs.WriteLine("stdout", fmt.Sprintf("deploying %s at %s", job.ServiceName, job.CommitSHA[:12]))

	root := e.WorkDir
	if root == "" {
		root = os.TempDir()
	}
	checkout, err := os.MkdirTemp(root, "launchpad-build-")
	if err != nil {
		return fmt.Errorf("create build directory: %w", err)
	}
	// The checkout is other people's source code; it does not outlive the
	// build that needed it.
	defer os.RemoveAll(checkout)

	logs.WriteLine("stdout", "fetching "+job.RepoURL)
	if err := e.Fetcher.Fetch(ctx, job.RepoURL, job.CommitSHA, checkout); err != nil {
		return fmt.Errorf("fetch source: %w", err)
	}

	contextDir := filepath.Join(checkout, filepath.Clean(job.SourcePath))
	strategy, err := Detect(os.DirFS(checkout), job.SourcePath)
	if err != nil {
		return err
	}
	logs.WriteLine("stdout", "detected build strategy: "+string(strategy))

	// The tag is derived from the deployment ID, so an image is traceable back
	// to exactly the deployment that produced it.
	tag := fmt.Sprintf("launchpad/%s:%s", job.ServiceName, job.DeploymentID[:8])

	logs.WriteLine("stdout", "building "+tag)
	if err := e.Driver.Build(ctx, BuildRequest{
		ContextDir: contextDir,
		Dockerfile: strategy.Dockerfile(job.Port),
		Tag:        tag,
	}, logs); err != nil {
		return fmt.Errorf("build image: %w", err)
	}

	if err := e.Store.TransitionDeployment(ctx, job.DeploymentID,
		domain.DeploymentBuilding, domain.DeploymentDeploying); err != nil {
		return fmt.Errorf("move to deploying: %w", err)
	}

	logs.WriteLine("stdout", "releasing to "+job.Subdomain)
	result, err := e.Driver.Release(ctx, ReleaseRequest{
		// Stable across deployments of this service and environment, so a
		// release replaces its predecessor rather than accumulating.
		Name:  workloadName(job),
		Image: tag,
		Port:  job.Port,
		Env: map[string]string{
			"PORT":                  fmt.Sprint(job.Port),
			"LAUNCHPAD_ENVIRONMENT": job.Subdomain,
		},
		Labels: map[string]string{
			"launchpad.deployment":  job.DeploymentID,
			"launchpad.service":     job.ServiceID,
			"launchpad.environment": job.EnvironmentID,
		},
	}, logs)
	if err != nil {
		return fmt.Errorf("release: %w", err)
	}

	if err := e.Store.MarkDeploymentLive(ctx, job.DeploymentID, tag, result.URL); err != nil {
		return fmt.Errorf("mark live: %w", err)
	}

	// Best effort: the new deployment is already live, and failing to retire
	// its predecessor is untidy rather than incorrect.
	if err := e.Store.SupersedePriorDeployments(ctx, job.ServiceID, job.EnvironmentID, job.DeploymentID); err != nil {
		e.Log.Warn("could not supersede prior deployments",
			slog.String("deployment_id", job.DeploymentID),
			slog.String("error", err.Error()))
	}

	elapsed := time.Since(started)
	logs.WriteLine("stdout", fmt.Sprintf("live at %s in %s", result.URL, elapsed.Round(time.Millisecond)))
	e.Log.Info("deployment live",
		slog.String("deployment_id", job.DeploymentID),
		slog.String("url", result.URL),
		slog.Duration("duration", elapsed))
	return nil
}

// workloadName is stable for a given service and environment so that releasing
// replaces the running workload instead of starting a second one beside it.
func workloadName(job domain.DeploymentJob) string {
	return "launchpad-" + job.Subdomain
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

// logRecorder persists build output. Lines are buffered and flushed in
// batches, because a verbose build emits thousands of lines and one INSERT
// each would make the database the bottleneck in a deployment.
type logRecorder struct {
	store        Store
	deploymentID string

	mu     sync.Mutex
	seq    int
	buffer []domain.DeploymentLog
}

const logFlushThreshold = 25

func newLogRecorder(store Store, deploymentID string) *logRecorder {
	return &logRecorder{store: store, deploymentID: deploymentID}
}

// WriteLine records one line. It never returns an error: losing a log line
// must not fail a deployment that is otherwise succeeding.
func (r *logRecorder) WriteLine(stream, message string) {
	for _, line := range strings.Split(strings.TrimRight(message, "\n"), "\n") {
		r.mu.Lock()
		r.seq++
		r.buffer = append(r.buffer, domain.DeploymentLog{
			Seq: r.seq, Stream: stream, Message: line,
		})
		full := len(r.buffer) >= logFlushThreshold
		r.mu.Unlock()

		if full {
			r.Flush()
		}
	}
}

// Flush writes buffered lines. It uses a background context so output is still
// persisted when a deployment is being torn down after a timeout, which is
// exactly when the logs matter most.
func (r *logRecorder) Flush() {
	r.mu.Lock()
	pending := r.buffer
	r.buffer = nil
	r.mu.Unlock()

	if len(pending) == 0 {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	for _, line := range pending {
		_ = r.store.AppendDeploymentLog(ctx, r.deploymentID, line.Seq, line.Stream, line.Message)
	}
}
