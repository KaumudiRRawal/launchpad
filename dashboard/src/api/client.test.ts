import { describe, expect, it } from 'vitest'

import { ApiError, fieldErrorOf, isUnauthorized, newClient } from './client'
import { project } from '../test/fixtures'

interface Call {
  url: string
  init: RequestInit
}

/**
 * harness wires a client to a scripted fetch and records what it was asked to
 * send, so a test can assert on the request as well as the response.
 */
function harness(respond: (call: Call) => { status: number; body: string }) {
  const calls: Call[] = []

  const client = newClient({
    token: 'lp_deadbeef_secret',
    fetch: (input, init = {}) => {
      const call = { url: String(input), init }
      calls.push(call)

      const { status, body } = respond(call)
      return Promise.resolve({
        ok: status >= 200 && status < 300,
        status,
        statusText: '',
        text: () => Promise.resolve(body),
        // The client only ever reads ok, status, statusText and text(), so a
        // stub of those four is a fair stand-in for a Response and avoids
        // depending on jsdom's fetch implementation.
      } as unknown as Response)
    },
  })

  return { client, calls }
}

function ok(body: unknown) {
  return { status: 200, body: JSON.stringify(body) }
}

describe('request shape', () => {
  it('authenticates and unwraps a collection', async () => {
    const { client, calls } = harness(() => ok({ projects: [project()] }))

    const projects = await client.listProjects()

    expect(projects).toHaveLength(1)
    expect(projects[0]?.slug).toBe('demo')
    expect(calls[0]?.url).toBe('/v1/projects')
    expect(calls[0]?.init.method).toBe('GET')
    expect(headers(calls[0]).Authorization).toBe('Bearer lp_deadbeef_secret')
  })

  it('sends a JSON body and declares its type', async () => {
    const { client, calls } = harness(() => ({ status: 201, body: JSON.stringify(project()) }))

    await client.createProject({
      slug: 'demo',
      name: 'Demo App',
      repo_url: 'https://github.com/example/demo',
    })

    expect(calls[0]?.init.method).toBe('POST')
    expect(headers(calls[0])['Content-Type']).toBe('application/json')
    expect(JSON.parse(String(calls[0]?.init.body))).toEqual({
      slug: 'demo',
      name: 'Demo App',
      repo_url: 'https://github.com/example/demo',
    })
  })

  it('accepts an empty body from a 204', async () => {
    const { client } = harness(() => ({ status: 204, body: '' }))

    await expect(client.deleteProject('proj-1')).resolves.toBeUndefined()
  })

  it('passes the log cursor as a query parameter', async () => {
    const { client, calls } = harness(() => ok({ logs: [], next_after: 12 }))

    const page = await client.deploymentLogs('dep-1', 12)

    expect(page.next_after).toBe(12)
    expect(calls[0]?.url).toBe('/v1/deployments/dep-1/logs?after=12')
  })

  it('escapes an identifier into the path', async () => {
    const { client, calls } = harness(() => ok(project()))

    await client.getProject('a b/../c')

    expect(calls[0]?.url).toBe('/v1/projects/a%20b%2F..%2Fc')
  })

  it('refuses to build a path from a missing identifier', () => {
    const { client } = harness(() => ok(project()))

    // The generated path types make this unreachable from TypeScript. A
    // JavaScript caller can still manage it, and requesting
    // /v1/projects/undefined would answer 404 as if the project were gone.
    expect(() => client.getProject(undefined as unknown as string)).toThrow(
      'missing path parameter projectID',
    )
  })

  it('prefixes a configured base URL', async () => {
    const calls: string[] = []
    const client = newClient({
      token: 't',
      baseUrl: 'https://launchpad.example.com/',
      fetch: (input) => {
        calls.push(String(input))
        return Promise.resolve({
          ok: true,
          status: 200,
          statusText: '',
          text: () => Promise.resolve('{"projects":[]}'),
        } as unknown as Response)
      },
    })

    await client.listProjects()

    // The trailing slash on the base must not survive into a doubled path.
    expect(calls[0]).toBe('https://launchpad.example.com/v1/projects')
  })
})

describe('error mapping', () => {
  const cases: {
    name: string
    status: number
    body: string
    wantCode: string
    wantMessage: string
    wantRequestID: string
    wantFields: number
  }[] = [
    {
      name: 'a not-found carries the code and request id',
      status: 404,
      body: JSON.stringify({
        error: { code: 'not_found', message: 'project not found', request_id: 'req-1' },
      }),
      wantCode: 'not_found',
      wantMessage: 'project not found',
      wantRequestID: 'req-1',
      wantFields: 0,
    },
    {
      name: 'a validation failure keeps every rejected field',
      status: 422,
      body: JSON.stringify({
        error: {
          code: 'validation_failed',
          message: 'one or more fields are invalid',
          request_id: 'req-2',
          fields: [
            { field: 'slug', message: 'is required' },
            { field: 'repo_url', message: 'must be an http or https clone URL' },
          ],
        },
      }),
      wantCode: 'validation_failed',
      wantMessage: 'one or more fields are invalid',
      wantRequestID: 'req-2',
      wantFields: 2,
    },
    {
      name: 'a body that is not the API envelope still reports the status',
      status: 502,
      body: '<html>Bad Gateway</html>',
      wantCode: 'unexpected_response',
      wantMessage: 'the control plane returned 502',
      wantRequestID: '',
      wantFields: 0,
    },
    {
      name: 'an envelope missing its fields does not crash',
      status: 500,
      body: JSON.stringify({ error: {} }),
      wantCode: 'unknown',
      wantMessage: 'request failed with 500',
      wantRequestID: '',
      wantFields: 0,
    },
  ]

  for (const tc of cases) {
    it(tc.name, async () => {
      const { client } = harness(() => ({ status: tc.status, body: tc.body }))

      const err = await client.listProjects().catch((e: unknown) => e)

      expect(err).toBeInstanceOf(ApiError)
      const apiErr = err as ApiError
      expect(apiErr.status).toBe(tc.status)
      expect(apiErr.code).toBe(tc.wantCode)
      expect(apiErr.message).toBe(tc.wantMessage)
      expect(apiErr.requestId).toBe(tc.wantRequestID)
      expect(apiErr.fields).toHaveLength(tc.wantFields)
    })
  }

  it('exposes one field at a time for a form to render', async () => {
    const { client } = harness(() => ({
      status: 422,
      body: JSON.stringify({
        error: {
          code: 'validation_failed',
          message: 'one or more fields are invalid',
          fields: [{ field: 'slug', message: 'is required' }],
        },
      }),
    }))

    const err = await client.listProjects().catch((e: unknown) => e)

    expect(fieldErrorOf(err, 'slug')).toBe('is required')
    expect(fieldErrorOf(err, 'name')).toBeUndefined()
    expect(fieldErrorOf(new Error('not an api error'), 'slug')).toBeUndefined()
  })

  it('recognises a rejected token', async () => {
    const { client } = harness(() => ({
      status: 401,
      body: JSON.stringify({ error: { code: 'unauthorized', message: 'invalid token' } }),
    }))

    const err = await client.listProjects().catch((e: unknown) => e)

    expect(isUnauthorized(err)).toBe(true)
    expect(isUnauthorized(new Error('nope'))).toBe(false)
  })

  it('reports an unreachable control plane rather than leaking a TypeError', async () => {
    const client = newClient({
      token: 't',
      fetch: () => Promise.reject(new TypeError('Failed to fetch')),
    })

    const err = await client.listProjects().catch((e: unknown) => e)

    expect(err).toBeInstanceOf(ApiError)
    expect((err as ApiError).code).toBe('network_error')
    expect((err as ApiError).status).toBe(0)
  })

  it('passes an abort through untouched so a cancelled poll stays quiet', async () => {
    const client = newClient({
      token: 't',
      fetch: () => Promise.reject(new DOMException('aborted', 'AbortError')),
    })

    const err = await client.listProjects().catch((e: unknown) => e)

    expect(err).toBeInstanceOf(DOMException)
    expect((err as DOMException).name).toBe('AbortError')
  })
})

function headers(call: Call | undefined): Record<string, string> {
  return (call?.init.headers ?? {}) as Record<string, string>
}
