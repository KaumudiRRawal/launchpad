package metrics

import (
	"math"
	"testing"
	"time"

	"github.com/KaumudiRRawal/launchpad/control-plane/internal/domain"
)

// observe builds a histogram from a list of latencies, so a test reads as the
// traffic it describes.
func observe(latencies ...float64) Histogram {
	h := NewHistogram()
	for _, ms := range latencies {
		h.Observe(ms)
	}
	return h
}

func repeat(ms float64, times int) []float64 {
	out := make([]float64, times)
	for i := range out {
		out[i] = ms
	}
	return out
}

func TestQuantileInterpolatesWithinTheBucket(t *testing.T) {
	tests := []struct {
		name      string
		latencies []float64
		quantile  float64
		want      float64
	}{
		{name: "no traffic has no latency", latencies: nil, quantile: 0.95, want: 0},

		// One hundred requests all in the 20–30ms bucket. The 95th percentile
		// is 95% of the way through it.
		{name: "single bucket p50", latencies: repeat(30, 100), quantile: 0.50, want: 25},
		{name: "single bucket p95", latencies: repeat(30, 100), quantile: 0.95, want: 29.5},

		// Ninety fast requests and ten slow ones: the median is in the fast
		// bucket and the 95th percentile is in the slow one, which is the whole
		// point of keeping the distribution.
		{
			name:      "median ignores the tail",
			latencies: append(repeat(10, 90), repeat(1000, 10)...),
			quantile:  0.50,
			want:      7.5 + (50.0/90.0)*2.5,
		},
		{
			name:      "the tail is where the tail is",
			latencies: append(repeat(10, 90), repeat(1000, 10)...),
			quantile:  0.95,
			want:      875,
		},

		// Beyond the last bound nothing more is known than "at least this
		// slow", so that is what is reported.
		{name: "overflow saturates at the last bound", latencies: repeat(60000, 10), quantile: 0.99, want: 30000},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := observe(tt.latencies...).Quantile(tt.quantile)
			if math.Abs(got-tt.want) > 0.001 {
				t.Errorf("Quantile(%v) = %v, want %v", tt.quantile, got, tt.want)
			}
		})
	}
}

func TestQuantilesAreOrdered(t *testing.T) {
	// A mixture wide enough to put the three percentiles in three different
	// buckets, because a percentile that crosses its neighbour would still
	// look plausible read on its own.
	h := observe(append(append(repeat(5, 500), repeat(120, 40)...), repeat(4000, 10)...)...)

	p50, p95, p99 := h.Quantile(0.50), h.Quantile(0.95), h.Quantile(0.99)
	if !(p50 <= p95 && p95 <= p99) {
		t.Errorf("percentiles out of order: p50=%v p95=%v p99=%v", p50, p95, p99)
	}
}

func TestCountAbove(t *testing.T) {
	h := observe(repeat(30, 100)...)

	tests := []struct {
		name      string
		threshold float64
		want      float64
	}{
		{name: "at the bucket floor everything is above", threshold: 20, want: 100},
		{name: "half way through the bucket", threshold: 25, want: 50},
		{name: "at the bucket ceiling nothing is above", threshold: 30, want: 0},
		{name: "beyond the traffic entirely", threshold: 500, want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := h.CountAbove(tt.threshold); math.Abs(got-tt.want) > 0.001 {
				t.Errorf("CountAbove(%v) = %v, want %v", tt.threshold, got, tt.want)
			}
		})
	}
}

// TestCountAboveAgreesWithQuantile is the property the analysis depends on:
// it puts a number of affected requests against a latency threshold taken from
// a percentile, so the two have to be interpolated the same way. Five per cent
// of requests are above the 95th percentile by definition.
func TestCountAboveAgreesWithQuantile(t *testing.T) {
	h := observe(append(append(repeat(8, 400), repeat(90, 80)...), repeat(1500, 20)...)...)

	total := float64(h.Total())
	for _, q := range []float64{0.50, 0.95, 0.99} {
		want := total * (1 - q)
		if got := h.CountAbove(h.Quantile(q)); math.Abs(got-want) > 0.001 {
			t.Errorf("CountAbove(Quantile(%v)) = %v, want %v", q, got, want)
		}
	}
}

func TestAddRefusesAnotherLayout(t *testing.T) {
	h := observe(repeat(5, 3)...)

	// A histogram of a different length holds counts against different bucket
	// edges. Merging it would report a latency nobody measured, so it is
	// dropped instead.
	h.Add([]int64{1, 2, 3})
	if h.Total() != 3 {
		t.Errorf("Total() = %d after adding a mismatched histogram, want 3", h.Total())
	}

	h.Add(observe(repeat(5, 4)...))
	if h.Total() != 7 {
		t.Errorf("Total() = %d after adding a matching histogram, want 7", h.Total())
	}
}

func TestSummarize(t *testing.T) {
	at := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)

	buckets := []domain.MetricBucket{
		{
			Bucket: at, Requests: 100, Failures: 1,
			LatencySumMS: 3000, LatencyMaxMS: 240,
			Histogram: observe(repeat(30, 100)...),
		},
		{
			Bucket: at.Add(time.Minute), Requests: 100, Failures: 3,
			LatencySumMS: 5000, LatencyMaxMS: 90,
			Histogram: observe(repeat(50, 100)...),
		},
	}

	summary, combined := Summarize(buckets)

	if summary.Requests != 200 || summary.Failures != 4 {
		t.Errorf("requests/failures = %d/%d, want 200/4", summary.Requests, summary.Failures)
	}
	if want := 0.98; math.Abs(summary.Availability-want) > 0.0001 {
		t.Errorf("Availability = %v, want %v", summary.Availability, want)
	}
	if want := 40.0; math.Abs(summary.LatencyAvgMS-want) > 0.0001 {
		t.Errorf("LatencyAvgMS = %v, want %v", summary.LatencyAvgMS, want)
	}
	// The largest single request, not the largest minute's average, and not
	// bounded by the histogram's edges.
	if summary.LatencyMaxMS != 240 {
		t.Errorf("LatencyMaxMS = %v, want 240", summary.LatencyMaxMS)
	}
	if combined.Total() != 200 {
		t.Errorf("combined histogram holds %d requests, want 200", combined.Total())
	}
	// Half the traffic at 30ms and half at 50ms: the median is the top of the
	// slower half's predecessor, not the mean of the two minutes' medians.
	if summary.LatencyP50MS <= 25 || summary.LatencyP50MS > 50 {
		t.Errorf("LatencyP50MS = %v, want it inside the observed range", summary.LatencyP50MS)
	}
}

// TestSummarizeOfNothing pins the one case an operator misreads most easily:
// silence is not failure.
func TestSummarizeOfNothing(t *testing.T) {
	summary, combined := Summarize(nil)

	if summary.Availability != 1 {
		t.Errorf("Availability = %v, want 1 — an environment nobody called has not been shown to be broken", summary.Availability)
	}
	if summary.Requests != 0 || combined.Total() != 0 {
		t.Errorf("summary of no buckets reported traffic: %+v", summary)
	}
}

func TestSeriesFoldsDeploymentsIntoOneLinePerMinute(t *testing.T) {
	at := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)

	// A release lands mid-window, so one minute has two deployments in it. The
	// series is about the environment, not about either deployment.
	buckets := []domain.MetricBucket{
		{DeploymentID: "new", Bucket: at.Add(time.Minute), Requests: 5, Histogram: observe(repeat(10, 5)...)},
		{DeploymentID: "old", Bucket: at, Requests: 10, Failures: 1, Histogram: observe(repeat(10, 10)...)},
		{DeploymentID: "new", Bucket: at, Requests: 4, Histogram: observe(repeat(10, 4)...)},
	}

	points := Series(buckets)

	if len(points) != 2 {
		t.Fatalf("Series() returned %d points, want 2", len(points))
	}
	// Oldest first, whatever order the rows arrived in.
	if !points[0].Bucket.Equal(at) || !points[1].Bucket.Equal(at.Add(time.Minute)) {
		t.Fatalf("points are not in time order: %v then %v", points[0].Bucket, points[1].Bucket)
	}
	if points[0].Requests != 14 || points[0].Failures != 1 {
		t.Errorf("first point = %d requests / %d failures, want 14/1",
			points[0].Requests, points[0].Failures)
	}
	if points[1].Requests != 5 {
		t.Errorf("second point = %d requests, want 5", points[1].Requests)
	}
}

// TestPercentilesNeverExceedTheObservedMaximum is the artefact interpolation
// creates: a handful of requests in one wide bucket put the 99th percentile
// near the top of that bucket however fast the slowest of them really was. A
// p99 above the maximum is a number nobody can act on, because no request took
// that long.
func TestPercentilesNeverExceedTheObservedMaximum(t *testing.T) {
	histogram := NewHistogram()
	for range 44 {
		histogram.Observe(1)
	}
	for range 5 {
		histogram.Observe(310)
	}

	// The slowest request took 310ms, which sits at the bottom of the
	// 300–500ms bucket.
	summary, _ := Summarize([]domain.MetricBucket{{
		Requests: 49, LatencySumMS: 1594, LatencyMaxMS: 310, Histogram: histogram,
	}})

	if summary.LatencyP99MS > summary.LatencyMaxMS {
		t.Errorf("p99 = %v with a maximum of %v; no request was that slow",
			summary.LatencyP99MS, summary.LatencyMaxMS)
	}
	if summary.LatencyP95MS > summary.LatencyMaxMS {
		t.Errorf("p95 = %v with a maximum of %v", summary.LatencyP95MS, summary.LatencyMaxMS)
	}
	// Still a real reading of the distribution: most requests were fast.
	if summary.LatencyP50MS > 2 {
		t.Errorf("p50 = %v, want it in the millisecond bucket the bulk of the traffic was in",
			summary.LatencyP50MS)
	}
}
