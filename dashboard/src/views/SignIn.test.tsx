import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { SignIn } from './SignIn'

/** stubFetch replaces the global fetch, which is what SignIn's own client uses. */
function stubFetch(status: number, body: string) {
  const calls: RequestInit[] = []
  vi.stubGlobal('fetch', (_input: unknown, init: RequestInit = {}) => {
    calls.push(init)
    return Promise.resolve({
      ok: status >= 200 && status < 300,
      status,
      statusText: '',
      text: () => Promise.resolve(body),
    } as unknown as Response)
  })
  return calls
}

describe('SignIn', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('accepts a token that the control plane recognises', async () => {
    const calls = stubFetch(200, '{"projects":[]}')
    const onToken = vi.fn()
    render(<SignIn onToken={onToken} />)

    fireEvent.change(screen.getByLabelText('API token'), {
      target: { value: '  lp_c8f32792_secret  ' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Continue' }))

    await waitFor(() => expect(onToken).toHaveBeenCalledWith('lp_c8f32792_secret'))
    // Surrounding whitespace from a copy-paste is trimmed before it is used, not
    // sent as part of the credential.
    expect((calls[0]?.headers as Record<string, string>).Authorization).toBe(
      'Bearer lp_c8f32792_secret',
    )
  })

  it('rejects a token the control plane will not accept, rather than storing it', async () => {
    stubFetch(401, '{"error":{"code":"invalid_token","message":"the bearer token is not valid"}}')
    const onToken = vi.fn()
    render(<SignIn onToken={onToken} />)

    fireEvent.change(screen.getByLabelText('API token'), { target: { value: 'lp_truncated' } })
    fireEvent.click(screen.getByRole('button', { name: 'Continue' }))

    expect(await screen.findByText('the bearer token is not valid')).toBeDefined()
    expect(onToken).not.toHaveBeenCalled()
  })

  it('will not submit an empty token', () => {
    render(<SignIn onToken={vi.fn()} />)

    expect(screen.getByRole('button', { name: 'Continue' }).hasAttribute('disabled')).toBe(true)
  })
})
