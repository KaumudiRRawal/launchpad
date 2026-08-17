import type { DeploymentStatus } from '../api/types'
import { isTerminal } from '../api/types'

/**
 * StatusBadge shows a deployment's state. The status is also written out as
 * text rather than conveyed by colour alone, so it survives being read by
 * someone who cannot distinguish the colours.
 */
export function StatusBadge({ status }: { status: DeploymentStatus }) {
  return (
    <span className={`badge badge-${status}`} data-status={status}>
      {!isTerminal(status) && <span className="badge-pulse" aria-hidden="true" />}
      {status}
    </span>
  )
}
