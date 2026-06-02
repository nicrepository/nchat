import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { ApiRequestError } from "../lib/api";
import LoginPage from "./LoginPage";

const mockNavigate = vi.fn();
vi.mock("react-router-dom", async () => {
  const actual = await vi.importActual<typeof import("react-router-dom")>("react-router-dom");
  return {
    ...actual,
    useNavigate: () => mockNavigate,
  };
});

const mockLogin = vi.fn();
vi.mock("./authApi", () => ({ login: (...args: unknown[]) => mockLogin(...args) }));

const mockClearTokens = vi.fn();
const mockSetTokens = vi.fn();
vi.mock("../lib/authSession", () => ({
  clearTokens: (...args: unknown[]) => mockClearTokens(...args),
  setTokens: (...args: unknown[]) => mockSetTokens(...args),
}));

function renderLogin() {
  return render(
    <MemoryRouter>
      <LoginPage />
    </MemoryRouter>,
  );
}

beforeEach(() => {
  vi.clearAllMocks();
});

describe("LoginPage", () => {
  it("renders email and password fields and submit button", () => {
    renderLogin();
    expect(screen.getByLabelText(/e-mail corporativo/i)).toBeInTheDocument();
    expect(screen.getByLabelText(/senha/i)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /entrar$/i })).toBeInTheDocument();
  });

  it("renders disabled SSO button", () => {
    renderLogin();
    const ssoBtn = screen.getByRole("button", { name: /sso/i });
    expect(ssoBtn).toBeDisabled();
  });

  it("renders link to forgot-password page", () => {
    renderLogin();
    expect(screen.getByRole("link", { name: /esqueci minha senha/i })).toHaveAttribute(
      "href",
      "/forgot-password",
    );
  });

  it("calls login API with email and password on submit", async () => {
    mockLogin.mockResolvedValue({
      accessToken: "at",
      refreshToken: "rt",
      tokenType: "Bearer",
      expiresIn: 900,
      user: { id: "u1", email: "a@b.com", displayName: "Alice", mustChangePassword: false },
    });
    renderLogin();
    await userEvent.type(screen.getByLabelText(/e-mail corporativo/i), "a@b.com");
    await userEvent.type(screen.getByLabelText(/senha/i), "pass");
    await userEvent.click(screen.getByRole("button", { name: /entrar$/i }));
    await waitFor(() => {
      expect(mockLogin).toHaveBeenCalledWith(
        expect.objectContaining({ email: "a@b.com", password: "pass" }),
      );
    });
  });

  it("stores tokens and navigates to / on success", async () => {
    mockLogin.mockResolvedValue({
      accessToken: "at",
      refreshToken: "rt",
      tokenType: "Bearer",
      expiresIn: 900,
      user: { id: "u1", email: "a@b.com", displayName: "Alice", mustChangePassword: false },
    });
    renderLogin();
    await userEvent.type(screen.getByLabelText(/e-mail corporativo/i), "a@b.com");
    await userEvent.type(screen.getByLabelText(/senha/i), "pass");
    await userEvent.click(screen.getByRole("button", { name: /entrar$/i }));
    await waitFor(() => {
      expect(mockSetTokens).toHaveBeenCalledWith("at", "rt");
      expect(mockNavigate).toHaveBeenCalledWith("/", { replace: true });
    });
  });

  it("blocks app navigation when password change is required", async () => {
    mockLogin.mockResolvedValue({
      accessToken: "at",
      refreshToken: "rt",
      tokenType: "Bearer",
      expiresIn: 900,
      user: { id: "u1", email: "a@b.com", displayName: "Alice", mustChangePassword: true },
    });
    renderLogin();
    await userEvent.type(screen.getByLabelText(/e-mail corporativo/i), "a@b.com");
    await userEvent.type(screen.getByLabelText(/senha/i), "temporary-pass");
    await userEvent.click(screen.getByRole("button", { name: /entrar$/i }));
    await waitFor(() => {
      expect(screen.getByRole("alert")).toHaveTextContent(/troca de senha obrigatória/i);
    });
    expect(mockClearTokens).toHaveBeenCalled();
    expect(mockSetTokens).not.toHaveBeenCalled();
    expect(mockNavigate).not.toHaveBeenCalledWith("/", expect.anything());
  });

  it("shows generic error message on 401 without revealing account status", async () => {
    mockLogin.mockRejectedValue(new ApiRequestError(401, "invalid_credentials", "locked account"));
    renderLogin();
    await userEvent.type(screen.getByLabelText(/e-mail corporativo/i), "x@x.com");
    await userEvent.type(screen.getByLabelText(/senha/i), "wrong");
    await userEvent.click(screen.getByRole("button", { name: /entrar$/i }));
    await waitFor(() => {
      expect(screen.getByRole("alert")).toBeInTheDocument();
    });
    expect(screen.getByRole("alert")).toHaveTextContent(/e-mail ou senha inválidos/i);
    expect(screen.getByRole("alert").textContent).not.toMatch(/locked|bloqueado|tentativas/i);
    expect(mockNavigate).not.toHaveBeenCalled();
  });

  it("shows generic error on network failure", async () => {
    mockLogin.mockRejectedValue(new ApiRequestError(0, "network_error", "Network error"));
    renderLogin();
    await userEvent.type(screen.getByLabelText(/e-mail corporativo/i), "x@x.com");
    await userEvent.type(screen.getByLabelText(/senha/i), "pass");
    await userEvent.click(screen.getByRole("button", { name: /entrar$/i }));
    await waitFor(() => {
      expect(screen.getByRole("alert")).toHaveTextContent(/e-mail ou senha inválidos/i);
    });
  });

  it("disables submit button while loading", async () => {
    let rejectLogin: () => void;
    mockLogin.mockReturnValue(
      new Promise<never>((_, reject) => {
        rejectLogin = () => reject(new Error("cancelled"));
      }),
    );
    renderLogin();
    await userEvent.type(screen.getByLabelText(/e-mail corporativo/i), "x@x.com");
    await userEvent.type(screen.getByLabelText(/senha/i), "pass");
    await userEvent.click(screen.getByRole("button", { name: /entrar$/i }));
    expect(screen.getByRole("button", { name: /entrando/i })).toBeDisabled();
    rejectLogin!();
  });
});
