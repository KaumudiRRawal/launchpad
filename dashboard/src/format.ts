/**
 * A CSI escape sequence: the escape byte, a bracket, parameters, then a
 * letter. Anchoring on the escape byte matters — without it the pattern would
 * also eat ordinary bracketed text such as "[main]" out of a build log.
 */
const ansiPattern = /\x1b\[[0-9;]*[A-Za-z]/g

/**
 * stripAnsi removes terminal colour codes from a log line.
 *
 * Build tools colour their output whether or not anything is watching: npm and
 * Docker both emit escape sequences during a build. The escape byte itself is
 * invisible in HTML, so leaving them in does not colour the line — it prints
 * the leftover "[91m" in front of it.
 */
export function stripAnsi(line: string): string {
  return line.replace(ansiPattern, '')
}

/** shortSHA trims a commit to the length a human reads without scanning. */
export function shortSHA(sha: string, length = 12): string {
  return sha.slice(0, length)
}

/** formatClock renders a log line's timestamp as local HH:MM:SS. */
export function formatClock(iso: string): string {
  const at = new Date(iso)
  if (Number.isNaN(at.getTime())) {
    return ''
  }
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${pad(at.getHours())}:${pad(at.getMinutes())}:${pad(at.getSeconds())}`
}

/**
 * relativeTime describes how long ago something happened. `now` is a parameter
 * rather than read from the clock so the result is a function of its inputs and
 * can be tested without freezing time.
 */
export function relativeTime(iso: string, now: Date): string {
  const at = new Date(iso)
  if (Number.isNaN(at.getTime())) {
    return ''
  }

  // Anything recent, and anything apparently in the future — the browser clock
  // and the server clock will not agree to the second — reads as "just now"
  // rather than as a negative age.
  const seconds = Math.round((now.getTime() - at.getTime()) / 1000)
  if (seconds < 45) {
    return 'just now'
  }

  const units: [limit: number, per: number, suffix: string][] = [
    [3600, 60, 'm'],
    [86400, 3600, 'h'],
    [2592000, 86400, 'd'],
  ]
  for (const [limit, per, suffix] of units) {
    if (seconds < limit) {
      return `${Math.round(seconds / per)}${suffix} ago`
    }
  }
  return `${Math.round(seconds / 2592000)}mo ago`
}

/** formatDuration renders an elapsed millisecond count compactly. */
export function formatDuration(ms: number): string {
  if (!Number.isFinite(ms) || ms < 0) {
    return ''
  }
  if (ms < 1000) {
    return `${Math.round(ms)}ms`
  }

  const totalSeconds = Math.round(ms / 1000)
  const minutes = Math.floor(totalSeconds / 60)
  const seconds = totalSeconds % 60
  if (minutes === 0) {
    return `${seconds}s`
  }
  return `${minutes}m ${seconds}s`
}

/**
 * elapsed reports how long a deployment took, or how long it has been running.
 * A deployment that has not started yet has no elapsed time to show.
 */
export function elapsed(
  deployment: { queued_at: string; started_at?: string; completed_at?: string },
  now: Date,
): string {
  const from = new Date(deployment.started_at ?? deployment.queued_at)
  if (Number.isNaN(from.getTime())) {
    return ''
  }
  const to = deployment.completed_at ? new Date(deployment.completed_at) : now
  if (Number.isNaN(to.getTime())) {
    return ''
  }
  return formatDuration(to.getTime() - from.getTime())
}

/**
 * formatLatency renders a millisecond latency the way someone says it out
 * loud. Distinct from formatDuration, which rounds to whole seconds: a p95 of
 * 1.4 seconds must not be reported as "1s".
 *
 * Exactly zero means nothing was measured — the collector records a fast
 * handler as a fraction of a millisecond, never as free — so it reads as a dash
 * rather than as an impossibly quick response.
 */
export function formatLatency(ms: number): string {
  if (!Number.isFinite(ms) || ms < 0) {
    return ''
  }
  if (ms === 0) {
    return '—'
  }
  if (ms < 1) {
    return '<1ms'
  }
  if (ms < 1000) {
    return `${Math.round(ms)}ms`
  }
  return `${(ms / 1000).toFixed(1)}s`
}

/**
 * formatPercent renders a 0–1 fraction. One decimal place, because the
 * difference between 99.9% and 100% availability is the whole point of showing
 * it and rounding would hide it.
 */
export function formatPercent(fraction: number): string {
  if (!Number.isFinite(fraction)) {
    return ''
  }
  return `${(fraction * 100).toFixed(1)}%`
}

/** formatPerHour rounds a rate to whole requests; the fraction is noise. */
export function formatPerHour(rate: number): string {
  if (!Number.isFinite(rate) || rate < 0) {
    return ''
  }
  return `${Math.round(rate).toLocaleString()}/h`
}
