import { ApiError } from '../api/client'

/**
 * ErrorBanner renders a failure without hiding what the server said. The
 * request ID is shown because it is the one piece of information that turns
 * "it broke" into a log line an operator can find, and a rejected token gets an
 * explicit hint because the fix — a new token — is not otherwise obvious.
 *
 * It deliberately does not list the rejected fields from a 422. Every form
 * renders an input for each field its endpoint validates and shows that field's
 * message beside it, so repeating them here would print each complaint twice.
 */
export function ErrorBanner({ error }: { error: unknown }) {
  if (error === undefined || error === null) {
    return null
  }

  if (error instanceof ApiError) {
    return (
      <div className="banner banner-error" role="alert">
        <p>{error.message}</p>
        {error.status === 401 && (
          <p className="banner-hint">
            The control plane rejected this token. Sign out and paste a current one.
          </p>
        )}
        {error.requestId !== '' && <p className="banner-meta">request {error.requestId}</p>}
      </div>
    )
  }

  return (
    <div className="banner banner-error" role="alert">
      <p>{error instanceof Error ? error.message : String(error)}</p>
    </div>
  )
}
