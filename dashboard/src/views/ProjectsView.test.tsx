import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'

import { ApiError } from '../api/client'
import type { CreateProjectInput, Project } from '../api/types'
import { project } from '../test/fixtures'
import { ProjectsView } from './ProjectsView'

/** stubProjects is the narrow client slice ProjectsView asks for, and no more. */
function stubProjects(options: {
  pages?: Project[][]
  onCreate?: (input: CreateProjectInput) => Promise<Project>
}) {
  const pages = options.pages ?? [[]]
  let call = 0

  return {
    listProjects: () => {
      // Each load takes the next scripted page, so a reload after a create can
      // return a different list.
      const page = pages[Math.min(call, pages.length - 1)] ?? []
      call++
      return Promise.resolve(page)
    },
    createProject: options.onCreate ?? (() => Promise.resolve(project())),
    deleteProject: () => Promise.resolve(),
  }
}

function fillProjectForm() {
  fireEvent.change(screen.getByLabelText('Name'), { target: { value: 'Demo App' } })
  fireEvent.change(screen.getByLabelText('Slug'), { target: { value: 'demo' } })
  fireEvent.change(screen.getByLabelText('Repository URL'), {
    target: { value: 'https://github.com/example/demo' },
  })
}

describe('ProjectsView', () => {
  it('lists the projects the account owns', async () => {
    render(<ProjectsView client={stubProjects({ pages: [[project()]] })} />)

    expect(await screen.findByText('Demo App')).toBeDefined()
    expect(screen.getByText(/github.com\/example\/demo/)).toBeDefined()
  })

  it('says so when there is nothing to show', async () => {
    render(<ProjectsView client={stubProjects({})} />)

    expect(await screen.findByText(/No projects yet/)).toBeDefined()
  })

  it('creates a project and reloads the list', async () => {
    const onCreate = vi.fn(() => Promise.resolve(project()))
    render(
      <ProjectsView
        client={stubProjects({ pages: [[], [project({ name: 'Demo App' })]], onCreate })}
      />,
    )
    await screen.findByText(/No projects yet/)

    fillProjectForm()
    fireEvent.click(screen.getByRole('button', { name: 'Create project' }))

    await waitFor(() => expect(onCreate).toHaveBeenCalledTimes(1))
    expect(onCreate).toHaveBeenCalledWith({
      slug: 'demo',
      name: 'Demo App',
      repo_url: 'https://github.com/example/demo',
    })
    // The reload is what proves the new project appears without a page refresh.
    expect(await screen.findByText('Demo App')).toBeDefined()
  })

  it('shows each rejected field next to the input that caused it', async () => {
    const rejection = new ApiError(422, 'validation_failed', 'one or more fields are invalid', 'req-1', [
      { field: 'slug', message: 'must be lowercase letters, digits and hyphens' },
      { field: 'repo_url', message: 'must be an http or https clone URL' },
    ])

    render(
      <ProjectsView
        client={stubProjects({ pages: [[]], onCreate: () => Promise.reject(rejection) })}
      />,
    )
    await screen.findByText(/No projects yet/)

    fillProjectForm()
    fireEvent.click(screen.getByRole('button', { name: 'Create project' }))

    // Each message appears exactly once, beside the input it belongs to, rather
    // than once inline and once in a summary list.
    expect(
      await screen.findAllByText('must be lowercase letters, digits and hyphens'),
    ).toHaveLength(1)
    expect(screen.getAllByText('must be an http or https clone URL')).toHaveLength(1)
    // The request ID is shown too: it is the only way to find the server log
    // line for this failure.
    expect(screen.getByText(/req-1/)).toBeDefined()
  })

  it('reports a failure to load rather than looking empty', async () => {
    const client = {
      listProjects: () => Promise.reject(new ApiError(401, 'unauthorized', 'invalid token')),
      createProject: () => Promise.resolve(project()),
      deleteProject: () => Promise.resolve(),
    }

    render(<ProjectsView client={client} />)

    expect(await screen.findByText('invalid token')).toBeDefined()
    expect(screen.getByText(/Sign out and paste a current one/)).toBeDefined()
  })
})
