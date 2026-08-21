import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { StrictMode } from "react";
import { MemoryRouter, Route, Routes, useSearchParams } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { createAdminSession, type AdminBootstrap } from "../api/adminApi";
import { AdminApiError } from "../api/client";
import { AuthError, exchangeOIDCCode } from "../api/authApi";
import { AdminSessionContext, type AdminSessionValue } from "../session/AdminSessionContext";
import OIDCCallbackPage from "./OIDCCallbackPage";

vi.mock("../api/authApi", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../api/authApi")>()),
  exchangeOIDCCode: vi.fn(),
}));

vi.mock("../api/adminApi", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../api/adminApi")>()),
  createAdminSession: vi.fn(),
}));

const exchangeMock = vi.mocked(exchangeOIDCCode);
const createSessionMock = vi.mocked(createAdminSession);

const BOOTSTRAP: AdminBootstrap = {
  identity: { user_id: "u1", email: "admin@example.test", display_name: "Admin", avatar_url: "" },
  capabilities: ["admin.audit.read"],
  environment: "STAGING",
  build: { service: "admin-service", version: "0.0.0", commit: "dev" },
  session: { idle_expires_at: "2026-08-20T12:00:00Z", absolute_expires_at: "2026-08-20T20:00:00Z" },
  csrf_token: "csrf-1",
};

function sessionValue(adopt: AdminSessionValue["adopt"]): AdminSessionValue {
  return {
    status: "unauthenticated",
    bootstrap: null,
    message: "",
    reload: () => {},
    adopt,
    signOut: async () => {},
    can: () => false,
  };
}

/**
 * Mounts the callback inside StrictMode, which double-invokes effects in
 * development exactly as the real console does (see src/main.tsx).
 */
function renderCallback(url: string, adopt: AdminSessionValue["adopt"] = vi.fn()) {
  const result = render(
    <StrictMode>
      <AdminSessionContext.Provider value={sessionValue(adopt)}>
        <MemoryRouter initialEntries={[url]}>
          <Routes>
            <Route path="/" element={<p>console montado</p>} />
            <Route path="/oidc-callback" element={<OIDCCallbackPage />} />
          </Routes>
        </MemoryRouter>
      </AdminSessionContext.Provider>
    </StrictMode>,
  );
  return { ...result, adopt };
}

beforeEach(() => {
  exchangeMock.mockReset();
  createSessionMock.mockReset();
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe("OIDCCallbackPage under StrictMode", () => {
  // The authorization code is single-use. StrictMode runs setup, cleanup and
  // setup again, so an effect that starts the exchange in its body replays the
  // code and the provider answers invalid_grant. This is the regression test
  // for that: the code must be exchanged exactly once, and the effect that is
  // actually mounted must still see the result.
  it("exchanges the authorization code exactly once and still adopts the session", async () => {
    exchangeMock.mockResolvedValue("access-token-1");
    createSessionMock.mockResolvedValue(BOOTSTRAP);

    const adopt = vi.fn();
    renderCallback("/oidc-callback?code=abc", adopt);

    await screen.findByText("console montado");

    expect(exchangeMock).toHaveBeenCalledTimes(1);
    expect(exchangeMock).toHaveBeenCalledWith("abc");
    expect(createSessionMock).toHaveBeenCalledTimes(1);
    expect(createSessionMock).toHaveBeenCalledWith("access-token-1");
    expect(adopt).toHaveBeenCalledTimes(1);
    expect(adopt).toHaveBeenCalledWith(BOOTSTRAP);
  });

  // The failure path must reach the mounted effect too: a message discarded by
  // the cleanup of a torn-down setup would leave the page spinning forever.
  it("reports an exchange failure once, from the mounted effect", async () => {
    exchangeMock.mockRejectedValue(new AuthError(401, "invalid_oidc_callback", "no"));

    const adopt = vi.fn();
    renderCallback("/oidc-callback?code=abc", adopt);

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Não foi possível concluir o login com SSO.",
    );
    expect(exchangeMock).toHaveBeenCalledTimes(1);
    expect(createSessionMock).not.toHaveBeenCalled();
    expect(adopt).not.toHaveBeenCalled();
  });

  // The exchange succeeded and the platform still refused the handshake: the
  // person authenticated but is not an administrator.
  it("reports an administrative refusal without retrying the exchange", async () => {
    exchangeMock.mockResolvedValue("access-token-1");
    createSessionMock.mockRejectedValue(new AdminApiError(403, "forbidden", "forbidden"));

    const adopt = vi.fn();
    renderCallback("/oidc-callback?code=abc", adopt);

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Sua conta não possui acesso administrativo.",
    );
    expect(exchangeMock).toHaveBeenCalledTimes(1);
    expect(createSessionMock).toHaveBeenCalledTimes(1);
    expect(adopt).not.toHaveBeenCalled();
  });

  it("refuses a callback with no code without calling the provider", async () => {
    renderCallback("/oidc-callback?next=https%3A%2F%2Fevil.test");

    expect(await screen.findByRole("alert")).toHaveTextContent("Retorno de SSO inválido.");
    expect(exchangeMock).not.toHaveBeenCalled();
    expect(createSessionMock).not.toHaveBeenCalled();
  });

  it("shows a loading state while the exchange is in flight", () => {
    exchangeMock.mockReturnValue(new Promise(() => {}));

    renderCallback("/oidc-callback?code=abc");

    expect(screen.getByRole("status")).toHaveTextContent(
      "Validando retorno do provedor de identidade…",
    );
    expect(exchangeMock).toHaveBeenCalledTimes(1);
  });

  // react-router keeps this element mounted when only the search string
  // changes, so a second callback must not reuse the first code's result. The
  // first exchange is left pending on purpose: that is what keeps the page
  // mounted while the code underneath it changes.
  it("starts a new exchange when the authorization code changes", async () => {
    exchangeMock.mockReturnValueOnce(new Promise(() => {})).mockResolvedValueOnce("access-token-2");
    createSessionMock.mockResolvedValue(BOOTSTRAP);

    const adopt = vi.fn();
    render(
      <StrictMode>
        <AdminSessionContext.Provider value={sessionValue(adopt)}>
          <MemoryRouter initialEntries={["/oidc-callback?code=first"]}>
            <Routes>
              <Route path="/" element={<p>console montado</p>} />
              <Route path="/oidc-callback" element={<CallbackWithCodeSwitch />} />
            </Routes>
          </MemoryRouter>
        </AdminSessionContext.Provider>
      </StrictMode>,
    );

    await waitFor(() => expect(exchangeMock).toHaveBeenCalledTimes(1));

    await userEvent.click(screen.getByRole("button", { name: "trocar code" }));

    await waitFor(() => expect(exchangeMock).toHaveBeenCalledTimes(2));
    expect(exchangeMock.mock.calls.map((call) => call[0])).toEqual(["first", "second"]);
    // And the second code, not the first, is the one that produced the session.
    await waitFor(() => expect(adopt).toHaveBeenCalledTimes(1));
    expect(createSessionMock).toHaveBeenCalledExactlyOnceWith("access-token-2");
  });
});

/**
 * The callback plus a control that rewrites only the search string. Both live
 * inside the same route element, so changing the code leaves the page mounted —
 * which is precisely the situation the per-code ref exists for.
 */
function CallbackWithCodeSwitch() {
  const [, setParams] = useSearchParams();
  return (
    <>
      <OIDCCallbackPage />
      <button type="button" onClick={() => setParams({ code: "second" })}>
        trocar code
      </button>
    </>
  );
}
