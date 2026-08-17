import { act, renderHook, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import type { DeploymentStatus, LogPage } from '../api/types'
import { deployment, logLine } from '../test/fixtures'
import { mergeLogs, pollIntervalMs, trailingPolls, useDeploymentFollow } from './useDeploymentFollow'

describe('mergeLogs', () => {
  const first = logLine(1, 'fetching')
  const second = logLine(2, 'building')
  const third = logLine(3, 'live')

  it('appends new lines in sequence order', () => {
    expect(mergeLogs([first], [second, third]).map((l) => l.seq)).toEqual([1, 2, 3])
  })

  it('sorts a page that arrives out of order', () => {
    expect(mergeLogs([], [third, first, second]).map((l) => l.seq)).toEqual([1, 2, 3])
  })

  it('ignores a line already held, so a repeated fetch cannot duplicate it', () => {
    expect(mergeLogs([first, second], [second, third]).map((l) => l.seq)).toEqual([1, 2, 3])
  })

  it('returns the same array when nothing is new, so an idle poll does not re-render', () => {
    const existing = [first, second]
    expect(mergeLogs(existing, [])).toBe(existing)
    expect(mergeLogs(existing, [first])).toBe(existing)
  })
})

/**
 * follower scripts a control plane that reveals one more log line on each poll
 * and reports `building` until `liveAfter` polls have happened.
 */
function follower(liveAfter: number, lines = 3) {
  const emitted = Array.from({ length: lines }, (_, i) => logLine(i + 1, `step ${i + 1}`))
  let polls = 0

  return {
    get polls() {
      return polls
    },
    getDeployment: () => {
      const status: DeploymentStatus = polls < liveAfter ? 'building' : 'live'
      return Promise.resolve(deployment({ status, ...(status === 'live' ? { url: 'http://x' } : {}) }))
    },
    deploymentLogs: (_id: string, after: number): Promise<LogPage> => {
      const visible = emitted.slice(0, Math.min(polls + 1, emitted.length))
      polls++
      const page = visible.filter((line) => line.seq > after)
      return Promise.resolve({
        logs: page,
        next_after: page.at(-1)?.seq ?? after,
      })
    },
  }
}

describe('useDeploymentFollow', () => {
  afterEach(() => {
    vi.useRealTimers()
  })

  it('accumulates output and stops once the status has settled', async () => {
    vi.useFakeTimers()
    const client = follower(2)

    const { result } = renderHook(() => useDeploymentFollow(client, 'dep-1'))

    // One poll per interval, plus the trailing polls that catch the log lines
    // the engine flushes after marking the deployment live.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(pollIntervalMs * (trailingPolls + 4))
    })

    expect(result.current.following).toBe(false)
    expect(result.current.deployment?.status).toBe('live')
    expect(result.current.logs.map((line) => line.message)).toEqual([
      'step 1',
      'step 2',
      'step 3',
    ])
  })

  it('keeps polling while the deployment is still building', async () => {
    vi.useFakeTimers()
    const client = follower(100)

    const { result } = renderHook(() => useDeploymentFollow(client, 'dep-1'))

    await act(async () => {
      await vi.advanceTimersByTimeAsync(pollIntervalMs * 3)
    })

    expect(result.current.following).toBe(true)
    expect(result.current.deployment?.status).toBe('building')
  })

  it('stops polling when unmounted mid-build', async () => {
    vi.useFakeTimers()
    const client = follower(100)

    const { unmount } = renderHook(() => useDeploymentFollow(client, 'dep-1'))
    await act(async () => {
      await vi.advanceTimersByTimeAsync(pollIntervalMs * 2)
    })

    const pollsAtUnmount = client.polls
    unmount()

    await act(async () => {
      await vi.advanceTimersByTimeAsync(pollIntervalMs * 5)
    })

    expect(client.polls).toBe(pollsAtUnmount)
  })

  it('surfaces a failure instead of polling a broken endpoint forever', async () => {
    const client = {
      getDeployment: () => Promise.reject(new Error('control plane is down')),
      deploymentLogs: () => Promise.resolve({ logs: [], next_after: 0 }),
    }

    const { result } = renderHook(() => useDeploymentFollow(client, 'dep-1'))

    await waitFor(() => expect(result.current.following).toBe(false))
    expect((result.current.error as Error).message).toBe('control plane is down')
  })
})
