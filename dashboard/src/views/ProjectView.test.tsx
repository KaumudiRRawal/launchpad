import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'

import type { Client } from '../api/client'
import {
  analysisReport,
  deployment,
  environment,
  environmentMetrics,
  logLine,
  project,
  service,
} from '../test/fixtures'
import { ProjectView } from './ProjectView'

/**
 * stubClient answers every call ProjectView makes. It is the whole Client
 * because the view passes the client down to panels that each use a different
 * slice of it.
 */
function stubClient(over: Partial<Client> = {}): Client {
  return {
    listProjects: () => Promise.resolve([project()]),
    createProject: () => Promise.resolve(project()),
    getProject: () => Promise.resolve(project()),
    deleteProject: () => Promise.resolve(),
    listServices: () => Promise.resolve([service()]),
    createService: () => Promise.resolve(service()),
    listEnvironments: () => Promise.resolve([environment()]),
    createEnvironment: () => Promise.resolve(environment()),
    environmentMetrics: () => Promise.resolve(environmentMetrics()),
    environmentAnalysis: () => Promise.resolve(analysisReport()),
    listDeployments: () => Promise.resolve([deployment({ status: 'live' })]),
    createDeployment: () => Promise.resolve(deployment()),
    getDeployment: () => Promise.resolve(deployment({ status: 'live' })),
    deploymentLogs: () => Promise.resolve({ logs: [], next_after: 0 }),
    ...over,
  }
}

describe('ProjectView', () => {
  it('shows the project with everything deployable under it', async () => {
    render(<ProjectView client={stubClient()} projectID="proj-1" deploymentID={undefined} />)

    expect(await screen.findByRole('heading', { name: 'Demo App' })).toBeDefined()
    expect(screen.getByText(/port 8080/)).toBeDefined()
    // The environment's public address, as the API rendered it — not a
    // hostname the dashboard assembled for itself.
    expect(screen.getByRole('link', { name: 'http://prod-demo.localhost:8081' })).toBeDefined()
    // The deploy form's selects are built from those same two lists.
    expect(screen.getByRole('option', { name: 'api' })).toBeDefined()
    expect(screen.getByRole('option', { name: 'prod (production)' })).toBeDefined()
    // The deployment history, keyed by its commit.
    expect(screen.getByText('a'.repeat(12))).toBeDefined()
    expect(screen.getByText('live')).toBeDefined()
  })

  it('deploys the only service to the only environment without touching a select', async () => {
    const createDeployment = vi.fn(() => Promise.resolve(deployment({ id: 'dep-new' })))
    render(
      <ProjectView
        client={stubClient({ createDeployment })}
        projectID="proj-1"
        deploymentID={undefined}
      />,
    )
    await screen.findByRole('heading', { name: 'Demo App' })

    fireEvent.change(screen.getByLabelText('Commit'), {
      target: { value: 'B'.repeat(40) },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Deploy' }))

    await waitFor(() => expect(createDeployment).toHaveBeenCalledTimes(1))
    expect(createDeployment).toHaveBeenCalledWith({
      service_id: 'svc-1',
      environment_id: 'env-1',
      // Lower-cased before it is sent, because the server only accepts a
      // lowercase SHA and a pasted one is often upper case.
      commit_sha: 'b'.repeat(40),
    })
    // The route moves to the new deployment so its build can be watched.
    await waitFor(() => expect(window.location.hash).toBe('#/projects/proj-1/deployments/dep-new'))
  })

  it('follows the selected deployment and renders its output', async () => {
    const client = stubClient({
      deploymentLogs: () =>
        Promise.resolve({
          logs: [
            logLine(1, 'detected build strategy: dockerfile'),
            logLine(2, '\x1b[91mnpm warn deprecated svgo@1.3.2\x1b[0m', 'stderr'),
            logLine(3, 'live at http://localhost:32770 in 36.891s'),
          ],
          next_after: 3,
        }),
      getDeployment: () =>
        Promise.resolve(
          deployment({
            status: 'live',
            url: 'http://localhost:32770',
            image_ref: 'launchpad/web:dep',
          }),
        ),
    })

    render(<ProjectView client={client} projectID="proj-1" deploymentID="dep-1" />)

    expect(await screen.findByText('detected build strategy: dockerfile')).toBeDefined()
    // Rendered without the escape sequence Docker wrapped it in.
    expect(screen.getByText('npm warn deprecated svgo@1.3.2')).toBeDefined()
    expect(screen.getByText('live at http://localhost:32770 in 36.891s')).toBeDefined()
    expect(screen.getByRole('link', { name: 'http://localhost:32770' })).toBeDefined()
    // The target is named, not shown as two opaque UUIDs.
    expect(screen.getByText('api → prod')).toBeDefined()
  })

  it('reports a deployment that failed, with the reason the engine recorded', async () => {
    const client = stubClient({
      listDeployments: () => Promise.resolve([deployment({ status: 'failed' })]),
      getDeployment: () =>
        Promise.resolve(
          deployment({ status: 'failed', error_message: 'fetch source: exit status 128' }),
        ),
      deploymentLogs: () =>
        Promise.resolve({ logs: [logLine(1, 'deployment failed', 'stderr')], next_after: 1 }),
    })

    render(<ProjectView client={client} projectID="proj-1" deploymentID="dep-1" />)

    expect(await screen.findByText('fetch source: exit status 128')).toBeDefined()
  })
})
