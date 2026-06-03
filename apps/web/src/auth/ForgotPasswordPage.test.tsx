import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";

import { ApiRequestError } from "../lib/api";
import ForgotPasswordPage from "./ForgotPasswordPage";

const mockForgotPassword = vi.fn();
vi.mock("./authApi", () => ({
  forgotPassword: (...args: unknown[]) => mockForgotPassword(...args),
}));

function renderPage() {
  return render(
    <MemoryRouter>
      <ForgotPasswordPage />
    </MemoryRouter>,
  );
}

afterEach(() => {
  vi.resetAllMocks();
});

describe("ForgotPasswordPage", () => {
  it("renders email field and submit button", () => {
    renderPage();
    expect(screen.getByLabelText(/e-mail/i)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /enviar/i })).toBeInTheDocument();
  });

  it("renders back to login link", () => {
    renderPage();
    expect(screen.getByRole("link", { name: /voltar/i })).toHaveAttribute("href", "/login");
  });

  it("shows generic success message after submit without revealing account existence", async () => {
    mockForgotPassword.mockResolvedValue(undefined);
    renderPage();
    await userEvent.type(screen.getByLabelText(/e-mail/i), "anyone@example.com");
    await userEvent.click(screen.getByRole("button", { name: /enviar/i }));
    await waitFor(() => {
      expect(screen.getByText(/se o e-mail estiver cadastrado/i)).toBeInTheDocument();
    });
  });

  it("shows the same generic success message even when API throws", async () => {
    mockForgotPassword.mockRejectedValue(new ApiRequestError(503, "service_unavailable", "down"));
    renderPage();
    await userEvent.type(screen.getByLabelText(/e-mail/i), "anyone@example.com");
    await userEvent.click(screen.getByRole("button", { name: /enviar/i }));
    await waitFor(() => {
      expect(screen.getByText(/se o e-mail estiver cadastrado/i)).toBeInTheDocument();
    });
    expect(screen.queryByText(/down/i)).not.toBeInTheDocument();
  });

  it("disables button while loading", async () => {
    let resolve: () => void;
    mockForgotPassword.mockReturnValue(new Promise<void>((r) => (resolve = r)));
    renderPage();
    await userEvent.type(screen.getByLabelText(/e-mail/i), "x@x.com");
    await userEvent.click(screen.getByRole("button", { name: /enviar/i }));
    expect(screen.getByRole("button", { name: /enviando/i })).toBeDisabled();
    resolve!();
  });

  it("hides form after success and shows login link", async () => {
    mockForgotPassword.mockResolvedValue(undefined);
    renderPage();
    await userEvent.type(screen.getByLabelText(/e-mail/i), "x@x.com");
    await userEvent.click(screen.getByRole("button", { name: /enviar/i }));
    await waitFor(() => {
      expect(screen.queryByLabelText(/e-mail/i)).not.toBeInTheDocument();
    });
    expect(screen.getByRole("link", { name: /entrar/i })).toBeInTheDocument();
  });
});
