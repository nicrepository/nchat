import { render, screen, waitFor } from "@testing-library/react";
import { StrictMode } from "react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";

import OIDCCallbackPage from "./OIDCCallbackPage";

const mockNavigate = vi.fn();
vi.mock("react-router-dom", async () => {
  const actual = await vi.importActual<typeof import("react-router-dom")>("react-router-dom");
  return {
    ...actual,
    useNavigate: () => mockNavigate,
  };
});

const mockOIDCExchange = vi.fn();
vi.mock("./authApi", () => ({ oidcExchange: (...args: unknown[]) => mockOIDCExchange(...args) }));

const mockSetTokens = vi.fn();
vi.mock("../lib/authSession", () => ({
  setTokens: (...args: unknown[]) => mockSetTokens(...args),
}));

function renderCallback(initialEntry: string) {
  return render(
    <MemoryRouter initialEntries={[initialEntry]}>
      <Routes>
        <Route path="/oidc-callback" element={<OIDCCallbackPage />} />
      </Routes>
    </MemoryRouter>,
  );
}

const SUCCESS_RESPONSE = {
  accessToken: "at",
  refreshToken: "rt",
  tokenType: "Bearer",
  expiresIn: 900,
  user: { id: "u1", email: "sso@example.com", displayName: "SSO", mustChangePassword: false },
};

beforeEach(() => {
  vi.clearAllMocks();
  window.history.replaceState({}, "", "/oidc-callback?code=opaque-code");
});

describe("OIDCCallbackPage", () => {
  it("exchanges code, stores tokens, removes code, and navigates home", async () => {
    const replaceSpy = vi.spyOn(window.history, "replaceState");
    mockOIDCExchange.mockResolvedValue(SUCCESS_RESPONSE);

    renderCallback("/oidc-callback?code=opaque-code");

    await waitFor(() => {
      expect(mockOIDCExchange).toHaveBeenCalledWith("opaque-code");
      expect(mockSetTokens).toHaveBeenCalledWith("at", "rt");
      expect(mockNavigate).toHaveBeenCalledWith("/", { replace: true });
    });
    expect(mockOIDCExchange).toHaveBeenCalledTimes(1);
    expect(replaceSpy).toHaveBeenCalledWith({}, "", "/oidc-callback");
  });

  it("removes code from URL before calling oidcExchange", async () => {
    const replaceSpy = vi.spyOn(window.history, "replaceState");
    mockOIDCExchange.mockImplementation(async () => {
      // replaceState must already be called before oidcExchange begins
      expect(replaceSpy).toHaveBeenCalledWith({}, "", "/oidc-callback");
      return SUCCESS_RESPONSE;
    });

    renderCallback("/oidc-callback?code=opaque-code");

    await waitFor(() => {
      expect(mockNavigate).toHaveBeenCalledWith("/", { replace: true });
    });
    expect(mockOIDCExchange).toHaveBeenCalledTimes(1);
  });

  it("does not call oidcExchange more than once under React StrictMode", async () => {
    mockOIDCExchange.mockResolvedValue(SUCCESS_RESPONSE);

    render(
      <StrictMode>
        <MemoryRouter initialEntries={["/oidc-callback?code=opaque-code"]}>
          <Routes>
            <Route path="/oidc-callback" element={<OIDCCallbackPage />} />
          </Routes>
        </MemoryRouter>
      </StrictMode>,
    );

    await waitFor(() => {
      expect(mockNavigate).toHaveBeenCalledWith("/", { replace: true });
    });
    expect(mockOIDCExchange).toHaveBeenCalledTimes(1);
  });

  it("shows generic error and cleans URL when code is blank (?code=)", async () => {
    const replaceSpy = vi.spyOn(window.history, "replaceState");
    renderCallback("/oidc-callback?code=");

    expect(await screen.findByRole("alert")).toHaveTextContent(
      /não foi possível concluir o login com sso/i,
    );
    expect(mockOIDCExchange).not.toHaveBeenCalled();
    expect(replaceSpy).toHaveBeenCalledWith({}, "", "/oidc-callback");
  });

  it("shows generic error when code is absent", async () => {
    renderCallback("/oidc-callback");

    expect(await screen.findByRole("alert")).toHaveTextContent(
      /não foi possível concluir o login com sso/i,
    );
    expect(mockOIDCExchange).not.toHaveBeenCalled();
  });

  it("shows generic error when exchange fails", async () => {
    mockOIDCExchange.mockRejectedValue(new Error("raw backend detail"));

    renderCallback("/oidc-callback?code=bad-code");

    expect(await screen.findByRole("alert")).toHaveTextContent(
      /não foi possível concluir o login com sso/i,
    );
    expect(screen.getByRole("alert").textContent).not.toMatch(/raw backend detail|bad-code/i);
  });
});
