import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { ApiRequestError, apiFetch } from "./api";

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
