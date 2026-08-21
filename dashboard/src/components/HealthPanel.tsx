import { useCallback, useState } from 'react'

import type { Client } from '../api/client'
import type { AnalysisReport, Environment, Finding, Remediation, Verdict } from '../api/types'
import { formatClock, formatLatency, formatPerHour, formatPercent } from '../format'
import { useResource } from '../hooks/useResource'
import { ErrorBanner } from './ErrorBanner'

/**
 * The window the chart covers. The analysis endpoint is deliberately not given
 * one: the report is a comparison, and a dashboard choosing the period it
 * compares over would be choosing how sensitive the answer is without saying
 * so anywhere a reader would see it.
 */
const metricsWindow = '1h'

/**
 * verdictTone maps a verdict onto the badge colours the deployment states
 * already use, so the same colour means the same thing everywhere in the UI.
 * Keyed by every verdict rather than defaulting, so a verdict added to the
 * specification fails to compile here instead of rendering unstyled.
 */
const verdictTone: Record<Verdict, string> = {
  insufficient_data: 'badge-queued',
  healthy: 'badge-live',
  degraded: 'badge-building',
  failing: 'badge-failed',
}

interface HealthPanelProps {
  client: Pick<Client, 'environmentMetrics' | 'environmentAnalysis'>
  environments: Environment[]
}

/**
 * HealthPanel shows what one environment's deployed application actually did:
 * how fast it answered, how often it failed, and what the control plane thinks
 * is worth doing about it.
 */
export function HealthPanel({ client, environments }: HealthPanelProps) {
  const [chosen, setChosen] = useState('')

  // Falling back to the first entry means the common case — one environment —
  // needs no interaction with the select at all.
  const environmentID = chosen || environments[0]?.id || ''

  // The loading lives in a child rather than behind a condition here, because
  // a hook cannot be skipped: asking for measurements before there is an
  // environment to measure would request an empty ID from the API.
  if (environmentID === '') {
    return (
      <section className="panel">
        <header className="panel-header">
          <h2>Health</h2>
        </header>
        <p className="muted">
          Nothing is measured yet. An environment is measured from the first request that reaches
          it through the platform proxy.
        </p>
      </section>
    )
  }

  return (
    <Measurements
      client={client}
      environments={environments}
      environmentID={environmentID}
      onChoose={setChosen}
    />
  )
}

function Measurements({
  client,
  environments,
  environmentID,
  onChoose,
}: HealthPanelProps & { environmentID: string; onChoose: (id: string) => void }) {
  const loadMetrics = useCallback(
    (signal: AbortSignal) => client.environmentMetrics(environmentID, metricsWindow, signal),
    [client, environmentID],
  )
  const loadAnalysis = useCallback(
    (signal: AbortSignal) => client.environmentAnalysis(environmentID, signal),
    [client, environmentID],
  )

  const metrics = useResource(loadMetrics)
  const analysis = useResource(loadAnalysis)

  const summary = metrics.data?.window.summary
  const series = metrics.data?.series ?? []
  const report = analysis.data

  return (
    <section className="panel">
      <header className="panel-header">
        <h2>Health</h2>
        <div className="panel-actions">
          {environments.length > 1 && (
            <select
              aria-label="Environment"
              value={environmentID}
              onChange={(e) => onChoose(e.target.value)}
            >
              {environments.map((environment) => (
                <option key={environment.id} value={environment.id}>
                  {environment.name} ({environment.kind})
                </option>
              ))}
            </select>
          )}
          <button
            type="button"
            className="ghost"
            onClick={() => {
              metrics.reload()
              analysis.reload()
            }}
          >
            Refresh
          </button>
        </div>
      </header>

      <ErrorBanner error={metrics.error} />
      <ErrorBanner error={analysis.error} />

      {report && (
        <p className="verdict">
          <span className={`badge ${verdictTone[report.verdict]}`}>
            {report.verdict.replace(/_/g, ' ')}
          </span>{' '}
          <span className="muted">{report.detail}</span>
        </p>
      )}

      {summary && (
        <dl className="stat-row">
          <Stat label="Requests" value={summary.requests.toLocaleString()} note={`last ${metricsWindow}`} />
          <Stat
            label="Availability"
            value={formatPercent(summary.availability)}
            note={`${summary.failures.toLocaleString()} failed`}
          />
          <Stat label="p50" value={formatLatency(summary.latency_p50_ms)} />
          <Stat label="p95" value={formatLatency(summary.latency_p95_ms)} />
          <Stat label="p99" value={formatLatency(summary.latency_p99_ms)} note={`max ${formatLatency(summary.latency_max_ms)}`} />
        </dl>
      )}

      {report && report.remediations.length > 0 && (
        <Remediations report={report} />
      )}

      {series.length > 0 && (
        <table className="minutes">
          <caption className="muted">
            The last {series.length} minute{series.length === 1 ? '' : 's'} of traffic, newest
            first. A minute is written once it has closed, so the current one appears a moment
            late.
          </caption>
          <thead>
            <tr>
              <th scope="col">Minute</th>
              <th scope="col">Requests</th>
              <th scope="col">Failed</th>
              <th scope="col">p95</th>
            </tr>
          </thead>
          <tbody>
            {[...series].reverse().map((point) => (
              <tr key={point.bucket}>
                <td>{formatClock(point.bucket)}</td>
                <td>{point.requests.toLocaleString()}</td>
                <td className={point.failures > 0 ? 'failed' : undefined}>{point.failures}</td>
                <td>{formatLatency(point.latency_p95_ms)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}

      {series.length === 0 && !metrics.loading && (
        <p className="muted">
          No requests in the last {metricsWindow}. Latency and reliability are measured from real
          traffic through the proxy, so an environment nobody has called has nothing to show.
        </p>
      )}
    </section>
  )
}

function Stat({ label, value, note }: { label: string; value: string; note?: string }) {
  return (
    <div className="stat">
      <dt>{label}</dt>
      <dd>
        {value}
        {note !== undefined && <span className="stat-note">{note}</span>}
      </dd>
    </div>
  )
}

/**
 * Remediations lists the steps in the order the control plane ranked them,
 * with the evidence for each underneath. The order is the server's: it is
 * computed from measured traffic, and re-sorting it here would quietly
 * substitute the dashboard's opinion for a measurement.
 */
function Remediations({ report }: { report: AnalysisReport }) {
  return (
    <div className="remediations">
      <h3>What to do first</h3>
      <ol className="list">
        {report.remediations.map((step, index) => (
          <li key={`${step.action}-${index}`} className="list-row">
            <div>
              <span className="list-title">{step.summary}</span>
              <p className="list-meta">{step.detail}</p>
              <p className="list-meta">
                <Evidence finding={findingFor(report, step)} />
              </p>
            </div>
            {/* The figure the list is ordered by, shown so the order is not
                something the reader has to take on trust. */}
            <span className="impact" title="requests an hour affected by the finding this step answers">
              {formatPerHour(step.affected_requests_per_hour)}
            </span>
          </li>
        ))}
      </ol>
    </div>
  )
}

function Evidence({ finding }: { finding: Finding | undefined }) {
  if (finding === undefined) {
    return null
  }
  return (
    <>
      <span className={`severity severity-${finding.severity}`}>{finding.severity}</span> ·{' '}
      {finding.summary} · <code>{finding.metric}</code> {round(finding.baseline_value)} →{' '}
      {round(finding.current_value)}
    </>
  )
}

/** findingFor is the evidence behind one step. */
function findingFor(report: AnalysisReport, step: Remediation): Finding | undefined {
  return report.findings.find((finding) => finding.code === step.finding)
}

/**
 * round keeps a raw metric value readable without pretending to know its unit:
 * these are latencies in one finding and a 0–1 availability in another, so the
 * only safe formatting is fewer digits.
 */
function round(value: number): string {
  return Math.abs(value) >= 10 ? value.toFixed(0) : value.toFixed(3).replace(/0+$/, '')
}
