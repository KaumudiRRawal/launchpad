import { useCallback, useMemo, useState } from 'react'

import { newClient } from './api/client'
import { clearToken, loadToken, saveToken } from './auth'
import { useHashRoute } from './hooks/useHashRoute'
import { ProjectsView } from './views/ProjectsView'
import { ProjectView } from './views/ProjectView'
import { SignIn } from './views/SignIn'

export default function App() {
  const [token, setToken] = useState(loadToken)
  const route = useHashRoute()

  // Memoised on the token alone: every hook that polls takes the client as a
  // dependency, so a client rebuilt on each render would restart the polls on
  // each render too.
  const client = useMemo(() => (token === '' ? undefined : newClient({ token })), [token])

  const onToken = useCallback((next: string) => {
    saveToken(next)
    setToken(next)
  }, [])

  const onSignOut = useCallback(() => {
    clearToken()
    setToken('')
  }, [])

  if (client === undefined) {
    return <SignIn onToken={onToken} />
  }

  return (
    <>
      <header className="topbar">
        <a className="brand" href="#/">
          Launchpad
        </a>
        <button type="button" className="ghost" onClick={onSignOut}>
          Sign out
        </button>
      </header>

      {route.name === 'projects' ? (
        <ProjectsView client={client} />
      ) : (
        <ProjectView
          client={client}
          projectID={route.projectID}
          deploymentID={route.deploymentID}
        />
      )}
    </>
  )
}
