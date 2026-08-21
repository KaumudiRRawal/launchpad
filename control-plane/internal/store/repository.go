package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/KaumudiRRawal/launchpad/control-plane/internal/domain"
)

// Repository reads and writes control-plane entities.
type Repository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// PostgreSQL error codes worth distinguishing from a generic failure.
const (
	pgUniqueViolation     = "23505"
	pgForeignKeyViolation = "23503"
)

// translate converts driver errors into the sentinels in the domain package,
// so nothing above this layer needs to understand pgx or SQLSTATE codes.
func translate(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrNotFound
	}

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case pgUniqueViolation:
			return domain.ErrConflict
		case pgForeignKeyViolation:
			// The row this one points at does not exist, which a caller
			// experiences as "the thing I referenced is not there".
			return domain.ErrNotFound
		}
	}
	return err
}

// --- Accounts ---

func (r *Repository) CreateAccount(ctx context.Context, email, name string) (domain.Account, error) {
	const query = `
		INSERT INTO accounts (email, name)
		VALUES ($1, $2)
		RETURNING id, email, name, created_at, updated_at`

	var a domain.Account
	err := r.pool.QueryRow(ctx, query, email, name).
		Scan(&a.ID, &a.Email, &a.Name, &a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		return domain.Account{}, fmt.Errorf("create account: %w", translate(err))
	}
	return a, nil
}

func (r *Repository) GetAccount(ctx context.Context, id string) (domain.Account, error) {
	const query = `
		SELECT id, email, name, created_at, updated_at
		FROM accounts WHERE id = $1`

	var a domain.Account
	err := r.pool.QueryRow(ctx, query, id).
		Scan(&a.ID, &a.Email, &a.Name, &a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		return domain.Account{}, fmt.Errorf("get account: %w", translate(err))
	}
	return a, nil
}

// --- Projects ---

func (r *Repository) CreateProject(ctx context.Context, accountID string, in domain.CreateProjectInput) (domain.Project, error) {
	const query = `
		INSERT INTO projects (account_id, slug, name, repo_url, default_branch)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, account_id, slug, name, repo_url, default_branch, created_at, updated_at`

	var p domain.Project
	err := r.pool.QueryRow(ctx, query, accountID, in.Slug, in.Name, in.RepoURL, in.DefaultBranch).
		Scan(&p.ID, &p.AccountID, &p.Slug, &p.Name, &p.RepoURL, &p.DefaultBranch, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return domain.Project{}, fmt.Errorf("create project: %w", translate(err))
	}
	return p, nil
}

// GetProject scopes the lookup to accountID so one account cannot read
// another's project by guessing its ID. A project belonging to someone else is
// reported as not found rather than forbidden, which avoids confirming that
// the ID exists at all.
func (r *Repository) GetProject(ctx context.Context, accountID, id string) (domain.Project, error) {
	const query = `
		SELECT id, account_id, slug, name, repo_url, default_branch, created_at, updated_at
		FROM projects WHERE id = $1 AND account_id = $2`

	var p domain.Project
	err := r.pool.QueryRow(ctx, query, id, accountID).
		Scan(&p.ID, &p.AccountID, &p.Slug, &p.Name, &p.RepoURL, &p.DefaultBranch, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return domain.Project{}, fmt.Errorf("get project: %w", translate(err))
	}
	return p, nil
}

func (r *Repository) ListProjects(ctx context.Context, accountID string) ([]domain.Project, error) {
	const query = `
		SELECT id, account_id, slug, name, repo_url, default_branch, created_at, updated_at
		FROM projects WHERE account_id = $1
		ORDER BY created_at DESC`

	rows, err := r.pool.Query(ctx, query, accountID)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", translate(err))
	}
	defer rows.Close()

	// Non-nil so an empty result encodes as [] rather than null.
	projects := []domain.Project{}
	for rows.Next() {
		var p domain.Project
		if err := rows.Scan(&p.ID, &p.AccountID, &p.Slug, &p.Name, &p.RepoURL,
			&p.DefaultBranch, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan project: %w", err)
		}
		projects = append(projects, p)
	}
	return projects, rows.Err()
}

func (r *Repository) DeleteProject(ctx context.Context, accountID, id string) error {
	const query = `DELETE FROM projects WHERE id = $1 AND account_id = $2`

	tag, err := r.pool.Exec(ctx, query, id, accountID)
	if err != nil {
		return fmt.Errorf("delete project: %w", translate(err))
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("delete project: %w", domain.ErrNotFound)
	}
	return nil
}

// --- Services ---

// CreateService joins through projects so a caller cannot attach a service to
// a project owned by another account.
func (r *Repository) CreateService(ctx context.Context, accountID, projectID string, in domain.CreateServiceInput) (domain.Service, error) {
	const query = `
		INSERT INTO services (project_id, name, source_path, port)
		SELECT p.id, $3, $4, $5
		FROM projects p
		WHERE p.id = $1 AND p.account_id = $2
		RETURNING id, project_id, name, source_path, port, created_at, updated_at`

	var s domain.Service
	err := r.pool.QueryRow(ctx, query, projectID, accountID, in.Name, in.SourcePath, in.Port).
		Scan(&s.ID, &s.ProjectID, &s.Name, &s.SourcePath, &s.Port, &s.CreatedAt, &s.UpdatedAt)
	if err != nil {
		return domain.Service{}, fmt.Errorf("create service: %w", translate(err))
	}
	return s, nil
}

func (r *Repository) ListServices(ctx context.Context, accountID, projectID string) ([]domain.Service, error) {
	const query = `
		SELECT s.id, s.project_id, s.name, s.source_path, s.port, s.created_at, s.updated_at
		FROM services s
		JOIN projects p ON p.id = s.project_id
		WHERE s.project_id = $1 AND p.account_id = $2
		ORDER BY s.name`

	rows, err := r.pool.Query(ctx, query, projectID, accountID)
	if err != nil {
		return nil, fmt.Errorf("list services: %w", translate(err))
	}
	defer rows.Close()

	services := []domain.Service{}
	for rows.Next() {
		var s domain.Service
		if err := rows.Scan(&s.ID, &s.ProjectID, &s.Name, &s.SourcePath, &s.Port,
			&s.CreatedAt, &s.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan service: %w", err)
		}
		services = append(services, s)
	}
	return services, rows.Err()
}

// --- Environments ---

func (r *Repository) CreateEnvironment(ctx context.Context, accountID, projectID string, in domain.CreateEnvironmentInput, subdomain string) (domain.Environment, error) {
	const query = `
		INSERT INTO environments (project_id, kind, name, subdomain)
		SELECT p.id, $3, $4, $5
		FROM projects p
		WHERE p.id = $1 AND p.account_id = $2
		RETURNING id, project_id, kind, name, subdomain, created_at, updated_at`

	var e domain.Environment
	err := r.pool.QueryRow(ctx, query, projectID, accountID, string(in.Kind), in.Name, subdomain).
		Scan(&e.ID, &e.ProjectID, &e.Kind, &e.Name, &e.Subdomain, &e.CreatedAt, &e.UpdatedAt)
	if err != nil {
		return domain.Environment{}, fmt.Errorf("create environment: %w", translate(err))
	}
	return e, nil
}

// GetEnvironment scopes the lookup to accountID for the same reason
// GetProject does: an environment belonging to someone else is reported as not
// found rather than forbidden, so a caller cannot use the difference to
// discover that an ID exists.
func (r *Repository) GetEnvironment(ctx context.Context, accountID, id string) (domain.Environment, error) {
	const query = `
		SELECT e.id, e.project_id, e.kind, e.name, e.subdomain, e.created_at, e.updated_at
		FROM environments e
		JOIN projects p ON p.id = e.project_id
		WHERE e.id = $1 AND p.account_id = $2`

	var e domain.Environment
	err := r.pool.QueryRow(ctx, query, id, accountID).
		Scan(&e.ID, &e.ProjectID, &e.Kind, &e.Name, &e.Subdomain, &e.CreatedAt, &e.UpdatedAt)
	if err != nil {
		return domain.Environment{}, fmt.Errorf("get environment: %w", translate(err))
	}
	return e, nil
}

func (r *Repository) ListEnvironments(ctx context.Context, accountID, projectID string) ([]domain.Environment, error) {
	const query = `
		SELECT e.id, e.project_id, e.kind, e.name, e.subdomain, e.created_at, e.updated_at
		FROM environments e
		JOIN projects p ON p.id = e.project_id
		WHERE e.project_id = $1 AND p.account_id = $2
		ORDER BY e.kind, e.name`

	rows, err := r.pool.Query(ctx, query, projectID, accountID)
	if err != nil {
		return nil, fmt.Errorf("list environments: %w", translate(err))
	}
	defer rows.Close()

	environments := []domain.Environment{}
	for rows.Next() {
		var e domain.Environment
		if err := rows.Scan(&e.ID, &e.ProjectID, &e.Kind, &e.Name, &e.Subdomain,
			&e.CreatedAt, &e.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan environment: %w", err)
		}
		environments = append(environments, e)
	}
	return environments, rows.Err()
}

// --- Deployments ---

// CreateDeployment verifies in a single statement that the service and the
// environment both belong to the caller AND to the same project. Without that
// join a caller could deploy one project's service into another project's
// environment, which is exactly the cross-environment leak the platform
// promises not to allow.
func (r *Repository) CreateDeployment(ctx context.Context, accountID string, in domain.CreateDeploymentInput) (domain.Deployment, error) {
	const query = `
		INSERT INTO deployments (service_id, environment_id, commit_sha)
		SELECT s.id, e.id, $4
		FROM services s
		JOIN environments e ON e.project_id = s.project_id
		JOIN projects p ON p.id = s.project_id
		WHERE s.id = $1 AND e.id = $2 AND p.account_id = $3
		RETURNING id, service_id, environment_id, commit_sha, status,
		          image_ref, url, error_message, queued_at, started_at, completed_at`

	var d domain.Deployment
	err := r.pool.QueryRow(ctx, query, in.ServiceID, in.EnvironmentID, accountID, in.CommitSHA).
		Scan(&d.ID, &d.ServiceID, &d.EnvironmentID, &d.CommitSHA, &d.Status,
			&d.ImageRef, &d.URL, &d.ErrorMessage, &d.QueuedAt, &d.StartedAt, &d.CompletedAt)
	if err != nil {
		return domain.Deployment{}, fmt.Errorf("create deployment: %w", translate(err))
	}
	return d, nil
}

func (r *Repository) GetDeployment(ctx context.Context, accountID, id string) (domain.Deployment, error) {
	const query = `
		SELECT d.id, d.service_id, d.environment_id, d.commit_sha, d.status,
		       d.image_ref, d.url, d.error_message, d.queued_at, d.started_at, d.completed_at
		FROM deployments d
		JOIN services s ON s.id = d.service_id
		JOIN projects p ON p.id = s.project_id
		WHERE d.id = $1 AND p.account_id = $2`

	var d domain.Deployment
	err := r.pool.QueryRow(ctx, query, id, accountID).
		Scan(&d.ID, &d.ServiceID, &d.EnvironmentID, &d.CommitSHA, &d.Status,
			&d.ImageRef, &d.URL, &d.ErrorMessage, &d.QueuedAt, &d.StartedAt, &d.CompletedAt)
	if err != nil {
		return domain.Deployment{}, fmt.Errorf("get deployment: %w", translate(err))
	}
	return d, nil
}

func (r *Repository) ListDeployments(ctx context.Context, accountID, projectID string) ([]domain.Deployment, error) {
	const query = `
		SELECT d.id, d.service_id, d.environment_id, d.commit_sha, d.status,
		       d.image_ref, d.url, d.error_message, d.queued_at, d.started_at, d.completed_at
		FROM deployments d
		JOIN services s ON s.id = d.service_id
		JOIN projects p ON p.id = s.project_id
		WHERE p.id = $1 AND p.account_id = $2
		ORDER BY d.queued_at DESC
		LIMIT 100`

	rows, err := r.pool.Query(ctx, query, projectID, accountID)
	if err != nil {
		return nil, fmt.Errorf("list deployments: %w", translate(err))
	}
	defer rows.Close()

	deployments := []domain.Deployment{}
	for rows.Next() {
		var d domain.Deployment
		if err := rows.Scan(&d.ID, &d.ServiceID, &d.EnvironmentID, &d.CommitSHA, &d.Status,
			&d.ImageRef, &d.URL, &d.ErrorMessage, &d.QueuedAt, &d.StartedAt, &d.CompletedAt); err != nil {
			return nil, fmt.Errorf("scan deployment: %w", err)
		}
		deployments = append(deployments, d)
	}
	return deployments, rows.Err()
}
