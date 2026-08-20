// The control-plane wire format. Nothing here is written by hand: every type
// is an alias into `schema.ts`, which `npm run generate:api` derives from
// control-plane/openapi/openapi.yaml. A field renamed on the server therefore
// fails to compile here rather than arriving in the browser as `undefined`.
//
// Components import their types from this module rather than from the
// generated file directly, so the generator's own naming — deep index types on
// `components['schemas']` — stays behind one boundary and can be replaced
// without touching a single view.

import type { components } from './schema'

type Schemas = components['schemas']

export type EnvironmentKind = Schemas['EnvironmentKind']
export type DeploymentStatus = Schemas['DeploymentStatus']

export type Project = Schemas['Project']
export type Service = Schemas['Service']
export type Environment = Schemas['Environment']
export type Deployment = Schemas['Deployment']
export type DeploymentLog = Schemas['DeploymentLog']

/**
 * LogPage is one slice of a deployment's output. next_after is the cursor to
 * pass back as `after`, so a follower never has to reason about sequence
 * numbers itself.
 */
export type LogPage = Schemas['LogPage']

export type CreateProjectInput = Schemas['CreateProjectInput']
export type CreateServiceInput = Schemas['CreateServiceInput']
export type CreateEnvironmentInput = Schemas['CreateEnvironmentInput']
export type CreateDeploymentInput = Schemas['CreateDeploymentInput']

/**
 * Which statuses a deployment never leaves. Keyed by every status rather than
 * being a set of the terminal ones, so a status added to the specification
 * fails to compile here until this file decides whether it is terminal — the
 * alternative is a dashboard that polls a finished deployment forever.
 */
const terminal: Record<DeploymentStatus, boolean> = {
  queued: false,
  building: false,
  deploying: false,
  live: true,
  failed: true,
  superseded: true,
}

/**
 * isTerminal mirrors domain.DeploymentStatus.Terminal. It is what tells the
 * dashboard to stop polling, so it has to agree with the server's own idea of
 * which states are final.
 */
export function isTerminal(status: DeploymentStatus): boolean {
  return terminal[status]
}
