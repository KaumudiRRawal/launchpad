package analyze

import (
	"strings"
	"testing"
	"time"

	"github.com/KaumudiRRawal/launchpad/control-plane/internal/domain"
	"github.com/KaumudiRRawal/launchpad/control-plane/internal/metrics"
)

// The two windows every test uses: fifteen minutes under examination against
// the hour before it, which is what the API asks for by default.
const (
	testWindow   = 15 * time.Minute
	testBaseline = time.Hour

	oldCommit = "1111111111111111111111111111111111111111"
	newCommit = "2222222222222222222222222222222222222222"
)

var (
	testNow      = time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	currentStart = testNow.Add(-testWindow)
	baseStart    = currentStart.Add(-testBaseline)
)

func repeat(ms float64, times int) []float64 {
	out := make([]float64, times)
	for i := range out {
		out[i] = ms
	}
	return out
}

// traffic builds one bucket per minute over [from, from+minutes), each holding
// the same requests. A test therefore states a rate and a distribution rather
// than arithmetic.
func traffic(from time.Time, minutes int, deployment, commit string, failuresPerMinute int, latencies []float64) []domain.MetricBucket {
	buckets := make([]domain.MetricBucket, 0, minutes)
	for i := range minutes {
		histogram := metrics.NewHistogram()
		var sum, highest float64
		for _, ms := range latencies {
			histogram.Observe(ms)
			sum += ms
			highest = max(highest, ms)
		}

		buckets = append(buckets, domain.MetricBucket{
			DeploymentID:  deployment,
			EnvironmentID: "env-1",
			CommitSHA:     commit,
			Bucket:        from.Add(time.Duration(i) * time.Minute),
			Requests:      int64(len(latencies)),
			Failures:      int64(failuresPerMinute),
			LatencySumMS:  sum,
			LatencyMaxMS:  highest,
			Histogram:     histogram,
		})
	}
	return buckets
}

func report(buckets ...[]domain.MetricBucket) Report {
	var all []domain.MetricBucket
	for _, set := range buckets {
		all = append(all, set...)
	}
	return Analyze(Input{
		EnvironmentID: "env-1",
		Now:           testNow,
		Window:        testWindow,
		Baseline:      testBaseline,
		Buckets:       all,
	})
}

// find returns the finding with a code, so a test asserts on the finding it
// means rather than on a position in a list.
func find(r Report, code string) (Finding, bool) {
	for _, f := range r.Findings {
		if f.Code == code {
			return f, true
		}
	}
	return Finding{}, false
}

func codes(r Report) []string {
	out := make([]string, 0, len(r.Findings))
	for _, f := range r.Findings {
		out = append(out, f.Code)
	}
	return out
}

func TestTooLittleTrafficIsNotHealth(t *testing.T) {
	// Five requests in the window. An environment nobody called has not been
	// shown to work, so this must not read as healthy.
	r := report(
		traffic(baseStart, 60, oldCommit, oldCommit, 0, repeat(20, 20)),
		traffic(currentStart, 5, oldCommit, oldCommit, 0, repeat(20, 1)),
	)

	if r.Verdict != VerdictInsufficientData {
		t.Errorf("Verdict = %q, want %q", r.Verdict, VerdictInsufficientData)
	}
	if len(r.Findings) != 0 || len(r.Remediations) != 0 {
		t.Errorf("a report that cannot judge produced %d findings and %d steps",
			len(r.Findings), len(r.Remediations))
	}
	if r.Detail == "" {
		t.Error("Detail is empty; a report that says nothing has to say why")
	}
	// Still reported, because the numbers are real even when they are too few
	// to judge.
	if r.Current.Summary.Requests != 5 {
		t.Errorf("Current requests = %d, want 5", r.Current.Summary.Requests)
	}
}

func TestUnchangedTrafficIsHealthy(t *testing.T) {
	r := report(
		traffic(baseStart, 60, oldCommit, oldCommit, 0, repeat(20, 20)),
		traffic(currentStart, 15, oldCommit, oldCommit, 0, repeat(20, 20)),
	)

	if r.Verdict != VerdictHealthy {
		t.Errorf("Verdict = %q with findings %v, want %q", r.Verdict, codes(r), VerdictHealthy)
	}
	// Non-nil rather than null, so a client can iterate without checking.
	if r.Findings == nil || r.Remediations == nil {
		t.Error("findings and remediations must encode as [] rather than null")
	}
}

func TestLatencyRegressionThresholds(t *testing.T) {
	tests := []struct {
		name             string
		baselineLatency  []float64
		currentLatency   []float64
		wantRegression   bool
		wantSeverityHigh bool
	}{
		{
			name:             "a slow release is flagged",
			baselineLatency:  repeat(20, 20),
			currentLatency:   repeat(120, 20),
			wantRegression:   true,
			wantSeverityHigh: true,
		},
		{
			// The ratio alone would call this a fifty per cent regression.
			// Nobody can tell two milliseconds apart.
			name:            "a large ratio on a tiny latency is not a regression",
			baselineLatency: repeat(4, 20),
			currentLatency:  repeat(6, 20),
			wantRegression:  false,
		},
		{
			// The floor alone would ignore this, because the service was
			// already slow enough for the absolute change to look ordinary.
			name:             "an already-slow service still regresses",
			baselineLatency:  repeat(400, 20),
			currentLatency:   repeat(1500, 20),
			wantRegression:   true,
			wantSeverityHigh: true,
		},
		{
			// 295ms to 333ms: past the absolute floor, well short of the ratio.
			// Latency moves on its own, and a detector that reports every
			// movement gets ignored.
			name:            "a small proportional change is not a regression",
			baselineLatency: repeat(300, 100),
			currentLatency:  append(repeat(300, 94), repeat(500, 6)...),
			wantRegression:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := report(
				traffic(baseStart, 60, oldCommit, oldCommit, 0, tt.baselineLatency),
				traffic(currentStart, 15, newCommit, newCommit, 0, tt.currentLatency),
			)

			finding, found := find(r, CodeLatencyRegression)
			if found != tt.wantRegression {
				t.Fatalf("latency regression reported = %v, want %v (findings: %v, p95 %v → %v)",
					found, tt.wantRegression, codes(r),
					r.Baseline.Summary.LatencyP95MS, r.Current.Summary.LatencyP95MS)
			}
			if !found {
				return
			}

			if tt.wantSeverityHigh && finding.Severity != SeverityCritical {
				t.Errorf("Severity = %q, want %q", finding.Severity, SeverityCritical)
			}
			if finding.AffectedRequestsPerHour <= 0 {
				t.Error("a regression with no affected traffic cannot be ranked against anything")
			}
			if finding.BaselineValue >= finding.CurrentValue {
				t.Errorf("evidence contradicts the finding: %v → %v",
					finding.BaselineValue, finding.CurrentValue)
			}
		})
	}
}

func TestALatencyRegressionAcrossAReleaseRecommendsRollingBack(t *testing.T) {
	r := report(
		traffic(baseStart, 60, "deploy-old", oldCommit, 0, repeat(20, 20)),
		traffic(currentStart, 15, "deploy-new", newCommit, 0, repeat(120, 20)),
	)

	if len(r.Remediations) == 0 {
		t.Fatalf("no remediation for findings %v", codes(r))
	}
	step := r.Remediations[0]
	if step.Action != ActionRollBack {
		t.Errorf("first step = %q, want %q", step.Action, ActionRollBack)
	}
	// The step has to name the commit to go back to, or it is advice rather
	// than an instruction.
	if want := oldCommit[:12]; !strings.Contains(step.Summary, want) {
		t.Errorf("Summary %q does not name the baseline commit %s", step.Summary, want)
	}
	if !strings.Contains(step.Detail, newCommit[:12]) {
		t.Errorf("Detail %q does not name the release that replaced it", step.Detail)
	}
	if step.Finding != CodeLatencyRegression {
		t.Errorf("Finding = %q, want %q", step.Finding, CodeLatencyRegression)
	}
	if r.Verdict != VerdictFailing {
		t.Errorf("Verdict = %q, want %q", r.Verdict, VerdictFailing)
	}
}

func TestALatencyRegressionWithinOneReleaseDoesNotRecommendRollingBack(t *testing.T) {
	// The same deployment served both windows, so there is no release to undo:
	// telling someone to roll back to the version they are already running
	// would send them looking in the wrong place entirely.
	r := report(
		traffic(baseStart, 60, "deploy-1", oldCommit, 0, repeat(20, 20)),
		traffic(currentStart, 15, "deploy-1", oldCommit, 0, repeat(120, 20)),
	)

	if len(r.Remediations) == 0 {
		t.Fatalf("no remediation for findings %v", codes(r))
	}
	for _, step := range r.Remediations {
		if step.Action == ActionRollBack {
			t.Fatalf("recommended a rollback with no release boundary: %+v", step)
		}
	}
	if r.Remediations[0].Action != ActionProfileRelease {
		t.Errorf("first step = %q, want %q", r.Remediations[0].Action, ActionProfileRelease)
	}
}

func TestFailureFindings(t *testing.T) {
	tests := []struct {
		name              string
		baselineFailures  int
		currentFailures   int
		wantCode          string
		wantAbsent        string
		wantSeverity      Severity
		wantAffectedAbove float64
	}{
		{
			// Twenty requests a minute, two of them failing: a tenth of the
			// traffic, where the baseline had none.
			name:              "a new failure mode is a regression",
			baselineFailures:  0,
			currentFailures:   2,
			wantCode:          CodeReliabilityRegression,
			wantAbsent:        CodeElevatedFailureRate,
			wantSeverity:      SeverityCritical,
			wantAffectedAbove: 100,
		},
		{
			// Failing before the baseline window began. Comparing against that
			// baseline calls it unchanged, which is true and useless.
			name:             "a long-standing failure rate is still reported",
			baselineFailures: 2,
			currentFailures:  2,
			wantCode:         CodeElevatedFailureRate,
			wantAbsent:       CodeReliabilityRegression,
			wantSeverity:     SeverityWarning,
		},
		{
			name:             "one failure in twenty is not worth a page",
			baselineFailures: 0,
			currentFailures:  0,
			wantAbsent:       CodeReliabilityRegression,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := report(
				traffic(baseStart, 60, "deploy-1", oldCommit, tt.baselineFailures, repeat(20, 20)),
				traffic(currentStart, 15, "deploy-1", oldCommit, tt.currentFailures, repeat(20, 20)),
			)

			if tt.wantCode != "" {
				finding, found := find(r, tt.wantCode)
				if !found {
					t.Fatalf("%s not reported; findings were %v", tt.wantCode, codes(r))
				}
				if finding.Severity != tt.wantSeverity {
					t.Errorf("Severity = %q, want %q", finding.Severity, tt.wantSeverity)
				}
				if finding.AffectedRequestsPerHour < tt.wantAffectedAbove {
					t.Errorf("AffectedRequestsPerHour = %v, want at least %v",
						finding.AffectedRequestsPerHour, tt.wantAffectedAbove)
				}
			}
			if _, found := find(r, tt.wantAbsent); found {
				t.Errorf("%s was also reported; findings were %v", tt.wantAbsent, codes(r))
			}
		})
	}
}

func TestFailuresAcrossAReleaseRankRollbackFirst(t *testing.T) {
	r := report(
		traffic(baseStart, 60, "deploy-old", oldCommit, 0, repeat(20, 20)),
		traffic(currentStart, 15, "deploy-new", newCommit, 2, repeat(20, 20)),
	)

	if len(r.Remediations) != 2 {
		t.Fatalf("got %d steps, want 2 (undo the release, then read the logs): %+v",
			len(r.Remediations), r.Remediations)
	}
	// Both answer the same finding and therefore carry the same figure. The
	// order between them is the order they were produced in, which puts
	// removing the cause ahead of going to look for it.
	if r.Remediations[0].Action != ActionRollBack {
		t.Errorf("first step = %q, want %q", r.Remediations[0].Action, ActionRollBack)
	}
	if r.Remediations[1].Action != ActionInspectFailures {
		t.Errorf("second step = %q, want %q", r.Remediations[1].Action, ActionInspectFailures)
	}
}

func TestTailSpreadIsFoundWithoutAnyRegression(t *testing.T) {
	// Nineteen fast requests and one very slow one, every minute, in both
	// windows. Nothing has changed, and a fortieth of the traffic is still
	// having a bad time — which only the distribution can show.
	mix := append(repeat(5, 19), 2000)
	r := report(
		traffic(baseStart, 60, "deploy-1", oldCommit, 0, mix),
		traffic(currentStart, 15, "deploy-1", oldCommit, 0, mix),
	)

	finding, found := find(r, CodeLatencyTailSpread)
	if !found {
		t.Fatalf("tail spread not reported; findings were %v (p50 %v, p99 %v)",
			codes(r), r.Current.Summary.LatencyP50MS, r.Current.Summary.LatencyP99MS)
	}
	if _, regressed := find(r, CodeLatencyRegression); regressed {
		t.Error("reported a regression where nothing changed")
	}
	if r.Verdict != VerdictDegraded {
		t.Errorf("Verdict = %q, want %q", r.Verdict, VerdictDegraded)
	}
	if finding.AffectedRequestsPerHour <= 0 {
		t.Error("AffectedRequestsPerHour = 0; the slow requests were counted as none")
	}
	if len(r.Remediations) != 1 || r.Remediations[0].Action != ActionInvestigateTail {
		t.Errorf("steps = %+v, want one %q", r.Remediations, ActionInvestigateTail)
	}
}

func TestAQuietBaselineIsNotComparedAgainst(t *testing.T) {
	// Three requests in the hour before. Calling that a baseline would let one
	// slow request an hour ago decide whether today is a regression.
	r := report(
		traffic(baseStart, 3, "deploy-1", oldCommit, 0, repeat(20, 1)),
		traffic(currentStart, 15, "deploy-1", oldCommit, 0, repeat(500, 20)),
	)

	if _, found := find(r, CodeLatencyRegression); found {
		t.Errorf("compared against a three-request baseline; findings were %v", codes(r))
	}
}

// TestRemediationsAreRankedByAffectedTraffic exercises the ranking rule on its
// own, because in a real report the findings arrive already ordered and the
// sort would look correct either way.
func TestRemediationsAreRankedByAffectedTraffic(t *testing.T) {
	var a analysis

	steps := a.remediations([]Finding{
		{Code: CodeLatencyTailSpread, AffectedRequestsPerHour: 4},
		{Code: CodeElevatedFailureRate, AffectedRequestsPerHour: 900},
	})

	if len(steps) != 2 {
		t.Fatalf("got %d steps, want 2", len(steps))
	}
	if steps[0].Finding != CodeElevatedFailureRate {
		t.Errorf("ranked %q first; the finding affecting 900 requests an hour belongs above the one affecting 4",
			steps[0].Finding)
	}
	if steps[0].AffectedRequestsPerHour != 900 {
		t.Errorf("AffectedRequestsPerHour = %v, want the figure inherited from the finding",
			steps[0].AffectedRequestsPerHour)
	}
}

func TestWindowsAreReportedAsMeasured(t *testing.T) {
	r := report(
		traffic(baseStart, 60, "deploy-1", oldCommit, 0, repeat(20, 20)),
		traffic(currentStart, 15, "deploy-1", oldCommit, 0, repeat(20, 20)),
	)

	if !r.Current.From.Equal(currentStart) || !r.Current.To.Equal(testNow) {
		t.Errorf("current window = %v..%v, want %v..%v", r.Current.From, r.Current.To, currentStart, testNow)
	}
	// The baseline ends where the current window begins: they must not overlap,
	// or the change would be compared against itself.
	if !r.Baseline.To.Equal(r.Current.From) {
		t.Errorf("baseline ends at %v but the window starts at %v", r.Baseline.To, r.Current.From)
	}
	if !r.Baseline.From.Equal(baseStart) {
		t.Errorf("baseline starts at %v, want %v", r.Baseline.From, baseStart)
	}
	if r.Baseline.Summary.Requests != 1200 || r.Current.Summary.Requests != 300 {
		t.Errorf("requests = %d baseline / %d current, want 1200/300",
			r.Baseline.Summary.Requests, r.Current.Summary.Requests)
	}
}

func TestBaselineForIsLongerThanTheWindow(t *testing.T) {
	// The baseline has to be several times the window under test, or one slow
	// minute moves the thing being compared against.
	if got := BaselineFor(15 * time.Minute); got <= 15*time.Minute {
		t.Errorf("BaselineFor(15m) = %v, want longer than the window", got)
	}
}

// TestAnElevatedFailureRateWithNoBaselineSaysSo covers the first minutes of an
// environment's life, when there is nothing behind the window to compare
// against. Reporting that "the baseline was no better" would be inventing a
// comparison that never happened.
func TestAnElevatedFailureRateWithNoBaselineSaysSo(t *testing.T) {
	r := report(traffic(currentStart, 15, "deploy-1", oldCommit, 2, repeat(20, 20)))

	finding, found := find(r, CodeElevatedFailureRate)
	if !found {
		t.Fatalf("elevated failure rate not reported; findings were %v", codes(r))
	}
	if strings.Contains(finding.Detail, "the baseline was no better") {
		t.Errorf("Detail claims a comparison against an empty baseline: %q", finding.Detail)
	}
	if !strings.Contains(finding.Detail, "too little traffic before this window") {
		t.Errorf("Detail does not say the baseline was silent: %q", finding.Detail)
	}
}
