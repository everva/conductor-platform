// Session token storage for the cockpit (ADR-0026). The bearer token lives in
// sessionStorage (NOT localStorage) so it is scoped to the tab and cleared when
// the tab closes — a deliberately narrower XSS surface. The token is never
// logged. This module is the single place that touches storage.
const STORAGE_KEY = "conductor.token";

// loadToken reads the saved token, or null when none / storage is unavailable
// (e.g. SSR or a privacy-locked context).
export function loadToken(): string | null {
  try {
    return window.sessionStorage.getItem(STORAGE_KEY);
  } catch {
    return null;
  }
}

// saveToken persists the token for the current tab. Failures are swallowed:
// auth still works for the live session even if persistence is blocked.
export function saveToken(token: string): void {
  try {
    window.sessionStorage.setItem(STORAGE_KEY, token);
  } catch {
    // ignore — non-persistent session.
  }
}

// clearToken removes the saved token (sign-out).
export function clearToken(): void {
  try {
    window.sessionStorage.removeItem(STORAGE_KEY);
  } catch {
    // ignore.
  }
}
