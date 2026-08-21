import { afterEach, describe, expect, it, vi } from "vitest";

import {
  createAdminSession,
  destroyAdminSession,
  fetchAdminBootstrap,
  listAuditEvents,
  type AdminBootstrap,
} from "./adminApi";
import { _resetCSRFToken, adminFetch } from "./client";

const BOOTSTRAP: AdminBootstrap = {
  identity: { user_id: "u1", email: "a@example.test", display_name: "Admin", avatar_url: "" },
  capabilities: ["admin.audit.read"],
  environment: "STAGING",
  build: { service: "admin-service", version: "0.0.0", commit: "dev" },
  session: { idle_expires_at: "2026-08-20T12:00:00Z", absolute_expires_at: "2026-08-20T20:00:00Z" },
  csrf_token: "csrf-1",
};

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "content-type": "application/json" },
  });
}

afterEach(() => {
  vi.restoreAllMocks();
  _resetCSRFToken();
});

describe("createAdminSession", () => {
  // The chat access token is a one-shot proof of identity. It goes out on this
  // request and is never persisted anywhere the console can read it again.
  it("sends the access token as a bearer and keeps the returned CSRF token", async () => {
    const fetchMock = vi
      .fn()
      .mockImplementationOnce(() => Promise.resolve(jsonResponse({ data: BOOTSTRAP }, 201)))
      .mockImplementationOnce(() => Promise.resolve(new Response(null, { status: 204 })));
    vi.stubGlobal("fetch", fetchMock);

    await createAdminSession("access-token-1");

    const handshake = fetchMock.mock.calls[0][1];
    expect(handshake.method).toBe("POST");
    expect(new Headers(handshake.headers).get("Authorization")).toBe("Bearer access-token-1");

    // The token adopted from the handshake is used by the next mutating call.
    await adminFetch("/session", { method: "DELETE" });
    expect(new Headers(fetchMock.mock.calls[1][1].headers).get("X-NChat-Admin-CSRF")).toBe(
      "csrf-1",
    );
  });

  it("never writes anything to web storage", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(jsonResponse({ data: BOOTSTRAP }, 201)));

    await createAdminSession("access-token-1");

    expect(localStorage.length).toBe(0);
    expect(sessionStorage.length).toBe(0);
  });
});

describe("fetchAdminBootstrap", () => {
  it("adopts the CSRF token from the payload", async () => {
    const fetchMock = vi
      .fn()
      .mockImplementationOnce(() => Promise.resolve(jsonResponse({ data: BOOTSTRAP })))
      .mockImplementationOnce(() => Promise.resolve(new Response(null, { status: 204 })));
    vi.stubGlobal("fetch", fetchMock);

    await fetchAdminBootstrap();
    await adminFetch("/session", { method: "DELETE" });

    expect(new Headers(fetchMock.mock.calls[1][1].headers).get("X-NChat-Admin-CSRF")).toBe(
      "csrf-1",
    );
  });
});

describe("destroyAdminSession", () => {
  it("drops the CSRF token even when the request fails", async () => {
    const fetchMock = vi
      .fn()
      .mockImplementationOnce(() => Promise.resolve(jsonResponse({ data: BOOTSTRAP })))
      .mockImplementationOnce(() =>
        Promise.resolve(jsonResponse({ error: { code: "internal_error", message: "x" } }, 500)),
      )
      .mockImplementationOnce(() => Promise.resolve(new Response(null, { status: 204 })));
    vi.stubGlobal("fetch", fetchMock);

    await fetchAdminBootstrap();
    await expect(destroyAdminSession()).rejects.toMatchObject({ status: 500 });

    await adminFetch("/session", { method: "DELETE" });
    expect(new Headers(fetchMock.mock.calls[2][1].headers).get("X-NChat-Admin-CSRF")).toBeNull();
  });
});

describe("listAuditEvents", () => {
  it("passes the limit and tolerates an empty payload", async () => {
    const fetchMock = vi
      .fn()
      .mockImplementationOnce(() => Promise.resolve(jsonResponse({ data: { events: [] } })))
      .mockImplementationOnce(() => Promise.resolve(jsonResponse({ data: {} })));
    vi.stubGlobal("fetch", fetchMock);

    await expect(listAuditEvents(25)).resolves.toEqual([]);
    expect(fetchMock.mock.calls[0][0]).toBe("/api/admin/audit/events?limit=25");

    await expect(listAuditEvents()).resolves.toEqual([]);
    expect(fetchMock.mock.calls[1][0]).toBe("/api/admin/audit/events");
  });
});
