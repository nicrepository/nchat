import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { ApiRequestError, apiFetch, apiUpload } from "./api";

const mockFetch = vi.fn<typeof fetch>();
vi.stubGlobal("fetch", mockFetch);

afterEach(() => {
  vi.resetAllMocks();
});

/** Headers actually handed to fetch, read the way the network layer reads them. */
function sentHeaders(call = 0): Headers {
  const [, init] = mockFetch.mock.calls[call] as [string, RequestInit];
  return new Headers(init.headers);
}

describe("apiFetch", () => {
  it("returns parsed JSON on 200", async () => {
    mockFetch.mockResolvedValue(
      new Response(JSON.stringify({ ok: true }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    );
    const result = await apiFetch<{ ok: boolean }>("/test", { method: "GET" });
    expect(result).toEqual({ ok: true });
  });

  it("returns undefined on 204", async () => {
    mockFetch.mockResolvedValue(new Response(null, { status: 204 }));
    const result = await apiFetch<void>("/test", { method: "POST", body: "{}" });
    expect(result).toBeUndefined();
  });

  it("returns undefined on 202", async () => {
    mockFetch.mockResolvedValue(new Response(null, { status: 202 }));
    const result = await apiFetch<void>("/test", { method: "POST", body: "{}" });
    expect(result).toBeUndefined();
  });

  it("throws ApiRequestError with code from error envelope on 401", async () => {
    mockFetch.mockResolvedValue(
      new Response(
        JSON.stringify({ error: { code: "invalid_credentials", message: "bad creds" } }),
        { status: 401, headers: { "Content-Type": "application/json" } },
      ),
    );
    await expect(apiFetch("/test", { method: "POST", body: "{}" })).rejects.toMatchObject({
      status: 401,
      code: "invalid_credentials",
      message: "bad creds",
    });
  });

  it("throws ApiRequestError with fallback code when error body is not JSON", async () => {
    mockFetch.mockResolvedValue(
      new Response("not json", { status: 500, headers: { "Content-Type": "text/plain" } }),
    );
    await expect(apiFetch("/test", { method: "POST", body: "{}" })).rejects.toMatchObject({
      status: 500,
      code: "unknown_error",
    });
  });

  it("throws ApiRequestError with network_error when fetch rejects", async () => {
    mockFetch.mockRejectedValue(new TypeError("Failed to fetch"));
    await expect(apiFetch("/test", { method: "GET" })).rejects.toMatchObject({
      status: 0,
      code: "network_error",
    });
  });

  describe("request headers", () => {
    beforeEach(() => {
      mockFetch.mockResolvedValue(new Response(null, { status: 204 }));
    });

    it("defaults Content-Type to application/json when the caller sends none", async () => {
      await apiFetch("/test", { method: "POST", body: "{}" });
      expect(sentHeaders().get("content-type")).toBe("application/json");
    });

    it("keeps a caller-provided Content-Type as a single value", async () => {
      await apiFetch("/test", {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: "{}",
      });
      expect(sentHeaders().get("content-type")).toBe("application/json");
    });

    it("does not overwrite a caller-provided non-JSON Content-Type", async () => {
      await apiFetch("/test", {
        method: "POST",
        headers: { "Content-Type": "application/json-patch+json" },
        body: "{}",
      });
      expect(sentHeaders().get("content-type")).toBe("application/json-patch+json");
    });

    it("preserves custom headers given as an object literal", async () => {
      await apiFetch("/test", {
        method: "POST",
        headers: { "x-custom": "val", authorization: "Bearer at" },
        body: "{}",
      });
      const headers = sentHeaders();
      expect(headers.get("x-custom")).toBe("val");
      expect(headers.get("authorization")).toBe("Bearer at");
      expect(headers.get("content-type")).toBe("application/json");
    });

    it("preserves headers given as a Headers instance", async () => {
      await apiFetch("/test", {
        method: "POST",
        headers: new Headers({ "x-custom": "val", "content-type": "application/json" }),
        body: "{}",
      });
      const headers = sentHeaders();
      expect(headers.get("x-custom")).toBe("val");
      expect(headers.get("content-type")).toBe("application/json");
    });

    it("preserves headers given as a tuple array", async () => {
      await apiFetch("/test", { method: "POST", headers: [["x-custom", "val"]], body: "{}" });
      expect(sentHeaders().get("x-custom")).toBe("val");
    });

    it("leaves the Content-Type to the browser for FormData bodies", async () => {
      const body = new FormData();
      body.append("file", "content");
      await apiFetch("/test", { method: "POST", headers: { "x-custom": "val" }, body });
      const headers = sentHeaders();
      expect(headers.has("content-type")).toBe(false);
      expect(headers.get("x-custom")).toBe("val");
    });

    it("keeps method, url and body untouched, and sends no body when there is none", async () => {
      await apiFetch("/test", { method: "POST" });
      const [url, init] = mockFetch.mock.calls[0] as [string, RequestInit];
      expect(url).toBe("/test");
      expect(init.method).toBe("POST");
      expect(init.body).toBeUndefined();
    });

    it("does not mutate the caller's init or headers", async () => {
      const callerHeaders = new Headers({ "x-custom": "val" });
      const init: RequestInit = { method: "POST", headers: callerHeaders, body: "{}" };
      await apiFetch("/test", init);
      expect(callerHeaders.has("content-type")).toBe(false);
      expect(init.headers).toBe(callerHeaders);
    });
  });

  it("ApiRequestError has correct name", () => {
    const err = new ApiRequestError(400, "bad_request", "bad");
    expect(err.name).toBe("ApiRequestError");
    expect(err instanceof Error).toBe(true);
  });
});

describe("apiFetch response parser", () => {
  // The preview route (RF-31) returns an image, which the JSON rules below the
  // parser would otherwise discard as undefined.
  it("hands a successful response to the caller's parser", async () => {
    const body = "jpeg-bytes";
    const response = new Response(body, {
      status: 200,
      headers: { "content-type": "image/jpeg" },
    });
    mockFetch.mockResolvedValue(response);

    const parse = vi.fn((r: Response) => r.blob());
    const parsed = await apiFetch<Blob>(
      "/api/files/attachments/a-1/preview",
      { method: "GET" },
      parse,
    );

    // The parser sees the response itself, and its result is what the caller
    // gets — not the undefined the non-JSON branch would have returned.
    expect(parse).toHaveBeenCalledWith(response);
    expect(parsed).toBeInstanceOf(Blob);
    expect(parsed.size).toBe(body.length);
  });

  // Error bodies stay on the one path that knows the services' envelope: a
  // parser must never be handed a failure to interpret.
  it("does not reach the parser for an error response", async () => {
    const parse = vi.fn();
    mockFetch.mockResolvedValue(
      new Response(JSON.stringify({ error: { code: "preview_not_available", message: "no" } }), {
        status: 409,
        headers: { "content-type": "application/json" },
      }),
    );

    await expect(
      apiFetch("/api/files/attachments/a-1/preview", { method: "GET" }, parse),
    ).rejects.toMatchObject({ status: 409, code: "preview_not_available" });
    expect(parse).not.toHaveBeenCalled();
  });
});

// ── apiUpload (RF-30) ────────────────────────────────────────────────────────
//
// The upload transport exists for one reason — fetch cannot say how much of a
// request body has left — so the tests that matter are the progress reports and
// the proof that everything *else* still behaves exactly like apiFetch. A
// divergence here would be invisible at the call site and would surface as a
// misclassified error under load.

interface ProgressInit {
  loaded: number;
  total: number;
  lengthComputable: boolean;
}

/** The bits of XMLHttpRequest apiUpload actually uses, driven by hand. */
class FakeXHR extends EventTarget {
  static last: FakeXHR | null = null;

  readonly upload = new EventTarget();
  readonly sentHeaders: Record<string, string> = {};
  method = "";
  url = "";
  body: unknown = undefined;
  status = 200;
  statusText = "OK";
  responseText = "";
  aborted = false;
  private responseHeaders: Record<string, string> = { "content-type": "application/json" };

  constructor() {
    super();
    FakeXHR.last = this;
  }

  open(method: string, url: string) {
    this.method = method;
    this.url = url;
  }

  setRequestHeader(name: string, value: string) {
    this.sentHeaders[name.toLowerCase()] = value;
  }

  getResponseHeader(name: string): string | null {
    return this.responseHeaders[name.toLowerCase()] ?? null;
  }

  send(body: unknown) {
    this.body = body;
  }

  abort() {
    this.aborted = true;
    this.dispatchEvent(new Event("abort"));
  }

  // ── Driving the fake ──────────────────────────────────────────────────────

  emitProgress(init: ProgressInit) {
    this.upload.dispatchEvent(new ProgressEvent("progress", init));
  }

  respond(status: number, body: string, contentType = "application/json", statusText?: string) {
    this.status = status;
    this.statusText = statusText ?? (status === 200 ? "OK" : "Error");
    this.responseText = body;
    this.responseHeaders["content-type"] = contentType;
    this.dispatchEvent(new Event("load"));
  }

  networkError() {
    this.dispatchEvent(new Event("error"));
  }

  timeout() {
    this.dispatchEvent(new Event("timeout"));
  }

  /** One progress event with a computable length, which is the only kind reported. */
  measure(loaded: number, total: number) {
    this.emitProgress({ loaded, total, lengthComputable: true });
  }
}

function currentXHR(): FakeXHR {
  const xhr = FakeXHR.last;
  if (!xhr) throw new Error("no upload was started");
  return xhr;
}

describe("apiUpload", () => {
  beforeEach(() => {
    FakeXHR.last = null;
    vi.stubGlobal("XMLHttpRequest", FakeXHR);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    vi.stubGlobal("fetch", mockFetch);
  });

  it("reports the bytes the transport actually counted", async () => {
    const seen: Array<{ loaded: number; total: number }> = [];
    const pending = apiUpload<{ data: unknown }>(
      "/api/files/channels/ch-1/attachments",
      { method: "POST", body: new FormData() },
      (progress) => seen.push(progress),
    );

    currentXHR().emitProgress({ loaded: 512, total: 2048, lengthComputable: true });
    currentXHR().emitProgress({ loaded: 2048, total: 2048, lengthComputable: true });
    currentXHR().respond(201, JSON.stringify({ data: { id: "a-1" } }));

    await expect(pending).resolves.toEqual({ data: { id: "a-1" } });
    expect(seen).toEqual([
      { loaded: 512, total: 2048 },
      { loaded: 2048, total: 2048 },
    ]);
  });

  // The whole point of the feature: a bar is drawn from measurements or not at
  // all. A body of unknown length must produce no number to draw one from.
  it("reports nothing when the length is not computable", async () => {
    const onProgress = vi.fn();
    const pending = apiUpload(
      "/api/files/channels/ch-1/attachments",
      { method: "POST" },
      onProgress,
    );

    currentXHR().emitProgress({ loaded: 512, total: 0, lengthComputable: false });
    currentXHR().emitProgress({ loaded: 512, total: 0, lengthComputable: true });
    currentXHR().respond(201, JSON.stringify({ data: {} }));

    await pending;
    expect(onProgress).not.toHaveBeenCalled();
  });

  it("leaves FormData to set its own Content-Type with the boundary", async () => {
    const pending = apiUpload(
      "/api/files/dm/dm-1/attachments",
      { method: "POST", body: new FormData(), headers: { authorization: "Bearer t" } },
      undefined,
    );

    const xhr = currentXHR();
    expect(xhr.method).toBe("POST");
    expect(xhr.url).toBe("/api/files/dm/dm-1/attachments");
    expect(xhr.sentHeaders["authorization"]).toBe("Bearer t");
    expect(xhr.sentHeaders["content-type"]).toBeUndefined();
    expect(xhr.body).toBeInstanceOf(FormData);

    xhr.respond(201, JSON.stringify({ data: {} }));
    await pending;
  });

  it("defaults a non-FormData body to JSON, exactly like apiFetch", async () => {
    const pending = apiUpload("/api/x", { method: "POST", body: "{}" });
    expect(currentXHR().sentHeaders["content-type"]).toBe("application/json");
    currentXHR().respond(200, JSON.stringify({ ok: true }));
    await pending;
  });

  it("reads the services' error envelope", async () => {
    const pending = apiUpload("/api/files/channels/ch-1/attachments", { method: "POST" });
    currentXHR().respond(
      413,
      JSON.stringify({ error: { code: "payload_too_large", message: "too big" } }),
    );
    await expect(pending).rejects.toMatchObject({
      status: 413,
      code: "payload_too_large",
    });
  });

  // Traefik refuses an oversized body itself and answers without the services'
  // envelope, so the status has to survive on its own.
  it("keeps the status of an error carrying no envelope", async () => {
    const pending = apiUpload("/api/files/channels/ch-1/attachments", { method: "POST" });
    currentXHR().respond(413, "Request Entity Too Large", "text/plain");
    await expect(pending).rejects.toMatchObject({ status: 413, code: "unknown_error" });
  });

  it("reports a transport failure the way apiFetch does", async () => {
    const pending = apiUpload("/api/files/channels/ch-1/attachments", { method: "POST" });
    currentXHR().networkError();
    await expect(pending).rejects.toMatchObject({ status: 0, code: "network_error" });
  });

  it("aborts the request when the caller's signal fires", async () => {
    const controller = new AbortController();
    const pending = apiUpload("/api/files/channels/ch-1/attachments", {
      method: "POST",
      signal: controller.signal,
    });

    controller.abort();

    expect(currentXHR().aborted).toBe(true);
    await expect(pending).rejects.toBeInstanceOf(DOMException);
  });

  it("does not open a request for an already-aborted signal", async () => {
    await expect(
      apiUpload("/api/x", { method: "POST", signal: AbortSignal.abort() }),
    ).rejects.toBeInstanceOf(DOMException);
    expect(FakeXHR.last).toBeNull();
  });

  it("returns undefined for a success with no JSON body", async () => {
    const pending = apiUpload<void>("/api/x", { method: "POST" });
    currentXHR().respond(204, "", "application/json");
    await expect(pending).resolves.toBeUndefined();
  });

  it("hands a successful response to the caller's parser", async () => {
    const pending = apiUpload<string>("/api/x", { method: "POST" }, undefined, (response) =>
      response.text(),
    );
    currentXHR().respond(200, "raw", "text/plain");
    await expect(pending).resolves.toBe("raw");
  });

  // A 2xx whose body is not the JSON it claims to be is a failure, not a
  // success carrying undefined: the caller would read the missing attachment as
  // an upload that worked.
  it("rejects a success whose JSON body will not parse", async () => {
    const pending = apiUpload("/api/x", { method: "POST" });
    currentXHR().respond(201, "{ truncated", "application/json");
    await expect(pending).rejects.toMatchObject({ status: 201, code: "unknown_error" });
  });

  it("treats a timeout as a transport failure", async () => {
    const pending = apiUpload("/api/x", { method: "POST" });
    currentXHR().timeout();
    await expect(pending).rejects.toMatchObject({ status: 0, code: "network_error" });
  });

  // The status a parser sees has to be the one the server sent: apiFetch hands
  // over the real Response, and a caller that branches on `response.status`
  // must not get a different answer from this transport.
  it("gives the parser the status and statusText the server sent", async () => {
    const pending = apiUpload("/api/x", { method: "POST" }, undefined, async (response) => ({
      status: response.status,
      statusText: response.statusText,
      body: await response.text(),
    }));

    currentXHR().respond(201, "created", "text/plain", "Created");

    await expect(pending).resolves.toEqual({
      status: 201,
      statusText: "Created",
      body: "created",
    });
  });

  // The parser runs inside an XHR event listener, where a synchronous throw
  // escapes the promise executor instead of rejecting it. Left unguarded the
  // caller waits forever, which is the one failure mode no timeout would catch.
  it("rejects when the response parser throws synchronously", async () => {
    const pending = apiUpload("/api/x", { method: "POST" }, undefined, () => {
      throw new Error("sync parse error");
    });

    currentXHR().respond(200, "raw", "text/plain");

    await expect(pending).rejects.toThrow("sync parse error");
  });

  // The same guard must not swallow the ordinary case: a parser that returns a
  // rejected promise still rejects, and does so with its own error.
  it("keeps rejecting for a parser that fails asynchronously", async () => {
    const pending = apiUpload("/api/x", { method: "POST" }, undefined, () =>
      Promise.reject(new Error("async parse error")),
    );

    currentXHR().respond(200, "raw", "text/plain");

    await expect(pending).rejects.toThrow("async parse error");
  });

  // Response refuses to be built with a body for these statuses, and an empty
  // responseText is precisely that absence — not a body of length zero.
  it("hands the parser a body-less response for a status that cannot carry one", async () => {
    const pending = apiUpload("/api/x", { method: "POST" }, undefined, (response) =>
      Promise.resolve(response.status),
    );

    currentXHR().respond(204, "", "application/json", "No Content");

    await expect(pending).resolves.toBe(204);
  });

  // ── Pacing (code review) ───────────────────────────────────────────────────
  //
  // Every report a caller receives is a React render of the composer, and the
  // socket produces them far faster than a bar can be read. What must survive
  // the pacing is the two measurements that carry meaning: the first, and the
  // last.

  describe("progress pacing", () => {
    beforeEach(() => {
      vi.useFakeTimers();
    });

    afterEach(() => {
      vi.useRealTimers();
    });

    it("collapses a burst inside one window into a single trailing report", () => {
      const onProgress = vi.fn();
      void apiUpload("/api/x", { method: "POST" }, onProgress);
      const xhr = currentXHR();

      // Twenty events across 100 ms — comfortably inside the 150 ms window.
      for (let i = 1; i <= 20; i += 1) {
        xhr.measure(i * 100, 10_000);
        vi.advanceTimersByTime(5);
      }

      // The first one is not delayed: it is what makes the bar appear.
      expect(onProgress).toHaveBeenCalledTimes(1);
      expect(onProgress).toHaveBeenLastCalledWith({ loaded: 100, total: 10_000 });

      vi.advanceTimersByTime(150);

      // The other nineteen collapse into the newest measurement, not the oldest.
      expect(onProgress).toHaveBeenCalledTimes(2);
      expect(onProgress).toHaveBeenLastCalledWith({ loaded: 2_000, total: 10_000 });
    });

    it("keeps reporting across windows without dropping to one update", () => {
      const onProgress = vi.fn();
      void apiUpload("/api/x", { method: "POST" }, onProgress);
      const xhr = currentXHR();

      xhr.measure(100, 1_000);
      vi.advanceTimersByTime(200);
      xhr.measure(400, 1_000);
      vi.advanceTimersByTime(200);
      xhr.measure(800, 1_000);

      expect(onProgress.mock.calls.map(([progress]) => progress.loaded)).toEqual([100, 400, 800]);
    });

    it("publishes the last measurement when the upload completes", async () => {
      const onProgress = vi.fn();
      const pending = apiUpload("/api/x", { method: "POST" }, onProgress);
      const xhr = currentXHR();

      xhr.measure(1, 100);
      xhr.measure(100, 100);
      // Still inside the window: the completed count has not gone out yet.
      expect(onProgress).toHaveBeenCalledTimes(1);

      xhr.respond(201, JSON.stringify({ data: {} }));

      expect(onProgress).toHaveBeenLastCalledWith({ loaded: 100, total: 100 });
      expect(vi.getTimerCount()).toBe(0);
      await expect(pending).resolves.toEqual({ data: {} });
    });

    it("never hands back a count below one it already published", () => {
      const onProgress = vi.fn();
      void apiUpload("/api/x", { method: "POST" }, onProgress);
      const xhr = currentXHR();

      xhr.measure(500, 1_000);
      vi.advanceTimersByTime(200);
      xhr.measure(200, 1_000);

      expect(onProgress).toHaveBeenCalledTimes(1);
      expect(onProgress).toHaveBeenLastCalledWith({ loaded: 500, total: 1_000 });
    });

    it("arms no timer at all for a caller that asked for no progress", () => {
      const pending = apiUpload("/api/x", { method: "POST" });
      currentXHR().measure(100, 1_000);

      expect(vi.getTimerCount()).toBe(0);
      currentXHR().respond(200, JSON.stringify({ ok: true }));
      return expect(pending).resolves.toEqual({ ok: true });
    });

    it.each([
      ["a transport failure", (xhr: FakeXHR) => xhr.networkError()],
      ["an abort", (xhr: FakeXHR) => xhr.abort()],
      ["a timeout", (xhr: FakeXHR) => xhr.timeout()],
    ])("leaves no timer armed after %s", async (_name, stop) => {
      const onProgress = vi.fn();
      const pending = apiUpload("/api/x", { method: "POST" }, onProgress);
      const xhr = currentXHR();

      xhr.measure(1, 100);
      xhr.measure(50, 100);
      expect(vi.getTimerCount()).toBe(1);

      stop(xhr);

      await expect(pending).rejects.toBeDefined();
      expect(vi.getTimerCount()).toBe(0);
      // A request that never arrived publishes nothing further: the pending
      // measurement dies with it rather than firing into a dead upload.
      expect(onProgress).toHaveBeenCalledTimes(1);
    });
  });
});
