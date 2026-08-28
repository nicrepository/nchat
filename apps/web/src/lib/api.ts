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

/**
 * Reads a successful response into the shape the caller wants.
 *
 * It exists for the responses that are not JSON — today the attachment preview,
 * which is an image. Without it those callers would have to reimplement the
 * bearer-token injection and the refresh-and-retry dance in authClient just to
 * reach `response.blob()`, and a second copy of that logic is the last thing
 * this codebase needs.
 *
 * Only the success path is pluggable. Error bodies stay on the one path that
 * knows how to read the services' `{error:{code,message}}` envelope.
 */
export type ResponseParser<T> = (response: Response) => Promise<T>;

/**
 * Reads the services' `{error:{code,message}}` envelope out of a raw body.
 *
 * Shared by the two transports so a 413 from the gateway and a 413 from
 * file-service are classified identically no matter which one carried it.
 */
function requestError(status: number, statusText: string, body: string): ApiRequestError {
  let code = "unknown_error";
  let message = statusText || "Request failed";
  try {
    const parsed = JSON.parse(body) as { error?: ApiError };
    if (parsed.error) {
      code = parsed.error.code;
      message = parsed.error.message;
    }
  } catch {
    // leave defaults
  }
  return new ApiRequestError(status, code, message);
}

export async function apiFetch<T>(
  url: string,
  init: RequestInit,
  parse?: ResponseParser<T>,
): Promise<T> {
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
    throw requestError(response.status, response.statusText, await response.text().catch(() => ""));
  }

  // A caller that knows how to read this response reads it. The JSON rules
  // below are the default, not a filter the parser has to survive: an image
  // response would fall through them as undefined.
  if (parse) {
    return parse(response);
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

/** Bytes handed to the network so far, out of the total to send. */
export interface UploadProgress {
  loaded: number;
  total: number;
}

/**
 * Shortest gap between two progress reports handed to a caller.
 *
 * `xhr.upload` fires as fast as the socket drains — dozens of times a second on
 * a fast link — and every report a caller receives becomes a React render of the
 * composer. A bar cannot be read faster than this anyway, so the reports are
 * paced here, at the boundary that produces them, rather than in each consumer.
 *
 * Throttling is only ever allowed to drop *intermediate* measurements: the first
 * report goes out immediately so the bar appears at once, and the last one is
 * flushed when the request completes, so the value the user is left looking at
 * is always one the transport actually observed.
 */
const UPLOAD_PROGRESS_INTERVAL_MS = 150;

/**
 * Sends a request over XMLHttpRequest instead of fetch, so the caller can watch
 * the bytes leave (RF-30).
 *
 * # Why a second transport exists at all
 *
 * fetch() reports nothing about a request body in flight. The streaming upload
 * that would — a ReadableStream body with `duplex: "half"` — needs HTTP/2 and is
 * unimplemented in Safari and Firefox, so on the browsers this product supports
 * there is exactly one way to learn how much of a file has been sent, and it is
 * `xhr.upload`'s progress event. The alternative is a progress bar animated from
 * a timer, which is a number this client invented about a transfer it cannot
 * see — so no bar at all is drawn unless these events supply one.
 *
 * This is deliberately not a general-purpose client. It is the upload transport,
 * used through authenticatedFetch exactly like apiFetch is: the bearer header,
 * the refresh-and-retry on 401 and the session guards all still live there and
 * are not reimplemented here. What is reproduced, and must stay reproduced, is
 * apiFetch's *contract*: the same `{error:{code,message}}` envelope reading, the
 * same ApiRequestError(0, "network_error") for a transport failure, the same
 * AbortError for a cancel, and the same JSON handling on success. A caller must
 * not be able to tell which transport carried its request except by asking for
 * progress.
 */
export function apiUpload<T>(
  url: string,
  init: RequestInit,
  onProgress?: (progress: UploadProgress) => void,
  parse?: ResponseParser<T>,
): Promise<T> {
  return new Promise<T>((resolve, reject) => {
    const signal = init.signal ?? undefined;
    if (signal?.aborted) {
      reject(new DOMException("Aborted", "AbortError"));
      return;
    }

    const xhr = new XMLHttpRequest();
    xhr.open(init.method ?? "POST", url, true);

    const headers = new Headers(init.headers);
    // FormData must set its own multipart Content-Type (with the boundary), and
    // XHR only does that when the header is left unset — same rule as apiFetch's.
    const isFormData = typeof FormData !== "undefined" && init.body instanceof FormData;
    if (!isFormData && !headers.has("Content-Type")) {
      headers.set("Content-Type", "application/json");
    }
    headers.forEach((value, name) => xhr.setRequestHeader(name, value));

    const abortUpload = () => xhr.abort();
    signal?.addEventListener("abort", abortUpload, { once: true });

    // ── Paced progress ────────────────────────────────────────────────────────
    //
    // `pending` holds the newest measurement that has not been handed out yet,
    // and `timer` is what will hand it out. There is at most one of each, and
    // every terminal path below goes through `settled`, so no timer outlives the
    // request that armed it.
    let pending: UploadProgress | null = null;
    let timer: number | null = null;
    let lastReportAt = 0;
    let lastLoaded = -1;

    const clearProgressTimer = () => {
      if (timer === null) return;
      window.clearTimeout(timer);
      timer = null;
    };

    const report = (progress: UploadProgress) => {
      // The counts a caller sees only ever move forward. XHR does not send them
      // out of order, but a trailing flush and a fresh event can otherwise race
      // to be last, and a bar that jumps backwards is worse than one that lags.
      if (progress.loaded < lastLoaded) return;
      lastLoaded = progress.loaded;
      lastReportAt = Date.now();
      pending = null;
      onProgress?.(progress);
    };

    /** Hands out whatever the throttle is still holding. */
    const flushProgress = () => {
      clearProgressTimer();
      if (pending) report(pending);
    };

    const settled = () => {
      clearProgressTimer();
      signal?.removeEventListener("abort", abortUpload);
    };

    if (onProgress) {
      xhr.upload.addEventListener("progress", (event) => {
        // A body of unknown length reports no total. Publishing zero — or
        // guessing one — would be a denominator this client made up, so nothing
        // is reported and the caller stays indeterminate.
        if (!event.lengthComputable || event.total <= 0) return;
        const measured: UploadProgress = { loaded: event.loaded, total: event.total };
        const waited = Date.now() - lastReportAt;
        // The first measurement is never delayed: it is what turns an
        // indeterminate state into a bar.
        if (lastLoaded < 0 || waited >= UPLOAD_PROGRESS_INTERVAL_MS) {
          clearProgressTimer();
          report(measured);
          return;
        }
        // Inside the window: keep only the newest, and make sure something will
        // publish it even if this turns out to be the last event of the upload.
        pending = measured;
        if (timer === null) {
          timer = window.setTimeout(flushProgress, UPLOAD_PROGRESS_INTERVAL_MS - waited);
        }
      });
    }

    xhr.addEventListener("abort", () => {
      settled();
      reject(new DOMException("Aborted", "AbortError"));
    });
    // Both mean the same thing fetch's rejection means: the request never got an
    // answer, and no status exists to classify.
    const failed = () => {
      settled();
      reject(new ApiRequestError(0, "network_error", "Network error"));
    };
    xhr.addEventListener("error", failed);
    xhr.addEventListener("timeout", failed);

    xhr.addEventListener("load", () => {
      // The bytes really did leave, whatever the server then answered, so the
      // last measurement is published before the outcome is. This is what makes
      // the final progress impossible to lose to the throttle.
      flushProgress();
      settled();
      const body = xhr.responseText;
      if (xhr.status < 200 || xhr.status >= 300) {
        reject(requestError(xhr.status, xhr.statusText, body));
        return;
      }
      if (parse) {
        const contentType = xhr.getResponseHeader("content-type");
        // The parser runs inside an XHR event listener, not inside the promise
        // executor, so a synchronous throw here escapes into the event loop
        // instead of rejecting: the caller would wait forever on a promise
        // nothing can settle. apiFetch cannot have this problem — it is an async
        // function, where a throw is already a rejection.
        try {
          resolve(
            parse(
              // The status is carried through rather than defaulted to 200: a
              // parser that branches on it — the way apiFetch's callers may —
              // must see the status the server actually sent. A body-less status
              // (204 and friends) refuses to be constructed with a body, and an
              // empty responseText is exactly that absence.
              new Response(body === "" ? null : body, {
                status: xhr.status,
                statusText: xhr.statusText,
                headers: contentType ? { "content-type": contentType } : {},
              }),
            ),
          );
        } catch (error) {
          reject(error);
        }
        return;
      }
      const contentType = xhr.getResponseHeader("content-type") ?? "";
      if (
        xhr.status === 204 ||
        xhr.status === 202 ||
        body === "" ||
        !contentType.includes("application/json")
      ) {
        resolve(undefined as T);
        return;
      }
      try {
        resolve(JSON.parse(body) as T);
      } catch {
        reject(new ApiRequestError(xhr.status, "unknown_error", "Request failed"));
      }
    });

    xhr.send((init.body ?? null) as XMLHttpRequestBodyInit | null);
  });
}
