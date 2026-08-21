import { afterEach, describe, expect, it, vi } from "vitest";

import { AuthError, exchangeOIDCCode, login, OIDC_LOGIN_PATH } from "./authApi";

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "content-type": "application/json" },
  });
}

afterEach(() => {
  vi.restoreAllMocks();
});

describe("login", () => {
  it("returns the access token without persisting it", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValue(jsonResponse({ access_token: "at-1", token_type: "Bearer" }));
    vi.stubGlobal("fetch", fetchMock);

    await expect(login("a@example.test", "secret")).resolves.toBe("at-1");
    expect(fetchMock.mock.calls[0][0]).toBe("/api/auth/login");
    expect(fetchMock.mock.calls[0][1].credentials).toBe("include");
    expect(localStorage.length).toBe(0);
    expect(sessionStorage.length).toBe(0);
  });

  it("surfaces the auth error code", async () => {
    vi.stubGlobal(
      "fetch",
      vi
        .fn()
        .mockResolvedValue(
          jsonResponse({ error: { code: "invalid_credentials", message: "bad" } }, 401),
        ),
    );

    const error = await login("a@example.test", "wrong").catch((caught: unknown) => caught);
    expect(error).toBeInstanceOf(AuthError);
    expect((error as AuthError).status).toBe(401);
    expect((error as AuthError).code).toBe("invalid_credentials");
  });

  it("does not surface a transport detail as a message", async () => {
    vi.stubGlobal("fetch", vi.fn().mockRejectedValue(new TypeError("ECONNREFUSED 10.0.0.1")));

    const error = await login("a@example.test", "x").catch((caught: unknown) => caught);
    expect((error as AuthError).status).toBe(0);
    expect((error as AuthError).message).not.toContain("10.0.0.1");
  });

  it("keeps a generic message when the error body is not JSON", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response("nope", { status: 500 })));

    const error = await login("a@example.test", "x").catch((caught: unknown) => caught);
    expect((error as AuthError).code).toBe("unknown_error");
  });
});

describe("exchangeOIDCCode", () => {
  it("posts the code to the exchange endpoint", async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse({ access_token: "at-2" }));
    vi.stubGlobal("fetch", fetchMock);

    await expect(exchangeOIDCCode("code-1")).resolves.toBe("at-2");
    expect(fetchMock.mock.calls[0][0]).toBe("/api/auth/oidc/keycloak/exchange");
    expect(JSON.parse(fetchMock.mock.calls[0][1].body)).toEqual({ code: "code-1" });
  });
});

// The sign-in link takes no caller-supplied destination. It names which
// application is signing in, and the server resolves that label to a redirect
// URI it holds; there is nothing here to smuggle a foreign origin into.
describe("OIDC_LOGIN_PATH", () => {
  it("is a fixed same-origin path that names the admin application", () => {
    expect(OIDC_LOGIN_PATH).toBe("/api/auth/oidc/keycloak/login?app=admin");
    expect(OIDC_LOGIN_PATH.startsWith("/")).toBe(true);
    expect(OIDC_LOGIN_PATH.startsWith("//")).toBe(false);
  });

  it("carries a label and never a URL", () => {
    const params = new URLSearchParams(OIDC_LOGIN_PATH.split("?")[1]);
    expect([...params.keys()]).toEqual(["app"]);
    expect(params.get("app")).toBe("admin");
    expect(OIDC_LOGIN_PATH).not.toMatch(/https?:|%2F%2F|redirect_uri|returnTo/i);
  });
});

describe("error bodies", () => {
  it("keeps the generic message when the JSON body carries no error", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response(JSON.stringify({ detail: "nope" }), {
          status: 403,
          headers: { "content-type": "application/json" },
        }),
      ),
    );

    const error = await login("a@example.test", "x").catch((caught: unknown) => caught);
    expect((error as AuthError).code).toBe("unknown_error");
    expect((error as AuthError).message).toBe("Não foi possível autenticar");
  });
});
