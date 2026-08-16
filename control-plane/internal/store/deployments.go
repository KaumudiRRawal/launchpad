package store

import (
	"context"
	"fmt"

	"github.com/KaumudiRRawal/launchpad/control-plane/internal/domain"
)

// ClaimNextDeployment atomically takes the oldest queued deployment and moves
// it to building, returning domain.ErrNotFound when the queue is empty.
//
// The UPDATE ... WHERE id = (SELECT ... FOR UPDATE SKIP LOCKED) form is what
// makes several workers safe: each claims a different row instead of blocking
// on the same one, and a deployment can never be built twice.
func (r *Repository) ClaimNextDeployment(ctx context.Context) (domain.DeploymentJob, error) {
	const query = `
		UPDATE deployments
		SET status = 'building', started_at = now()
		WHERE id = (
			SELECT d.id FROM deployments d
			WHERE d.status = 'queued'
			ORDER BY d.queued_at
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		RETURNING id, service_id, environment_id, commit_sha`

	var job domain.DeploymentJob
	err := r.pool.QueryRow(ctx, query).
		Scan(&job.DeploymentID, &job.ServiceID, &job.EnvironmentID, &job.CommitSHA)
	if err != nil {
		return domain.DeploymentJob{}, translate(err)
	}

	// The build needs the repository and the port, which live on the parent
	// rows rather than on the deployment.
	const details = `
		SELECT p.repo_url, s.source_path, s.port, s.name, e.subdomain
		FROM services s
		JOIN projects p ON p.id = s.project_id
		JOIN environments e ON e.id = $2
		WHERE s.id = $1`

	err = r.pool.QueryRow(ctx, details, job.ServiceID, job.EnvironmentID).
		Scan(&job.RepoURL, &job.SourcePath, &job.Port, &job.ServiceName, &job.Subdomain)
	if err != nil {
		return domain.DeploymentJob{}, fmt.Errorf("load deployment details: %w", translate(err))
	}
	return job, nil
}

// TransitionDeployment moves a deployment from one status to another, refusing
// any move the domain does not allow.
//
// The expected current status is part of the WHERE clause, so the check and
// the write are one atomic operation. Verifying first and updating second
// would leave a window in which another worker could change the row in
// between.
func (r *Repository) TransitionDeployment(ctx context.Context, id string, from, to domain.DeploymentStatus) error {
	if !from.CanTransitionTo(to) {
		return fmt.Errorf("transition deployment: %w", domain.ErrInvalidTransition)
	}

	const query = `
		UPDATE deployments
		SET status = $3,
		    completed_at = CASE WHEN $4 THEN now() ELSE completed_at END
		WHERE id = $1 AND status = $2`

	tag, err := r.pool.Exec(ctx, query, id, string(from), string(to), to.Terminal())
	if err != nil {
		return fmt.Errorf("transition deployment: %w", translate(err))
	}
	if tag.RowsAffected() == 0 {
		// Either the deployment is gone or it is no longer in `from`, which
		// means another worker moved it first.
		return fmt.Errorf("transition deployment from %s to %s: %w", from, to, domain.ErrConflict)
	}
	return nil
}

// MarkDeploymentLive records a successful release along with the artefacts it
// produced, in the same statement that moves the status.
func (r *Repository) MarkDeploymentLive(ctx context.Context, id, imageRef, url string) error {
	const query = `
		UPDATE deployments
		SET status = 'live', image_ref = $2, url = $3, completed_at = now()
		WHERE id = $1 AND status = 'deploying'`

	tag, err := r.pool.Exec(ctx, query, id, imageRef, url)
	if err != nil {
		return fmt.Errorf("mark deployment live: %w", translate(err))
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("mark deployment live: %w", domain.ErrConflict)
	}
	return nil
}

// MarkDeploymentFailed records why a deployment failed. It accepts any
// non-terminal starting status, because a failure can arrive at any stage.
func (r *Repository) MarkDeploymentFailed(ctx context.Context, id, reason string) error {
	const query = `
		UPDATE deployments
		SET status = 'failed', error_message = $2, completed_at = now()
		WHERE id = $1 AND status IN ('queued', 'building', 'deploying')`

	tag, err := r.pool.Exec(ctx, query, id, reason)
	if err != nil {
		return fmt.Errorf("mark deployment failed: %w", translate(err))
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("mark deployment failed: %w", domain.ErrConflict)
	}
	return nil
}

// SupersedePriorDeployments retires the deployments a new live release
// replaces, so exactly one deployment is live per service and environment.
func (r *Repository) SupersedePriorDeployments(ctx context.Context, serviceID, environmentID, keepID string) error {
	const query = `
		UPDATE deployments
		SET status = 'superseded', completed_at = now()
		WHERE service_id = $1 AND environment_id = $2 AND id <> $3
		  AND status = 'live'`

	if _, err := r.pool.Exec(ctx, query, serviceID, environmentID, keepID); err != nil {
		return fmt.Errorf("supersede prior deployments: %w", translate(err))
	}
	return nil
}

// AppendDeploymentLog stores one line of build output. seq orders the lines,
// because timestamps from a fast build collide at the resolution Postgres
// stores them.
func (r *Repository) AppendDeploymentLog(ctx context.Context, deploymentID string, seq int, stream, message string) error {
	const query = `
		INSERT INTO deployment_logs (deployment_id, seq, stream, message)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (deployment_id, seq) DO NOTHING`

	if _, err := r.pool.Exec(ctx, query, deploymentID, seq, stream, message); err != nil {
		return fmt.Errorf("append deployment log: %w", translate(err))
	}
	return nil
}

// ListDeploymentLogs returns log lines in order, starting after afterSeq so a
// streaming client can poll for what it has not seen yet.
func (r *Repository) ListDeploymentLogs(ctx context.Context, accountID, deploymentID string, afterSeq int) ([]domain.DeploymentLog, error) {
	const query = `
		SELECT l.seq, l.stream, l.message, l.logged_at
		FROM deployment_logs l
		JOIN deployments d ON d.id = l.deployment_id
		JOIN services s ON s.id = d.service_id
		JOIN projects p ON p.id = s.project_id
		WHERE l.deployment_id = $1 AND p.account_id = $2 AND l.seq > $3
		ORDER BY l.seq
		LIMIT 1000`

	rows, err := r.pool.Query(ctx, query, deploymentID, accountID, afterSeq)
	if err != nil {
		return nil, fmt.Errorf("list deployment logs: %w", translate(err))
	}
	defer rows.Close()

	logs := []domain.DeploymentLog{}
	for rows.Next() {
		var l domain.DeploymentLog
		if err := rows.Scan(&l.Seq, &l.Stream, &l.Message, &l.LoggedAt); err != nil {
			return nil, fmt.Errorf("scan deployment log: %w", err)
		}
		logs = append(logs, l)
	}
	return logs, rows.Err()
}
