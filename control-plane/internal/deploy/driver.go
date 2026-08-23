package deploy

import "context"

// BuildRequest describes one image to produce.
type BuildRequest struct {
	// ContextDir is the directory on disk to build from.
	ContextDir string
	// Dockerfile is the build definition. When empty the driver uses the
	// Dockerfile already present in ContextDir.
	Dockerfile string
	// Tag is the image reference to produce.
	Tag string
}

// BuildResult reports the image a build produced.
//
// It is not always the tag that was asked for. A driver releasing into a cloud
// has to push somewhere the runtime can pull from, so it qualifies the
// requested tag into a registry reference — and that reference, not the
// request, is what the release and the deployment record have to name.
type BuildResult struct {
	// Image is the reference to release.
	Image string
}

// ReleaseRequest describes one image to put into service.
type ReleaseRequest struct {
	// Name identifies the workload, and must be stable across deployments of
	// the same service and environment so a release replaces its predecessor.
	Name string
	// Image is the reference produced by Build.
	Image string
	// Port is the port the container listens on.
	Port int
	// Env is injected into the running workload.
	Env map[string]string
	// Labels record which deployment a workload belongs to, so orphans can be
	// found and reaped.
	Labels map[string]string
}

// ReleaseResult reports where a released workload can be reached.
type ReleaseResult struct {
	// URL is the address the workload now answers on.
	URL string
	// WorkloadID identifies the running unit to the driver.
	WorkloadID string
}

// Driver is a deployment backend. The Docker driver runs workloads locally;
// the Cloud Run driver runs them on Google Cloud. The engine is written
// against this interface alone, so the two are interchangeable.
type Driver interface {
	// Build produces an image and streams progress to logs.
	Build(ctx context.Context, req BuildRequest, logs LogWriter) (BuildResult, error)
	// Release puts an image into service, replacing any earlier workload with
	// the same name.
	Release(ctx context.Context, req ReleaseRequest, logs LogWriter) (ReleaseResult, error)
	// Name identifies the driver in logs.
	Name() string
}

// Fetcher retrieves a repository at a specific commit into destDir.
type Fetcher interface {
	Fetch(ctx context.Context, repoURL, commitSHA, destDir string) error
}

// LogWriter receives build and release output one line at a time. The engine
// supplies an implementation that persists lines so the dashboard can stream
// them while a deployment is still running.
type LogWriter interface {
	WriteLine(stream, message string)
}
