// The admin token lives in sessionStorage: it survives reloads but not closing
// the tab, and is never written to localStorage or cookies.
const KEY = "gateway.adminToken";

export function loadToken(): string | null {
  try {
    return sessionStorage.getItem(KEY);
  } catch {
    return null; // storage blocked (privacy mode): the user signs in each load
  }
}

export function saveToken(token: string): void {
  try {
    sessionStorage.setItem(KEY, token);
  } catch {
    // not persisted; the in-memory session still works
  }
}

export function clearToken(): void {
  try {
    sessionStorage.removeItem(KEY);
  } catch {
    // nothing stored
  }
}
