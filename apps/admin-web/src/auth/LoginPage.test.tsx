import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { createAdminSession, type AdminBootstrap } from "../api/adminApi";
import { AdminApiError } from "../api/client";
import { AuthError, login } from "../api/authApi";
import { AdminSessionContext, type AdminSessionValue } from "../session/AdminSessionContext";
import LoginPage from "./LoginPage";

vi.mock("../api/authApi", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../api/authApi")>()),
  login: vi.fn(),
}));

vi.mock("../api/adminApi", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../api/adminApi")>()),
  createAdminSession: vi.fn(),
}));

const loginMock = vi.mocked(login);
const createSessionMock = vi.mocked(createAdminSession);

const BOOTSTRAP: AdminBootstrap = {
  identity: { user_id: "u1", email: "admin@example.test", display_name: "Admin", avatar_url: "" },
  capabilities: ["admin.audit.read"],
  environment: "STAGING",
  build: { service: "admin-service", version: "0.0.0", commit: "dev" },
  session: { idle_expires_at: "2026-08-20T12:00:00Z", absolute_expires_at: "2026-08-20T20:00:00Z" },
  csrf_token: "csrf-1",
};

function renderLogin(adopt: AdminSessionValue["adopt"] = vi.fn()) {
  const value: AdminSessionValue = {
    status: "unauthenticated",
    bootstrap: null,
    message: "",
    reload: () => {},
    adopt,
    signOut: async () => {},
    can: () => false,
  };
  render(
    <AdminSessionContext.Provider value={value}>
      <LoginPage />
    </AdminSessionContext.Provider>,
  );
  return { adopt };
}

async function signIn(email = "admin@example.test", password = "correct-horse") {
  await userEvent.type(screen.getByLabelText("E-mail"), email);
  await userEvent.type(screen.getByLabelText("Senha"), password);
  await userEvent.click(screen.getByRole("button", { name: "Entrar" }));
}

beforeEach(() => {
  loginMock.mockReset();
  createSessionMock.mockReset();
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe("LoginPage", () => {
  // The button is disabled while the request is in flight, so a second click
  // cannot start a second sign-in.
  it("marks the form as submitting while the sign-in runs", async () => {
    let release: (token: string) => void = () => {};
    loginMock.mockReturnValue(
      new Promise<string>((resolve) => {
        release = resolve;
      }),
    );
    createSessionMock.mockResolvedValue(BOOTSTRAP);
    renderLogin();

    await signIn();

    const button = await screen.findByRole("button", { name: "Entrando…" });
    expect(button).toBeDisabled();

    release("access-token-1");
    await waitFor(() => expect(createSessionMock).toHaveBeenCalled());
  });

  // The success path proves the identity, exchanges it for an administrative
  // session and hands that to adopt(). Nothing after adopt() is needed: it
  // replaces this screen, so the flow completes without a further state update
  // on a component that is on its way out of the tree.
  it("exchanges the credentials for an administrative session and adopts it", async () => {
    loginMock.mockResolvedValue("access-token-1");
    createSessionMock.mockResolvedValue(BOOTSTRAP);
    const { adopt } = renderLogin();

    await signIn();

    await waitFor(() => expect(adopt).toHaveBeenCalledTimes(1));
    expect(loginMock).toHaveBeenCalledExactlyOnceWith("admin@example.test", "correct-horse");
    expect(createSessionMock).toHaveBeenCalledExactlyOnceWith("access-token-1");
    expect(adopt).toHaveBeenCalledWith(BOOTSTRAP);
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("restores the form after invalid credentials so the attempt can be retried", async () => {
    loginMock.mockRejectedValueOnce(new AuthError(401, "invalid_credentials", "bad"));
    const { adopt } = renderLogin();

    await signIn("admin@example.test", "wrong");

    expect(await screen.findByRole("alert")).toHaveTextContent("E-mail ou senha inválidos.");
    const button = await screen.findByRole("button", { name: "Entrar" });
    expect(button).toBeEnabled();
    expect(adopt).not.toHaveBeenCalled();

    // And the retry really runs: the button was not left latched.
    loginMock.mockResolvedValueOnce("access-token-1");
    createSessionMock.mockResolvedValue(BOOTSTRAP);
    await userEvent.click(button);
    await waitFor(() => expect(adopt).toHaveBeenCalledTimes(1));
  });

  // Correct credentials, no administrative authority. The handshake failed, not
  // the login, and the form must come back so the person is not left staring at
  // a disabled button.
  it("restores the form when the administrative handshake is refused", async () => {
    loginMock.mockResolvedValue("access-token-1");
    createSessionMock.mockRejectedValue(new AdminApiError(403, "forbidden", "forbidden"));
    const { adopt } = renderLogin();

    await signIn();

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Sua conta não possui acesso administrativo.",
    );
    expect(await screen.findByRole("button", { name: "Entrar" })).toBeEnabled();
    expect(adopt).not.toHaveBeenCalled();
  });

  it("restores the form when the handshake is rate limited", async () => {
    loginMock.mockResolvedValue("access-token-1");
    createSessionMock.mockRejectedValue(new AdminApiError(429, "rate_limited", "slow down"));
    renderLogin();

    await signIn();

    expect(await screen.findByRole("alert")).toHaveTextContent("Muitas tentativas");
    expect(await screen.findByRole("button", { name: "Entrar" })).toBeEnabled();
  });

  it("restores the form on a network failure", async () => {
    loginMock.mockRejectedValue(new AuthError(0, "network_error", "offline"));
    renderLogin();

    await signIn();

    expect(await screen.findByRole("alert")).toHaveTextContent("Falha de rede.");
    expect(await screen.findByRole("button", { name: "Entrar" })).toBeEnabled();
  });

  it("keeps the access token out of web storage", async () => {
    loginMock.mockResolvedValue("access-token-1");
    createSessionMock.mockResolvedValue(BOOTSTRAP);
    const { adopt } = renderLogin();

    await signIn();

    await waitFor(() => expect(adopt).toHaveBeenCalled());
    expect(localStorage.length).toBe(0);
    expect(sessionStorage.length).toBe(0);
  });
});
