import { renderHook, waitFor } from '@testing-library/react'
import { describe, expect, it } from 'vitest'

import {
  deploymentHref,
  parseRoute,
  projectHref,
  projectsHref,
  useHashRoute,
  type Route,
} from './useHashRoute'

describe('parseRoute', () => {
  const cases: { hash: string; want: Route }[] = [
    { hash: '', want: { name: 'projects' } },
    { hash: '#', want: { name: 'projects' } },
    { hash: '#/', want: { name: 'projects' } },
    { hash: '#/projects', want: { name: 'projects' } },
    { hash: '#/projects/proj-1', want: { name: 'project', projectID: 'proj-1' } },
    { hash: '#/projects/proj-1/', want: { name: 'project', projectID: 'proj-1' } },
    {
      hash: '#/projects/proj-1/deployments/dep-9',
      want: { name: 'project', projectID: 'proj-1', deploymentID: 'dep-9' },
    },
    // A deployments segment with no ID is the project, not a broken log view.
    { hash: '#/projects/proj-1/deployments', want: { name: 'project', projectID: 'proj-1' } },
    // Anything unrecognised lands on the project list rather than a blank page.
    { hash: '#/nowhere/at/all', want: { name: 'projects' } },
    { hash: '#/projects/with%20space', want: { name: 'project', projectID: 'with space' } },
  ]

  for (const tc of cases) {
    it(`maps ${JSON.stringify(tc.hash)}`, () => {
      expect(parseRoute(tc.hash)).toEqual(tc.want)
    })
  }
})

describe('href builders', () => {
  it('round-trip through parseRoute', () => {
    expect(parseRoute(projectsHref)).toEqual({ name: 'projects' })
    expect(parseRoute(projectHref('proj-1'))).toEqual({ name: 'project', projectID: 'proj-1' })
    expect(parseRoute(deploymentHref('proj-1', 'dep-9'))).toEqual({
      name: 'project',
      projectID: 'proj-1',
      deploymentID: 'dep-9',
    })
  })

  it('escapes identifiers that would otherwise change the route', () => {
    expect(parseRoute(projectHref('a/b'))).toEqual({ name: 'project', projectID: 'a/b' })
  })
})

describe('useHashRoute', () => {
  it('follows the fragment as it changes', async () => {
    window.location.hash = '#/projects/proj-1'
    const { result } = renderHook(() => useHashRoute())

    expect(result.current).toEqual({ name: 'project', projectID: 'proj-1' })

    window.location.hash = '#/projects/proj-1/deployments/dep-9'

    await waitFor(() =>
      expect(result.current).toEqual({
        name: 'project',
        projectID: 'proj-1',
        deploymentID: 'dep-9',
      }),
    )
  })
})
