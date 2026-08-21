// Package metrics measures deployed applications and turns the measurements
// into the numbers an operator reads.
//
// Collection happens at the proxy, because every request to a deployed
// workload already passes through it: nothing has to be installed in the
// application, and a workload that has stopped answering is measured by the
// same code that measured it while it was healthy. An agent inside the
// container would go quiet at exactly the moment its numbers mattered.
package metrics

import "math"

// bounds are the inclusive upper edges, in milliseconds, of the latency
// buckets every histogram uses. They run from a millisecond to half a minute
// in roughly half-order-of-magnitude steps, so the interpolation error inside
// any one bucket stays well under the smallest regression the analysis is
// willing to report.
//
// These edges are part of the stored format: a histogram in the database is
// counts against this list and nothing else. Changing the list is therefore a
// migration, not an edit.
var bounds = [...]float64{
	1, 2, 3, 5, 7.5,
	10, 20, 30, 50, 75,
	100, 200, 300, 500, 750,
	1000, 2000, 3000, 5000, 7500,
	10000, 20000, 30000,
}

// numBuckets is one counter per bound plus a final counter for everything
// slower than the last bound.
const numBuckets = len(bounds) + 1

// Histogram is a latency distribution: how many requests fell into each
// bucket. It is stored instead of a percentile because percentiles do not add
// up — the mean of two minutes' p95 is not the p95 of the two minutes
// together, but the sum of their histograms is exactly the distribution of
// both.
type Histogram []int64

// NewHistogram returns an empty histogram with the current bucket layout.
func NewHistogram() Histogram { return make(Histogram, numBuckets) }

// Observe records one request that took ms milliseconds.
func (h Histogram) Observe(ms float64) {
	h[bucketFor(ms)]++
}

// Add sums other into h.
//
// A histogram whose length does not match the current layout is skipped rather
// than merged: its counts belong to different bucket edges, so adding them
// would quietly report a latency nobody measured. Nothing in the database has
// a different length today, and a change to the edges is expected to migrate
// or drop what came before.
func (h Histogram) Add(other []int64) {
	if len(other) != len(h) {
		return
	}
	for i, count := range other {
		h[i] += count
	}
}

// Total is how many requests the histogram holds.
func (h Histogram) Total() int64 {
	var total int64
	for _, count := range h {
		total += count
	}
	return total
}

// Quantile estimates the q-th quantile in milliseconds, spreading each
// bucket's requests uniformly across the bucket to interpolate inside it.
//
// A quantile landing in the overflow bucket is reported as the last bound,
// which reads as "at least this slow". The true worst case is not lost: it is
// recorded exactly as the maximum alongside the histogram.
func (h Histogram) Quantile(q float64) float64 {
	total := h.Total()
	if total == 0 {
		return 0
	}

	target := q * float64(total)
	var cumulative float64
	for i, count := range h {
		if count == 0 {
			continue
		}
		if cumulative+float64(count) >= target {
			lo, hi := edges(i)
			if math.IsInf(hi, 1) {
				return bounds[len(bounds)-1]
			}
			// Where in this bucket the target rank falls.
			position := (target - cumulative) / float64(count)
			return lo + position*(hi-lo)
		}
		cumulative += float64(count)
	}
	return bounds[len(bounds)-1]
}

// CountAbove estimates how many requests were slower than ms.
//
// It interpolates inside the straddling bucket exactly as Quantile does, so
// the two agree: CountAbove(Quantile(0.95)) is five per cent of the total.
// That agreement is what lets the analysis put a number of affected requests
// against a latency regression.
func (h Histogram) CountAbove(ms float64) float64 {
	var above float64
	for i, count := range h {
		if count == 0 {
			continue
		}
		lo, hi := edges(i)
		switch {
		case lo >= ms:
			above += float64(count)
		case math.IsInf(hi, 1):
			// The overflow bucket knows only that these requests exceeded the
			// last bound, so once the threshold is out there too they are all
			// counted. A threshold above half a minute means the question has
			// stopped being about latency.
			above += float64(count)
		case hi > ms:
			above += float64(count) * (hi - ms) / (hi - lo)
		}
	}
	return above
}

// bucketFor is the index a latency belongs in.
func bucketFor(ms float64) int {
	for i, bound := range bounds {
		if ms <= bound {
			return i
		}
	}
	return numBuckets - 1
}

// edges returns the bucket's exclusive lower and inclusive upper millisecond
// bounds. The overflow bucket has no upper bound.
func edges(i int) (lo, hi float64) {
	if i > 0 {
		lo = bounds[i-1]
	}
	if i >= len(bounds) {
		return lo, math.Inf(1)
	}
	return lo, bounds[i]
}
