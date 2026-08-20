import type { Deployment, DeploymentLog, Environment, Project, Service } from '../api/types'

const at = '2026-01-01T12:00:00Z'

/** Fixture builders: every field is set, so a test only states what it cares about. */

export function project(over: Partial<Project> = {}): Project {
  return {
    id: 'proj-1',
    account_id: 'acct-1',
    slug: 'demo',
    name: 'Demo App',
    repo_url: 'https://github.com/example/demo',
    default_branch: 'main',
    created_at: at,
    updated_at: at,
    ...over,
  }
}

export function service(over: Partial<Service> = {}): Service {
  return {
    id: 'svc-1',
    project_id: 'proj-1',
    name: 'api',
    source_path: '.',
    port: 8080,
    created_at: at,
    updated_at: at,
    ...over,
  }
}

export function environment(over: Partial<Environment> = {}): Environment {
  return {
    id: 'env-1',
    project_id: 'proj-1',
    kind: 'production',
    name: 'prod',
    subdomain: 'prod-demo',
    url: 'http://prod-demo.localhost:8081',
    created_at: at,
    updated_at: at,
    ...over,
  }
}

export function deployment(over: Partial<Deployment> = {}): Deployment {
  return {
    id: 'dep-1',
    service_id: 'svc-1',
    environment_id: 'env-1',
    commit_sha: 'a'.repeat(40),
    status: 'queued',
    queued_at: at,
    ...over,
  }
}

export function logLine(seq: number, message: string, stream = 'stdout'): DeploymentLog {
  return { seq, stream, message, logged_at: at }
}
