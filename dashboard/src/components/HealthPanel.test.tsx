import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'

import type { Client } from '../api/client'
import { ApiError } from '../api/client'
import {
  analysisReport,
  environment,
  environmentMetrics,
  finding,
  remediation,
} from '../test/fixtures'
import { HealthPanel } from './HealthPanel'

type MeasurementClient = Pick<Client, 'environmentMetrics' | 'environmentAnalysis'>

function stubClient(over: Partial<MeasurementClient> = {}): MeasurementClient {
  return {
    environmentMetrics: () => Promise.resolve(environmentMetrics()),
    environmentAnalysis: () => Promise.resolve(analysisReport()),
    ...over,
  }
}

describe('HealthPanel', () => {
  it('shows the window the API measured', async () => {
    render(<HealthPanel client={stubClient()} environments={[environment()]} />)

    expect(await screen.findByText('healthy')).toBeDefined()
    expect(screen.getByText('1,200')).toBeDefined()
    expect(screen.getByText('100.0%')).toBeDefined()
    // Each percentile, so a wide tail beside a healthy median is visible
    // rather than averaged away.
    expect(screen.getByText('15ms')).toBeDefined()
    expect(screen.getByText('42ms')).toBeDefined()
    expect(screen.getByText('96ms')).toBeDefined()
    // One row per measured minute.
    expect(screen.getAllByRole('row')).toHaveLength(3)
  })

  it('ranks the remediation steps in the order the API returned them', async () => {
    const report = analysisReport({
      verdict: 'failing',
      detail: '95th percentile latency is 4.6× the baseline',
      findings: [
        finding(),
        finding({
          code: 'latency_tail_spread',
          severity: 'warning',
          summary: 'the slowest requests take 40× the median',
          metric: 'latency_p99_ms',
          affected_requests_per_hour: 4,
        }),
      ],
      remediations: [
        remediation(),
        remediation({
          action: 'investigate_the_slow_path',
          summary: 'Find the work only the slow requests do',
          finding: 'latency_tail_spread',
          affected_requests_per_hour: 4,
        }),
      ],
    })

    render(
      <HealthPanel
        client={stubClient({ environmentAnalysis: () => Promise.resolve(report) })}
        environments={[environment()]}
      />,
    )

    expect(await screen.findByText('failing')).toBeDefined()

    // The server ranked these by measured traffic. The dashboard must not
    // re-sort them, or it substitutes its own opinion for a measurement.
    const steps = screen.getAllByRole('listitem')
    expect(steps[0]?.textContent).toContain('Roll back to')
    expect(steps[0]?.textContent).toContain('1,140/h')
    expect(steps[1]?.textContent).toContain('Find the work only the slow requests do')
    expect(steps[1]?.textContent).toContain('4/h')

    // The evidence travels with the step: a reader should not have to trust
    // the ranking to check it.
    expect(steps[0]?.textContent).toContain('latency_p95_ms')
    expect(steps[0]?.textContent).toContain('critical')
  })

  it('says why there is nothing to show rather than showing zeroes', async () => {
    render(
      <HealthPanel
        client={stubClient({
          environmentMetrics: () => Promise.resolve(environmentMetrics({ series: [] })),
          environmentAnalysis: () =>
            Promise.resolve(
              analysisReport({
                verdict: 'insufficient_data',
                detail: '0 requests in the last 15m0s; at least 20 are needed',
              }),
            ),
        })}
        environments={[environment()]}
      />,
    )

    expect(await screen.findByText('insufficient data')).toBeDefined()
    expect(screen.getByText(/at least 20 are needed/)).toBeDefined()
    expect(screen.getByText(/No requests in the last/)).toBeDefined()
  })

  it('needs an environment before it can measure one', () => {
    const environmentMetrics = vi.fn()
    render(<HealthPanel client={stubClient({ environmentMetrics })} environments={[]} />)

    expect(screen.getByText(/Nothing is measured yet/)).toBeDefined()
    expect(environmentMetrics).not.toHaveBeenCalled()
  })

  it('measures the environment chosen from the select', async () => {
    const asked: string[] = []
    const client = stubClient({
      environmentMetrics: (environmentID) => {
        asked.push(environmentID)
        return Promise.resolve(environmentMetrics())
      },
    })

    render(
      <HealthPanel
        client={client}
        environments={[
          environment(),
          environment({ id: 'env-2', name: 'pr-42', kind: 'preview', subdomain: 'pr-42-demo' }),
        ]}
      />,
    )

    await screen.findByText('healthy')
    expect(asked).toEqual(['env-1'])

    fireEvent.change(screen.getByLabelText('Environment'), { target: { value: 'env-2' } })
    await waitFor(() => expect(asked).toEqual(['env-1', 'env-2']))
  })

  it('reports a failure to read the measurements', async () => {
    render(
      <HealthPanel
        client={stubClient({
          environmentMetrics: () =>
            Promise.reject(new ApiError(500, 'internal_error', 'an unexpected error occurred')),
        })}
        environments={[environment()]}
      />,
    )

    expect(await screen.findByText(/an unexpected error occurred/)).toBeDefined()
  })
})
