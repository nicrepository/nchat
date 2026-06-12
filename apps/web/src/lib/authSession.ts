const ACCESS_TOKEN_KEY = "nchat_at";

type AuthChangeListener = () => void;

const listeners = new Set<AuthChangeListener>();

function notifyAuthChange(): void {
  for (const listener of listeners) {
    listener();
  }
}

/**
 * Register a callback that fires whenever tokens are set or cleared.
 * Returns an unsubscribe function.
 */
export function onAuthChange(listener: AuthChangeListener): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

/** Reset listener state. For test isolation only. */
export function _resetListeners(): void {
  listeners.clear();
}

/**
 * Persist the access token in sessionStorage.
 * The refresh token is managed server-side via an HttpOnly cookie — it is
 * never stored in Web Storage.
 */
export function setTokens(accessToken: string): void {
  sessionStorage.setItem(ACCESS_TOKEN_KEY, accessToken);
  notifyAuthChange();
}

export function getAccessToken(): string | null {
  return sessionStorage.getItem(ACCESS_TOKEN_KEY);
}

export function clearTokens(): void {
  sessionStorage.removeItem(ACCESS_TOKEN_KEY);
  notifyAuthChange();
}

export function isAuthenticated(): boolean {
  return sessionStorage.getItem(ACCESS_TOKEN_KEY) !== null;
}
