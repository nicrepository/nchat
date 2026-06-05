const ACCESS_TOKEN_KEY = "nchat_at";
const REFRESH_TOKEN_KEY = "nchat_rt";

type AuthChangeListener = () => void;

const listeners = new Set<AuthChangeListener>();

function notifyAuthChange(): void {
  for (const listener of listeners) {
    listener();
  }
}

/**
 * Register a callback that fires whenever tokens are set or cleared.
 * The callback receives no arguments — callers read state via isAuthenticated().
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

export function setTokens(accessToken: string, refreshToken: string): void {
  sessionStorage.setItem(ACCESS_TOKEN_KEY, accessToken);
  sessionStorage.setItem(REFRESH_TOKEN_KEY, refreshToken);
  notifyAuthChange();
}

export function getAccessToken(): string | null {
  return sessionStorage.getItem(ACCESS_TOKEN_KEY);
}

export function getRefreshToken(): string | null {
  return sessionStorage.getItem(REFRESH_TOKEN_KEY);
}

export function clearTokens(): void {
  sessionStorage.removeItem(ACCESS_TOKEN_KEY);
  sessionStorage.removeItem(REFRESH_TOKEN_KEY);
  notifyAuthChange();
}

export function isAuthenticated(): boolean {
  return sessionStorage.getItem(ACCESS_TOKEN_KEY) !== null;
}
