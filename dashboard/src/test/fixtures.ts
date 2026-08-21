import type {
  AnalysisReport,
  Deployment,
  DeploymentLog,
  Environment,
  EnvironmentMetrics,
  Finding,
  MetricSummary,
  Project,
  Remediation,
  Service,
} from '../api/types'

const at = '2026-01-01T12:00:00Z'

/** Fixture builders: every field is set, so a test only states what it cares about. */

export function project(over: Partial<Project> = {}): Project {
  return {
    id: 'proj-1',
    account_id: 'acct-1',
    slug: 'demo',
    name: 'Demo App',
    repo_url: 'https://github.com/example/demo',
    default_branch: 'main',
    created_at: at,
    updated_at: at,
    ...over,
  }
}

export function service(over: Partial<Service> = {}): Service {
  return {
    id: 'svc-1',
    project_id: 'proj-1',
    name: 'api',
    source_path: '.',
    port: 8080,
    created_at: at,
    updated_at: at,
    ...over,
  }
}

export function environment(over: Partial<Environment> = {}): Environment {
  return {
    id: 'env-1',
    project_id: 'proj-1',
    kind: 'production',
    name: 'prod',
    subdomain: 'prod-demo',
    url: 'http://prod-demo.localhost:8081',
    created_at: at,
    updated_at: at,
    ...over,
  }
}

export function deployment(over: Partial<Deployment> = {}): Deployment {
  return {
    id: 'dep-1',
    service_id: 'svc-1',
    environment_id: 'env-1',
    commit_sha: 'a'.repeat(40),
    status: 'queued',
    queued_at: at,
    ...over,
  }
}

export function logLine(seq: number, message: string, stream = 'stdout'): DeploymentLog {
  return { seq, stream, message, logged_at: at }
}

export function metricSummary(over: Partial<MetricSummary> = {}): MetricSummary {
  return {
    requests: 1200,
    failures: 0,
    availability: 1,
    latency_avg_ms: 18,
    latency_p50_ms: 15,
    latency_p95_ms: 42,
    latency_p99_ms: 96,
    latency_max_ms: 130,
    ...over,
  }
}

export function environmentMetrics(over: Partial<EnvironmentMetrics> = {}): EnvironmentMetrics {
  return {
    environment_id: 'env-1',
    window: { from: '2026-01-01T11:00:00Z', to: at, summary: metricSummary() },
    series: [
      { bucket: '2026-01-01T11:58:00Z', requests: 20, failures: 0, latency_p95_ms: 40 },
      { bucket: '2026-01-01T11:59:00Z', requests: 22, failures: 1, latency_p95_ms: 44 },
    ],
    ...over,
  }
}

export function finding(over: Partial<Finding> = {}): Finding {
  return {
    code: 'latency_regression',
    severity: 'critical',
    summary: '95th percentile latency is 4.6\u00d7 the baseline',
    detail: 'p95 rose from 42ms to 195ms.',
    metric: 'latency_p95_ms',
    baseline_value: 42,
    current_value: 195,
    affected_requests_per_hour: 1140,
    ...over,
  }
}

export function remediation(over: Partial<Remediation> = {}): Remediation {
  return {
    action: 'roll_back_deployment',
    summary: 'Roll back to 111111111111',
    detail: 'the slowdown arrived with the release.',
    finding: 'latency_regression',
    affected_requests_per_hour: 1140,
    ...over,
  }
}

export function analysisReport(over: Partial<AnalysisReport> = {}): AnalysisReport {
  return {
    environment_id: 'env-1',
    generated_at: at,
    verdict: 'healthy',
    detail: 'no regression against the preceding 1h0m0s',
    baseline: {
      from: '2026-01-01T10:45:00Z',
      to: '2026-01-01T11:45:00Z',
      summary: metricSummary(),
    },
    current: { from: '2026-01-01T11:45:00Z', to: at, summary: metricSummary() },
    findings: [],
    remediations: [],
    ...over,
  }
}
