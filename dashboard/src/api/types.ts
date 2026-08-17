// The control-plane wire format, mirrored by hand from the Go structs in
// internal/domain. Keeping every wire type in this one module means the
// components import their types from a single boundary, so replacing these
// declarations with generated ones changes nothing above it.

export type EnvironmentKind = 'preview' | 'production'

export type DeploymentStatus =
  | 'queued'
  | 'building'
  | 'deploying'
  | 'live'
  | 'failed'
  | 'superseded'

/** The statuses a deployment never leaves on its own. */
const terminalStatuses: ReadonlySet<DeploymentStatus> = new Set<DeploymentStatus>([
  'live',
  'failed',
  'superseded',
])

/**
 * isTerminal mirrors domain.DeploymentStatus.Terminal. It is what tells the
 * dashboard to stop polling, so it has to agree with the server's own idea of
 * which states are final.
 */
export function isTerminal(status: DeploymentStatus): boolean {
  return terminalStatuses.has(status)
}

export interface Project {
  id: string
  account_id: string
  slug: string
  name: string
  repo_url: string
  default_branch: string
  created_at: string
  updated_at: string
}

export interface Service {
  id: string
  project_id: string
  name: string
  source_path: string
  port: number
  created_at: string
  updated_at: string
}

export interface Environment {
  id: string
  project_id: string
  kind: EnvironmentKind
  name: string
  subdomain: string
  created_at: string
  updated_at: string
}

export interface Deployment {
  id: string
  service_id: string
  environment_id: string
  commit_sha: string
  status: DeploymentStatus
  image_ref?: string
  url?: string
  error_message?: string
  queued_at: string
  started_at?: string
  completed_at?: string
}

export interface DeploymentLog {
  seq: number
  stream: string
  message: string
  logged_at: string
}

/**
 * LogPage is one slice of a deployment's output. next_after is the cursor to
 * pass back as `after`, so a follower never has to reason about sequence
 * numbers itself.
 */
export interface LogPage {
  logs: DeploymentLog[]
  next_after: number
}

export interface CreateProjectInput {
  slug: string
  name: string
  repo_url: string
  default_branch?: string
}

export interface CreateServiceInput {
  name: string
  source_path?: string
  port?: number
}

export interface CreateEnvironmentInput {
  kind: EnvironmentKind
  name: string
}

export interface CreateDeploymentInput {
  service_id: string
  environment_id: string
  commit_sha: string
}
