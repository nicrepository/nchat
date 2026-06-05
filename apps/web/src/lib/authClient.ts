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
 * Returns true if the URL's pathname starts with an auth endpoint prefix.
 * Parses the URL to avoid false positives from query-string values that contain
 * auth path segments (e.g. /api/search?next=/api/auth/login).
 */
function isAuthUrl(url: string): boolean {
  let pathname: string;
  try {
    pathname = new URL(url, window.location.origin).pathname;
  } catch {
    // Non-parseable URL: fall back to startsWith on the raw string.
    return AUTH_SKIP_PREFIXES.some((prefix) => url.startsWith(prefix));
  }
  return AUTH_SKIP_PREFIXES.some((prefix) => pathname.startsWith(prefix));
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

// Shared in-flight refresh promise. Prevents concurrent refresh calls.
let inflightRefresh: Promise<TokenPair> | null = null;

/**
 * Authenticated wrapper around apiFetch.
 *
 * - Injects `Authorization: Bearer <access_token>` for every call.
 * - On 401 from a non-auth endpoint, attempts a single token refresh and retries once.
 * - Late-arrival guard: if the stored access token changed while the request was in
 *   flight (a concurrent request already refreshed), retries immediately with the
 *   newer token instead of triggering another refresh.
 * - Concurrency guard: concurrent 401s share one refresh promise; only one
 *   POST /api/auth/refresh is ever sent at a time.
 * - Session-binding guard: setTokens/clearTokens are no-ops if the stored refresh
 *   token changed while the refresh was in flight (e.g. logout or new login).
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

    // Acquire or share the in-flight refresh promise to avoid concurrent refresh calls.
    if (inflightRefresh === null) {
      inflightRefresh = refresh(refreshToken).finally(() => {
        inflightRefresh = null;
      });
    }

    let newTokens: TokenPair;
    try {
      newTokens = await inflightRefresh;
    } catch {
      // Session-binding guard: only clear tokens if the session hasn't changed
      // (e.g. a newer login) while the refresh was in flight.
      if (getRefreshToken() === refreshToken) {
        clearTokens();
      }
      throw err;
    }

    // Session-binding guard: only store tokens if the session hasn't changed while
    // the refresh was in flight. Concurrent waiters on the same promise will see the
    // RT already updated by the first settler and correctly skip this.
    if (getRefreshToken() === refreshToken) {
      setTokens(newTokens.accessToken, newTokens.refreshToken);
    }

    // Retry the original request exactly once with the current access token.
    const newAccessToken = getAccessToken();
    return apiFetch<T>(url, { ...init, headers: buildHeaders(init.headers, newAccessToken) });
  }
}
