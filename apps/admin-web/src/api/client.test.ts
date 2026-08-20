import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { AdminApiError, _resetCSRFToken, adminFetch, setCSRFToken } from "./client";

function jsonResponse(body: unknown, init: ResponseInit = {}) {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { "content-type": "application/json" },
    ...init,
  });
}

beforeEach(() => {
  _resetCSRFToken();
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe("adminFetch", () => {
  it("unwraps the service envelope", async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse({ data: { ok: true } }));
    vi.stubGlobal("fetch", fetchMock);

    await expect(adminFetch<{ ok: boolean }>("/bootstrap")).resolves.toEqual({ ok: true });
    expect(fetchMock.mock.calls[0][0]).toBe("/api/admin/bootstrap");
  });

  // The HttpOnly cookie is the console's only credential, and it only travels
  // when the request asks for it.
  it("always sends credentials", async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse({ data: { ok: true } }));
    vi.stubGlobal("fetch", fetchMock);

    await adminFetch("/bootstrap");

    expect(fetchMock.mock.calls[0][1].credentials).toBe("include");
  });

  it("echoes the CSRF token on mutating requests only", async () => {
    const fetchMock = vi
      .fn()
      .mockImplementationOnce(() => Promise.resolve(new Response(null, { status: 204 })))
      .mockImplementationOnce(() => Promise.resolve(jsonResponse({ data: { ok: true } })));
    vi.stubGlobal("fetch", fetchMock);
    setCSRFToken("csrf-1");

    await adminFetch("/session", { method: "DELETE" });
    await adminFetch("/bootstrap");

    const mutating = new Headers(fetchMock.mock.calls[0][1].headers);
    const safe = new Headers(fetchMock.mock.calls[1][1].headers);
    expect(mutating.get("X-NChat-Admin-CSRF")).toBe("csrf-1");
    expect(safe.get("X-NChat-Admin-CSRF")).toBeNull();
  });

  it("omits the CSRF header when no token is held", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(null, { status: 204 }));
    vi.stubGlobal("fetch", fetchMock);

    await adminFetch("/session", { method: "DELETE" });

    expect(new Headers(fetchMock.mock.calls[0][1].headers).get("X-NChat-Admin-CSRF")).toBeNull();
  });

  it("reads the error envelope", async () => {
    vi.stubGlobal(
      "fetch",
      vi
        .fn()
        .mockResolvedValue(
          jsonResponse({ error: { code: "forbidden", message: "forbidden" } }, { status: 403 }),
        ),
    );

    await expect(adminFetch("/audit/events")).rejects.toMatchObject({
      status: 403,
      code: "forbidden",
    });
  });

  it("falls back to a generic error for a non-JSON body", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(new Response("<html>gateway</html>", { status: 502 })),
    );

    const error = await adminFetch("/bootstrap").catch((caught: unknown) => caught);
    expect(error).toBeInstanceOf(AdminApiError);
    expect((error as AdminApiError).status).toBe(502);
    expect((error as AdminApiError).code).toBe("unknown_error");
  });

  it("classifies a transport failure without a status", async () => {
    vi.stubGlobal("fetch", vi.fn().mockRejectedValue(new TypeError("offline")));

    await expect(adminFetch("/bootstrap")).rejects.toMatchObject({
      status: 0,
      code: "network_error",
    });
  });

  it("returns undefined for a 204", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(null, { status: 204 })));

    await expect(adminFetch("/session", { method: "DELETE" })).resolves.toBeUndefined();
  });

  it("sets a JSON content type only when there is a body", async () => {
    const fetchMock = vi
      .fn()
      .mockImplementation(() => Promise.resolve(jsonResponse({ data: { ok: true } })));
    vi.stubGlobal("fetch", fetchMock);

    await adminFetch("/session", { method: "POST", body: JSON.stringify({}) });
    await adminFetch("/session", { method: "POST" });

    expect(new Headers(fetchMock.mock.calls[0][1].headers).get("Content-Type")).toBe(
      "application/json",
    );
    expect(new Headers(fetchMock.mock.calls[1][1].headers).get("Content-Type")).toBeNull();
  });
});

describe("envelope validation", () => {
  // A 2xx whose body is not the documented envelope must fail here, not three
  // screens later as an "undefined is not an object" in a component.
  it("rejects a 2xx with no data field", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(jsonResponse({})));

    const error = await adminFetch("/bootstrap").catch((caught: unknown) => caught);
    expect(error).toBeInstanceOf(AdminApiError);
    expect((error as AdminApiError).code).toBe("invalid_response");
    expect((error as AdminApiError).status).toBe(200);
  });

  it("rejects a 2xx whose data is null", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(jsonResponse({ data: null })));

    await expect(adminFetch("/bootstrap")).rejects.toMatchObject({ code: "invalid_response" });
  });

  it("rejects a 2xx that is not valid JSON", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response("<html>gateway</html>", {
          status: 200,
          headers: { "content-type": "application/json" },
        }),
      ),
    );

    await expect(adminFetch("/bootstrap")).rejects.toMatchObject({ code: "invalid_response" });
  });

  it("rejects a 2xx whose body is a bare array", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(jsonResponse([1, 2, 3])));

    await expect(adminFetch("/bootstrap")).rejects.toMatchObject({ code: "invalid_response" });
  });

  // A 2xx that somehow carries the error envelope is still not a usable
  // success; it has no data.
  it("rejects a 2xx that carries an error envelope", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(jsonResponse({ error: { code: "forbidden", message: "no" } })),
    );

    await expect(adminFetch("/bootstrap")).rejects.toMatchObject({ code: "invalid_response" });
  });

  it("accepts a valid envelope, including falsy payloads", async () => {
    const fetchMock = vi
      .fn()
      .mockImplementationOnce(() => Promise.resolve(jsonResponse({ data: { ok: true } })))
      .mockImplementationOnce(() => Promise.resolve(jsonResponse({ data: 0 })))
      .mockImplementationOnce(() => Promise.resolve(jsonResponse({ data: "" })));
    vi.stubGlobal("fetch", fetchMock);

    await expect(adminFetch("/bootstrap")).resolves.toEqual({ ok: true });
    await expect(adminFetch("/bootstrap")).resolves.toBe(0);
    await expect(adminFetch("/bootstrap")).resolves.toBe("");
  });

  // The documented empty answer still works: logout carries no body at all.
  it("still accepts a 204 with no body", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(null, { status: 204 })));

    await expect(adminFetch("/session", { method: "DELETE" })).resolves.toBeUndefined();
  });

  // The existing error path is unchanged.
  it("keeps reading the error envelope on a non-2xx", async () => {
    vi.stubGlobal(
      "fetch",
      vi
        .fn()
        .mockResolvedValue(
          jsonResponse({ error: { code: "forbidden", message: "forbidden" } }, { status: 403 }),
        ),
    );

    await expect(adminFetch("/audit/events")).rejects.toMatchObject({
      status: 403,
      code: "forbidden",
    });
  });
});
