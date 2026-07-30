const ACCESS_TOKEN_KEY = "nchat_at";

type AuthChangeListener = () => void;

const listeners = new Set<AuthChangeListener>();

/**
 * Counts how many times the stored session has been installed, replaced or
 * removed.
 *
 * It exists so a screen can tell "still the same session" from "a different
 * session" without inspecting the token. Presence alone cannot: replacing
 * session A with session B leaves a token present both before and after, so a
 * view keyed on presence would keep showing A's data under B's identity.
 *
 * Monotonic and never reused, so no two sessions of a page's lifetime share a
 * value — including two sessions of the same user, which must still invalidate
 * anything fetched under the first.
 *
 * It is a frontend cache key and nothing more: it is not persisted, not sent to
 * any server, and grants no authority. Every access decision stays server-side,
 * where the token is verified.
 */
let sessionGeneration = 0;

/** Current session generation. Changes on every setTokens and clearTokens. */
export function getSessionGeneration(): number {
  return sessionGeneration;
}

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
  // Bumped unconditionally, before notifying: a listener reading the generation
  // during the notification must already see the new one. Installing over an
  // existing token is a replacement, not a no-op, even when the token is
  // identical — sameness of string is not sameness of session.
  sessionGeneration += 1;
  notifyAuthChange();
}

export function getAccessToken(): string | null {
  return sessionStorage.getItem(ACCESS_TOKEN_KEY);
}

export function clearTokens(): void {
  sessionStorage.removeItem(ACCESS_TOKEN_KEY);
  // Logout ends a session as surely as login starts one, so it advances the
  // generation too. Without this, logging out and back in as the same user
  // could land on the generation the first session already used.
  sessionGeneration += 1;
  notifyAuthChange();
}

export function isAuthenticated(): boolean {
  return sessionStorage.getItem(ACCESS_TOKEN_KEY) !== null;
}
