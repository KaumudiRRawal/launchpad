import { useCallback, useState } from 'react'

import { fieldErrorOf } from '../api/client'

export interface Action {
  busy: boolean
  error?: unknown
  /** run performs a mutation, capturing its failure instead of throwing. */
  run: (fn: () => Promise<unknown>) => Promise<void>
  /** fieldError is the API's complaint about one form field, if it made one. */
  fieldError: (name: string) => string | undefined
  reset: () => void
}

/**
 * useAction is the write side of every form: one in-flight flag and one error,
 * with the 422 field breakdown still reachable. Without it each form would
 * repeat the same try/finally, and one of them would eventually forget to clear
 * the busy flag on failure.
 */
export function useAction(): Action {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<unknown>(undefined)

  const run = useCallback(async (fn: () => Promise<unknown>) => {
    setBusy(true)
    setError(undefined)
    try {
      await fn()
    } catch (err) {
      setError(err)
    } finally {
      setBusy(false)
    }
  }, [])

  const fieldError = useCallback((name: string) => fieldErrorOf(error, name), [error])
  const reset = useCallback(() => setError(undefined), [])

  return { busy, error, run, fieldError, reset }
}
