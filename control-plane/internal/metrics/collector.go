package metrics

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/KaumudiRRawal/launchpad/control-plane/internal/domain"
)

// Sink persists flushed rollups. It is the slice of the repository the
// collector needs, declared here at the point of use so the collector can be
// tested without a database.
type Sink interface {
	RecordMetrics(ctx context.Context, buckets []domain.MetricBucket) error
	PruneMetrics(ctx context.Context, before time.Time) (int64, error)
}

// Collector accumulates one observation per proxied request and writes them
// out as per-minute rollups.
//
// It aggregates in memory first because the proxy calls it on the request path:
// a row per request would put a database write in front of every response, and
// no question the analysis asks needs a single request back.
type Collector struct {
	Sink Sink
	Log  *slog.Logger

	// FlushInterval is how often closed minutes are written out.
	FlushInterval time.Duration
	// Retention is how long rollups are kept. Measurements are for reading
	// after a deploy or an incident, both of which are recent events, and an
	// unbounded table would eventually be the largest thing in the database
	// with the least in it worth reading.
	Retention time.Duration
	// Now is the clock. Injectable so a test does not have to wait for a
	// minute to pass to see a minute close.
	Now func() time.Time

	mu   sync.Mutex
	open map[bucketKey]*rollup
	// lastPrune is zero until the first prune, so a freshly started process
	// prunes on its first flush rather than an hour later.
	lastPrune time.Time
}

const (
	defaultFlushInterval = 15 * time.Second
	defaultRetention     = 7 * 24 * time.Hour
	// pruneInterval keeps the delete off the flush path. Retention is measured
	// in days, so checking hourly is already far more often than it matters.
	pruneInterval = time.Hour
)

// bucketKey identifies one minute of one deployment's traffic. The environment
// is not part of the key because a deployment belongs to exactly one.
type bucketKey struct {
	deploymentID string
	minute       time.Time
}

type rollup struct {
	environmentID string
	requests      int64
	failures      int64
	sumMS         float64
	maxMS         float64
	histogram     Histogram
}

// Observe records the outcome of one proxied request.
//
// It never blocks on anything but its own lock and never returns an error:
// measurement runs on the request path, so it has to be cheaper than the thing
// it measures, and a lost observation must not become a lost response.
func (c *Collector) Observe(deploymentID, environmentID string, status int, latency time.Duration) {
	// Microseconds rather than Milliseconds: a fast handler answers in well
	// under a millisecond, and integer milliseconds would record it as zero.
	ms := float64(latency.Microseconds()) / 1000

	key := bucketKey{deploymentID: deploymentID, minute: c.now().Truncate(time.Minute)}

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.open == nil {
		c.open = make(map[bucketKey]*rollup)
	}
	r, ok := c.open[key]
	if !ok {
		r = &rollup{environmentID: environmentID, histogram: NewHistogram()}
		c.open[key] = r
	}

	r.requests++
	// A 5xx is the platform failing to get a working answer out of the
	// workload, which includes the proxy's own 502 when the container has
	// stopped listening. A 404 the application chose to return is its own
	// business and says nothing about whether it is healthy.
	if status >= 500 {
		r.failures++
	}
	r.sumMS += ms
	r.maxMS = max(r.maxMS, ms)
	r.histogram.Observe(ms)
}

// Flush writes out every minute that has closed.
//
// The minute in progress is held back so each bucket is written once, whole.
// The upsert accumulates, so writing a minute twice is still correct — it is
// just work nobody needed.
func (c *Collector) Flush(ctx context.Context) error {
	return c.flush(ctx, c.now().Truncate(time.Minute))
}

// FlushAll writes everything, the minute in progress included. Shutdown uses
// it: otherwise every restart would leave a hole in the last minute before it,
// which is precisely the minute someone restarting the control plane goes on
// to look at.
func (c *Collector) FlushAll(ctx context.Context) error {
	return c.flush(ctx, time.Time{})
}

// flush writes out every bucket starting before notAfter. The zero time means
// everything.
func (c *Collector) flush(ctx context.Context, notAfter time.Time) error {
	c.mu.Lock()
	var due []domain.MetricBucket
	for key, r := range c.open {
		if !notAfter.IsZero() && !key.minute.Before(notAfter) {
			continue
		}
		delete(c.open, key)
		due = append(due, domain.MetricBucket{
			DeploymentID:  key.deploymentID,
			EnvironmentID: r.environmentID,
			Bucket:        key.minute,
			Requests:      r.requests,
			Failures:      r.failures,
			LatencySumMS:  r.sumMS,
			LatencyMaxMS:  r.maxMS,
			Histogram:     r.histogram,
		})
	}
	c.mu.Unlock()

	if len(due) == 0 {
		return nil
	}
	// Buckets are dropped from the map before the write and not put back if it
	// fails. Holding them would grow without bound while the database is
	// unreachable, and a process serving traffic is worth more than the record
	// of how fast it served it.
	return c.Sink.RecordMetrics(ctx, due)
}

// Run flushes on a timer until ctx is cancelled, then writes out what is left.
func (c *Collector) Run(ctx context.Context) {
	interval := c.FlushInterval
	if interval <= 0 {
		interval = defaultFlushInterval
	}

	c.Log.Info("metrics collector started",
		slog.Duration("flush_interval", interval),
		slog.Duration("retention", c.retention()))

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			// A cancelled context cannot be written through, so the final
			// flush gets a fresh one with a deadline of its own.
			final, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			defer cancel()

			if err := c.FlushAll(final); err != nil {
				c.Log.Error("final metrics flush failed", slog.String("error", err.Error()))
			}
			c.Log.Info("metrics collector stopped")
			return

		case <-ticker.C:
			if err := c.Flush(ctx); err != nil {
				c.Log.Error("metrics flush failed", slog.String("error", err.Error()))
			}
			c.prune(ctx)
		}
	}
}

// prune drops rollups past the retention window, at most once an hour. It runs
// from the flush loop rather than as its own job because it needs no
// coordination: deleting a row twice is deleting it once, so several replicas
// pruning at once is harmless.
func (c *Collector) prune(ctx context.Context) {
	now := c.now()

	c.mu.Lock()
	due := now.Sub(c.lastPrune) >= pruneInterval
	if due {
		c.lastPrune = now
	}
	c.mu.Unlock()

	if !due {
		return
	}

	deleted, err := c.Sink.PruneMetrics(ctx, now.Add(-c.retention()))
	if err != nil {
		c.Log.Warn("prune metrics failed", slog.String("error", err.Error()))
		return
	}
	if deleted > 0 {
		c.Log.Info("pruned expired metrics", slog.Int64("rows", deleted))
	}
}

func (c *Collector) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Collector) retention() time.Duration {
	if c.Retention > 0 {
		return c.Retention
	}
	return defaultRetention
}
