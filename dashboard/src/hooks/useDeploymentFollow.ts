import { useEffect, useState } from 'react'

import type { Client } from '../api/client'
import type { Deployment, DeploymentLog } from '../api/types'
import { isTerminal } from '../api/types'

/** pollIntervalMs is how often a running deployment is re-read. */
export const pollIntervalMs = 1000

/**
 * trailingPolls is how many times output is fetched after the status settles.
 * The engine records a deployment as live and only then flushes its final log
 * lines, so stopping the moment the status becomes terminal would reliably drop
 * the last few lines — including the one naming the URL.
 */
export const trailingPolls = 3

export interface Follow {
  deployment: Deployment | undefined
  logs: DeploymentLog[]
  error: unknown
  /** following is true while the dashboard is still polling. */
  following: boolean
}

/** Follower is the slice of the client a log follower needs. */
export type Follower = Pick<Client, 'getDeployment' | 'deploymentLogs'>

/**
 * mergeLogs appends lines that are not already present, keyed by sequence
 * number. Returning the existing array unchanged when nothing is new keeps an
 * idle poll from re-rendering the log view, and makes an overlapping or
 * repeated fetch harmless rather than a source of duplicate lines.
 */
export function mergeLogs(existing: DeploymentLog[], incoming: DeploymentLog[]): DeploymentLog[] {
  if (incoming.length === 0) {
    return existing
  }

  const seen = new Set(existing.map((line) => line.seq))
  const added = incoming.filter((line) => !seen.has(line.seq))
  if (added.length === 0) {
    return existing
  }

  return [...existing, ...added].sort((a, b) => a.seq - b.seq)
}

/**
 * useDeploymentFollow watches one deployment: its status and its output, until
 * it reaches a terminal state.
 *
 * The control plane exposes logs as pages with a cursor rather than as a
 * stream, so this polls. That is also what survives a dropped connection: a
 * follower that reconnects asks for everything after the last sequence number
 * it saw and misses nothing, where a broken stream would have to be replayed
 * from the start.
 */
export function useDeploymentFollow(client: Follower, deploymentID: string): Follow {
  const [deployment, setDeployment] = useState<Deployment | undefined>(undefined)
  const [logs, setLogs] = useState<DeploymentLog[]>([])
  const [error, setError] = useState<unknown>(undefined)
  const [following, setFollowing] = useState(true)

  useEffect(() => {
    const controller = new AbortController()

    setDeployment(undefined)
    setLogs([])
    setError(undefined)
    setFollowing(true)

    void (async () => {
      let cursor = 0
      let remaining = trailingPolls

      while (!controller.signal.aborted) {
        try {
          const current = await client.getDeployment(deploymentID, controller.signal)
          const page = await client.deploymentLogs(deploymentID, cursor, controller.signal)
          if (controller.signal.aborted) {
            return
          }

          cursor = page.next_after
          setDeployment(current)
          setLogs((prev) => mergeLogs(prev, page.logs))

          if (isTerminal(current.status) && remaining-- <= 0) {
            break
          }
        } catch (err) {
          if (controller.signal.aborted) {
            return
          }
          setError(err)
          break
        }

        await sleep(pollIntervalMs, controller.signal)
      }

      if (!controller.signal.aborted) {
        setFollowing(false)
      }
    })()

    return () => controller.abort()
  }, [client, deploymentID])

  return { deployment, logs, error, following }
}

/** sleep resolves early when the signal aborts, so unmounting does not wait. */
function sleep(ms: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve) => {
    const timer = setTimeout(resolve, ms)
    signal.addEventListener(
      'abort',
      () => {
        clearTimeout(timer)
        resolve()
      },
      { once: true },
    )
  })
}
