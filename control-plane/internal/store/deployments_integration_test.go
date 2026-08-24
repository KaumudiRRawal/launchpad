package store_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/KaumudiRRawal/launchpad/control-plane/internal/domain"
	"github.com/KaumudiRRawal/launchpad/control-plane/internal/store"
)

// deploymentFixture is an account owning one service and one environment,
// which is the least a deployment needs to exist.
type deploymentFixture struct {
	repo    *store.Repository
	account domain.Account
	service domain.Service
	env     domain.Environment
}

func newDeploymentFixture(t *testing.T, prefix string) deploymentFixture {
	t.Helper()

	repo, pool := newTestRepo(t)
	ctx := context.Background()

	account := newAccount(t, repo, prefix)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM accounts WHERE id = $1`, account.ID)
	})

	project, err := repo.CreateProject(ctx, account.ID, domain.CreateProjectInput{
		Slug: prefix, Name: prefix,
		RepoURL: "https://github.com/example/" + prefix, DefaultBranch: "main",
	})
	if err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}

	service, err := repo.CreateService(ctx, account.ID, project.ID,
		domain.CreateServiceInput{Name: "api", SourcePath: "services/api", Port: 8080})
	if err != nil {
		t.Fatalf("CreateService() error = %v", err)
	}

	env, err := repo.CreateEnvironment(ctx, account.ID, project.ID,
		domain.CreateEnvironmentInput{Kind: domain.EnvironmentProduction, Name: "production"},
		"production-"+prefix)
	if err != nil {
		t.Fatalf("CreateEnvironment() error = %v", err)
	}

	return deploymentFixture{repo: repo, account: account, service: service, env: env}
}

// queue enqueues one deployment. The commit is derived from n so a failure
// message names which of several deployments went wrong.
func (f deploymentFixture) queue(t *testing.T, n int) domain.Deployment {
	t.Helper()

	d, err := f.repo.CreateDeployment(context.Background(), f.account.ID, domain.CreateDeploymentInput{
		ServiceID: f.service.ID, EnvironmentID: f.env.ID,
		CommitSHA: fmt.Sprintf("%040x", n),
	})
	if err != nil {
		t.Fatalf("CreateDeployment(%d) error = %v", n, err)
	}
	return d
}

// drainQueue claims until the queue is empty.
//
// Claiming is deliberately global — one worker serves every account — so a row
// left behind by an interrupted run would be claimed ahead of the rows the test
// just queued. Draining first is what lets a test say "the next claim is mine".
func drainQueue(t *testing.T, repo *store.Repository) []string {
	t.Helper()

	// The bound is a stuck-loop guard, not an expected depth: every iteration
	// moves a row out of 'queued', so the loop cannot legitimately run away.
	const maxDrain = 1000
	claimed := []string{}
	for range maxDrain {
		job, err := repo.ClaimNextDeployment(context.Background())
		if errors.Is(err, domain.ErrNotFound) {
			return claimed
		}
		if err != nil {
			t.Fatalf("ClaimNextDeployment() error = %v", err)
		}
		claimed = append(claimed, job.DeploymentID)
	}
	t.Fatalf("queue still not empty after %d claims", maxDrain)
	return nil
}

// claimAll claims until every deployment in want has been served, and returns
// the jobs in the order the queue handed them out. Claims belonging to another
// account are drained past rather than asserted on.
func claimAll(t *testing.T, repo *store.Repository, accountID string, want []domain.Deployment) []domain.DeploymentJob {
	t.Helper()

	pending := make(map[string]bool, len(want))
	for _, d := range want {
		pending[d.ID] = true
	}

	jobs := make([]domain.DeploymentJob, 0, len(want))
	for len(pending) > 0 {
		job, err := repo.ClaimNextDeployment(context.Background())
		if errors.Is(err, domain.ErrNotFound) {
			explainMissingClaims(t, repo, accountID, pending)
			// Reaching here means the explainer recorded a defect rather than
			// skipping, so stop: the caller indexes what it asked for and must
			// not run on with a short slice.
			t.FailNow()
		}
		if err != nil {
			t.Fatalf("ClaimNextDeployment() error = %v", err)
		}
		if !pending[job.DeploymentID] {
			continue
		}
		delete(pending, job.DeploymentID)
		jobs = append(jobs, job)
	}
	return jobs
}

// explainMissingClaims says why deployments this test queued never came back
// from the queue.
//
// The distinction is what makes the answer useful. A row still sitting in
// 'queued' after the claim reported an empty queue is a bug in the claim query.
// A row that has moved on without this test claiming it means another worker is
// polling the same database — a control plane left running against
// LAUNCHPAD_TEST_DATABASE_URL does exactly that, and its engine will even build
// the test's fixtures — which is a contaminated environment rather than a
// defect, and is reported as a skip so it is not mistaken for one.
func explainMissingClaims(t *testing.T, repo *store.Repository, accountID string, missing map[string]bool) {
	t.Helper()

	claimedElsewhere := 0
	for id := range missing {
		d, err := repo.GetDeployment(context.Background(), accountID, id)
		switch {
		case err != nil:
			t.Errorf("deployment %s was neither served by the queue nor readable: %v", id, err)
		case d.Status == domain.DeploymentQueued:
			t.Errorf("ClaimNextDeployment() reported an empty queue while deployment %s is still queued", id)
		default:
			claimedElsewhere++
		}
	}
	if claimedElsewhere > 0 {
		t.Skipf("%d of this test's deployments left the queue without it claiming them, so something "+
			"else is polling LAUNCHPAD_TEST_DATABASE_URL; stop any control plane pointed at the test "+
			"database and re-run", claimedElsewhere)
	}
}

// TestClaimNextDeploymentServesTheQueueInOrder covers the claim's two jobs:
// handing out the oldest queued deployment first, and flattening onto the job
// the parent rows a worker would otherwise have to fetch itself.
func TestClaimNextDeploymentServesTheQueueInOrder(t *testing.T) {
	f := newDeploymentFixture(t, "claimorder")
	ctx := context.Background()
	drainQueue(t, f.repo)

	queued := []domain.Deployment{f.queue(t, 1), f.queue(t, 2), f.queue(t, 3)}
	jobs := claimAll(t, f.repo, f.account.ID, queued)

	for i, want := range queued {
		job := jobs[i]
		if job.DeploymentID != want.ID {
			t.Fatalf("claim #%d returned %s, want %s — the queue is not FIFO",
				i+1, job.DeploymentID, want.ID)
		}

		// A worker builds from these without querying again, so a column
		// missing from the join is a build against the wrong source.
		for _, field := range []struct {
			name      string
			got, want any
		}{
			{"ServiceID", job.ServiceID, f.service.ID},
			{"EnvironmentID", job.EnvironmentID, f.env.ID},
			{"CommitSHA", job.CommitSHA, want.CommitSHA},
			{"RepoURL", job.RepoURL, "https://github.com/example/claimorder"},
			{"SourcePath", job.SourcePath, f.service.SourcePath},
			{"Port", job.Port, f.service.Port},
			{"ServiceName", job.ServiceName, f.service.Name},
			{"Subdomain", job.Subdomain, f.env.Subdomain},
		} {
			if field.got != field.want {
				t.Errorf("claim #%d %s = %v, want %v", i+1, field.name, field.got, field.want)
			}
		}

		// Claiming is also the queued -> building transition; a claim that
		// left the row queued would be handed out again on the next poll.
		got, err := f.repo.GetDeployment(ctx, f.account.ID, want.ID)
		if err != nil {
			t.Fatalf("GetDeployment() error = %v", err)
		}
		if got.Status != domain.DeploymentBuilding {
			t.Errorf("status after claim = %q, want %q", got.Status, domain.DeploymentBuilding)
		}
		if got.StartedAt == nil {
			t.Error("StartedAt is nil after a claim, so the build has no start time")
		}
	}

	if _, err := f.repo.ClaimNextDeployment(ctx); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("ClaimNextDeployment() on an empty queue = %v, want ErrNotFound", err)
	}
}

// TestClaimNextDeploymentNeverServesOneDeploymentTwice is the FOR UPDATE SKIP
// LOCKED promise: several workers polling at once divide the queue instead of
// duplicating it.
//
// It asserts uniqueness and completeness rather than counting successful
// claims, because a concurrent claim is allowed to come back empty — SKIP
// LOCKED steps over rows another transaction holds but has not committed, so a
// worker can see an empty queue while work remains. Building a deployment
// twice would be a bug; looking one poll too early is not.
func TestClaimNextDeploymentNeverServesOneDeploymentTwice(t *testing.T) {
	f := newDeploymentFixture(t, "claimrace")
	drainQueue(t, f.repo)

	const deployments = 8
	queued := make(map[string]bool, deployments)
	for n := range deployments {
		queued[f.queue(t, n).ID] = true
	}

	// More workers than deployments, so the losers exercise the empty-queue
	// path against a queue that is being emptied underneath them.
	const workers = 12
	var (
		mu     sync.Mutex
		claims []string
		errs   []error
		wg     sync.WaitGroup
	)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			job, err := f.repo.ClaimNextDeployment(context.Background())

			mu.Lock()
			defer mu.Unlock()
			switch {
			case errors.Is(err, domain.ErrNotFound):
			case err != nil:
				errs = append(errs, err)
			default:
				claims = append(claims, job.DeploymentID)
			}
		}()
	}
	wg.Wait()

	for _, err := range errs {
		t.Errorf("concurrent ClaimNextDeployment() error = %v", err)
	}

	// The uniqueness check covers every claim, not only this test's rows: no
	// deployment may be handed to two workers whoever owns it.
	handedOut := make(map[string]int, len(claims))
	for _, id := range claims {
		handedOut[id]++
	}
	for id, count := range handedOut {
		if count > 1 {
			t.Errorf("deployment %s was claimed %d times, so it would be built %d times", id, count, count)
		}
	}

	seen := make(map[string]int, deployments)
	for id, count := range handedOut {
		if queued[id] {
			seen[id] = count
		}
	}

	// Whatever the workers left behind must still be claimable, and between
	// the two phases every deployment must have been handed out exactly once.
	for _, id := range drainQueue(t, f.repo) {
		if queued[id] {
			seen[id]++
		}
	}
	missing := map[string]bool{}
	for id := range queued {
		switch seen[id] {
		case 1:
		case 0:
			missing[id] = true
		default:
			t.Errorf("deployment %s was claimed %d times in total, want exactly 1", id, seen[id])
		}
	}
	if len(missing) > 0 {
		explainMissingClaims(t, f.repo, f.account.ID, missing)
	}
}

// TestTransitionDeploymentRejectsMovesItCannotMake separates the two refusals a
// caller has to tell apart: a move the lifecycle forbids outright, and a legal
// move that lost a race to another worker.
func TestTransitionDeploymentRejectsMovesItCannotMake(t *testing.T) {
	f := newDeploymentFixture(t, "transitions")
	ctx := context.Background()

	t.Run("illegal moves are refused before touching the row", func(t *testing.T) {
		d := f.queue(t, 100)
		for _, tc := range []struct {
			name     string
			from, to domain.DeploymentStatus
		}{
			{"skipping building", domain.DeploymentQueued, domain.DeploymentLive},
			{"backwards", domain.DeploymentDeploying, domain.DeploymentBuilding},
			{"out of a terminal state", domain.DeploymentFailed, domain.DeploymentBuilding},
			{"to itself", domain.DeploymentBuilding, domain.DeploymentBuilding},
		} {
			t.Run(tc.name, func(t *testing.T) {
				err := f.repo.TransitionDeployment(ctx, d.ID, tc.from, tc.to)
				if !errors.Is(err, domain.ErrInvalidTransition) {
					t.Errorf("TransitionDeployment(%s->%s) = %v, want ErrInvalidTransition",
						tc.from, tc.to, err)
				}
			})
		}

		// An illegal move must not have written anything on its way to being
		// rejected.
		got, err := f.repo.GetDeployment(ctx, f.account.ID, d.ID)
		if err != nil {
			t.Fatalf("GetDeployment() error = %v", err)
		}
		if got.Status != domain.DeploymentQueued {
			t.Errorf("status = %q after rejected transitions, want %q", got.Status, domain.DeploymentQueued)
		}
	})

	t.Run("a legal move applied twice conflicts", func(t *testing.T) {
		d := f.queue(t, 101)
		if err := f.repo.TransitionDeployment(ctx, d.ID,
			domain.DeploymentQueued, domain.DeploymentBuilding); err != nil {
			t.Fatalf("TransitionDeployment(queued->building) error = %v", err)
		}

		// The row is no longer queued, so the second caller is a worker that
		// lost the race and must be told, not silently ignored.
		err := f.repo.TransitionDeployment(ctx, d.ID,
			domain.DeploymentQueued, domain.DeploymentBuilding)
		if !errors.Is(err, domain.ErrConflict) {
			t.Errorf("second TransitionDeployment() = %v, want ErrConflict", err)
		}
	})

	t.Run("a terminal move records when it completed", func(t *testing.T) {
		d := f.queue(t, 102)
		if err := f.repo.TransitionDeployment(ctx, d.ID,
			domain.DeploymentQueued, domain.DeploymentSuperseded); err != nil {
			t.Fatalf("TransitionDeployment(queued->superseded) error = %v", err)
		}

		got, err := f.repo.GetDeployment(ctx, f.account.ID, d.ID)
		if err != nil {
			t.Fatalf("GetDeployment() error = %v", err)
		}
		if got.CompletedAt == nil {
			t.Error("CompletedAt is nil after a terminal transition")
		}
	})

	t.Run("a deployment that does not exist conflicts", func(t *testing.T) {
		err := f.repo.TransitionDeployment(ctx, "00000000-0000-0000-0000-000000000000",
			domain.DeploymentQueued, domain.DeploymentBuilding)
		if !errors.Is(err, domain.ErrConflict) {
			t.Errorf("TransitionDeployment() on a missing row = %v, want ErrConflict", err)
		}
	})
}

// TestMarkDeploymentFailedAcceptsEveryStageAFailureCanReach covers the reason a
// failure is written by its own statement rather than through
// TransitionDeployment: it has to work from whichever stage the build died in,
// and only from those.
func TestMarkDeploymentFailedAcceptsEveryStageAFailureCanReach(t *testing.T) {
	f := newDeploymentFixture(t, "failures")
	ctx := context.Background()

	// advance walks a fresh deployment to the status a case starts from, using
	// only moves the lifecycle allows.
	advance := func(t *testing.T, n int, to domain.DeploymentStatus) domain.Deployment {
		t.Helper()

		d := f.queue(t, n)
		path := map[domain.DeploymentStatus][]domain.DeploymentStatus{
			domain.DeploymentQueued:    {},
			domain.DeploymentBuilding:  {domain.DeploymentBuilding},
			domain.DeploymentDeploying: {domain.DeploymentBuilding, domain.DeploymentDeploying},
			domain.DeploymentFailed:    {domain.DeploymentFailed},
			domain.DeploymentSuperseded: {domain.DeploymentBuilding, domain.DeploymentDeploying,
				domain.DeploymentSuperseded},
		}[to]

		from := domain.DeploymentQueued
		for _, next := range path {
			if err := f.repo.TransitionDeployment(ctx, d.ID, from, next); err != nil {
				t.Fatalf("TransitionDeployment(%s->%s) error = %v", from, next, err)
			}
			from = next
		}
		return d
	}

	for n, tc := range []struct {
		from    domain.DeploymentStatus
		wantErr error
	}{
		{domain.DeploymentQueued, nil},
		{domain.DeploymentBuilding, nil},
		{domain.DeploymentDeploying, nil},
		// A deployment that already reached a terminal state must keep the
		// reason it got there; a late failure from a cancelled build should not
		// overwrite it.
		{domain.DeploymentFailed, domain.ErrConflict},
		{domain.DeploymentSuperseded, domain.ErrConflict},
	} {
		t.Run(string(tc.from), func(t *testing.T) {
			d := advance(t, 200+n, tc.from)

			const reason = "build exited with status 1"
			err := f.repo.MarkDeploymentFailed(ctx, d.ID, reason)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("MarkDeploymentFailed() from %s = %v, want %v", tc.from, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("MarkDeploymentFailed() from %s error = %v", tc.from, err)
			}

			got, err := f.repo.GetDeployment(ctx, f.account.ID, d.ID)
			if err != nil {
				t.Fatalf("GetDeployment() error = %v", err)
			}
			if got.Status != domain.DeploymentFailed {
				t.Errorf("status = %q, want %q", got.Status, domain.DeploymentFailed)
			}
			if got.ErrorMessage == nil || *got.ErrorMessage != reason {
				t.Errorf("ErrorMessage = %v, want %q", got.ErrorMessage, reason)
			}
			if got.CompletedAt == nil {
				t.Error("CompletedAt is nil on a failed deployment")
			}
		})
	}
}

// TestDeploymentLogsFollowACursor covers what a log follower depends on: lines
// come back in sequence order, asking for everything after a cursor returns
// only newer lines, and a reader from another account sees none of it.
func TestDeploymentLogsFollowACursor(t *testing.T) {
	f := newDeploymentFixture(t, "logs")
	ctx := context.Background()

	d := f.queue(t, 300)
	lines := []struct {
		seq     int
		stream  string
		message string
	}{
		{1, "stdout", "Step 1/6 : FROM golang:1.26-alpine"},
		{2, "stdout", "Step 2/6 : WORKDIR /src"},
		{3, "stderr", "warning: no .dockerignore found"},
		{4, "stdout", "Successfully tagged launchpad/api:latest"},
	}
	for _, l := range lines {
		if err := f.repo.AppendDeploymentLog(ctx, d.ID, l.seq, l.stream, l.message); err != nil {
			t.Fatalf("AppendDeploymentLog(%d) error = %v", l.seq, err)
		}
	}

	t.Run("a cursor of zero returns the whole build in order", func(t *testing.T) {
		logs, err := f.repo.ListDeploymentLogs(ctx, f.account.ID, d.ID, 0)
		if err != nil {
			t.Fatalf("ListDeploymentLogs() error = %v", err)
		}
		if len(logs) != len(lines) {
			t.Fatalf("got %d lines, want %d", len(logs), len(lines))
		}
		for i, want := range lines {
			if logs[i].Seq != want.seq || logs[i].Stream != want.stream || logs[i].Message != want.message {
				t.Errorf("line %d = %+v, want seq %d stream %q message %q",
					i, logs[i], want.seq, want.stream, want.message)
			}
		}
	})

	t.Run("a cursor returns only what follows it", func(t *testing.T) {
		logs, err := f.repo.ListDeploymentLogs(ctx, f.account.ID, d.ID, 2)
		if err != nil {
			t.Fatalf("ListDeploymentLogs() error = %v", err)
		}
		if len(logs) != 2 {
			t.Fatalf("got %d lines after seq 2, want 2", len(logs))
		}
		if logs[0].Seq != 3 || logs[1].Seq != 4 {
			t.Errorf("got seqs %d and %d after seq 2, want 3 and 4", logs[0].Seq, logs[1].Seq)
		}
	})

	t.Run("a cursor past the end returns nothing rather than failing", func(t *testing.T) {
		// A follower polls after the last line it saw, which is the common
		// case while it waits for the next one.
		logs, err := f.repo.ListDeploymentLogs(ctx, f.account.ID, d.ID, 4)
		if err != nil {
			t.Fatalf("ListDeploymentLogs() error = %v", err)
		}
		if len(logs) != 0 {
			t.Errorf("got %d lines past the end, want 0", len(logs))
		}
	})

	t.Run("re-appending a sequence keeps the first line", func(t *testing.T) {
		// The engine flushes buffered lines and may retry a flush; a retry
		// must not duplicate a line or rewrite one already stored.
		if err := f.repo.AppendDeploymentLog(ctx, d.ID, 2, "stdout", "a different message"); err != nil {
			t.Fatalf("AppendDeploymentLog() on a repeated seq error = %v", err)
		}

		logs, err := f.repo.ListDeploymentLogs(ctx, f.account.ID, d.ID, 1)
		if err != nil {
			t.Fatalf("ListDeploymentLogs() error = %v", err)
		}
		if len(logs) != 3 {
			t.Fatalf("got %d lines after re-appending seq 2, want 3", len(logs))
		}
		if logs[0].Message != "Step 2/6 : WORKDIR /src" {
			t.Errorf("seq 2 message = %q, want the originally stored line", logs[0].Message)
		}
	})

	t.Run("another account reads none of the build log", func(t *testing.T) {
		// Build logs quote source paths and environment names, so the
		// ownership join matters as much here as on the deployment itself.
		intruder := newDeploymentFixture(t, "logsintruder")

		logs, err := f.repo.ListDeploymentLogs(ctx, intruder.account.ID, d.ID, 0)
		if err != nil {
			t.Fatalf("ListDeploymentLogs() error = %v", err)
		}
		if len(logs) != 0 {
			t.Errorf("another account read %d log lines, want 0", len(logs))
		}
	})
}
