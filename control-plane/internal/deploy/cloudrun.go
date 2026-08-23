package deploy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// CloudRunDriver builds images with Cloud Build and releases them as Cloud Run
// services.
//
// Like the Docker driver it shells out to the vendor CLI rather than linking
// the client libraries. gcloud is the documented interface to these products,
// the alternative is a large generated dependency tree to express four calls,
// and a remote deployment that fails can be reproduced by pasting the command
// out of the build log — which matters far more when the failure happened in
// somebody else's data centre. The cost is that the control-plane image has to
// carry the CLI, which control-plane/Dockerfile does.
type CloudRunDriver struct {
	// Project is the Google Cloud project that owns the builds, the images and
	// the services.
	Project string
	// Region is where images are built and services run. One region for both:
	// a service pulling its image across regions pays for it on every cold
	// start, and an install with data-residency rules does not get to spread
	// the two around.
	Region string
	// Repository is the Artifact Registry Docker repository builds push to.
	Repository string
	// ServiceAccount is the identity released workloads run as. Empty falls
	// back to the project's default compute account, which holds far more
	// permission than somebody else's code should get, so the Terraform module
	// makes a dedicated one and names it here.
	ServiceAccount string
	// AllowUnauthenticated makes released workloads publicly reachable.
	//
	// It defaults on because a deployed application is meant to be reached, and
	// the platform's proxy forwards to it without minting an identity token: a
	// service requiring IAM authentication would answer 403 to every request
	// that came through the front door. Installs that put something which does
	// sign requests in front — a load balancer with IAP — turn it off.
	AllowUnauthenticated bool
	// Binary overrides the gcloud executable, for testing.
	Binary string
}

func (d *CloudRunDriver) Name() string { return "cloudrun" }

func (d *CloudRunDriver) binary() string {
	if d.Binary != "" {
		return d.Binary
	}
	return "gcloud"
}

// validate reports missing settings before a command is run, because gcloud's
// own complaint about an absent --project arrives halfway through a build log
// and reads like a transient failure rather than a misconfiguration.
func (d *CloudRunDriver) validate() error {
	var missing []string
	if d.Project == "" {
		missing = append(missing, "project")
	}
	if d.Region == "" {
		missing = append(missing, "region")
	}
	if d.Repository == "" {
		missing = append(missing, "artifact registry repository")
	}
	if len(missing) > 0 {
		return fmt.Errorf("cloud run driver is not configured: missing %s", strings.Join(missing, ", "))
	}
	return nil
}

// Build submits the context to Cloud Build, which produces the image and
// pushes it to Artifact Registry.
//
// Building in the cloud rather than locally is deliberate: the control plane
// then needs no Docker daemon of its own, so it can run as a Cloud Run service
// itself, and a build gets a machine sized for building instead of whatever is
// left over on the box serving the API.
func (d *CloudRunDriver) Build(ctx context.Context, req BuildRequest, logs LogWriter) (BuildResult, error) {
	if err := d.validate(); err != nil {
		return BuildResult{}, err
	}

	// Cloud Build reads the Dockerfile out of the uploaded context; there is no
	// equivalent of docker build --file - to pipe one in. Writing the generated
	// file into the context cannot clobber anything, because detection only
	// generates a Dockerfile for a repository that had none.
	if req.Dockerfile != "" {
		generated := filepath.Join(req.ContextDir, "Dockerfile")
		if err := os.WriteFile(generated, []byte(req.Dockerfile), 0o644); err != nil {
			return BuildResult{}, fmt.Errorf("write generated Dockerfile: %w", err)
		}
	}

	image := d.imageRef(req.Tag)
	args := []string{
		"builds", "submit",
		"--project", d.Project,
		"--region", d.Region,
		"--tag", image,
		"--quiet",
		req.ContextDir,
	}

	if err := d.run(ctx, args, logs); err != nil {
		return BuildResult{}, err
	}
	return BuildResult{Image: image}, nil
}

// Release deploys the image as a Cloud Run service, replacing the running
// revision of a service that already carries the workload's name.
func (d *CloudRunDriver) Release(ctx context.Context, req ReleaseRequest, logs LogWriter) (ReleaseResult, error) {
	if err := d.validate(); err != nil {
		return ReleaseResult{}, err
	}

	service := cloudRunServiceName(req.Name)

	args := []string{
		"run", "deploy", service,
		"--project", d.Project,
		"--region", d.Region,
		"--platform", "managed",
		"--image", req.Image,
		"--port", strconv.Itoa(req.Port),
		"--quiet",
	}

	// --set-* rather than --update-*: a revision is a whole configuration, not
	// a patch on its predecessor, so a variable dropped from a deployment has
	// to be absent from the next revision instead of surviving because nothing
	// asked for it to go.
	if env := cloudRunEnvFlag(req.Env); env != "" {
		args = append(args, "--set-env-vars", env)
	}
	if labels := cloudRunLabelFlag(req.Labels); labels != "" {
		args = append(args, "--labels", labels)
	}
	if d.ServiceAccount != "" {
		args = append(args, "--service-account", d.ServiceAccount)
	}
	if d.AllowUnauthenticated {
		args = append(args, "--allow-unauthenticated")
	} else {
		args = append(args, "--no-allow-unauthenticated")
	}

	if err := d.run(ctx, args, logs); err != nil {
		return ReleaseResult{}, err
	}

	url, err := d.serviceURL(ctx, service)
	if err != nil {
		return ReleaseResult{}, err
	}

	return ReleaseResult{URL: url, WorkloadID: service}, nil
}

// serviceURL asks Cloud Run where it put the service. The address is assigned
// by the platform, so as with the Docker driver's published port there is
// nothing to do but read it back.
func (d *CloudRunDriver) serviceURL(ctx context.Context, service string) (string, error) {
	out, err := exec.CommandContext(ctx, d.binary(),
		"run", "services", "describe", service,
		"--project", d.Project,
		"--region", d.Region,
		"--platform", "managed",
		"--format", "value(status.url)",
	).Output()
	if err != nil {
		return "", fmt.Errorf("read service url: %w", err)
	}

	url := strings.TrimSpace(string(out))
	if url == "" {
		return "", fmt.Errorf("cloud run reported no url for service %q", service)
	}
	return url, nil
}

// imageRef qualifies the engine's local tag into an Artifact Registry
// reference.
//
// Only the last component of the tag is used. The engine names images
// "launchpad/<service>:<deployment>" so they stand out in a local daemon shared
// with everything else on the machine; a registry path already carries the
// project and the repository, and repeating the namespace inside it would say
// launchpad twice. The deployment ID stays in the tag, so the image is still
// traceable to what produced it.
func (d *CloudRunDriver) imageRef(tag string) string {
	return fmt.Sprintf("%s-docker.pkg.dev/%s/%s/%s", d.Region, d.Project, d.Repository, path.Base(tag))
}

// run executes a gcloud command, streaming its output to logs as it arrives.
//
// The command itself is written to the log first. A local Docker build can be
// reproduced by looking at what the driver would have done; a Cloud Build that
// failed for one project in one region cannot, so the log carries the line an
// operator needs to paste into a terminal.
func (d *CloudRunDriver) run(ctx context.Context, args []string, logs LogWriter) error {
	logs.WriteLine("stdout", "$ gcloud "+strings.Join(redactEnvFlag(args), " "))

	cmd := exec.CommandContext(ctx, d.binary(), args...)
	if err := streamCommand(cmd, logs); err != nil {
		return fmt.Errorf("gcloud %s %s: %w", args[0], args[1], err)
	}
	return nil
}

// redactEnvFlag hides environment values in the echoed command. Build logs are
// served to anyone who can read the deployment, and the engine's variables are
// harmless today only because nothing has yet passed a workload a credential
// through them.
func redactEnvFlag(args []string) []string {
	safe := slices.Clone(args)
	for i, arg := range safe {
		if arg == "--set-env-vars" && i+1 < len(safe) {
			safe[i+1] = "[redacted]"
		}
	}
	return safe
}

// cloudRunReservedEnv are variables Cloud Run injects itself and rejects in a
// deploy. PORT is the one that bites: the engine passes it to every driver, and
// Cloud Run takes it from --port instead, so sending both fails the deployment
// rather than being ignored as a duplicate.
var cloudRunReservedEnv = map[string]bool{
	"PORT":            true,
	"K_SERVICE":       true,
	"K_REVISION":      true,
	"K_CONFIGURATION": true,
}

// cloudRunEnvFlag renders the workload's environment for --set-env-vars.
//
// gcloud splits the value on commas unless it opens with ^DELIM^, so a
// delimiter is declared rather than trusting that no value ever contains a
// comma. Keys are sorted so the same environment always produces the same
// command, which is what makes the echoed line worth comparing between two
// deployments.
func cloudRunEnvFlag(env map[string]string) string {
	keys := make([]string, 0, len(env))
	for key := range env {
		if cloudRunReservedEnv[strings.ToUpper(key)] {
			continue
		}
		keys = append(keys, key)
	}
	if len(keys) == 0 {
		return ""
	}
	slices.Sort(keys)

	pairs := make([]string, 0, len(keys))
	for _, key := range keys {
		pairs = append(pairs, key+"="+env[key])
	}
	return "^|^" + strings.Join(pairs, "|")
}

// cloudRunLabelFlag renders the workload's labels for --labels.
//
// Google Cloud accepts only lowercase letters, digits, hyphens and underscores
// in a label, so the engine's dotted keys — launchpad.deployment — are
// translated rather than sent to be rejected. The engine's keys all begin with
// a letter, which Google also requires of a key; nothing here enforces that,
// because a key that did not is a bug in the caller rather than something to
// paper over. Sanitising removes commas, so the comma-separated form gcloud
// expects here is unambiguous without a declared delimiter.
func cloudRunLabelFlag(labels map[string]string) string {
	safe := make(map[string]string, len(labels))
	for key, value := range labels {
		safe[cloudRunLabel(key)] = cloudRunLabel(value)
	}

	pairs := make([]string, 0, len(safe))
	for _, key := range slices.Sorted(maps.Keys(safe)) {
		pairs = append(pairs, key+"="+safe[key])
	}
	return strings.Join(pairs, ",")
}

// cloudRunLabel reduces a string to what Google Cloud stores in a label.
//
// Values are reshaped as freely as keys. A label is how an orphaned workload is
// found in the console; which deployment produced it is recorded exactly in the
// database, so a value that had to be altered to be storable costs nothing.
func cloudRunLabel(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}

	out := b.String()
	if len(out) > maxCloudRunLabel {
		out = out[:maxCloudRunLabel]
	}
	return out
}

const (
	// maxCloudRunLabel is Google Cloud's limit on a label key or value.
	maxCloudRunLabel = 63
	// maxCloudRunService is Cloud Run's limit on a service name. It is 49 and
	// not the 63 of a DNS label because revisions are named
	// <service>-<suffix> and have to fit in a label themselves.
	maxCloudRunService = 49
)

// cloudRunServiceName fits a workload name into what Cloud Run accepts.
//
// An over-long name is truncated and given a digest of the whole, so two
// environments whose names share the first forty characters still get two
// services, and one name always maps to the same service — which is what makes
// a release replace its predecessor instead of standing a second one up beside
// it. The engine's names begin "launchpad-" and carry only characters legal in
// a hostname, so nothing else needs adjusting.
func cloudRunServiceName(name string) string {
	if len(name) <= maxCloudRunService {
		return name
	}

	sum := sha256.Sum256([]byte(name))
	digest := hex.EncodeToString(sum[:])[:8]
	// A name may not end in a hyphen, and truncation can easily land on one.
	head := strings.TrimRight(name[:maxCloudRunService-len(digest)-1], "-")
	return head + "-" + digest
}
