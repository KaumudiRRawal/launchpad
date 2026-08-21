package metrics

import (
	"slices"
	"time"

	"github.com/KaumudiRRawal/launchpad/control-plane/internal/domain"
)

// Summarize folds every bucket in a window into one set of numbers and the
// combined distribution behind them. The histogram is returned as well as the
// summary because the analysis needs to ask questions of the distribution that
// a fixed set of percentiles cannot answer.
func Summarize(buckets []domain.MetricBucket) (domain.MetricSummary, Histogram) {
	combined := NewHistogram()
	var summary domain.MetricSummary
	var sumMS float64

	for _, b := range buckets {
		summary.Requests += b.Requests
		summary.Failures += b.Failures
		sumMS += b.LatencySumMS
		summary.LatencyMaxMS = max(summary.LatencyMaxMS, b.LatencyMaxMS)
		combined.Add(b.Histogram)
	}

	if summary.Requests == 0 {
		// An environment nobody called has not been shown to be broken.
		summary.Availability = 1
		return summary, combined
	}

	summary.Availability = float64(summary.Requests-summary.Failures) / float64(summary.Requests)
	summary.LatencyAvgMS = sumMS / float64(summary.Requests)

	// Capped at the largest request actually seen. A percentile is interpolated
	// inside whichever bucket it lands in, so five slow requests in the
	// 300–500ms bucket put the 99th percentile near 500ms however fast the
	// slowest of them really was — and a p99 above the maximum is a number
	// nobody can act on, because it did not happen.
	summary.LatencyP50MS = min(combined.Quantile(0.50), summary.LatencyMaxMS)
	summary.LatencyP95MS = min(combined.Quantile(0.95), summary.LatencyMaxMS)
	summary.LatencyP99MS = min(combined.Quantile(0.99), summary.LatencyMaxMS)
	return summary, combined
}

// Series folds buckets into one point per minute across every deployment that
// served the environment, oldest first. A redeploy mid-window does not break
// the line: traffic is continuous even though the deployment behind it is not.
func Series(buckets []domain.MetricBucket) []domain.MetricPoint {
	type minute struct {
		requests  int64
		failures  int64
		histogram Histogram
	}

	byMinute := make(map[int64]*minute, len(buckets))
	order := make([]int64, 0, len(buckets))

	for _, b := range buckets {
		at := b.Bucket.Unix()
		m, ok := byMinute[at]
		if !ok {
			m = &minute{histogram: NewHistogram()}
			byMinute[at] = m
			order = append(order, at)
		}
		m.requests += b.Requests
		m.failures += b.Failures
		m.histogram.Add(b.Histogram)
	}

	slices.Sort(order)

	points := make([]domain.MetricPoint, 0, len(order))
	for _, at := range order {
		m := byMinute[at]
		points = append(points, domain.MetricPoint{
			Bucket:       time.Unix(at, 0).UTC(),
			Requests:     m.requests,
			Failures:     m.failures,
			LatencyP95MS: m.histogram.Quantile(0.95),
		})
	}
	return points
}
