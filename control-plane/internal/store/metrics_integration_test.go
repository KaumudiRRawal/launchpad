package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/KaumudiRRawal/launchpad/control-plane/internal/domain"
	"github.com/KaumudiRRawal/launchpad/control-plane/internal/metrics"
	"github.com/KaumudiRRawal/launchpad/control-plane/internal/store"
)

// measured is one account with somewhere for traffic to land: a project, a
// service, an environment and a live deployment inside it.
type measured struct {
	account     domain.Account
	environment domain.Environment
	deployment  domain.Deployment
}

func newMeasured(t *testing.T, repo *store.Repository, slug string) measured {
	t.Helper()
	ctx := context.Background()

	account := newAccount(t, repo, slug)

	project, err := repo.CreateProject(ctx, account.ID, domain.CreateProjectInput{
		Slug: slug, Name: slug,
		RepoURL: "https://github.com/example/" + slug, DefaultBranch: "main",
	})
	if err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}

	service, err := repo.CreateService(ctx, account.ID, project.ID,
		domain.CreateServiceInput{Name: "api", SourcePath: ".", Port: 8080})
	if err != nil {
		t.Fatalf("CreateService() error = %v", err)
	}

	environment, err := repo.CreateEnvironment(ctx, account.ID, project.ID,
		domain.CreateEnvironmentInput{Kind: domain.EnvironmentProduction, Name: "production"},
		"production-"+slug)
	if err != nil {
		t.Fatalf("CreateEnvironment() error = %v", err)
	}

	deployment, err := repo.CreateDeployment(ctx, account.ID, domain.CreateDeploymentInput{
		ServiceID: service.ID, EnvironmentID: environment.ID,
		CommitSHA: "0123456789abcdef0123456789abcdef01234567",
	})
	if err != nil {
		t.Fatalf("CreateDeployment() error = %v", err)
	}

	return measured{account: account, environment: environment, deployment: deployment}
}

// bucket builds one minute of measurements, with every request at the same
// latency so the assertions can be exact.
func bucket(m measured, at time.Time, requests, failures int, latencyMS float64) domain.MetricBucket {
	histogram := metrics.NewHistogram()
	for range requests {
		histogram.Observe(latencyMS)
	}

	return domain.MetricBucket{
		DeploymentID:  m.deployment.ID,
		EnvironmentID: m.environment.ID,
		Bucket:        at,
		Requests:      int64(requests),
		Failures:      int64(failures),
		LatencySumMS:  latencyMS * float64(requests),
		LatencyMaxMS:  latencyMS,
		Histogram:     histogram,
	}
}

// TestRecordMetricsAccumulatesARepeatedMinute is the behaviour two control-plane
// replicas depend on. Each holds its own copy of the minute in progress, and
// whichever writes second has to add to what it finds rather than replace it —
// including the latency histogram, which is what histogram_add exists for.
func TestRecordMetricsAccumulatesARepeatedMinute(t *testing.T) {
	repo, pool := newTestRepo(t)
	ctx := context.Background()

	m := newMeasured(t, repo, "accumulate")
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM accounts WHERE id = $1`, m.account.ID)
	})

	at := time.Now().UTC().Truncate(time.Minute)

	if err := repo.RecordMetrics(ctx, []domain.MetricBucket{bucket(m, at, 10, 1, 30)}); err != nil {
		t.Fatalf("RecordMetrics() error = %v", err)
	}
	if err := repo.RecordMetrics(ctx, []domain.MetricBucket{bucket(m, at, 5, 0, 80)}); err != nil {
		t.Fatalf("RecordMetrics() second flush error = %v", err)
	}

	buckets, err := repo.EnvironmentMetrics(ctx, m.account.ID, m.environment.ID, at.Add(-time.Minute))
	if err != nil {
		t.Fatalf("EnvironmentMetrics() error = %v", err)
	}
	if len(buckets) != 1 {
		t.Fatalf("read back %d rows, want 1 — the same minute became two rows", len(buckets))
	}

	got := buckets[0]
	if got.Requests != 15 || got.Failures != 1 {
		t.Errorf("requests/failures = %d/%d, want 15/1", got.Requests, got.Failures)
	}
	if want := 300.0 + 400.0; got.LatencySumMS != want {
		t.Errorf("LatencySumMS = %v, want %v", got.LatencySumMS, want)
	}
	// The worst request of either flush, not the most recent one.
	if got.LatencyMaxMS != 80 {
		t.Errorf("LatencyMaxMS = %v, want 80", got.LatencyMaxMS)
	}

	combined := metrics.NewHistogram()
	combined.Add(got.Histogram)
	if combined.Total() != 15 {
		t.Errorf("histogram holds %d requests, want 15 — the two flushes did not add up", combined.Total())
	}
	if !got.Bucket.Equal(at) {
		t.Errorf("Bucket = %v, want %v", got.Bucket, at)
	}
	// Filled in by the read, because a remediation has to name the commit it
	// is telling you to go back to.
	if got.CommitSHA != m.deployment.CommitSHA {
		t.Errorf("CommitSHA = %q, want %q", got.CommitSHA, m.deployment.CommitSHA)
	}
}

func TestEnvironmentMetricsIsScopedToTheOwner(t *testing.T) {
	repo, pool := newTestRepo(t)
	ctx := context.Background()

	m := newMeasured(t, repo, "owned")
	intruder := newAccount(t, repo, "metrics-intruder")
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM accounts WHERE id = ANY($1)`,
			[]string{m.account.ID, intruder.ID})
	})

	at := time.Now().UTC().Truncate(time.Minute)
	if err := repo.RecordMetrics(ctx, []domain.MetricBucket{bucket(m, at, 10, 0, 30)}); err != nil {
		t.Fatalf("RecordMetrics() error = %v", err)
	}

	// Holding the environment's ID is not holding the environment. Another
	// account asking for it sees an environment with no traffic, which is what
	// an environment it cannot see looks like.
	buckets, err := repo.EnvironmentMetrics(ctx, intruder.ID, m.environment.ID, at.Add(-time.Hour))
	if err != nil {
		t.Fatalf("EnvironmentMetrics() error = %v", err)
	}
	if len(buckets) != 0 {
		t.Errorf("another account read %d rows of someone else's traffic", len(buckets))
	}
}

// TestRecordMetricsSkipsADeletedDeployment covers requests still in flight when
// a project is deleted. The foreign key would abort the whole batch and lose
// every other environment's measurements with it.
func TestRecordMetricsSkipsADeletedDeployment(t *testing.T) {
	repo, pool := newTestRepo(t)
	ctx := context.Background()

	m := newMeasured(t, repo, "vanished")
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM accounts WHERE id = $1`, m.account.ID)
	})

	at := time.Now().UTC().Truncate(time.Minute)
	orphan := bucket(m, at, 3, 0, 10)
	orphan.DeploymentID = "00000000-0000-0000-0000-000000000000"

	if err := repo.RecordMetrics(ctx, []domain.MetricBucket{orphan, bucket(m, at, 7, 0, 10)}); err != nil {
		t.Fatalf("RecordMetrics() error = %v; a vanished deployment must not fail the flush", err)
	}

	buckets, err := repo.EnvironmentMetrics(ctx, m.account.ID, m.environment.ID, at.Add(-time.Minute))
	if err != nil {
		t.Fatalf("EnvironmentMetrics() error = %v", err)
	}
	if len(buckets) != 1 || buckets[0].Requests != 7 {
		t.Errorf("read back %+v, want only the surviving deployment's 7 requests", buckets)
	}
}

func TestEnvironmentMetricsStartsAtSince(t *testing.T) {
	repo, pool := newTestRepo(t)
	ctx := context.Background()

	m := newMeasured(t, repo, "windowed")
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM accounts WHERE id = $1`, m.account.ID)
	})

	now := time.Now().UTC().Truncate(time.Minute)
	if err := repo.RecordMetrics(ctx, []domain.MetricBucket{
		bucket(m, now.Add(-90*time.Minute), 5, 0, 10),
		bucket(m, now.Add(-30*time.Minute), 6, 0, 10),
		bucket(m, now, 7, 0, 10),
	}); err != nil {
		t.Fatalf("RecordMetrics() error = %v", err)
	}

	buckets, err := repo.EnvironmentMetrics(ctx, m.account.ID, m.environment.ID, now.Add(-time.Hour))
	if err != nil {
		t.Fatalf("EnvironmentMetrics() error = %v", err)
	}
	if len(buckets) != 2 {
		t.Fatalf("read %d rows for a one-hour window, want 2", len(buckets))
	}
	// Oldest first: the series is drawn left to right and the analysis splits
	// it at a boundary.
	if !buckets[0].Bucket.Before(buckets[1].Bucket) {
		t.Errorf("rows are not in time order: %v then %v", buckets[0].Bucket, buckets[1].Bucket)
	}
}

func TestPruneMetricsDropsWhatIsPastRetention(t *testing.T) {
	repo, pool := newTestRepo(t)
	ctx := context.Background()

	m := newMeasured(t, repo, "pruned")
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM accounts WHERE id = $1`, m.account.ID)
	})

	now := time.Now().UTC().Truncate(time.Minute)
	old := now.Add(-10 * 24 * time.Hour)
	if err := repo.RecordMetrics(ctx, []domain.MetricBucket{
		bucket(m, old, 5, 0, 10),
		bucket(m, now, 5, 0, 10),
	}); err != nil {
		t.Fatalf("RecordMetrics() error = %v", err)
	}

	deleted, err := repo.PruneMetrics(ctx, now.Add(-7*24*time.Hour))
	if err != nil {
		t.Fatalf("PruneMetrics() error = %v", err)
	}
	// Other tests in this package leave their own rows behind, so the count is
	// a floor rather than an equality.
	if deleted < 1 {
		t.Errorf("PruneMetrics() deleted %d rows, want at least the one past retention", deleted)
	}

	buckets, err := repo.EnvironmentMetrics(ctx, m.account.ID, m.environment.ID, old.Add(-time.Minute))
	if err != nil {
		t.Fatalf("EnvironmentMetrics() error = %v", err)
	}
	if len(buckets) != 1 || !buckets[0].Bucket.Equal(now) {
		t.Errorf("read back %+v, want only the recent minute", buckets)
	}
}

// TestEnvironmentMetricsKeepsTheNewestRowsWhenCapped pins which end of the
// range the row cap eats.
//
// EnvironmentMetrics is not paged: a caller asks for a window and gets what
// fits. So when a busy environment has more minutes in the window than the cap
// allows, the rows that have to go are the oldest ones. Dropping the newest
// instead would empty the current window while leaving the baseline intact,
// and the analysis would answer "insufficient data" about an environment
// taking more traffic than any other.
func TestEnvironmentMetricsKeepsTheNewestRowsWhenCapped(t *testing.T) {
	repo, pool := newTestRepo(t)
	ctx := context.Background()

	m := newMeasured(t, repo, "capped")
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM accounts WHERE id = $1`, m.account.ID)
	})

	// Comfortably past the cap the query applies, inserted directly because
	// this test is about how many rows come back rather than how they got in.
	const minutes = 5100

	newest := time.Now().UTC().Truncate(time.Minute)
	oldest := newest.Add(-(minutes - 1) * time.Minute)

	histogram := metrics.NewHistogram()
	histogram.Observe(10)

	_, err := pool.Exec(ctx, `
		INSERT INTO deployment_metrics (deployment_id, environment_id, bucket,
			requests, failures, latency_sum_ms, latency_max_ms, histogram)
		SELECT $1, $2, $3::timestamptz - make_interval(mins => n), 1, 0, 10, 10, $5
		FROM generate_series(0, $4::int) AS n`,
		m.deployment.ID, m.environment.ID, newest, minutes-1, []int64(histogram))
	if err != nil {
		t.Fatalf("seed metrics: %v", err)
	}

	buckets, err := repo.EnvironmentMetrics(ctx, m.account.ID, m.environment.ID, oldest)
	if err != nil {
		t.Fatalf("EnvironmentMetrics() error = %v", err)
	}
	if len(buckets) == 0 {
		t.Fatal("EnvironmentMetrics() returned nothing")
	}
	if len(buckets) >= minutes {
		t.Fatalf("read back %d of %d rows; the cap did not apply, so this test proves nothing",
			len(buckets), minutes)
	}

	// The minute just gone is the one someone watching a deploy is looking at.
	if got := buckets[len(buckets)-1].Bucket; !got.Equal(newest) {
		t.Errorf("newest row = %v, want %v — the cap dropped the recent end of the window", got, newest)
	}
	if got := buckets[0].Bucket; got.Equal(oldest) {
		t.Errorf("oldest row = %v, want the cap to have dropped it", got)
	}
	// Still the order every caller reads them in.
	if !buckets[0].Bucket.Before(buckets[len(buckets)-1].Bucket) {
		t.Errorf("rows are not oldest first: %v then %v",
			buckets[0].Bucket, buckets[len(buckets)-1].Bucket)
	}
}
