import { useMemo, useSyncExternalStore } from 'react'

/**
 * Route is every place the dashboard can be. A selected deployment is part of
 * the route rather than component state, so reloading the page while watching a
 * build keeps you where you were, and a log view can be linked to.
 */
export type Route =
  | { name: 'projects' }
  | { name: 'project'; projectID: string; deploymentID?: string }

/**
 * Routing is done in the fragment, not with the history API. A path-based
 * router needs the server to rewrite every unknown path to index.html; a hash
 * never reaches the server at all, so the built assets can be served by
 * anything without a catch-all rule.
 */
export function parseRoute(hash: string): Route {
  const segments = hash.replace(/^#\/?/, '').split('/').filter(Boolean)

  if (segments[0] === 'projects' && segments[1]) {
    const projectID = decodeURIComponent(segments[1])
    if (segments[2] === 'deployments' && segments[3]) {
      return { name: 'project', projectID, deploymentID: decodeURIComponent(segments[3]) }
    }
    return { name: 'project', projectID }
  }
  return { name: 'projects' }
}

export const projectsHref = '#/'

export function projectHref(projectID: string): string {
  return `#/projects/${encodeURIComponent(projectID)}`
}

export function deploymentHref(projectID: string, deploymentID: string): string {
  return `${projectHref(projectID)}/deployments/${encodeURIComponent(deploymentID)}`
}

function subscribe(onChange: () => void): () => void {
  window.addEventListener('hashchange', onChange)
  return () => window.removeEventListener('hashchange', onChange)
}

function currentHash(): string {
  return window.location.hash
}

export function useHashRoute(): Route {
  const hash = useSyncExternalStore(subscribe, currentHash, () => '')
  return useMemo(() => parseRoute(hash), [hash])
}

/** navigate changes the route from code, such as after triggering a deploy. */
export function navigate(href: string): void {
  window.location.hash = href.replace(/^#/, '')
}
