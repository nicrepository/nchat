import { afterEach, describe, expect, it, vi } from "vitest";

import {
  acceptInvite,
  forgotPassword,
  login,
  logout,
  oidcExchange,
  oidcLoginUrl,
  refresh,
  resetPassword,
} from "./authApi";

const mockFetch = vi.fn<typeof fetch>();
vi.stubGlobal("fetch", mockFetch);

afterEach(() => {
  vi.resetAllMocks();
});

function jsonOk(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function emptyResponse(status: number): Response {
  return new Response(null, { status });
}

describe("login", () => {
  it("calls POST /api/auth/login with snake_case body", async () => {
    mockFetch.mockResolvedValue(
      jsonOk({
        access_token: "at",
        refresh_token: "rt",
        token_type: "Bearer",
        expires_in: 900,
        user: {
          id: "u1",
          email: "a@b.com",
          display_name: "Alice",
          must_change_password: false,
        },
      }),
    );
    await login({ email: "a@b.com", password: "pass", deviceName: "Browser" });
    const [url, init] = mockFetch.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/auth/login");
    expect(init.method).toBe("POST");
    const body = JSON.parse(init.body as string) as Record<string, unknown>;
    expect(body["email"]).toBe("a@b.com");
    expect(body["password"]).toBe("pass");
    expect(body["device_name"]).toBe("Browser");
  });

  it("maps snake_case response to camelCase", async () => {
    mockFetch.mockResolvedValue(
      jsonOk({
        access_token: "at",
        refresh_token: "rt",
        token_type: "Bearer",
        expires_in: 900,
        user: {
          id: "u1",
          email: "a@b.com",
          display_name: "Alice",
          must_change_password: false,
        },
      }),
    );
    const result = await login({ email: "a@b.com", password: "pass" });
    expect(result.accessToken).toBe("at");
    expect(result.refreshToken).toBe("rt");
    expect(result.user.displayName).toBe("Alice");
    expect(result.user.mustChangePassword).toBe(false);
  });

  it("propagates ApiRequestError on 401", async () => {
    mockFetch.mockResolvedValue(
      new Response(JSON.stringify({ error: { code: "invalid_credentials", message: "bad" } }), {
        status: 401,
        headers: { "Content-Type": "application/json" },
      }),
    );
    await expect(login({ email: "a@b.com", password: "bad" })).rejects.toMatchObject({
      code: "invalid_credentials",
    });
  });
});

describe("refresh", () => {
  it("calls POST /api/auth/refresh with refresh_token body", async () => {
    mockFetch.mockResolvedValue(
      jsonOk({ access_token: "at2", refresh_token: "rt2", token_type: "Bearer", expires_in: 900 }),
    );
    await refresh("rt1");
    const [url, init] = mockFetch.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/auth/refresh");
    expect(JSON.parse(init.body as string)).toEqual({ refresh_token: "rt1" });
  });

  it("maps response to camelCase TokenPair", async () => {
    mockFetch.mockResolvedValue(
      jsonOk({ access_token: "at2", refresh_token: "rt2", token_type: "Bearer", expires_in: 900 }),
    );
    const result = await refresh("rt1");
    expect(result.accessToken).toBe("at2");
    expect(result.refreshToken).toBe("rt2");
  });
});

describe("logout", () => {
  it("calls POST /api/auth/logout with refresh_token body", async () => {
    mockFetch.mockResolvedValue(emptyResponse(204));
    await logout("rt1");
    const [url, init] = mockFetch.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/auth/logout");
    expect(JSON.parse(init.body as string)).toEqual({ refresh_token: "rt1" });
  });
});

describe("forgotPassword", () => {
  it("calls POST /api/auth/password/forgot with email body", async () => {
    mockFetch.mockResolvedValue(emptyResponse(202));
    await forgotPassword("a@b.com");
    const [url, init] = mockFetch.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/auth/password/forgot");
    expect(JSON.parse(init.body as string)).toEqual({ email: "a@b.com" });
  });
});

describe("resetPassword", () => {
  it("calls POST /api/auth/password/reset with token and new_password", async () => {
    mockFetch.mockResolvedValue(emptyResponse(204));
    await resetPassword("tok123", "NewP@ss1");
    const [url, init] = mockFetch.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/auth/password/reset");
    const body = JSON.parse(init.body as string) as Record<string, unknown>;
    expect(body["token"]).toBe("tok123");
    expect(body["new_password"]).toBe("NewP@ss1");
  });

  it("propagates ApiRequestError on 401 invalid_token", async () => {
    mockFetch.mockResolvedValue(
      new Response(JSON.stringify({ error: { code: "invalid_token", message: "expired" } }), {
        status: 401,
        headers: { "Content-Type": "application/json" },
      }),
    );
    await expect(resetPassword("bad", "pass")).rejects.toMatchObject({ code: "invalid_token" });
  });
});

describe("acceptInvite", () => {
  it("calls POST /api/auth/invites/accept with snake_case body", async () => {
    mockFetch.mockResolvedValue(
      jsonOk(
        {
          id: "u2",
          email: "b@c.com",
          display_name: "Bob",
          full_name: "Bob Smith",
          created_at: "2026-06-01T00:00:00Z",
        },
        201,
      ),
    );
    await acceptInvite({
      token: "inv_tok",
      displayName: "Bob",
      fullName: "Bob Smith",
      password: "P@ss1",
    });
    const [url, init] = mockFetch.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/auth/invites/accept");
    const body = JSON.parse(init.body as string) as Record<string, unknown>;
    expect(body["token"]).toBe("inv_tok");
    expect(body["display_name"]).toBe("Bob");
    expect(body["full_name"]).toBe("Bob Smith");
    expect(body["password"]).toBe("P@ss1");
  });

  it("maps response to camelCase AcceptInviteResponse", async () => {
    mockFetch.mockResolvedValue(
      jsonOk(
        { id: "u2", email: "b@c.com", display_name: "Bob", created_at: "2026-06-01T00:00:00Z" },
        201,
      ),
    );
    const result = await acceptInvite({ token: "t", displayName: "Bob", password: "P@ss1" });
    expect(result.displayName).toBe("Bob");
    expect(result.createdAt).toBe("2026-06-01T00:00:00Z");
  });

  it("propagates ApiRequestError on 401 invalid_invite_token", async () => {
    mockFetch.mockResolvedValue(
      new Response(
        JSON.stringify({ error: { code: "invalid_invite_token", message: "expired" } }),
        {
          status: 401,
          headers: { "Content-Type": "application/json" },
        },
      ),
    );
    await expect(
      acceptInvite({ token: "bad", displayName: "X", password: "P@ss1" }),
    ).rejects.toMatchObject({ code: "invalid_invite_token" });
  });
});

describe("oidcLoginUrl", () => {
  it("points to backend Keycloak login endpoint", () => {
    expect(oidcLoginUrl()).toBe("/api/auth/oidc/keycloak/login");
  });
});

describe("oidcExchange", () => {
  it("calls POST /api/auth/oidc/keycloak/exchange with opaque code", async () => {
    mockFetch.mockResolvedValue(
      jsonOk({
        access_token: "at",
        refresh_token: "rt",
        token_type: "Bearer",
        expires_in: 900,
        user: {
          id: "u1",
          email: "sso@example.com",
          display_name: "SSO User",
          must_change_password: false,
        },
      }),
    );

    const result = await oidcExchange("opaque-code");

    const [url, init] = mockFetch.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/auth/oidc/keycloak/exchange");
    expect(init.method).toBe("POST");
    expect(JSON.parse(init.body as string)).toEqual({ code: "opaque-code" });
    expect(result.accessToken).toBe("at");
    expect(result.refreshToken).toBe("rt");
    expect(result.user.displayName).toBe("SSO User");
  });

  it("propagates generic OIDC ApiRequestError", async () => {
    mockFetch.mockResolvedValue(
      new Response(
        JSON.stringify({ error: { code: "invalid_oidc_callback", message: "failed" } }),
        {
          status: 401,
          headers: { "Content-Type": "application/json" },
        },
      ),
    );

    await expect(oidcExchange("bad-code")).rejects.toMatchObject({
      code: "invalid_oidc_callback",
    });
  });
});
