import { refresh, type TokenResponse } from "../auth/authApi";
import { ApiRequestError, apiFetch, type ResponseParser } from "./api";
import { clearTokens, getAccessToken, setTokens } from "./authSession";

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
 * Normalize any HeadersInit form into a plain Record<string, string>, injecting
 * the Authorization header when an access token is provided.
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
 * Records the most recent successful refresh rotation committed by this module.
 * Used by the late-arrival guard: maps the expired AT to the newly-issued AT.
 * The refresh token is HttpOnly and never stored in JS.
 */
type AppliedRefreshRotation = {
  fromAccessToken: string;
  toAccessToken: string;
};

/**
 * Generation-bound in-flight refresh state. Each session generation (identified
 * by the expired access token that triggered it) has at most one pending refresh
 * call. Cross-session requests each get their own independent refresh call.
 */
type InflightRefresh = {
  accessToken: string | null; // the expired AT that triggered this refresh
  promise: Promise<TokenResponse>;
};

let lastAppliedRefreshRotation: AppliedRefreshRotation | null = null;
let inflightRefresh: InflightRefresh | null = null;

/** Reset module-level concurrency state. For test isolation only. */
export function _resetState(): void {
  inflightRefresh = null;
  lastAppliedRefreshRotation = null;
}

/**
 * How a request actually reaches the network.
 *
 * It exists so the upload transport (apiUpload, which reports progress) can be
 * used *through* this module instead of beside it. Everything below — the
 * bearer header, the single refresh-and-retry on 401, the late-arrival guard and
 * the session-binding guard — is the same for both transports, and a second copy
 * of that logic living in the file client is exactly what this avoids.
 */
export type RequestSender = <T>(
  url: string,
  init: RequestInit,
  parse?: ResponseParser<T>,
) => Promise<T>;

/**
 * Authenticated wrapper around apiFetch.
 *
 * - Injects `Authorization: Bearer <access_token>` for every call.
 * - On 401 from a non-auth endpoint, attempts a single token refresh and retries once.
 * - The refresh token is HttpOnly and sent automatically by the browser via cookie;
 *   it is never read or stored in JavaScript.
 * - Late-arrival guard: if the stored access token changed while the request was in
 *   flight (a concurrent request already refreshed), retries immediately with the
 *   newer token instead of triggering another refresh.
 * - Generation-bound concurrency guard: concurrent 401s sharing the same expired AT
 *   share one refresh promise. Requests with a different AT each create their own.
 * - Session-binding guard: after the refresh settles, actions are conditional on
 *   the stored access token:
 *     - First settler (AT unchanged): calls setTokens, then retries.
 *     - Concurrent waiter (AT === newTokens.accessToken): falls through to retry.
 *     - External session change (logout / newer login): throws original 401.
 */
export async function authenticatedFetch<T>(
  url: string,
  init: RequestInit,
  parse?: ResponseParser<T>,
  send: RequestSender = apiFetch,
): Promise<T> {
  const originalAccessToken = getAccessToken();

  try {
    return await send<T>(
      url,
      {
        ...init,
        headers: buildHeaders(init.headers, originalAccessToken),
      },
      parse,
    );
  } catch (err) {
    if (!(err instanceof ApiRequestError) || err.status !== 401 || isAuthUrl(url)) {
      throw err;
    }

    // Late-arrival guard: if the stored AT changed, only retry when the change is
    // a known same-generation rotation committed by this module.
    const currentAccessToken = getAccessToken();
    if (currentAccessToken !== originalAccessToken) {
      const rotation = lastAppliedRefreshRotation;
      if (
        originalAccessToken !== null &&
        rotation !== null &&
        rotation.fromAccessToken === originalAccessToken &&
        currentAccessToken === rotation.toAccessToken
      ) {
        return send<T>(
          url,
          {
            ...init,
            headers: buildHeaders(init.headers, currentAccessToken),
          },
          parse,
        );
      }
      // Token changed due to logout or a different-session login: throw original 401.
      throw err;
    }

    // 401 on a non-auth endpoint: attempt a single refresh via the HttpOnly cookie.
    // Acquire or share a generation-bound in-flight refresh keyed by the expired AT.
    if (!inflightRefresh || inflightRefresh.accessToken !== originalAccessToken) {
      inflightRefresh = { accessToken: originalAccessToken, promise: refresh() };
    }
    const captured = inflightRefresh;

    let newTokens: TokenResponse;
    try {
      newTokens = await captured.promise;
    } catch {
      try {
        // Session-binding guard: only clear tokens if this session is still active.
        if (getAccessToken() === originalAccessToken) {
          clearTokens();
          if (lastAppliedRefreshRotation?.fromAccessToken === originalAccessToken) {
            lastAppliedRefreshRotation = null;
          }
        }
        throw err;
      } finally {
        if (inflightRefresh === captured) inflightRefresh = null;
      }
    }

    // Three-way session-binding guard.
    try {
      const currentAT = getAccessToken();
      if (currentAT === originalAccessToken) {
        // First settler: commit the new access token and record the rotation.
        setTokens(newTokens.accessToken);
        lastAppliedRefreshRotation = {
          fromAccessToken: originalAccessToken ?? "",
          toAccessToken: newTokens.accessToken,
        };
      } else if (currentAT === newTokens.accessToken) {
        // Concurrent waiter: first settler already committed the tokens.
      } else {
        // External session change (logout or newer login) while refresh was in flight.
        throw err;
      }
    } finally {
      if (inflightRefresh === captured) inflightRefresh = null;
    }

    // Retry the original request once with the current access token.
    const newAccessToken = getAccessToken();
    return send<T>(url, { ...init, headers: buildHeaders(init.headers, newAccessToken) }, parse);
  }
}
