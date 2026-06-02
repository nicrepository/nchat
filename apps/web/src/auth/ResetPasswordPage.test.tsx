import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";

import { ApiRequestError } from "../lib/api";
import ResetPasswordPage from "./ResetPasswordPage";

const mockResetPassword = vi.fn();
vi.mock("./authApi", () => ({
  resetPassword: (...args: unknown[]) => mockResetPassword(...args),
}));

function renderPage(search = "?token=abc123") {
  return render(
    <MemoryRouter initialEntries={[`/reset-password${search}`]}>
      <ResetPasswordPage />
    </MemoryRouter>,
  );
}

afterEach(() => {
  vi.resetAllMocks();
});

describe("ResetPasswordPage", () => {
  it("renders invalid-link state when token query param is missing", () => {
    renderPage("");
    expect(screen.getByText(/link inválido/i)).toBeInTheDocument();
    expect(screen.queryByLabelText(/nova senha/i)).not.toBeInTheDocument();
  });

  it("renders password fields when token is present without rendering the token", () => {
    renderPage();
    expect(screen.getByLabelText(/nova senha/i)).toBeInTheDocument();
    expect(screen.getByLabelText(/confirmar senha/i)).toBeInTheDocument();
    expect(document.body).not.toHaveTextContent("abc123");
  });

  it("shows validation error when passwords do not match", async () => {
    renderPage();
    await userEvent.type(screen.getByLabelText(/nova senha/i), "Pass@1");
    await userEvent.type(screen.getByLabelText(/confirmar senha/i), "Different1");
    await userEvent.click(screen.getByRole("button", { name: /redefinir/i }));
    expect(screen.getByText(/senhas não coincidem/i)).toBeInTheDocument();
    expect(mockResetPassword).not.toHaveBeenCalled();
  });

  it("calls resetPassword with token from URL and new password", async () => {
    mockResetPassword.mockResolvedValue(undefined);
    renderPage("?token=abc123");
    await userEvent.type(screen.getByLabelText(/nova senha/i), "NewP@ss1");
    await userEvent.type(screen.getByLabelText(/confirmar senha/i), "NewP@ss1");
    await userEvent.click(screen.getByRole("button", { name: /redefinir/i }));
    await waitFor(() => {
      expect(mockResetPassword).toHaveBeenCalledWith("abc123", "NewP@ss1");
    });
  });

  it("shows success state and login link after 204", async () => {
    mockResetPassword.mockResolvedValue(undefined);
    renderPage();
    await userEvent.type(screen.getByLabelText(/nova senha/i), "NewP@ss1");
    await userEvent.type(screen.getByLabelText(/confirmar senha/i), "NewP@ss1");
    await userEvent.click(screen.getByRole("button", { name: /redefinir/i }));
    await waitFor(() => {
      expect(screen.getByText(/senha redefinida/i)).toBeInTheDocument();
    });
    expect(screen.getByRole("link", { name: /entrar/i })).toHaveAttribute("href", "/login");
  });

  it("shows generic error on 401 invalid_token", async () => {
    mockResetPassword.mockRejectedValue(new ApiRequestError(401, "invalid_token", "expired"));
    renderPage();
    await userEvent.type(screen.getByLabelText(/nova senha/i), "NewP@ss1");
    await userEvent.type(screen.getByLabelText(/confirmar senha/i), "NewP@ss1");
    await userEvent.click(screen.getByRole("button", { name: /redefinir/i }));
    await waitFor(() => {
      expect(screen.getByRole("alert")).toHaveTextContent(/link inválido ou expirado/i);
    });
    expect(screen.getByRole("alert").textContent).not.toMatch(/expired/i);
  });

  it("shows expired-link error when invalid_token is returned with 400", async () => {
    mockResetPassword.mockRejectedValue(new ApiRequestError(400, "invalid_token", "expired"));
    renderPage();
    await userEvent.type(screen.getByLabelText(/nova senha/i), "NewP@ss1");
    await userEvent.type(screen.getByLabelText(/confirmar senha/i), "NewP@ss1");
    await userEvent.click(screen.getByRole("button", { name: /redefinir/i }));
    await waitFor(() => {
      expect(screen.getByRole("alert")).toHaveTextContent(/link inválido ou expirado/i);
    });
    expect(screen.getByRole("alert").textContent).not.toMatch(/requisitos de segurança|expired/i);
  });

  it("shows generic password policy message on 400 without raw backend text", async () => {
    mockResetPassword.mockRejectedValue(
      new ApiRequestError(400, "bad_request", "minimum length is 8 characters"),
    );
    renderPage();
    await userEvent.type(screen.getByLabelText(/nova senha/i), "short");
    await userEvent.type(screen.getByLabelText(/confirmar senha/i), "short");
    await userEvent.click(screen.getByRole("button", { name: /redefinir/i }));
    await waitFor(() => {
      expect(screen.getByRole("alert")).toHaveTextContent(/requisitos de segurança/i);
    });
    expect(screen.getByRole("alert").textContent).not.toMatch(/minimum length/i);
  });

  it("disables submit while loading", async () => {
    let resolve: () => void;
    mockResetPassword.mockReturnValue(new Promise<void>((r) => (resolve = r)));
    renderPage();
    await userEvent.type(screen.getByLabelText(/nova senha/i), "NewP@ss1");
    await userEvent.type(screen.getByLabelText(/confirmar senha/i), "NewP@ss1");
    await userEvent.click(screen.getByRole("button", { name: /redefinir/i }));
    expect(screen.getByRole("button", { name: /redefinindo/i })).toBeDisabled();
    resolve!();
  });
});
