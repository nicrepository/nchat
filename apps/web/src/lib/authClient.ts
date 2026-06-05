import { refresh, type TokenPair } from "../auth/authApi";
import { ApiRequestError, apiFetch } from "./api";
import { clearTokens, getAccessToken, getRefreshToken, setTokens } from "./authSession";

/**
 * Auth endpoint path segments excluded from auto-refresh.
 * These functions already use apiFetch directly, so they never reach authenticatedFetch
 * in normal usage — this list is a safety net.
 */
export const AUTH_SKIP_PATHS = [
  "/auth/login",
  "/auth/refresh",
  "/auth/password",
  "/auth/oidc",
  "/auth/invites",
  "/auth/logout",
] as const;

function isAuthUrl(url: string): boolean {
  return AUTH_SKIP_PATHS.some((segment) => url.includes(segment));
}

// Shared in-flight refresh promise. Prevents concurrent refresh calls.
let inflightRefresh: Promise<TokenPair> | null = null;

/**
 * Authenticated wrapper around apiFetch.
 *
 * Injects `Authorization: Bearer <access_token>` for every call.
 * On 401 from a non-auth endpoint, attempts a single token refresh and retries once.
 *
 * Refresh is request-driven only — no background timer, no keepalive.
 * Backend remains authoritative for idle/absolute session expiry.
 *
 * NOTE: `init.body` must be a non-stream type (string, Blob, ArrayBuffer, etc.).
 * ReadableStream bodies are not safe to reuse across a retry.
 */
export async function authenticatedFetch<T>(url: string, init: RequestInit): Promise<T> {
  const accessToken = getAccessToken();
  const headersWithToken: HeadersInit = {
    ...init.headers,
    ...(accessToken !== null ? { Authorization: `Bearer ${accessToken}` } : {}),
  };

  try {
    return await apiFetch<T>(url, { ...init, headers: headersWithToken });
  } catch (err) {
    if (!(err instanceof ApiRequestError) || err.status !== 401 || isAuthUrl(url)) {
      throw err;
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
      clearTokens();
      throw err;
    }

    setTokens(newTokens.accessToken, newTokens.refreshToken);

    // Retry the original request exactly once with the new access token.
    const newAccessToken = getAccessToken();
    return apiFetch<T>(url, {
      ...init,
      headers: {
        ...init.headers,
        ...(newAccessToken !== null ? { Authorization: `Bearer ${newAccessToken}` } : {}),
      },
    });
  }
}
