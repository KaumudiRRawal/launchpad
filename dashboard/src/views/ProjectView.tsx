import { useCallback, useState } from 'react'

import type { Client } from '../api/client'
import type { Deployment, Environment, EnvironmentKind, Service } from '../api/types'
import { DeploymentPanel } from '../components/DeploymentPanel'
import { ErrorBanner } from '../components/ErrorBanner'
import { Field } from '../components/Field'
import { HealthPanel } from '../components/HealthPanel'
import { StatusBadge } from '../components/StatusBadge'
import { relativeTime, shortSHA } from '../format'
import { useAction } from '../hooks/useAction'
import { deploymentHref, navigate, projectsHref } from '../hooks/useHashRoute'
import { useResource } from '../hooks/useResource'

interface ProjectViewProps {
  client: Client
  projectID: string
  deploymentID: string | undefined
}

export function ProjectView({ client, projectID, deploymentID }: ProjectViewProps) {
  const loadProject = useCallback(
    (signal: AbortSignal) => client.getProject(projectID, signal),
    [client, projectID],
  )
  const loadServices = useCallback(
    (signal: AbortSignal) => client.listServices(projectID, signal),
    [client, projectID],
  )
  const loadEnvironments = useCallback(
    (signal: AbortSignal) => client.listEnvironments(projectID, signal),
    [client, projectID],
  )
  const loadDeployments = useCallback(
    (signal: AbortSignal) => client.listDeployments(projectID, signal),
    [client, projectID],
  )

  const project = useResource(loadProject)
  const services = useResource(loadServices)
  const environments = useResource(loadEnvironments)
  const deployments = useResource(loadDeployments)

  const serviceList = services.data ?? []
  const environmentList = environments.data ?? []
  const followed = deployments.data?.find((deployment) => deployment.id === deploymentID)

  return (
    <main className="layout">
      <section className="panel">
        <a className="back" href={projectsHref}>
          ← All projects
        </a>
        <ErrorBanner error={project.error} />
        {project.data && (
          <>
            <h1>{project.data.name}</h1>
            <p className="list-meta">
              <code>{project.data.slug}</code> ·{' '}
              <a href={project.data.repo_url} target="_blank" rel="noreferrer">
                {project.data.repo_url}
              </a>{' '}
              · {project.data.default_branch}
            </p>
          </>
        )}
      </section>

      <ServicesPanel
        client={client}
        projectID={projectID}
        services={serviceList}
        error={services.error}
        onCreated={services.reload}
      />

      <EnvironmentsPanel
        client={client}
        projectID={projectID}
        environments={environmentList}
        error={environments.error}
        onCreated={environments.reload}
      />

      <HealthPanel client={client} environments={environmentList} />

      <DeployForm
        client={client}
        projectID={projectID}
        services={serviceList}
        environments={environmentList}
        onDeployed={deployments.reload}
      />

      <DeploymentsPanel
        deployments={deployments.data ?? []}
        error={deployments.error}
        projectID={projectID}
        services={serviceList}
        environments={environmentList}
        onRefresh={deployments.reload}
      />

      {deploymentID !== undefined && (
        <DeploymentPanel
          client={client}
          deploymentID={deploymentID}
          serviceName={nameOf(serviceList, followed?.service_id)}
          environmentName={nameOf(environmentList, followed?.environment_id)}
          // The history list is a snapshot from before this build ran, so it is
          // re-read once the deployment settles rather than left showing
          // "queued" next to a deployment that has finished.
          onFinished={deployments.reload}
        />
      )}
    </main>
  )
}

/** nameOf resolves an ID to its human name, when the list holding it is loaded. */
function nameOf(items: { id: string; name: string }[], id: string | undefined): string | undefined {
  return items.find((item) => item.id === id)?.name
}

function ServicesPanel({
  client,
  projectID,
  services,
  error,
  onCreated,
}: {
  client: Pick<Client, 'createService'>
  projectID: string
  services: Service[]
  error: unknown
  onCreated: () => void
}) {
  const [name, setName] = useState('')
  const [sourcePath, setSourcePath] = useState('')
  const [port, setPort] = useState('')
  const action = useAction()

  function onSubmit(event: React.FormEvent) {
    event.preventDefault()
    void action.run(async () => {
      await client.createService(projectID, {
        name,
        // Both are omitted when blank so the server's defaults — the repository
        // root, and port 8080 — apply rather than being restated here.
        ...(sourcePath.trim() === '' ? {} : { source_path: sourcePath.trim() }),
        ...(port.trim() === '' ? {} : { port: Number(port) }),
      })
      setName('')
      setSourcePath('')
      setPort('')
      onCreated()
    })
  }

  return (
    <section className="panel">
      <header className="panel-header">
        <h2>Services</h2>
      </header>

      <ErrorBanner error={error} />

      {services.length === 0 ? (
        <p className="muted">No services yet. A service is one deployable unit of the repo.</p>
      ) : (
        <ul className="list">
          {services.map((service) => (
            <li key={service.id} className="list-row">
              <div>
                <span className="list-title">{service.name}</span>
                <p className="list-meta">
                  <code>{service.source_path}</code> · port {service.port}
                </p>
              </div>
            </li>
          ))}
        </ul>
      )}

      <form onSubmit={onSubmit} className="inline-form">
        <ErrorBanner error={action.error} />

        <Field label="Name" error={action.fieldError('name')}>
          {(id) => (
            <input id={id} value={name} onChange={(e) => setName(e.target.value)} required />
          )}
        </Field>

        <Field
          label="Source path"
          hint="Relative to the repository root. Defaults to the root."
          error={action.fieldError('source_path')}
        >
          {(id) => (
            <input
              id={id}
              value={sourcePath}
              onChange={(e) => setSourcePath(e.target.value)}
              placeholder="."
            />
          )}
        </Field>

        <Field label="Port" hint="Defaults to 8080." error={action.fieldError('port')}>
          {(id) => (
            <input
              id={id}
              type="number"
              min={1}
              max={65535}
              value={port}
              onChange={(e) => setPort(e.target.value)}
              placeholder="8080"
            />
          )}
        </Field>

        <button type="submit" disabled={action.busy}>
          {action.busy ? 'Adding…' : 'Add service'}
        </button>
      </form>
    </section>
  )
}

function EnvironmentsPanel({
  client,
  projectID,
  environments,
  error,
  onCreated,
}: {
  client: Pick<Client, 'createEnvironment'>
  projectID: string
  environments: Environment[]
  error: unknown
  onCreated: () => void
}) {
  const [name, setName] = useState('')
  const [kind, setKind] = useState<EnvironmentKind>('preview')
  const action = useAction()

  function onSubmit(event: React.FormEvent) {
    event.preventDefault()
    void action.run(async () => {
      await client.createEnvironment(projectID, { kind, name })
      setName('')
      onCreated()
    })
  }

  return (
    <section className="panel">
      <header className="panel-header">
        <h2>Environments</h2>
      </header>

      <ErrorBanner error={error} />

      {environments.length === 0 ? (
        <p className="muted">No environments yet. Each one is its own isolation boundary.</p>
      ) : (
        <ul className="list">
          {environments.map((environment) => (
            <li key={environment.id} className="list-row">
              <div>
                <span className="list-title">{environment.name}</span>
                <p className="list-meta">
                  {/*
                    The address comes from the API rather than being assembled
                    from the subdomain here: the platform's domain and the
                    proxy's port are the control plane's configuration, and a
                    dashboard guessing at them is a link that silently stops
                    working when either changes.
                  */}
                  {environment.kind} ·{' '}
                  <a href={environment.url} target="_blank" rel="noreferrer">
                    {environment.url}
                  </a>
                </p>
              </div>
            </li>
          ))}
        </ul>
      )}

      <form onSubmit={onSubmit} className="inline-form">
        <ErrorBanner error={action.error} />

        <Field label="Name" error={action.fieldError('name')}>
          {(id) => (
            <input id={id} value={name} onChange={(e) => setName(e.target.value)} required />
          )}
        </Field>

        <Field label="Kind" error={action.fieldError('kind')}>
          {(id) => (
            <select
              id={id}
              value={kind}
              onChange={(e) => setKind(e.target.value as EnvironmentKind)}
            >
              <option value="preview">preview</option>
              <option value="production">production</option>
            </select>
          )}
        </Field>

        <button type="submit" disabled={action.busy}>
          {action.busy ? 'Adding…' : 'Add environment'}
        </button>
      </form>
    </section>
  )
}

function DeployForm({
  client,
  projectID,
  services,
  environments,
  onDeployed,
}: {
  client: Pick<Client, 'createDeployment'>
  projectID: string
  services: Service[]
  environments: Environment[]
  onDeployed: () => void
}) {
  const [serviceID, setServiceID] = useState('')
  const [environmentID, setEnvironmentID] = useState('')
  const [commit, setCommit] = useState('')
  const action = useAction()

  // Falling back to the first entry means the common case — one service, one
  // environment — needs no interaction with the selects at all.
  const chosenService = serviceID || services[0]?.id || ''
  const chosenEnvironment = environmentID || environments[0]?.id || ''
  const ready = chosenService !== '' && chosenEnvironment !== ''

  function onSubmit(event: React.FormEvent) {
    event.preventDefault()
    void action.run(async () => {
      const deployment = await client.createDeployment({
        service_id: chosenService,
        environment_id: chosenEnvironment,
        commit_sha: commit.trim().toLowerCase(),
      })
      setCommit('')
      onDeployed()
      // Straight to the log view: a deploy the user cannot watch is a deploy
      // they have to go looking for.
      navigate(deploymentHref(projectID, deployment.id))
    })
  }

  return (
    <section className="panel">
      <header className="panel-header">
        <h2>Deploy</h2>
      </header>

      <form onSubmit={onSubmit}>
        <ErrorBanner error={action.error} />

        {!ready && (
          <p className="muted">Add a service and an environment before deploying.</p>
        )}

        <Field label="Service" error={action.fieldError('service_id')}>
          {(id) => (
            <select
              id={id}
              value={chosenService}
              onChange={(e) => setServiceID(e.target.value)}
              disabled={services.length === 0}
            >
              {services.map((service) => (
                <option key={service.id} value={service.id}>
                  {service.name}
                </option>
              ))}
            </select>
          )}
        </Field>

        <Field label="Environment" error={action.fieldError('environment_id')}>
          {(id) => (
            <select
              id={id}
              value={chosenEnvironment}
              onChange={(e) => setEnvironmentID(e.target.value)}
              disabled={environments.length === 0}
            >
              {environments.map((environment) => (
                <option key={environment.id} value={environment.id}>
                  {environment.name} ({environment.kind})
                </option>
              ))}
            </select>
          )}
        </Field>

        <Field
          label="Commit"
          hint="The full 40-character SHA. A short SHA can become ambiguous as the repo grows."
          error={action.fieldError('commit_sha')}
        >
          {(id) => (
            <input
              id={id}
              value={commit}
              onChange={(e) => setCommit(e.target.value)}
              pattern="[0-9a-fA-F]{40}"
              title="40 hexadecimal characters"
              spellCheck={false}
              required
            />
          )}
        </Field>

        <button type="submit" disabled={action.busy || !ready}>
          {action.busy ? 'Queueing…' : 'Deploy'}
        </button>
      </form>
    </section>
  )
}

function DeploymentsPanel({
  deployments,
  error,
  projectID,
  services,
  environments,
  onRefresh,
}: {
  deployments: Deployment[]
  error: unknown
  projectID: string
  services: Service[]
  environments: Environment[]
  onRefresh: () => void
}) {
  const now = new Date()

  return (
    <section className="panel">
      <header className="panel-header">
        <h2>Deployments</h2>
        <button type="button" className="ghost" onClick={onRefresh}>
          Refresh
        </button>
      </header>

      <ErrorBanner error={error} />

      {deployments.length === 0 ? (
        <p className="muted">Nothing deployed yet.</p>
      ) : (
        <ul className="list">
          {deployments.map((deployment) => (
            <li key={deployment.id} className="list-row">
              <div>
                <a
                  className="list-title"
                  href={deploymentHref(projectID, deployment.id)}
                >
                  <code>{shortSHA(deployment.commit_sha)}</code>
                </a>
                <p className="list-meta">
                  {nameOf(services, deployment.service_id) ?? 'unknown service'} →{' '}
                  {nameOf(environments, deployment.environment_id) ?? 'unknown environment'} ·{' '}
                  {relativeTime(deployment.queued_at, now)}
                </p>
              </div>
              <StatusBadge status={deployment.status} />
            </li>
          ))}
        </ul>
      )}
    </section>
  )
}
