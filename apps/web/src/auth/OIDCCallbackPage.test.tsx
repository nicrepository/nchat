import { render, screen, waitFor } from "@testing-library/react";
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

beforeEach(() => {
  vi.clearAllMocks();
  window.history.replaceState({}, "", "/oidc-callback?code=opaque-code");
});

describe("OIDCCallbackPage", () => {
  it("exchanges code, stores tokens, removes code, and navigates home", async () => {
    const replaceSpy = vi.spyOn(window.history, "replaceState");
    mockOIDCExchange.mockResolvedValue({
      accessToken: "at",
      refreshToken: "rt",
      tokenType: "Bearer",
      expiresIn: 900,
      user: { id: "u1", email: "sso@example.com", displayName: "SSO", mustChangePassword: false },
    });

    renderCallback("/oidc-callback?code=opaque-code");

    await waitFor(() => {
      expect(mockOIDCExchange).toHaveBeenCalledWith("opaque-code");
      expect(mockSetTokens).toHaveBeenCalledWith("at", "rt");
      expect(mockNavigate).toHaveBeenCalledWith("/", { replace: true });
    });
    expect(replaceSpy).toHaveBeenCalledWith({}, "", "/oidc-callback");
  });

  it("shows generic error when code is absent", async () => {
    renderCallback("/oidc-callback");

    expect(await screen.findByRole("alert")).toHaveTextContent(/sso indisponível/i);
    expect(mockOIDCExchange).not.toHaveBeenCalled();
  });

  it("shows generic error when exchange fails", async () => {
    mockOIDCExchange.mockRejectedValue(new Error("raw backend detail"));

    renderCallback("/oidc-callback?code=bad-code");

    expect(await screen.findByRole("alert")).toHaveTextContent(/sso indisponível/i);
    expect(screen.getByRole("alert").textContent).not.toMatch(/raw backend detail|bad-code/i);
  });
});
