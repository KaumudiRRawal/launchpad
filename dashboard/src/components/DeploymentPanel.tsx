import { useEffect, useRef, useState } from 'react'

import type { Follower } from '../hooks/useDeploymentFollow'
import { useDeploymentFollow } from '../hooks/useDeploymentFollow'
import { elapsed, formatClock, shortSHA, stripAnsi } from '../format'
import { ErrorBanner } from './ErrorBanner'
import { StatusBadge } from './StatusBadge'

interface DeploymentPanelProps {
  client: Follower
  deploymentID: string
  serviceName: string | undefined
  environmentName: string | undefined
  /** Called once, when the deployment reaches a state it will not leave. */
  onFinished: () => void
}

/** stickyThresholdPx is how close to the bottom still counts as "at the bottom". */
const stickyThresholdPx = 24

export function DeploymentPanel({
  client,
  deploymentID,
  serviceName,
  environmentName,
  onFinished,
}: DeploymentPanelProps) {
  const { deployment, logs, error, following } = useDeploymentFollow(client, deploymentID)
  const pane = useRef<HTMLDivElement>(null)
  // Whether the reader is watching the tail. Kept in a ref because scrolling
  // must not re-render the log view.
  const pinnedToBottom = useRef(true)

  // A running build is timed live, so the elapsed figure has to advance on its
  // own rather than only when a new log line arrives.
  const [now, setNow] = useState(() => new Date())
  useEffect(() => {
    if (!following) {
      return
    }
    const timer = setInterval(() => setNow(new Date()), 1000)
    return () => clearInterval(timer)
  }, [following])

  useEffect(() => {
    if (!following) {
      onFinished()
    }
  }, [following, onFinished])

  useEffect(() => {
    const el = pane.current
    // Only follow the tail if the reader has not scrolled away from it.
    // Yanking the view back to the bottom while someone is reading an error
    // higher up is worse than not auto-scrolling at all.
    if (el && pinnedToBottom.current) {
      el.scrollTop = el.scrollHeight
    }
  }, [logs])

  function onScroll() {
    const el = pane.current
    if (el) {
      pinnedToBottom.current =
        el.scrollHeight - el.scrollTop - el.clientHeight < stickyThresholdPx
    }
  }

  return (
    <section className="panel">
      <header className="panel-header">
        <h2>Deployment</h2>
        {deployment && <StatusBadge status={deployment.status} />}
      </header>

      <ErrorBanner error={error} />

      {deployment && (
        <>
          <dl className="detail-grid">
            <dt>Target</dt>
            <dd>
              {serviceName ?? deployment.service_id} →{' '}
              {environmentName ?? deployment.environment_id}
            </dd>

            <dt>Commit</dt>
            <dd>
              <code>{shortSHA(deployment.commit_sha)}</code>
            </dd>

            <dt>Elapsed</dt>
            <dd>{elapsed(deployment, now)}</dd>

            {deployment.image_ref !== undefined && (
              <>
                <dt>Image</dt>
                <dd>
                  <code>{deployment.image_ref}</code>
                </dd>
              </>
            )}

            {deployment.url !== undefined && (
              <>
                <dt>URL</dt>
                <dd>
                  <a href={deployment.url} target="_blank" rel="noreferrer">
                    {deployment.url}
                  </a>
                </dd>
              </>
            )}
          </dl>

          {deployment.error_message !== undefined && (
            <div className="banner banner-error">
              <p>{deployment.error_message}</p>
            </div>
          )}
        </>
      )}

      <div className="log-pane" ref={pane} onScroll={onScroll} aria-label="Build log">
        {logs.map((line) => (
          <div key={line.seq} className={`log-line log-${line.stream}`}>
            <span className="log-time">{formatClock(line.logged_at)}</span>
            <span className="log-message">{stripAnsi(line.message)}</span>
          </div>
        ))}
        {logs.length === 0 && (
          <p className="log-empty">{following ? 'waiting for output…' : 'no output recorded'}</p>
        )}
      </div>

      <footer className="panel-footer">
        {following ? 'following…' : 'not following — this deployment has finished'}
      </footer>
    </section>
  )
}
