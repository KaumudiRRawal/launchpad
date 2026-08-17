import { useState } from 'react'

import { newClient } from '../api/client'
import { ErrorBanner } from '../components/ErrorBanner'
import { Field } from '../components/Field'
import { useAction } from '../hooks/useAction'

/**
 * SignIn takes an API token. It is not a login: the control plane has no
 * password to check, and the first token is minted by `make bootstrap` against
 * the database directly.
 *
 * The token is verified with a real request before it is accepted, so a
 * truncated paste is reported here instead of turning into an empty dashboard
 * that looks like an account with no projects.
 */
export function SignIn({ onToken }: { onToken: (token: string) => void }) {
  const [token, setToken] = useState('')
  const action = useAction()

  function onSubmit(event: React.FormEvent) {
    event.preventDefault()
    void action.run(async () => {
      await newClient({ token: token.trim() }).listProjects()
      onToken(token.trim())
    })
  }

  return (
    <main className="signin">
      <h1>Launchpad</h1>
      <p className="signin-lede">
        Paste an API token to continue. Mint the first one with{' '}
        <code>make bootstrap EMAIL=you@example.com NAME=&quot;Your Name&quot;</code>.
      </p>

      <form onSubmit={onSubmit} className="card">
        <ErrorBanner error={action.error} />

        <Field label="API token">
          {(id) => (
            <input
              id={id}
              type="password"
              autoComplete="off"
              value={token}
              onChange={(e) => setToken(e.target.value)}
              placeholder="lp_…"
            />
          )}
        </Field>

        <button type="submit" disabled={action.busy || token.trim() === ''}>
          {action.busy ? 'Checking…' : 'Continue'}
        </button>
      </form>
    </main>
  )
}
