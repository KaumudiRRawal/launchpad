import { useCallback, useEffect, useState } from 'react'

export interface Resource<T> {
  data?: T
  error?: unknown
  loading: boolean
  reload: () => void
}

/**
 * useResource loads one thing from the API and re-loads it on demand.
 *
 * `load` must be memoised by the caller — a useCallback over the client and
 * whatever IDs it needs. Taking a stable function rather than a dependency
 * array means TypeScript, not a lint rule, is what catches a missing
 * dependency.
 */
export function useResource<T>(load: (signal: AbortSignal) => Promise<T>): Resource<T> {
  const [state, setState] = useState<Omit<Resource<T>, 'reload'>>({ loading: true })
  const [nonce, setNonce] = useState(0)

  useEffect(() => {
    const controller = new AbortController()

    // The previous value is deliberately kept while reloading, so refreshing a
    // list after a create does not blank the page for a moment.
    setState((prev) => ({ ...prev, loading: true }))

    load(controller.signal).then(
      (data) => {
        if (!controller.signal.aborted) {
          setState({ data, loading: false })
        }
      },
      (error: unknown) => {
        if (!controller.signal.aborted) {
          setState({ error, loading: false })
        }
      },
    )

    return () => controller.abort()
  }, [load, nonce])

  const reload = useCallback(() => setNonce((n) => n + 1), [])

  return { ...state, reload }
}
