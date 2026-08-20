/**
 * The single HTTP client of the administrative console.
 *
 * Two properties matter more than anything else here:
 *
 *  - `credentials: "include"` is what sends the HttpOnly administrative cookie.
 *    That cookie is the console's only credential; there is no token in
 *    `localStorage`, none in `sessionStorage`, and none in module state waiting
 *    to be read by injected script.
 *  - the CSRF token is echoed on every mutating request. It is held in memory
 *    only, is bound server-side to one session, and is refreshed from the
 *    bootstrap payload on every load.
 */

const ADMIN_BASE = "/api/admin";
const CSRF_HEADER = "X-NChat-Admin-CSRF";

export class AdminApiError extends Error {
  readonly status: number;
  readonly code: string;

  constructor(status: number, code: string, message: string) {
    super(message);
    this.name = "AdminApiError";
    this.status = status;
    this.code = code;
  }
}

/**
 * The current CSRF token.
 *
 * Deliberately module state and not Web Storage: it must not survive a tab
 * being closed, and it must not be readable by another origin's frame. Losing
 * it costs one bootstrap round trip, which is what happens on every load
 * anyway.
 */
let csrfToken: string | null = null;

export function setCSRFToken(token: string | null): void {
  csrfToken = token;
}

/** Test-only reset so one spec cannot inherit another's token. */
export function _resetCSRFToken(): void {
  csrfToken = null;
}

interface Envelope<T> {
  data?: T;
  error?: { code: string; message: string };
}

/**
 * Raised when a 2xx response is not the envelope the Admin API promises.
 *
 * It exists so the console fails visibly instead of continuing with
 * `undefined` cast to whatever the caller expected. A gateway error page, a
 * truncated body or a proxy that rewrote the response all land here rather than
 * surfacing three screens later as "cannot read property of undefined".
 */
const ERR_INVALID_RESPONSE = "invalid_response";

function isMutating(method: string): boolean {
  return method !== "GET" && method !== "HEAD" && method !== "OPTIONS";
}

async function readError(response: Response): Promise<AdminApiError> {
  let code = "unknown_error";
  let message = response.statusText || "Request failed";
  try {
    const body = (await response.json()) as Envelope<unknown>;
    if (body.error) {
      code = body.error.code;
      message = body.error.message;
    }
  } catch {
    // A non-JSON error body (a gateway page, for instance) leaves the defaults.
  }
  return new AdminApiError(response.status, code, message);
}

export async function adminFetch<T>(path: string, init: RequestInit = {}): Promise<T> {
  const method = (init.method ?? "GET").toUpperCase();
  const headers = new Headers(init.headers);
  if (!headers.has("Content-Type") && init.body !== undefined) {
    headers.set("Content-Type", "application/json");
  }
  if (isMutating(method) && csrfToken !== null) {
    headers.set(CSRF_HEADER, csrfToken);
  }

  let response: Response;
  try {
    response = await fetch(`${ADMIN_BASE}${path}`, {
      ...init,
      method,
      headers,
      credentials: "include",
    });
  } catch {
    throw new AdminApiError(0, "network_error", "Falha de rede");
  }

  if (!response.ok) {
    throw await readError(response);
  }
  // 204 is the documented empty answer — logout is the only one today — and is
  // the single case where the absence of a body is correct.
  if (response.status === 204) {
    return undefined as T;
  }
  return readEnvelope<T>(response);
}

/**
 * Reads a successful response as the envelope the Admin API promises.
 *
 * Split out from adminFetch so the transport concerns (headers, credentials,
 * CSRF) and the payload contract stay separately readable and separately
 * testable — the checks below are the ones that decide whether the console
 * continues with real data or with `undefined` cast to whatever the caller
 * declared.
 */
async function readEnvelope<T>(response: Response): Promise<T> {
  const invalid = (message: string) =>
    new AdminApiError(response.status, ERR_INVALID_RESPONSE, message);

  let body: Envelope<T>;
  try {
    body = (await response.json()) as Envelope<T>;
  } catch {
    throw invalid("Resposta inválida do servidor");
  }
  if (body === null || typeof body !== "object" || Array.isArray(body)) {
    throw invalid("Resposta inválida do servidor");
  }
  // A 2xx that carries an error envelope, or no data at all, is not a success
  // the caller can use. The services always wrap a payload in `data`; anything
  // else means the response did not come from the Admin API as it is deployed.
  if (body.data === undefined || body.data === null) {
    throw invalid("Resposta sem dados");
  }
  return body.data;
}
