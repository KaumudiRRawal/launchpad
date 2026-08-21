// Package analyze compares a recent window of an environment's traffic against
// the period before it, reports what has got worse, and ranks what to do about
// it by how much traffic each problem affects.
//
// Every number in a report is derived from observations the proxy actually
// made. Nothing is modelled, assumed or predicted — including the figure the
// remediation steps are ranked by, which is the measured cost of the problem
// rather than a guess at how well the fix will work. The platform can see how
// much traffic a regression affects; it cannot see the future.
package analyze

import (
	"fmt"
	"slices"
	"time"

	"github.com/KaumudiRRawal/launchpad/control-plane/internal/domain"
	"github.com/KaumudiRRawal/launchpad/control-plane/internal/metrics"
)

// Severity is how much a finding deserves someone's attention. There is no
// informational level on purpose: a finding the report will not ask anyone to
// act on is noise, and a report full of noise stops being read.
type Severity string

const (
	SeverityWarning  Severity = "warning"
	SeverityCritical Severity = "critical"
)

// Verdict is the whole report in one word.
type Verdict string

const (
	// VerdictInsufficientData means the window was too quiet to judge. It is
	// deliberately not "healthy": an environment nobody called has not been
	// shown to work.
	VerdictInsufficientData Verdict = "insufficient_data"
	VerdictHealthy          Verdict = "healthy"
	VerdictDegraded         Verdict = "degraded"
	VerdictFailing          Verdict = "failing"
)

// Finding codes. They are stable identifiers a client may branch on; the
// summary and detail beside them are prose and may be reworded.
const (
	CodeLatencyRegression     = "latency_regression"
	CodeReliabilityRegression = "reliability_regression"
	CodeElevatedFailureRate   = "elevated_failure_rate"
	CodeLatencyTailSpread     = "latency_tail_spread"
)

// Remediation actions, likewise stable.
const (
	ActionRollBack        = "roll_back_deployment"
	ActionProfileRelease  = "profile_the_request_path"
	ActionInspectFailures = "inspect_failing_requests"
	ActionInvestigateTail = "investigate_the_slow_path"
)

// Thresholds. Each one exists to keep a particular false positive out of the
// report, and says which.
const (
	// minRequests is the fewest requests a window needs before it is judged at
	// all. Below it a 95th percentile is one or two requests, and the analysis
	// would spend its time reporting the ordinary variation of a quiet
	// environment as a regression.
	minRequests = 20

	// A latency regression has to clear both of these. The ratio alone would
	// call 4ms → 6ms a fifty per cent regression; the floor alone would say
	// nothing about 400ms → 800ms on a service that was already slow.
	latencyRegressionRatio   = 1.25
	latencyRegressionFloorMS = 25.0

	// latencyCriticalRatio is where a regression stops being something to
	// watch and becomes something to act on now.
	latencyCriticalRatio = 2.0

	// reliabilityRegressionPoints is a rise in the failure rate in absolute
	// terms rather than relative: 0% → 1% is a new failure mode worth naming,
	// where 4% → 5% is the same one continuing.
	reliabilityRegressionPoints = 0.01

	// elevatedFailureRate catches an environment that was already failing
	// before the baseline window began. Comparing against that baseline would
	// call it unchanged, which is true and useless: it is still broken.
	elevatedFailureRate = 0.05
	criticalFailureRate = 0.20

	// A wide gap between the median and the tail means most requests are fine
	// and a minority are doing something extra. It needs more requests than
	// the other findings, because a 99th percentile drawn from twenty
	// observations is just the slowest of them.
	tailSpreadRatio   = 4.0
	tailSpreadFloorMS = 100.0
	minTailRequests   = 100

	// baselineFactor makes the baseline several times longer than the window
	// under test, so one slow minute cannot move the thing being compared
	// against while the comparison still reflects recent traffic rather than
	// last week's.
	baselineFactor = 4
)

// DefaultWindow is the recent period a report examines unless asked otherwise.
// Short enough that a bad release shows up while someone is still watching the
// deploy.
const DefaultWindow = 15 * time.Minute

// BaselineFor is how far back before a window the comparison reaches.
func BaselineFor(window time.Duration) time.Duration {
	return window * baselineFactor
}

// Finding is one thing that is wrong, with the evidence for it.
type Finding struct {
	Code     string   `json:"code"`
	Severity Severity `json:"severity"`
	Summary  string   `json:"summary"`
	Detail   string   `json:"detail"`
	// Metric names the field of MetricSummary the two values below are drawn
	// from, so a client can point at the number it is already showing.
	Metric        string  `json:"metric"`
	BaselineValue float64 `json:"baseline_value"`
	CurrentValue  float64 `json:"current_value"`
	// AffectedRequestsPerHour is how much traffic this finding costs, measured
	// from the window's own distribution and scaled to an hour so findings
	// from windows of different lengths can be compared.
	AffectedRequestsPerHour float64 `json:"affected_requests_per_hour"`
}

// Remediation is one step to take, and how much traffic the finding behind it
// affects.
type Remediation struct {
	Action  string `json:"action"`
	Summary string `json:"summary"`
	Detail  string `json:"detail"`
	// Finding is the code of the finding this step answers.
	Finding string `json:"finding"`
	// AffectedRequestsPerHour is inherited from that finding. It is what the
	// step stands to recover if it works, not a claim that it will.
	AffectedRequestsPerHour float64 `json:"affected_requests_per_hour"`
}

// Report is the whole analysis of one environment.
type Report struct {
	EnvironmentID string    `json:"environment_id"`
	GeneratedAt   time.Time `json:"generated_at"`
	Verdict       Verdict   `json:"verdict"`
	// Detail explains the verdict in one sentence, including why a report says
	// nothing when it says nothing.
	Detail   string              `json:"detail"`
	Baseline domain.MetricWindow `json:"baseline"`
	Current  domain.MetricWindow `json:"current"`
	// Findings are the evidence, worst first.
	Findings []Finding `json:"findings"`
	// Remediations are the steps, in the order they are worth taking.
	Remediations []Remediation `json:"remediations"`
}

// Input is everything Analyze needs. Now is a parameter rather than read from
// the clock, so a report is a function of its arguments and can be tested
// without freezing time.
type Input struct {
	EnvironmentID string
	Now           time.Time
	// Window is the recent period under examination.
	Window time.Duration
	// Baseline is how far back before Window the comparison reaches.
	Baseline time.Duration
	Buckets  []domain.MetricBucket
}

// Analyze produces the report.
func Analyze(in Input) Report {
	currentFrom := in.Now.Add(-in.Window)
	baselineFrom := currentFrom.Add(-in.Baseline)

	var current, baseline []domain.MetricBucket
	for _, b := range in.Buckets {
		switch {
		case !b.Bucket.Before(currentFrom):
			current = append(current, b)
		case !b.Bucket.Before(baselineFrom):
			baseline = append(baseline, b)
		}
	}

	currentSummary, currentHist := metrics.Summarize(current)
	baselineSummary, _ := metrics.Summarize(baseline)

	report := Report{
		EnvironmentID: in.EnvironmentID,
		GeneratedAt:   in.Now,
		Baseline:      domain.MetricWindow{From: baselineFrom, To: currentFrom, Summary: baselineSummary},
		Current:       domain.MetricWindow{From: currentFrom, To: in.Now, Summary: currentSummary},
		// Non-nil so an untroubled environment encodes as [] rather than null.
		Findings:     []Finding{},
		Remediations: []Remediation{},
	}

	if currentSummary.Requests < minRequests {
		report.Verdict = VerdictInsufficientData
		report.Detail = fmt.Sprintf(
			"%d requests in the last %s; at least %d are needed before a percentile describes anything",
			currentSummary.Requests, in.Window, minRequests)
		return report
	}

	a := analysis{
		current:         currentSummary,
		baseline:        baselineSummary,
		currentHist:     currentHist,
		currentRelease:  dominantRelease(current),
		baselineRelease: dominantRelease(baseline),
		windowFrom:      currentFrom,
		windowTo:        in.Now,
		windowHours:     in.Window.Hours(),
	}

	report.Findings = a.findings()
	report.Remediations = a.remediations(report.Findings)
	report.Verdict, report.Detail = verdictOf(report.Findings, in)
	return report
}

// analysis is the two windows and everything derived from them, so each
// detector reads as the question it asks rather than as argument plumbing.
type analysis struct {
	current, baseline domain.MetricSummary
	currentHist       metrics.Histogram
	currentRelease    release
	baselineRelease   release
	windowFrom        time.Time
	windowTo          time.Time
	windowHours       float64
}

// release is which deployment served most of a window's requests. The analysis
// uses it to tell "the code changed" from "something around the code changed",
// which is the difference between rolling back and going looking.
type release struct {
	deploymentID string
	commitSHA    string
	requests     int64
}

func dominantRelease(buckets []domain.MetricBucket) release {
	byDeployment := make(map[string]*release, 2)
	for _, b := range buckets {
		r, ok := byDeployment[b.DeploymentID]
		if !ok {
			r = &release{deploymentID: b.DeploymentID, commitSHA: b.CommitSHA}
			byDeployment[b.DeploymentID] = r
		}
		r.requests += b.Requests
	}

	var winner release
	for _, r := range byDeployment {
		// Ties broken by ID so the report does not change between two
		// identical requests.
		if r.requests > winner.requests ||
			(r.requests == winner.requests && r.deploymentID < winner.deploymentID) {
			winner = *r
		}
	}
	return winner
}

// releaseChanged reports whether a different deployment served each window,
// which is what makes a rollback a candidate rather than a guess.
func (a analysis) releaseChanged() bool {
	return a.baselineRelease.deploymentID != "" &&
		a.currentRelease.deploymentID != "" &&
		a.baselineRelease.deploymentID != a.currentRelease.deploymentID
}

// comparable reports whether the baseline holds enough traffic to be compared
// against. A regression is a statement about a change, and there is no change
// to describe without both sides of it.
func (a analysis) comparable() bool {
	return a.baseline.Requests >= minRequests
}

// findings runs every detector. The order matters only where one finding makes
// another redundant, which is stated where it happens.
func (a analysis) findings() []Finding {
	findings := []Finding{}

	if f, ok := a.latencyRegression(); ok {
		findings = append(findings, f)
	}

	reliability, regressed := a.reliabilityRegression()
	if regressed {
		findings = append(findings, reliability)
	} else if f, ok := a.elevatedFailures(); ok {
		// Only when the failure rate has not just risen. A regression already
		// says the environment is failing and says when it started, which is
		// strictly more than the absolute rate says on its own.
		findings = append(findings, f)
	}

	if f, ok := a.tailSpread(); ok {
		findings = append(findings, f)
	}

	slices.SortStableFunc(findings, func(x, y Finding) int {
		if c := compareDesc(x.AffectedRequestsPerHour, y.AffectedRequestsPerHour); c != 0 {
			return c
		}
		return severityRank(y.Severity) - severityRank(x.Severity)
	})
	return findings
}

func (a analysis) latencyRegression() (Finding, bool) {
	if !a.comparable() {
		return Finding{}, false
	}

	before, now := a.baseline.LatencyP95MS, a.current.LatencyP95MS
	if before <= 0 {
		return Finding{}, false
	}
	ratio := now / before
	if ratio < latencyRegressionRatio || now-before < latencyRegressionFloorMS {
		return Finding{}, false
	}

	// A window always has five per cent of its requests above its own p95, so
	// only what exceeds that share is attributable to the change.
	slower := a.currentHist.CountAbove(before)
	expected := 0.05 * float64(a.current.Requests)
	affected := max(0, slower-expected) / a.windowHours

	severity := SeverityWarning
	if ratio >= latencyCriticalRatio {
		severity = SeverityCritical
	}

	return Finding{
		Code:     CodeLatencyRegression,
		Severity: severity,
		Summary:  fmt.Sprintf("95th percentile latency is %.1f× the baseline", ratio),
		Detail: fmt.Sprintf(
			"p95 rose from %s to %s. About %s requests an hour are now slower than the baseline's p95, over and above the share that always would be.",
			millis(before), millis(now), count(affected)),
		Metric:                  "latency_p95_ms",
		BaselineValue:           before,
		CurrentValue:            now,
		AffectedRequestsPerHour: affected,
	}, true
}

func (a analysis) reliabilityRegression() (Finding, bool) {
	if !a.comparable() {
		return Finding{}, false
	}

	before, now := failureRate(a.baseline), failureRate(a.current)
	if now-before < reliabilityRegressionPoints {
		return Finding{}, false
	}

	affected := (now - before) * float64(a.current.Requests) / a.windowHours

	severity := SeverityWarning
	if now >= elevatedFailureRate {
		severity = SeverityCritical
	}

	return Finding{
		Code:     CodeReliabilityRegression,
		Severity: severity,
		Summary:  fmt.Sprintf("failure rate rose from %s to %s", percent(before), percent(now)),
		Detail: fmt.Sprintf(
			"%d of %d requests returned 5xx, against %s of the baseline's traffic. That is about %s extra failures an hour.",
			a.current.Failures, a.current.Requests, percent(before), count(affected)),
		Metric:                  "availability",
		BaselineValue:           a.baseline.Availability,
		CurrentValue:            a.current.Availability,
		AffectedRequestsPerHour: affected,
	}, true
}

func (a analysis) elevatedFailures() (Finding, bool) {
	rate := failureRate(a.current)
	if rate < elevatedFailureRate {
		return Finding{}, false
	}

	severity := SeverityWarning
	if rate >= criticalFailureRate {
		severity = SeverityCritical
	}

	// How this reads depends on whether there was anything to compare against.
	// Saying the baseline "was no better" when the baseline had no traffic at
	// all would be inventing a comparison that never happened.
	detail := fmt.Sprintf(
		"%d of %d requests returned 5xx. There was too little traffic before this window to say whether it is new.",
		a.current.Failures, a.current.Requests)
	if a.comparable() {
		detail = fmt.Sprintf(
			"%d of %d requests returned 5xx, and the baseline was no better, so this did not start in this window.",
			a.current.Failures, a.current.Requests)
	}

	return Finding{
		Code:                    CodeElevatedFailureRate,
		Severity:                severity,
		Summary:                 fmt.Sprintf("%s of requests are failing", percent(rate)),
		Detail:                  detail,
		Metric:                  "availability",
		BaselineValue:           a.baseline.Availability,
		CurrentValue:            a.current.Availability,
		AffectedRequestsPerHour: float64(a.current.Failures) / a.windowHours,
	}, true
}

func (a analysis) tailSpread() (Finding, bool) {
	if a.current.Requests < minTailRequests {
		return Finding{}, false
	}

	median, tail := a.current.LatencyP50MS, a.current.LatencyP99MS
	if median <= 0 {
		return Finding{}, false
	}
	if tail < median*tailSpreadRatio || tail-median < tailSpreadFloorMS {
		return Finding{}, false
	}

	// Counted against the same multiple of the median that triggered the
	// finding, so the number and the threshold describe one thing.
	threshold := median * tailSpreadRatio
	affected := a.currentHist.CountAbove(threshold) / a.windowHours

	return Finding{
		Code:     CodeLatencyTailSpread,
		Severity: SeverityWarning,
		Summary:  fmt.Sprintf("the slowest requests take %.0f× the median", tail/median),
		Detail: fmt.Sprintf(
			"the median request takes %s and the 99th percentile %s, so most requests are fine and a minority are doing something extra. About %s an hour take longer than %s.",
			millis(median), millis(tail), count(affected), millis(threshold)),
		Metric:                  "latency_p99_ms",
		BaselineValue:           a.baseline.LatencyP99MS,
		CurrentValue:            tail,
		AffectedRequestsPerHour: affected,
	}, true
}

// remediations turns findings into steps to take, ranked by the traffic each
// stands to recover.
//
// The figure a step is ranked by is its finding's measured cost. It is not a
// prediction that the step will work: the platform can measure how much
// traffic a problem affects and cannot measure how good a fix will be, so it
// reports the first and says nothing about the second. Where one finding
// produces two steps they keep the order they were generated in, which puts
// removing a cause ahead of going looking for one.
func (a analysis) remediations(findings []Finding) []Remediation {
	steps := []Remediation{}
	for _, f := range findings {
		for _, step := range a.stepsFor(f) {
			step.Finding = f.Code
			step.AffectedRequestsPerHour = f.AffectedRequestsPerHour
			steps = append(steps, step)
		}
	}

	slices.SortStableFunc(steps, func(x, y Remediation) int {
		return compareDesc(x.AffectedRequestsPerHour, y.AffectedRequestsPerHour)
	})
	return steps
}

func (a analysis) stepsFor(f Finding) []Remediation {
	switch f.Code {
	case CodeLatencyRegression:
		if a.releaseChanged() {
			return []Remediation{a.rollBack("the slowdown arrived with the release")}
		}
		return []Remediation{{
			Action:  ActionProfileRelease,
			Summary: "Profile the request path — no release explains this",
			Detail: fmt.Sprintf(
				"the same deployment (%s) served both windows, so nothing in this environment's code changed while p95 rose. What did change is around it: request volume, a dependency's latency, or a query whose table has grown past its index.",
				shortSHA(a.currentRelease.commitSHA)),
		}}

	case CodeReliabilityRegression:
		if a.releaseChanged() {
			return []Remediation{a.rollBack("the failures arrived with the release"), a.inspectFailures()}
		}
		return []Remediation{a.inspectFailures()}

	case CodeElevatedFailureRate:
		return []Remediation{a.inspectFailures()}

	case CodeLatencyTailSpread:
		return []Remediation{{
			Action:  ActionInvestigateTail,
			Summary: "Find the work only the slow requests do",
			Detail:  "a wide gap between the median and the tail is one code path behaving differently, not the service being slow: a cold start, a cache miss, or a query without an index. Compare a fast request with a slow one rather than looking at the average of both.",
		}}
	}
	return nil
}

func (a analysis) rollBack(because string) Remediation {
	return Remediation{
		Action:  ActionRollBack,
		Summary: "Roll back to " + shortSHA(a.baselineRelease.commitSHA),
		Detail: fmt.Sprintf(
			"%s: the baseline window was served by %s and this one by %s. Deploying %s into this environment again restores the code that produced the baseline numbers.",
			because,
			shortSHA(a.baselineRelease.commitSHA), shortSHA(a.currentRelease.commitSHA),
			shortSHA(a.baselineRelease.commitSHA)),
	}
}

func (a analysis) inspectFailures() Remediation {
	return Remediation{
		Action:  ActionInspectFailures,
		Summary: "Read the workload's own output for these minutes",
		Detail: fmt.Sprintf(
			"%d requests returned 5xx between %s and %s. The proxy records what a request did, not what the application said while doing it, so why they failed has to come from the workload's own logs for that interval.",
			a.current.Failures,
			a.windowFrom.UTC().Format("15:04Z"), a.windowTo.UTC().Format("15:04Z")),
	}
}

func verdictOf(findings []Finding, in Input) (Verdict, string) {
	if len(findings) == 0 {
		return VerdictHealthy, fmt.Sprintf(
			"no regression against the preceding %s", in.Baseline)
	}
	for _, f := range findings {
		if f.Severity == SeverityCritical {
			return VerdictFailing, findings[0].Summary
		}
	}
	return VerdictDegraded, findings[0].Summary
}

func failureRate(s domain.MetricSummary) float64 { return 1 - s.Availability }

func severityRank(s Severity) int {
	if s == SeverityCritical {
		return 1
	}
	return 0
}

// compareDesc orders two measurements largest first.
func compareDesc(x, y float64) int {
	switch {
	case x > y:
		return -1
	case x < y:
		return 1
	}
	return 0
}

// millis renders a latency the way someone reads it out loud.
func millis(ms float64) string {
	if ms < 1000 {
		return fmt.Sprintf("%.0fms", ms)
	}
	return fmt.Sprintf("%.1fs", ms/1000)
}

func percent(fraction float64) string {
	return fmt.Sprintf("%.1f%%", fraction*100)
}

// count rounds a rate to something a person would say, since the fractional
// part of "about 41.7 requests an hour" is noise.
func count(rate float64) string {
	return fmt.Sprintf("%.0f", rate)
}

// shortSHA trims a commit to the length a human reads without scanning. An
// empty SHA means no deployment served that window, which reads better as a
// dash than as an empty gap in a sentence.
func shortSHA(sha string) string {
	if sha == "" {
		return "—"
	}
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
