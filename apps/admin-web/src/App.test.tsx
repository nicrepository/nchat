import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import App from "./App";
import type { AdminBootstrap } from "./api/adminApi";

const BOOTSTRAP: AdminBootstrap = {
  identity: { user_id: "u1", email: "admin@example.test", display_name: "Admin", avatar_url: "" },
  capabilities: ["admin.audit.read", "admin.config.read"],
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

function errorResponse(status: number, code = "x") {
  return jsonResponse({ error: { code, message: "x" } }, status);
}

beforeEach(() => {
  window.history.pushState({}, "", "/");
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe("console bootstrap states", () => {
  it("shows a loading state before the session is known", () => {
    vi.stubGlobal("fetch", vi.fn().mockReturnValue(new Promise(() => {})));
    render(<App />);

    expect(screen.getByRole("status")).toHaveTextContent("Carregando console administrativo…");
  });

  // Anonymous access mounts no administrative surface at all.
  it("shows the sign-in screen and no shell for an anonymous visitor", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(errorResponse(401)));
    render(<App />);

    await screen.findByRole("heading", { name: "Console administrativo" });
    expect(screen.queryByRole("navigation", { name: "Seções administrativas" })).toBeNull();
    expect(screen.queryByTestId("admin-identity")).toBeNull();
  });

  // A signed-in NChat user without administrative authority is told so, rather
  // than being bounced into a sign-in loop that would succeed and change
  // nothing.
  it("explains the refusal for a non-administrator", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(errorResponse(403)));
    render(<App />);

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Sua conta não possui acesso administrativo.",
    );
    expect(screen.queryByRole("navigation", { name: "Seções administrativas" })).toBeNull();
  });

  it("reports an unavailable Admin API without offering a shell", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(errorResponse(503)));
    render(<App />);

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "O console administrativo está indisponível no momento.",
    );
  });

  it("offers a retry on a 5xx", async () => {
    const fetchMock = vi
      .fn()
      .mockImplementationOnce(() => Promise.resolve(errorResponse(500)))
      .mockImplementation(() => Promise.resolve(jsonResponse({ data: BOOTSTRAP })));
    vi.stubGlobal("fetch", fetchMock);
    render(<App />);

    await screen.findByRole("heading", { name: "Console indisponível" });
    await userEvent.click(screen.getByRole("button", { name: "Tentar novamente" }));
    await screen.findByRole("heading", { name: "Visão geral" });
  });
});

describe("route protection", () => {
  // Typing the URL is the same code path as clicking: the gate renders from the
  // session, never from the location.
  it("does not let a deep link reach a section without a session", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(errorResponse(401)));
    window.history.pushState({}, "", "/audit");
    render(<App />);

    await screen.findByRole("heading", { name: "Console administrativo" });
    expect(screen.queryByRole("heading", { name: "Auditoria" })).toBeNull();
  });

  it("honours a deep link once the session is established", async () => {
    vi.stubGlobal(
      "fetch",
      vi
        .fn()
        .mockImplementation((url: string) =>
          Promise.resolve(
            url.startsWith("/api/admin/audit")
              ? jsonResponse({ data: { events: [] } })
              : jsonResponse({ data: BOOTSTRAP }),
          ),
        ),
    );
    window.history.pushState({}, "", "/audit");
    render(<App />);

    await screen.findByRole("heading", { name: "Auditoria" });
  });

  it("answers an unknown path with an explicit not-implemented page", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(jsonResponse({ data: BOOTSTRAP })));
    window.history.pushState({}, "", "/health-center");
    render(<App />);

    await screen.findByRole("heading", { name: "Seção não disponível" });
  });
});

describe("sign-in", () => {
  it("exchanges credentials for an administrative session and mounts the shell", async () => {
    const fetchMock = vi.fn().mockImplementation((url: string, init?: RequestInit) => {
      if (url === "/api/admin/bootstrap") return Promise.resolve(errorResponse(401));
      if (url === "/api/auth/login") {
        return Promise.resolve(jsonResponse({ access_token: "at-1" }));
      }
      if (url === "/api/admin/session" && init?.method === "POST") {
        return Promise.resolve(jsonResponse({ data: BOOTSTRAP }, 201));
      }
      return Promise.resolve(errorResponse(404));
    });
    vi.stubGlobal("fetch", fetchMock);
    render(<App />);

    await screen.findByRole("heading", { name: "Console administrativo" });
    await userEvent.type(screen.getByLabelText("E-mail"), "admin@example.test");
    await userEvent.type(screen.getByLabelText("Senha"), "correct-horse");
    await userEvent.click(screen.getByRole("button", { name: "Entrar" }));

    await screen.findByRole("heading", { name: "Visão geral" });

    // The proof of identity is used once and never persisted.
    const handshake = fetchMock.mock.calls.find((call) => call[0] === "/api/admin/session");
    expect(new Headers(handshake?.[1]?.headers).get("Authorization")).toBe("Bearer at-1");
    expect(localStorage.length).toBe(0);
    expect(sessionStorage.length).toBe(0);
  });

  it("reports invalid credentials without mounting anything", async () => {
    vi.stubGlobal(
      "fetch",
      vi
        .fn()
        .mockImplementation((url: string) =>
          Promise.resolve(
            url === "/api/auth/login"
              ? errorResponse(401, "invalid_credentials")
              : errorResponse(401),
          ),
        ),
    );
    render(<App />);

    await screen.findByRole("heading", { name: "Console administrativo" });
    await userEvent.type(screen.getByLabelText("E-mail"), "admin@example.test");
    await userEvent.type(screen.getByLabelText("Senha"), "wrong");
    await userEvent.click(screen.getByRole("button", { name: "Entrar" }));

    await waitFor(() =>
      expect(screen.getByRole("alert")).toHaveTextContent("E-mail ou senha inválidos."),
    );
    expect(screen.queryByRole("navigation", { name: "Seções administrativas" })).toBeNull();
  });

  // Correct credentials plus no administrative authority: the console says so
  // instead of letting someone retry a working password forever.
  it("explains a valid login that carries no administrative authority", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation((url: string, init?: RequestInit) => {
        if (url === "/api/auth/login")
          return Promise.resolve(jsonResponse({ access_token: "at-1" }));
        if (url === "/api/admin/session" && init?.method === "POST") {
          return Promise.resolve(errorResponse(403, "forbidden"));
        }
        return Promise.resolve(errorResponse(401));
      }),
    );
    render(<App />);

    await screen.findByRole("heading", { name: "Console administrativo" });
    await userEvent.type(screen.getByLabelText("E-mail"), "user@example.test");
    await userEvent.type(screen.getByLabelText("Senha"), "correct-horse");
    await userEvent.click(screen.getByRole("button", { name: "Entrar" }));

    await waitFor(() =>
      expect(screen.getAllByRole("alert").at(-1)).toHaveTextContent(
        "Sua conta não possui acesso administrativo.",
      ),
    );
  });

  it("reports a rate-limited handshake", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation((url: string, init?: RequestInit) => {
        if (url === "/api/auth/login")
          return Promise.resolve(jsonResponse({ access_token: "at-1" }));
        if (url === "/api/admin/session" && init?.method === "POST") {
          return Promise.resolve(errorResponse(429, "rate_limited"));
        }
        return Promise.resolve(errorResponse(401));
      }),
    );
    render(<App />);

    await screen.findByRole("heading", { name: "Console administrativo" });
    await userEvent.type(screen.getByLabelText("E-mail"), "admin@example.test");
    await userEvent.type(screen.getByLabelText("Senha"), "x");
    await userEvent.click(screen.getByRole("button", { name: "Entrar" }));

    await waitFor(() =>
      expect(screen.getAllByRole("alert").at(-1)).toHaveTextContent("Muitas tentativas"),
    );
  });

  // The link names which application is signing in so auth-service can send the
  // browser back to this origin. The label is all it carries: no destination.
  it("offers single sign-on as a fixed same-origin link naming the admin app", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(errorResponse(401)));
    render(<App />);

    const link = await screen.findByRole("link", { name: "Entrar com SSO" });
    expect(link).toHaveAttribute("href", "/api/auth/oidc/keycloak/login?app=admin");
  });

  it("reports a network failure during sign-in", async () => {
    vi.stubGlobal(
      "fetch",
      vi
        .fn()
        .mockImplementation((url: string) =>
          url === "/api/auth/login"
            ? Promise.reject(new TypeError("offline"))
            : Promise.resolve(errorResponse(401)),
        ),
    );
    render(<App />);

    await screen.findByRole("heading", { name: "Console administrativo" });
    await userEvent.type(screen.getByLabelText("E-mail"), "a@example.test");
    await userEvent.type(screen.getByLabelText("Senha"), "x");
    await userEvent.click(screen.getByRole("button", { name: "Entrar" }));

    await waitFor(() =>
      expect(screen.getAllByRole("alert").at(-1)).toHaveTextContent("Falha de rede."),
    );
  });
});

describe("single sign-on callback", () => {
  it("completes the exchange and mounts the shell", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation((url: string) => {
        if (url === "/api/auth/oidc/keycloak/exchange") {
          return Promise.resolve(jsonResponse({ access_token: "at-sso" }));
        }
        if (url === "/api/admin/session")
          return Promise.resolve(jsonResponse({ data: BOOTSTRAP }, 201));
        return Promise.resolve(errorResponse(401));
      }),
    );
    window.history.pushState({}, "", "/oidc-callback?code=abc");
    render(<App />);

    await waitFor(() => expect(screen.queryByRole("status")).toBeNull());
  });

  // The code is the only thing read from the URL, and it is never used to build
  // a destination.
  it("refuses a callback without a code", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(errorResponse(401)));
    window.history.pushState({}, "", "/oidc-callback?next=https://evil.test");
    render(<App />);

    expect(await screen.findByRole("alert")).toHaveTextContent("Retorno de SSO inválido.");
    expect(window.location.href).not.toContain("evil.test/");
  });

  it("explains an SSO login that carries no administrative authority", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation((url: string) => {
        if (url === "/api/auth/oidc/keycloak/exchange") {
          return Promise.resolve(jsonResponse({ access_token: "at-sso" }));
        }
        return Promise.resolve(errorResponse(403));
      }),
    );
    window.history.pushState({}, "", "/oidc-callback?code=abc");
    render(<App />);

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Sua conta não possui acesso administrativo.",
    );
  });

  it("reports a failed exchange", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(errorResponse(401)));
    window.history.pushState({}, "", "/oidc-callback?code=abc");
    render(<App />);

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Não foi possível concluir o login com SSO.",
    );
  });
});
