import type {
  CreateDeploymentInput,
  CreateEnvironmentInput,
  CreateProjectInput,
  CreateServiceInput,
  Deployment,
  Environment,
  LogPage,
  Project,
  Service,
} from './types'

/** FieldError is one rejected field from a 422 response. */
export interface FieldError {
  field: string
  message: string
}

/**
 * ApiError carries everything the control plane reported about a failure. The
 * request ID is kept because it is the only thing that lets a user reporting a
 * problem be matched to a server log line.
 */
export class ApiError extends Error {
  readonly status: number
  readonly code: string
  readonly requestId: string
  readonly fields: FieldError[]

  constructor(
    status: number,
    code: string,
    message: string,
    requestId = '',
    fields: FieldError[] = [],
  ) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code
    this.requestId = requestId
    this.fields = fields
  }

  /** field returns the message for one rejected field, if it was rejected. */
  field(name: string): string | undefined {
    return this.fields.find((f) => f.field === name)?.message
  }
}

/** fieldErrorOf reads a field-level message out of an unknown thrown value. */
export function fieldErrorOf(err: unknown, name: string): string | undefined {
  return err instanceof ApiError ? err.field(name) : undefined
}

/** isUnauthorized reports whether err means the API rejected the token. */
export function isUnauthorized(err: unknown): boolean {
  return err instanceof ApiError && err.status === 401
}

export interface Client {
  listProjects(signal?: AbortSignal): Promise<Project[]>
  createProject(input: CreateProjectInput): Promise<Project>
  getProject(projectID: string, signal?: AbortSignal): Promise<Project>
  deleteProject(projectID: string): Promise<void>

  listServices(projectID: string, signal?: AbortSignal): Promise<Service[]>
  createService(projectID: string, input: CreateServiceInput): Promise<Service>

  listEnvironments(projectID: string, signal?: AbortSignal): Promise<Environment[]>
  createEnvironment(projectID: string, input: CreateEnvironmentInput): Promise<Environment>

  listDeployments(projectID: string, signal?: AbortSignal): Promise<Deployment[]>
  createDeployment(input: CreateDeploymentInput): Promise<Deployment>
  getDeployment(deploymentID: string, signal?: AbortSignal): Promise<Deployment>
  deploymentLogs(deploymentID: string, after: number, signal?: AbortSignal): Promise<LogPage>
}

export interface ClientOptions {
  token: string
  /**
   * Where the control plane lives. Empty means same-origin, which is how both
   * the Vite dev proxy and a deployment that serves the built assets beside the
   * API are set up.
   */
  baseUrl?: string
  /** Injectable for tests; defaults to the global fetch. */
  fetch?: typeof globalThis.fetch
}

interface RequestOptions {
  method?: string
  body?: unknown
  signal?: AbortSignal
}

/** newClient builds a client bound to one API token. */
export function newClient(options: ClientOptions): Client {
  const baseUrl = (options.baseUrl ?? '').replace(/\/$/, '')
  const doFetch = options.fetch ?? globalThis.fetch.bind(globalThis)

  async function request<T>(path: string, opts: RequestOptions = {}): Promise<T> {
    const headers: Record<string, string> = {
      Accept: 'application/json',
      Authorization: `Bearer ${options.token}`,
    }
    if (opts.body !== undefined) {
      headers['Content-Type'] = 'application/json'
    }

    let response: Response
    try {
      response = await doFetch(baseUrl + path, {
        method: opts.method ?? 'GET',
        headers,
        ...(opts.body === undefined ? {} : { body: JSON.stringify(opts.body) }),
        ...(opts.signal === undefined ? {} : { signal: opts.signal }),
      })
    } catch (err) {
      // An aborted request is the caller's own doing — a poll being cancelled
      // on unmount — so it is passed through untouched for them to recognise.
      if (err instanceof DOMException && err.name === 'AbortError') {
        throw err
      }
      throw new ApiError(0, 'network_error', 'could not reach the control plane')
    }

    // Read the body as text first. An error from something other than the API
    // — a dev proxy with nothing behind it, say — answers with HTML, and a
    // JSON parse failure there would hide the status code that explains what
    // actually went wrong.
    const text = await response.text()

    if (!response.ok) {
      throw errorFromBody(response, text)
    }
    if (text === '') {
      return undefined as T
    }
    return JSON.parse(text) as T
  }

  return {
    listProjects: (signal) =>
      // The API wraps collections in a named key. Unwrapping here means no
      // component ever has to know that.
      request<{ projects: Project[] }>('/v1/projects', { ...maybeSignal(signal) }).then(
        (body) => body.projects ?? [],
      ),
    createProject: (input) => request<Project>('/v1/projects', { method: 'POST', body: input }),
    getProject: (projectID, signal) =>
      request<Project>(`/v1/projects/${encodeURIComponent(projectID)}`, {
        ...maybeSignal(signal),
      }),
    deleteProject: (projectID) =>
      request<void>(`/v1/projects/${encodeURIComponent(projectID)}`, { method: 'DELETE' }),

    listServices: (projectID, signal) =>
      request<{ services: Service[] }>(
        `/v1/projects/${encodeURIComponent(projectID)}/services`,
        { ...maybeSignal(signal) },
      ).then((body) => body.services ?? []),
    createService: (projectID, input) =>
      request<Service>(`/v1/projects/${encodeURIComponent(projectID)}/services`, {
        method: 'POST',
        body: input,
      }),

    listEnvironments: (projectID, signal) =>
      request<{ environments: Environment[] }>(
        `/v1/projects/${encodeURIComponent(projectID)}/environments`,
        { ...maybeSignal(signal) },
      ).then((body) => body.environments ?? []),
    createEnvironment: (projectID, input) =>
      request<Environment>(`/v1/projects/${encodeURIComponent(projectID)}/environments`, {
        method: 'POST',
        body: input,
      }),

    listDeployments: (projectID, signal) =>
      request<{ deployments: Deployment[] }>(
        `/v1/projects/${encodeURIComponent(projectID)}/deployments`,
        { ...maybeSignal(signal) },
      ).then((body) => body.deployments ?? []),
    createDeployment: (input) =>
      request<Deployment>('/v1/deployments', { method: 'POST', body: input }),
    getDeployment: (deploymentID, signal) =>
      request<Deployment>(`/v1/deployments/${encodeURIComponent(deploymentID)}`, {
        ...maybeSignal(signal),
      }),
    deploymentLogs: (deploymentID, after, signal) =>
      request<LogPage>(
        `/v1/deployments/${encodeURIComponent(deploymentID)}/logs?after=${after}`,
        { ...maybeSignal(signal) },
      ),
  }
}

/**
 * maybeSignal omits the key entirely when there is no signal, because
 * exactOptionalPropertyTypes distinguishes an absent property from one
 * explicitly set to undefined.
 */
function maybeSignal(signal: AbortSignal | undefined): { signal?: AbortSignal } {
  return signal === undefined ? {} : { signal }
}

interface WireError {
  error?: {
    code?: string
    message?: string
    request_id?: string
    fields?: FieldError[]
  }
}

/** errorFromBody turns a failed response into an ApiError, whatever it holds. */
function errorFromBody(response: Response, text: string): ApiError {
  let parsed: WireError | undefined
  try {
    parsed = JSON.parse(text) as WireError
  } catch {
    parsed = undefined
  }

  const wire = parsed?.error
  if (!wire) {
    return new ApiError(
      response.status,
      'unexpected_response',
      `the control plane returned ${response.status} ${response.statusText}`.trim(),
    )
  }

  return new ApiError(
    response.status,
    wire.code ?? 'unknown',
    wire.message ?? `request failed with ${response.status}`,
    wire.request_id ?? '',
    wire.fields ?? [],
  )
}
