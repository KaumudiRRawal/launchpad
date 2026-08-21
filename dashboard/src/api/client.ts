import type { components, paths } from './schema'
import type {
  AnalysisReport,
  CreateDeploymentInput,
  CreateEnvironmentInput,
  CreateProjectInput,
  CreateServiceInput,
  Deployment,
  Environment,
  EnvironmentMetrics,
  LogPage,
  Project,
  Service,
} from './types'

/**
 * The collection envelopes the API wraps its lists in. They are read from the
 * generated schema rather than restated here, so the key a response is
 * unwrapped by is the key the server documents.
 */
type Schemas = components['schemas']

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
  environmentMetrics(
    environmentID: string,
    window: string,
    signal?: AbortSignal,
  ): Promise<EnvironmentMetrics>
  environmentAnalysis(environmentID: string, signal?: AbortSignal): Promise<AnalysisReport>

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
      request<Schemas['ProjectList']>(apiPath('/v1/projects'), { ...maybeSignal(signal) }).then(
        (body) => body.projects ?? [],
      ),
    createProject: (input) =>
      request<Project>(apiPath('/v1/projects'), { method: 'POST', body: input }),
    getProject: (projectID, signal) =>
      request<Project>(apiPath('/v1/projects/{projectID}', { projectID }), {
        ...maybeSignal(signal),
      }),
    deleteProject: (projectID) =>
      request<void>(apiPath('/v1/projects/{projectID}', { projectID }), { method: 'DELETE' }),

    listServices: (projectID, signal) =>
      request<Schemas['ServiceList']>(apiPath('/v1/projects/{projectID}/services', { projectID }), {
        ...maybeSignal(signal),
      }).then((body) => body.services ?? []),
    createService: (projectID, input) =>
      request<Service>(apiPath('/v1/projects/{projectID}/services', { projectID }), {
        method: 'POST',
        body: input,
      }),

    listEnvironments: (projectID, signal) =>
      request<Schemas['EnvironmentList']>(
        apiPath('/v1/projects/{projectID}/environments', { projectID }),
        { ...maybeSignal(signal) },
      ).then((body) => body.environments ?? []),
    createEnvironment: (projectID, input) =>
      request<Environment>(apiPath('/v1/projects/{projectID}/environments', { projectID }), {
        method: 'POST',
        body: input,
      }),

    environmentMetrics: (environmentID, window, signal) =>
      request<EnvironmentMetrics>(
        `${apiPath('/v1/environments/{environmentID}/metrics', { environmentID })}?window=${encodeURIComponent(window)}`,
        { ...maybeSignal(signal) },
      ),
    // The window is the server's default: the report is a comparison, and a
    // dashboard choosing the period it compares over would be choosing how
    // sensitive the answer is without saying so.
    environmentAnalysis: (environmentID, signal) =>
      request<AnalysisReport>(
        apiPath('/v1/environments/{environmentID}/analysis', { environmentID }),
        { ...maybeSignal(signal) },
      ),

    listDeployments: (projectID, signal) =>
      request<Schemas['DeploymentList']>(
        apiPath('/v1/projects/{projectID}/deployments', { projectID }),
        { ...maybeSignal(signal) },
      ).then((body) => body.deployments ?? []),
    createDeployment: (input) =>
      request<Deployment>(apiPath('/v1/deployments'), { method: 'POST', body: input }),
    getDeployment: (deploymentID, signal) =>
      request<Deployment>(apiPath('/v1/deployments/{deploymentID}', { deploymentID }), {
        ...maybeSignal(signal),
      }),
    deploymentLogs: (deploymentID, after, signal) =>
      request<LogPage>(
        `${apiPath('/v1/deployments/{deploymentID}/logs', { deploymentID })}?after=${after}`,
        { ...maybeSignal(signal) },
      ),
  }
}

/**
 * PathParams is the placeholders a path template declares, as a record the
 * caller has to fill: `/v1/projects/{projectID}` demands a projectID and
 * nothing besides.
 */
type PathParams<P extends string> = P extends `${string}{${infer Name}}${infer Rest}`
  ? Record<Name, string> & PathParams<Rest>
  : object

/** PathArgs drops the argument entirely on a template with no placeholders. */
type PathArgs<P extends string> = keyof PathParams<P> extends never ? [] : [params: PathParams<P>]

/**
 * apiPath fills a path template declared by the specification. Constraining
 * the template to `keyof paths` is what binds this module to the generated
 * schema: a route the control plane stops describing, or a parameter it
 * renames, fails to compile here rather than 404ing in a browser.
 */
function apiPath<P extends keyof paths & string>(template: P, ...args: PathArgs<P>): string {
  const [params = {}] = args as [Record<string, string | undefined>?]

  return template.replace(/\{(\w+)\}/g, (_, name: string) => {
    const value = params[name]
    if (value === undefined) {
      // Unreachable through the types; it exists because an ID read out of a
      // URL fragment reaches here as a plain string and an empty one would
      // otherwise silently request the collection instead of the member.
      throw new Error(`missing path parameter ${name}`)
    }
    return encodeURIComponent(value)
  })
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
