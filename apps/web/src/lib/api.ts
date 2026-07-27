export interface ApiError {
  code: string;
  message: string;
}

export class ApiRequestError extends Error {
  readonly status: number;
  readonly code: string;

  constructor(status: number, code: string, message: string) {
    super(message);
    this.name = "ApiRequestError";
    this.status = status;
    this.code = code;
  }
}

export async function apiFetch<T>(url: string, init: RequestInit): Promise<T> {
  let response: Response;
  // Headers merges case-insensitively, which a plain object spread does not: a
  // caller-normalized "content-type" next to the literal "Content-Type" default
  // are two distinct object keys, and fetch appends both into
  // "application/json, application/json" — a value the services reject with 415.
  // Normalizing here also keeps every HeadersInit form (object literal, Headers
  // instance, tuple array) instead of silently dropping the non-literal ones.
  const headers = new Headers(init.headers);
  // FormData must set its own multipart Content-Type (with the boundary), so the
  // JSON default is only applied to non-FormData bodies, and never overwrites a
  // content type the caller already chose.
  const isFormData = typeof FormData !== "undefined" && init.body instanceof FormData;
  if (!isFormData && !headers.has("Content-Type")) {
    headers.set("Content-Type", "application/json");
  }
  try {
    response = await fetch(url, { ...init, headers });
  } catch {
    throw new ApiRequestError(0, "network_error", "Network error");
  }

  if (!response.ok) {
    let code = "unknown_error";
    let message = response.statusText || "Request failed";
    try {
      const body = (await response.json()) as { error?: ApiError };
      if (body.error) {
        code = body.error.code;
        message = body.error.message;
      }
    } catch {
      // leave defaults
    }
    throw new ApiRequestError(response.status, code, message);
  }

  const contentType = response.headers.get("content-type") ?? "";
  if (
    response.status === 204 ||
    response.status === 202 ||
    response.headers.get("content-length") === "0" ||
    !contentType.includes("application/json")
  ) {
    return undefined as T;
  }

  return response.json() as Promise<T>;
}
