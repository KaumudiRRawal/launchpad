const storageKey = 'launchpad.token'

/**
 * The control plane has no login endpoint: the first credential comes from
 * `make bootstrap`, so the operator pastes a token rather than signing in. It
 * is held in localStorage so a reload does not mean pasting it again, which is
 * the same exposure as any single-page app holding a session token — a script
 * running on this origin can read it. That is acceptable for an operator tool
 * with no third-party scripts on the page, and stops being acceptable the
 * moment there is a real login to issue a cookie instead.
 */
export function loadToken(): string {
  try {
    return window.localStorage.getItem(storageKey) ?? ''
  } catch {
    // Storage can be unavailable — private browsing, or a blocked origin. A
    // dashboard that still works for one session is better than a blank page.
    return ''
  }
}

export function saveToken(token: string): void {
  try {
    window.localStorage.setItem(storageKey, token)
  } catch {
    // Nothing to do: the token stays in memory for this session only.
  }
}

export function clearToken(): void {
  try {
    window.localStorage.removeItem(storageKey)
  } catch {
    // As above.
  }
}
