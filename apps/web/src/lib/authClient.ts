import { refresh, type TokenPair } from "../auth/authApi";
import { ApiRequestError, apiFetch } from "./api";
import { clearTokens, getAccessToken, getRefreshToken, setTokens } from "./authSession";

/**
 * Auth endpoint path prefixes (by pathname) excluded from auto-refresh.
 * These functions call apiFetch directly, so they never reach authenticatedFetch
 * in normal usage — this list is a safety net.
 */
export const AUTH_SKIP_PREFIXES = [
  "/api/auth/login",
  "/api/auth/refresh",
  "/api/auth/password",
  "/api/auth/oidc",
  "/api/auth/invites",
  "/api/auth/logout",
] as const;

/**
 * Returns true if the URL's pathname matches an auth endpoint prefix.
 * Uses exact-match or prefix+"/" boundary so /api/auth/loginExtra does NOT match
 * /api/auth/login, but /api/auth/login/sub does.
 * Parses the URL to avoid false positives from query-string values that contain
 * auth path segments (e.g. /api/search?next=/api/auth/login).
 */
function isAuthUrl(url: string): boolean {
  let pathname: string;
  try {
    pathname = new URL(url, window.location.origin).pathname;
  } catch {
    pathname = url;
  }
  return AUTH_SKIP_PREFIXES.some(
    (prefix) => pathname === prefix || pathname.startsWith(prefix + "/"),
  );
}

/**
 * Normalize any HeadersInit form (plain object, Headers instance, or tuple array)
 * into a plain Record<string, string>, injecting the Authorization header when an
 * access token is provided. Creates a new Headers instance so the caller-provided
 * object is never mutated.
 *
 * Note: WHATWG Headers normalizes names to lowercase, so the returned object uses
 * lowercase header names (e.g. "authorization", "content-type").
 */
function buildHeaders(
  existing: HeadersInit | undefined,
  accessToken: string | null,
): Record<string, string> {
  const headers = new Headers(existing);
  if (accessToken !== null) {
    headers.set("authorization", `Bearer ${accessToken}`);
  }
  return Object.fromEntries(headers.entries());
}

/**
 * Generation-bound in-flight refresh state. Each session generation (identified by
 * its refresh token) has at most one pending refresh call. Cross-session requests
 * (different refresh token) each get their own independent refresh call and cannot
 * observe or mutate each other's in-flight state.
 */
type InflightRefresh = {
  refreshToken: string;
  promise: Promise<TokenPair>;
};

let inflightRefresh: InflightRefresh | null = null;

/**
 * Authenticated wrapper around apiFetch.
 *
 * - Injects `Authorization: Bearer <access_token>` for every call.
 * - On 401 from a non-auth endpoint, attempts a single token refresh and retries once.
 * - Late-arrival guard: if the stored access token changed while the request was in
 *   flight (a concurrent request already refreshed), retries immediately with the
 *   newer token instead of triggering another refresh.
 * - Generation-bound concurrency guard: concurrent 401s sharing the same refresh
 *   token share one refresh promise. Requests with a different refresh token (cross-
 *   session) each create their own refresh call and cannot affect each other.
 * - Session-binding guard: after the refresh settles, actions are conditional on
 *   the stored refresh token:
 *     - First settler (RT unchanged): calls setTokens, then retries.
 *     - Concurrent waiter on same refresh (RT === newTokens.refreshToken): falls
 *       through to retry without re-calling setTokens.
 *     - External session change (logout / newer login): throws original 401 without
 *       retrying and without touching the new session's tokens.
 *
 * Refresh is request-driven only — no background timer, no keepalive.
 * Backend remains authoritative for idle/absolute session expiry.
 *
 * NOTE: `init.body` must be a non-stream type (string, Blob, ArrayBuffer, etc.).
 * ReadableStream bodies cannot be safely reused across a retry.
 */
export async function authenticatedFetch<T>(url: string, init: RequestInit): Promise<T> {
  // Capture the access token BEFORE the first call so the late-arrival guard can
  // detect whether a concurrent request already refreshed between the call and 401.
  const originalAccessToken = getAccessToken();

  try {
    return await apiFetch<T>(url, {
      ...init,
      headers: buildHeaders(init.headers, originalAccessToken),
    });
  } catch (err) {
    if (!(err instanceof ApiRequestError) || err.status !== 401 || isAuthUrl(url)) {
      throw err;
    }

    // Late-arrival guard: if the stored access token already changed, a concurrent
    // request refreshed while this one was in flight. Retry once with the newer
    // token rather than triggering a second refresh.
    const currentAccessToken = getAccessToken();
    if (currentAccessToken !== originalAccessToken) {
      return apiFetch<T>(url, { ...init, headers: buildHeaders(init.headers, currentAccessToken) });
    }

    // 401 on a non-auth endpoint: attempt a single refresh.
    const refreshToken = getRefreshToken();
    if (refreshToken === null) {
      clearTokens();
      throw err;
    }

    // Acquire or share a generation-bound in-flight refresh. Only share if the
    // stored refresh token matches; cross-session requests each get their own call.
    if (!inflightRefresh || inflightRefresh.refreshToken !== refreshToken) {
      inflightRefresh = { refreshToken, promise: refresh(refreshToken) };
    }
    const captured = inflightRefresh;

    let newTokens: TokenPair;
    try {
      newTokens = await captured.promise;
    } catch {
      if (inflightRefresh === captured) inflightRefresh = null;
      // Session-binding guard: only clear tokens if this session is still active.
      if (getRefreshToken() === refreshToken) clearTokens();
      throw err;
    }

    // Clear in-flight state for this generation after the promise settled and before
    // committing tokens, so no new 401 for this session starts an extra refresh.
    if (inflightRefresh === captured) inflightRefresh = null;

    // Three-way session-binding guard:
    const currentRT = getRefreshToken();
    if (currentRT === refreshToken) {
      // First settler: commit the new tokens for this session generation.
      setTokens(newTokens.accessToken, newTokens.refreshToken);
    } else if (currentRT === newTokens.refreshToken) {
      // Concurrent waiter on the same session: first settler already committed the
      // tokens. Fall through to retry with the now-current access token.
    } else {
      // External session change (logout or newer login) while refresh was in flight.
      // Do not retry under the new/empty session; return the original 401.
      throw err;
    }

    // Retry the original request exactly once with the current access token.
    const newAccessToken = getAccessToken();
    return apiFetch<T>(url, { ...init, headers: buildHeaders(init.headers, newAccessToken) });
  }
}
