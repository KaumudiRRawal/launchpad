import { useCallback, useState } from 'react'

import type { Client } from '../api/client'
import type { Project } from '../api/types'
import { ErrorBanner } from '../components/ErrorBanner'
import { Field } from '../components/Field'
import { relativeTime } from '../format'
import { useAction } from '../hooks/useAction'
import { useResource } from '../hooks/useResource'
import { projectHref } from '../hooks/useHashRoute'

/** Projects is the slice of the client this view uses. */
type Projects = Pick<Client, 'listProjects' | 'createProject' | 'deleteProject'>

export function ProjectsView({ client }: { client: Projects }) {
  const load = useCallback((signal: AbortSignal) => client.listProjects(signal), [client])
  const projects = useResource(load)

  return (
    <main className="layout">
      <section className="panel">
        <header className="panel-header">
          <h2>Projects</h2>
        </header>

        <ErrorBanner error={projects.error} />

        {projects.data === undefined && projects.loading && <p className="muted">Loading…</p>}
        {projects.data?.length === 0 && (
          <p className="muted">No projects yet. Create one to get started.</p>
        )}

        {projects.data !== undefined && projects.data.length > 0 && (
          <ul className="list">
            {projects.data.map((project) => (
              <ProjectRow
                key={project.id}
                project={project}
                client={client}
                onDeleted={projects.reload}
              />
            ))}
          </ul>
        )}
      </section>

      <NewProjectForm client={client} onCreated={projects.reload} />
    </main>
  )
}

function ProjectRow({
  project,
  client,
  onDeleted,
}: {
  project: Project
  client: Pick<Client, 'deleteProject'>
  onDeleted: () => void
}) {
  const action = useAction()

  function onDelete() {
    // Deleting a project cascades to its services, environments and
    // deployments, so it is worth one interruption.
    if (!window.confirm(`Delete ${project.name} and every deployment under it?`)) {
      return
    }
    void action.run(async () => {
      await client.deleteProject(project.id)
      onDeleted()
    })
  }

  return (
    <li className="list-row">
      <div>
        <a className="list-title" href={projectHref(project.id)}>
          {project.name}
        </a>
        <p className="list-meta">
          <code>{project.slug}</code> · {project.repo_url} · {project.default_branch} · created{' '}
          {relativeTime(project.created_at, new Date())}
        </p>
        <ErrorBanner error={action.error} />
      </div>
      <button type="button" className="danger" onClick={onDelete} disabled={action.busy}>
        {action.busy ? 'Deleting…' : 'Delete'}
      </button>
    </li>
  )
}

function NewProjectForm({
  client,
  onCreated,
}: {
  client: Pick<Client, 'createProject'>
  onCreated: () => void
}) {
  const [slug, setSlug] = useState('')
  const [name, setName] = useState('')
  const [repoURL, setRepoURL] = useState('')
  const [branch, setBranch] = useState('')
  const action = useAction()

  function onSubmit(event: React.FormEvent) {
    event.preventDefault()
    void action.run(async () => {
      await client.createProject({
        slug,
        name,
        repo_url: repoURL,
        // Omitted rather than sent empty, so the server's own default applies.
        ...(branch.trim() === '' ? {} : { default_branch: branch.trim() }),
      })
      setSlug('')
      setName('')
      setRepoURL('')
      setBranch('')
      onCreated()
    })
  }

  return (
    <section className="panel">
      <header className="panel-header">
        <h2>New project</h2>
      </header>

      <form onSubmit={onSubmit}>
        <ErrorBanner error={action.error} />

        <Field label="Name" error={action.fieldError('name')}>
          {(id) => (
            <input id={id} value={name} onChange={(e) => setName(e.target.value)} required />
          )}
        </Field>

        <Field
          label="Slug"
          hint="Becomes part of every subdomain: lowercase letters, digits and hyphens."
          error={action.fieldError('slug')}
        >
          {(id) => (
            <input id={id} value={slug} onChange={(e) => setSlug(e.target.value)} required />
          )}
        </Field>

        <Field
          label="Repository URL"
          hint="An http or https clone URL."
          error={action.fieldError('repo_url')}
        >
          {(id) => (
            <input
              id={id}
              value={repoURL}
              onChange={(e) => setRepoURL(e.target.value)}
              placeholder="https://github.com/example/demo"
              required
            />
          )}
        </Field>

        <Field
          label="Default branch"
          hint="Defaults to main."
          error={action.fieldError('default_branch')}
        >
          {(id) => <input id={id} value={branch} onChange={(e) => setBranch(e.target.value)} />}
        </Field>

        <button type="submit" disabled={action.busy}>
          {action.busy ? 'Creating…' : 'Create project'}
        </button>
      </form>
    </section>
  )
}
