package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/KaumudiRRawal/launchpad/control-plane/internal/domain"
)

// RecordMetrics writes one flush of per-minute rollups.
//
// The whole flush goes out as one batch: it is a handful of rows describing a
// handful of minutes, and a round trip each would make the collector's flush
// interval a function of how many environments are taking traffic.
//
// Each row is guarded by an EXISTS on the deployment rather than left to the
// foreign key. Traffic in flight when a project is deleted arrives here with
// nowhere to go, and a constraint violation would abort the batch — losing the
// measurements of every other environment along with it. Skipping the row
// silently is right: the deployment those requests reached no longer exists.
func (r *Repository) RecordMetrics(ctx context.Context, buckets []domain.MetricBucket) error {
	const query = `
		INSERT INTO deployment_metrics AS m (
			deployment_id, environment_id, bucket,
			requests, failures, latency_sum_ms, latency_max_ms, histogram)
		SELECT $1::uuid, $2::uuid, $3::timestamptz,
		       $4::bigint, $5::bigint, $6::double precision, $7::double precision, $8::bigint[]
		WHERE EXISTS (SELECT 1 FROM deployments d WHERE d.id = $1::uuid)
		ON CONFLICT (deployment_id, bucket) DO UPDATE SET
			requests       = m.requests + EXCLUDED.requests,
			failures       = m.failures + EXCLUDED.failures,
			latency_sum_ms = m.latency_sum_ms + EXCLUDED.latency_sum_ms,
			latency_max_ms = greatest(m.latency_max_ms, EXCLUDED.latency_max_ms),
			histogram      = histogram_add(m.histogram, EXCLUDED.histogram)`

	batch := &pgx.Batch{}
	for _, b := range buckets {
		batch.Queue(query, b.DeploymentID, b.EnvironmentID, b.Bucket,
			b.Requests, b.Failures, b.LatencySumMS, b.LatencyMaxMS, b.Histogram)
	}

	if err := r.pool.SendBatch(ctx, batch).Close(); err != nil {
		return fmt.Errorf("record metrics: %w", translate(err))
	}
	return nil
}

// EnvironmentMetrics returns an environment's rollups from since onwards,
// oldest first, one row per deployment per minute.
//
// The rows are not folded up here. The caller needs the same data cut two
// ways — by minute for a series, by deployment to tell which release served
// which window — and both are cheap in Go once the rows have arrived.
//
// The account scope is in the query even though a caller has to hold the
// environment already: authorization that lives in the WHERE clause cannot be
// forgotten by a handler, and this is the one query in the pair that returns
// somebody's traffic.
func (r *Repository) EnvironmentMetrics(ctx context.Context, accountID, environmentID string, since time.Time) ([]domain.MetricBucket, error) {
	const query = `
		SELECT m.deployment_id, m.environment_id, d.commit_sha, m.bucket,
		       m.requests, m.failures, m.latency_sum_ms, m.latency_max_ms, m.histogram
		FROM deployment_metrics m
		JOIN deployments d ON d.id = m.deployment_id
		JOIN services s ON s.id = d.service_id
		JOIN projects p ON p.id = s.project_id
		WHERE m.environment_id = $1 AND p.account_id = $2 AND m.bucket >= $3
		ORDER BY m.bucket
		LIMIT 5000`

	rows, err := r.pool.Query(ctx, query, environmentID, accountID, since)
	if err != nil {
		return nil, fmt.Errorf("environment metrics: %w", translate(err))
	}
	defer rows.Close()

	buckets := []domain.MetricBucket{}
	for rows.Next() {
		var b domain.MetricBucket
		if err := rows.Scan(&b.DeploymentID, &b.EnvironmentID, &b.CommitSHA, &b.Bucket,
			&b.Requests, &b.Failures, &b.LatencySumMS, &b.LatencyMaxMS, &b.Histogram); err != nil {
			return nil, fmt.Errorf("scan metric bucket: %w", err)
		}
		buckets = append(buckets, b)
	}
	return buckets, rows.Err()
}

// PruneMetrics deletes rollups for minutes starting before the cutoff and
// reports how many went. Measurements are read after a deploy or during an
// incident, both recent events; without this the table would end up the
// largest in the database with the least in it anyone wants.
func (r *Repository) PruneMetrics(ctx context.Context, before time.Time) (int64, error) {
	const query = `DELETE FROM deployment_metrics WHERE bucket < $1`

	tag, err := r.pool.Exec(ctx, query, before)
	if err != nil {
		return 0, fmt.Errorf("prune metrics: %w", translate(err))
	}
	return tag.RowsAffected(), nil
}
