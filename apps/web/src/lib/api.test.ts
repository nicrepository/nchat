import { afterEach, describe, expect, it, vi } from "vitest";

import { ApiRequestError, apiFetch } from "./api";

const mockFetch = vi.fn<typeof fetch>();
vi.stubGlobal("fetch", mockFetch);

afterEach(() => {
  vi.resetAllMocks();
});

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

  it("always sets Content-Type: application/json", async () => {
    mockFetch.mockResolvedValue(new Response(null, { status: 204 }));
    await apiFetch("/test", { method: "POST", body: "{}" });
    expect(mockFetch).toHaveBeenCalledWith(
      "/test",
      expect.objectContaining({
        headers: expect.objectContaining({ "Content-Type": "application/json" }),
      }),
    );
  });

  it("ApiRequestError has correct name", () => {
    const err = new ApiRequestError(400, "bad_request", "bad");
    expect(err.name).toBe("ApiRequestError");
    expect(err instanceof Error).toBe(true);
  });
});
