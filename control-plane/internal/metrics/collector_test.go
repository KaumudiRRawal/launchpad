package metrics

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/KaumudiRRawal/launchpad/control-plane/internal/domain"
)

// fakeSink records what the collector wrote, and can be told to fail.
type fakeSink struct {
	mu       sync.Mutex
	written  []domain.MetricBucket
	prunedTo []time.Time
	err      error
	// recorded fires on every successful write, so a test waiting for the
	// background loop does not have to sleep and hope.
	recorded chan struct{}
}

func newFakeSink() *fakeSink {
	return &fakeSink{recorded: make(chan struct{}, 8)}
}

func (f *fakeSink) RecordMetrics(_ context.Context, buckets []domain.MetricBucket) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.err != nil {
		return f.err
	}
	f.written = append(f.written, buckets...)
	select {
	case f.recorded <- struct{}{}:
	default:
	}
	return nil
}

func (f *fakeSink) PruneMetrics(_ context.Context, before time.Time) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prunedTo = append(f.prunedTo, before)
	return 0, nil
}

func (f *fakeSink) rows() []domain.MetricBucket {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]domain.MetricBucket(nil), f.written...)
}

func (f *fakeSink) prunes() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.prunedTo)
}

// testClock is a clock a test moves by hand, so a minute passing does not mean
// waiting for one.
type testClock struct {
	mu sync.Mutex
	at time.Time
}

func (c *testClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *testClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(d)
}

func newTestCollector(sink Sink) (*Collector, *testClock) {
	clock := &testClock{at: time.Date(2026, 8, 21, 10, 30, 20, 0, time.UTC)}
	return &Collector{
		Sink: sink,
		Log:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:  clock.now,
	}, clock
}

func TestFlushHoldsBackTheMinuteInProgress(t *testing.T) {
	sink := newFakeSink()
	collector, clock := newTestCollector(sink)
	ctx := context.Background()

	collector.Observe("deploy-1", "env-1", 200, 12*time.Millisecond)
	collector.Observe("deploy-1", "env-1", 200, 18*time.Millisecond)

	// The minute is still open: writing it now would mean writing it again when
	// the rest of it arrives.
	if err := collector.Flush(ctx); err != nil {
		t.Fatalf("Flush() error = %v", err)
	}
	if rows := sink.rows(); len(rows) != 0 {
		t.Fatalf("flushed %d rows while the minute was still open", len(rows))
	}

	clock.advance(time.Minute)
	if err := collector.Flush(ctx); err != nil {
		t.Fatalf("Flush() error = %v", err)
	}

	rows := sink.rows()
	if len(rows) != 1 {
		t.Fatalf("flushed %d rows once the minute closed, want 1", len(rows))
	}
	if rows[0].Requests != 2 {
		t.Errorf("Requests = %d, want 2", rows[0].Requests)
	}
	if want := time.Date(2026, 8, 21, 10, 30, 0, 0, time.UTC); !rows[0].Bucket.Equal(want) {
		t.Errorf("Bucket = %v, want the start of the minute %v", rows[0].Bucket, want)
	}
}

func TestFlushAllIncludesTheMinuteInProgress(t *testing.T) {
	sink := newFakeSink()
	collector, _ := newTestCollector(sink)

	collector.Observe("deploy-1", "env-1", 200, time.Millisecond)

	// What shutdown does. The alternative is a hole in the measurements
	// immediately before every restart.
	if err := collector.FlushAll(context.Background()); err != nil {
		t.Fatalf("FlushAll() error = %v", err)
	}
	if rows := sink.rows(); len(rows) != 1 {
		t.Fatalf("FlushAll() wrote %d rows, want 1", len(rows))
	}
}

func TestObserveRollsUpPerMinutePerDeployment(t *testing.T) {
	sink := newFakeSink()
	collector, clock := newTestCollector(sink)

	collector.Observe("deploy-1", "env-1", 200, 10*time.Millisecond)
	collector.Observe("deploy-2", "env-1", 200, 10*time.Millisecond)
	clock.advance(time.Minute)
	collector.Observe("deploy-2", "env-1", 200, 10*time.Millisecond)

	if err := collector.FlushAll(context.Background()); err != nil {
		t.Fatalf("FlushAll() error = %v", err)
	}

	// One row per deployment per minute: three keys, not one.
	if rows := sink.rows(); len(rows) != 3 {
		t.Fatalf("wrote %d rows, want 3 (two deployments in the first minute, one in the second)", len(rows))
	}
}

func TestOnlyServerErrorsCountAsFailures(t *testing.T) {
	tests := []struct {
		name   string
		status int
		want   int64
	}{
		{name: "ok", status: 200, want: 0},
		{name: "redirect", status: 302, want: 0},
		// The application's own decision, and no evidence about its health.
		{name: "not found", status: 404, want: 0},
		{name: "client error", status: 429, want: 0},
		{name: "server error", status: 500, want: 1},
		// The proxy's own answer when the workload is not listening, which is
		// exactly the failure worth counting.
		{name: "bad gateway", status: 502, want: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sink := newFakeSink()
			collector, _ := newTestCollector(sink)

			collector.Observe("deploy-1", "env-1", tt.status, time.Millisecond)
			if err := collector.FlushAll(context.Background()); err != nil {
				t.Fatalf("FlushAll() error = %v", err)
			}

			rows := sink.rows()
			if len(rows) != 1 {
				t.Fatalf("wrote %d rows, want 1", len(rows))
			}
			if rows[0].Failures != tt.want {
				t.Errorf("status %d recorded %d failures, want %d", tt.status, rows[0].Failures, tt.want)
			}
		})
	}
}

func TestSubMillisecondRequestsAreNotRecordedAsZero(t *testing.T) {
	sink := newFakeSink()
	collector, _ := newTestCollector(sink)

	collector.Observe("deploy-1", "env-1", 200, 400*time.Microsecond)
	if err := collector.FlushAll(context.Background()); err != nil {
		t.Fatalf("FlushAll() error = %v", err)
	}

	rows := sink.rows()
	if len(rows) != 1 {
		t.Fatalf("wrote %d rows, want 1", len(rows))
	}
	if rows[0].LatencySumMS <= 0 {
		t.Errorf("LatencySumMS = %v; a fast handler must not measure as free", rows[0].LatencySumMS)
	}
}

// TestObserveIsSafeUnderConcurrency matters because Observe runs on the proxy's
// request path, where every request is its own goroutine.
func TestObserveIsSafeUnderConcurrency(t *testing.T) {
	sink := newFakeSink()
	collector, _ := newTestCollector(sink)

	const goroutines, each = 16, 100
	var wg sync.WaitGroup
	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range each {
				collector.Observe("deploy-1", "env-1", 200, time.Millisecond)
			}
		}()
	}
	wg.Wait()

	if err := collector.FlushAll(context.Background()); err != nil {
		t.Fatalf("FlushAll() error = %v", err)
	}

	var total int64
	for _, row := range sink.rows() {
		total += row.Requests
	}
	if want := int64(goroutines * each); total != want {
		t.Errorf("recorded %d requests, want %d", total, want)
	}
}

func TestFlushDropsWhatItCannotWrite(t *testing.T) {
	sink := newFakeSink()
	sink.err = errors.New("database is down")

	collector, clock := newTestCollector(sink)
	collector.Observe("deploy-1", "env-1", 200, time.Millisecond)
	clock.advance(time.Minute)

	if err := collector.Flush(context.Background()); err == nil {
		t.Fatal("Flush() succeeded with a failing sink")
	}

	// The failed batch is gone rather than retried: holding it would grow
	// without bound for as long as the database stayed unreachable.
	sink.mu.Lock()
	sink.err = nil
	sink.mu.Unlock()

	if err := collector.FlushAll(context.Background()); err != nil {
		t.Fatalf("FlushAll() error = %v", err)
	}
	if rows := sink.rows(); len(rows) != 0 {
		t.Errorf("a failed flush was retried: %d rows arrived later", len(rows))
	}
}

func TestPruneRunsAtMostHourly(t *testing.T) {
	sink := newFakeSink()
	collector, clock := newTestCollector(sink)
	collector.Retention = 48 * time.Hour
	ctx := context.Background()

	collector.prune(ctx)
	if sink.prunes() != 1 {
		t.Fatalf("pruned %d times on the first call, want 1", sink.prunes())
	}

	clock.advance(30 * time.Minute)
	collector.prune(ctx)
	if sink.prunes() != 1 {
		t.Errorf("pruned again after 30 minutes; the delete belongs off the flush path")
	}

	clock.advance(31 * time.Minute)
	collector.prune(ctx)
	if sink.prunes() != 2 {
		t.Fatalf("pruned %d times after an hour had passed, want 2", sink.prunes())
	}

	sink.mu.Lock()
	cutoff := sink.prunedTo[1]
	sink.mu.Unlock()
	if want := clock.now().Add(-48 * time.Hour); !cutoff.Equal(want) {
		t.Errorf("pruned everything before %v, want %v", cutoff, want)
	}
}

func TestRunFlushesOnShutdown(t *testing.T) {
	sink := newFakeSink()
	collector, _ := newTestCollector(sink)
	// Long enough that only the shutdown path can be what wrote anything.
	collector.FlushInterval = time.Hour

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		collector.Run(ctx)
	}()

	collector.Observe("deploy-1", "env-1", 200, 5*time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run() did not return after its context was cancelled")
	}

	if rows := sink.rows(); len(rows) != 1 {
		t.Errorf("shutdown wrote %d rows, want 1 — the last minute before a restart is the one worth having", len(rows))
	}
}
