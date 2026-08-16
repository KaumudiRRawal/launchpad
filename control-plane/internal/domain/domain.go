// Package domain holds the control-plane entities and the rules that decide
// whether a caller's input is acceptable. It deliberately depends on nothing
// else in the project, so the rules can be read and tested in isolation.
package domain

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Sentinel errors the store layer returns and the HTTP layer maps to status
// codes. Handlers match on these rather than inspecting driver-specific errors.
var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("already exists")

	// ErrInvalidTransition reports a deployment status change the lifecycle
	// does not permit, such as reviving a failed deployment.
	ErrInvalidTransition = errors.New("invalid status transition")
)

// DeploymentJob is everything a worker needs to build and release one
// deployment, flattened from the rows it is spread across so the worker makes
// one query rather than four.
type DeploymentJob struct {
	DeploymentID  string
	ServiceID     string
	EnvironmentID string
	CommitSHA     string
	RepoURL       string
	SourcePath    string
	Port          int
	ServiceName   string
	Subdomain     string
}

// DeploymentLog is one line of build or release output.
type DeploymentLog struct {
	Seq      int       `json:"seq"`
	Stream   string    `json:"stream"`
	Message  string    `json:"message"`
	LoggedAt time.Time `json:"logged_at"`
}

// ValidationError reports a single rejected field. The field name is included
// so the API can tell a caller exactly what to fix.
type ValidationError struct {
	Field   string
	Message string
}

func (e ValidationError) Error() string {
	return fmt.Sprintf("%s: %s", e.Field, e.Message)
}

// ValidationErrors is every problem found in one request. Validation collects
// all of them instead of stopping at the first, so a caller fixing a form does
// not have to submit repeatedly to discover the next mistake.
type ValidationErrors []ValidationError

func (e ValidationErrors) Error() string {
	parts := make([]string, len(e))
	for i, ve := range e {
		parts[i] = ve.Error()
	}
	return strings.Join(parts, "; ")
}

// OrNil returns nil when there are no problems, which keeps callers to the
// usual `if err := ...; err != nil` shape.
func (e ValidationErrors) OrNil() error {
	if len(e) == 0 {
		return nil
	}
	return e
}

// EnvironmentKind distinguishes the two kinds of environment. It matches the
// environment_kind enum in the database.
type EnvironmentKind string

const (
	EnvironmentPreview    EnvironmentKind = "preview"
	EnvironmentProduction EnvironmentKind = "production"
)

// Valid reports whether k is a kind the database will accept.
func (k EnvironmentKind) Valid() bool {
	return k == EnvironmentPreview || k == EnvironmentProduction
}

// DeploymentStatus is the state of one deployment. It matches the
// deployment_status enum in the database.
type DeploymentStatus string

const (
	DeploymentQueued     DeploymentStatus = "queued"
	DeploymentBuilding   DeploymentStatus = "building"
	DeploymentDeploying  DeploymentStatus = "deploying"
	DeploymentLive       DeploymentStatus = "live"
	DeploymentFailed     DeploymentStatus = "failed"
	DeploymentSuperseded DeploymentStatus = "superseded"
)

// Terminal reports whether s is an end state, meaning the deployment will not
// change again on its own.
func (s DeploymentStatus) Terminal() bool {
	return s == DeploymentLive || s == DeploymentFailed || s == DeploymentSuperseded
}

// allowedTransitions encodes the deployment lifecycle. Anything absent is
// rejected, so an out-of-order worker update cannot resurrect a failed
// deployment or move one straight from queued to live without building.
var allowedTransitions = map[DeploymentStatus][]DeploymentStatus{
	DeploymentQueued:     {DeploymentBuilding, DeploymentFailed, DeploymentSuperseded},
	DeploymentBuilding:   {DeploymentDeploying, DeploymentFailed, DeploymentSuperseded},
	DeploymentDeploying:  {DeploymentLive, DeploymentFailed, DeploymentSuperseded},
	DeploymentLive:       {DeploymentSuperseded},
	DeploymentFailed:     {},
	DeploymentSuperseded: {},
}

// CanTransitionTo reports whether moving from s to next is legal.
func (s DeploymentStatus) CanTransitionTo(next DeploymentStatus) bool {
	for _, allowed := range allowedTransitions[s] {
		if allowed == next {
			return true
		}
	}
	return false
}

type Account struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Project struct {
	ID            string    `json:"id"`
	AccountID     string    `json:"account_id"`
	Slug          string    `json:"slug"`
	Name          string    `json:"name"`
	RepoURL       string    `json:"repo_url"`
	DefaultBranch string    `json:"default_branch"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type Service struct {
	ID         string    `json:"id"`
	ProjectID  string    `json:"project_id"`
	Name       string    `json:"name"`
	SourcePath string    `json:"source_path"`
	Port       int       `json:"port"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type Environment struct {
	ID        string          `json:"id"`
	ProjectID string          `json:"project_id"`
	Kind      EnvironmentKind `json:"kind"`
	Name      string          `json:"name"`
	Subdomain string          `json:"subdomain"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}

type Deployment struct {
	ID            string           `json:"id"`
	ServiceID     string           `json:"service_id"`
	EnvironmentID string           `json:"environment_id"`
	CommitSHA     string           `json:"commit_sha"`
	Status        DeploymentStatus `json:"status"`
	ImageRef      *string          `json:"image_ref,omitempty"`
	URL           *string          `json:"url,omitempty"`
	ErrorMessage  *string          `json:"error_message,omitempty"`
	QueuedAt      time.Time        `json:"queued_at"`
	StartedAt     *time.Time       `json:"started_at,omitempty"`
	CompletedAt   *time.Time       `json:"completed_at,omitempty"`
}

// APIKey never carries the plaintext token; that exists only in the response
// to the request that created it.
type APIKey struct {
	ID          string     `json:"id"`
	AccountID   string     `json:"account_id"`
	Name        string     `json:"name"`
	TokenPrefix string     `json:"token_prefix"`
	CreatedAt   time.Time  `json:"created_at"`
	LastUsedAt  *time.Time `json:"last_used_at,omitempty"`
	RevokedAt   *time.Time `json:"revoked_at,omitempty"`
}

// slugPattern is deliberately a DNS label: a project slug and an environment
// name are both concatenated into a subdomain, so anything not valid in a
// hostname cannot be allowed in here.
var slugPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

const maxSlugLen = 63 // RFC 1035 limit for a single DNS label.

// ValidateSlug checks a value destined to become part of a hostname.
func ValidateSlug(field, value string) *ValidationError {
	switch {
	case value == "":
		return &ValidationError{Field: field, Message: "is required"}
	case len(value) > maxSlugLen:
		return &ValidationError{Field: field, Message: fmt.Sprintf("must be at most %d characters", maxSlugLen)}
	case !slugPattern.MatchString(value):
		return &ValidationError{
			Field:   field,
			Message: "must be lowercase letters, digits and hyphens, and may not start or end with a hyphen",
		}
	}
	return nil
}

func validateName(field, value string) *ValidationError {
	trimmed := strings.TrimSpace(value)
	switch {
	case trimmed == "":
		return &ValidationError{Field: field, Message: "is required"}
	case len(trimmed) > 200:
		return &ValidationError{Field: field, Message: "must be at most 200 characters"}
	}
	return nil
}

// CreateProjectInput is the accepted body for creating a project.
type CreateProjectInput struct {
	Slug          string `json:"slug"`
	Name          string `json:"name"`
	RepoURL       string `json:"repo_url"`
	DefaultBranch string `json:"default_branch"`
}

// Validate checks the input and fills in defaults. It has a pointer receiver
// because it normalises the input in place.
func (in *CreateProjectInput) Validate() error {
	var problems ValidationErrors

	in.Slug = strings.TrimSpace(in.Slug)
	in.Name = strings.TrimSpace(in.Name)
	in.RepoURL = strings.TrimSpace(in.RepoURL)
	in.DefaultBranch = strings.TrimSpace(in.DefaultBranch)

	if err := ValidateSlug("slug", in.Slug); err != nil {
		problems = append(problems, *err)
	}
	if err := validateName("name", in.Name); err != nil {
		problems = append(problems, *err)
	}
	if err := validateRepoURL(in.RepoURL); err != nil {
		problems = append(problems, *err)
	}
	if in.DefaultBranch == "" {
		in.DefaultBranch = "main"
	}

	return problems.OrNil()
}

// validateRepoURL accepts only HTTP(S) clone URLs. SSH remotes are rejected
// because the builder authenticates with a token, not a deploy key, so an
// ssh:// URL would fail later at clone time with a far less obvious error.
func validateRepoURL(raw string) *ValidationError {
	const field = "repo_url"
	if raw == "" {
		return &ValidationError{Field: field, Message: "is required"}
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return &ValidationError{Field: field, Message: "is not a valid URL"}
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return &ValidationError{Field: field, Message: "must be an http or https clone URL"}
	}
	if parsed.Host == "" {
		return &ValidationError{Field: field, Message: "must include a host"}
	}
	return nil
}

// CreateServiceInput is the accepted body for creating a service.
type CreateServiceInput struct {
	Name       string `json:"name"`
	SourcePath string `json:"source_path"`
	Port       int    `json:"port"`
}

func (in *CreateServiceInput) Validate() error {
	var problems ValidationErrors

	in.Name = strings.TrimSpace(in.Name)
	in.SourcePath = strings.TrimSpace(in.SourcePath)

	if err := ValidateSlug("name", in.Name); err != nil {
		problems = append(problems, *err)
	}

	if in.SourcePath == "" {
		in.SourcePath = "."
	}
	// A source path is joined onto a clone directory, so anything that could
	// climb out of it is refused rather than sanitised.
	if strings.HasPrefix(in.SourcePath, "/") || strings.Contains(in.SourcePath, "..") {
		problems = append(problems, ValidationError{
			Field:   "source_path",
			Message: "must be a relative path inside the repository",
		})
	}

	if in.Port == 0 {
		in.Port = 8080
	}
	if in.Port < 1 || in.Port > 65535 {
		problems = append(problems, ValidationError{
			Field:   "port",
			Message: "must be between 1 and 65535",
		})
	}

	return problems.OrNil()
}

// CreateEnvironmentInput is the accepted body for creating an environment.
type CreateEnvironmentInput struct {
	Kind EnvironmentKind `json:"kind"`
	Name string          `json:"name"`
}

func (in *CreateEnvironmentInput) Validate() error {
	var problems ValidationErrors

	in.Name = strings.TrimSpace(in.Name)

	if !in.Kind.Valid() {
		problems = append(problems, ValidationError{
			Field:   "kind",
			Message: `must be "preview" or "production"`,
		})
	}
	if err := ValidateSlug("name", in.Name); err != nil {
		problems = append(problems, *err)
	}

	return problems.OrNil()
}

// Subdomain builds the hostname label for an environment. Both parts are
// already validated as DNS labels; the combined length is checked because two
// legal labels can concatenate into an illegal one.
func Subdomain(projectSlug, environmentName string) (string, error) {
	combined := environmentName + "-" + projectSlug
	if len(combined) > maxSlugLen {
		return "", ValidationError{
			Field:   "name",
			Message: fmt.Sprintf("environment name and project slug combine to %d characters, which exceeds the %d-character hostname limit", len(combined), maxSlugLen),
		}
	}
	return combined, nil
}

// commitSHAPattern matches a full 40-character git object ID. Short SHAs are
// rejected: a deployment record has to name exactly one commit forever, and a
// short SHA can become ambiguous as the repository grows.
var commitSHAPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

// CreateDeploymentInput is the accepted body for triggering a deployment.
type CreateDeploymentInput struct {
	ServiceID     string `json:"service_id"`
	EnvironmentID string `json:"environment_id"`
	CommitSHA     string `json:"commit_sha"`
}

func (in *CreateDeploymentInput) Validate() error {
	var problems ValidationErrors

	in.ServiceID = strings.TrimSpace(in.ServiceID)
	in.EnvironmentID = strings.TrimSpace(in.EnvironmentID)
	in.CommitSHA = strings.ToLower(strings.TrimSpace(in.CommitSHA))

	if in.ServiceID == "" {
		problems = append(problems, ValidationError{Field: "service_id", Message: "is required"})
	}
	if in.EnvironmentID == "" {
		problems = append(problems, ValidationError{Field: "environment_id", Message: "is required"})
	}
	if !commitSHAPattern.MatchString(in.CommitSHA) {
		problems = append(problems, ValidationError{
			Field:   "commit_sha",
			Message: "must be a full 40-character git commit SHA",
		})
	}

	return problems.OrNil()
}

// CreateAPIKeyInput is the accepted body for minting an API key.
type CreateAPIKeyInput struct {
	Name string `json:"name"`
}

func (in *CreateAPIKeyInput) Validate() error {
	var problems ValidationErrors

	in.Name = strings.TrimSpace(in.Name)
	if err := validateName("name", in.Name); err != nil {
		problems = append(problems, *err)
	}

	return problems.OrNil()
}
