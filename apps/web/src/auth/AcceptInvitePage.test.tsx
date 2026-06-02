import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";

import { ApiRequestError } from "../lib/api";
import AcceptInvitePage from "./AcceptInvitePage";

const mockAcceptInvite = vi.fn();
vi.mock("./authApi", () => ({
  acceptInvite: (...args: unknown[]) => mockAcceptInvite(...args),
}));

function renderPage(search = "?token=inv123") {
  return render(
    <MemoryRouter initialEntries={[`/accept-invite${search}`]}>
      <AcceptInvitePage />
    </MemoryRouter>,
  );
}

afterEach(() => {
  vi.resetAllMocks();
});

describe("AcceptInvitePage", () => {
  it("renders invalid-link state when token is missing", () => {
    renderPage("");
    expect(screen.getByText(/convite inválido/i)).toBeInTheDocument();
    expect(screen.queryByLabelText(/nome de exibição/i)).not.toBeInTheDocument();
  });

  it("renders display name, full name, password and confirm fields without rendering the token", () => {
    renderPage();
    expect(screen.getByLabelText(/nome de exibição/i)).toBeInTheDocument();
    expect(screen.getByLabelText(/nome completo/i)).toBeInTheDocument();
    expect(screen.getByLabelText(/^senha$/i)).toBeInTheDocument();
    expect(screen.getByLabelText(/confirmar senha/i)).toBeInTheDocument();
    expect(document.body).not.toHaveTextContent("inv123");
  });

  it("shows validation error when passwords do not match", async () => {
    renderPage();
    await userEvent.type(screen.getByLabelText(/nome de exibição/i), "Alice");
    await userEvent.type(screen.getByLabelText(/^senha$/i), "P@ss1");
    await userEvent.type(screen.getByLabelText(/confirmar senha/i), "Other1");
    await userEvent.click(screen.getByRole("button", { name: /ativar/i }));
    expect(screen.getByText(/senhas não coincidem/i)).toBeInTheDocument();
    expect(mockAcceptInvite).not.toHaveBeenCalled();
  });

  it("calls acceptInvite with token from URL and form data", async () => {
    mockAcceptInvite.mockResolvedValue({
      id: "u1",
      email: "a@b.com",
      displayName: "Alice",
      createdAt: "2026-06-01T00:00:00Z",
    });
    renderPage("?token=inv123");
    await userEvent.type(screen.getByLabelText(/nome de exibição/i), "Alice");
    await userEvent.type(screen.getByLabelText(/nome completo/i), "Alice Smith");
    await userEvent.type(screen.getByLabelText(/^senha$/i), "P@ss1word");
    await userEvent.type(screen.getByLabelText(/confirmar senha/i), "P@ss1word");
    await userEvent.click(screen.getByRole("button", { name: /ativar/i }));
    await waitFor(() => {
      expect(mockAcceptInvite).toHaveBeenCalledWith(
        expect.objectContaining({
          token: "inv123",
          displayName: "Alice",
          fullName: "Alice Smith",
          password: "P@ss1word",
        }),
      );
    });
  });

  it("shows success state with login link on 201 and does not auto-login", async () => {
    mockAcceptInvite.mockResolvedValue({
      id: "u1",
      email: "a@b.com",
      displayName: "Alice",
      createdAt: "2026-06-01T00:00:00Z",
    });
    renderPage();
    await userEvent.type(screen.getByLabelText(/nome de exibição/i), "Alice");
    await userEvent.type(screen.getByLabelText(/^senha$/i), "P@ss1word");
    await userEvent.type(screen.getByLabelText(/confirmar senha/i), "P@ss1word");
    await userEvent.click(screen.getByRole("button", { name: /ativar/i }));
    await waitFor(() => {
      expect(screen.getByText(/conta ativada/i)).toBeInTheDocument();
    });
    expect(screen.getByRole("link", { name: /entrar/i })).toHaveAttribute("href", "/login");
  });

  it("shows generic error on 401 invalid_invite_token", async () => {
    mockAcceptInvite.mockRejectedValue(new ApiRequestError(401, "invalid_invite_token", "expired"));
    renderPage();
    await userEvent.type(screen.getByLabelText(/nome de exibição/i), "Alice");
    await userEvent.type(screen.getByLabelText(/^senha$/i), "P@ss1word");
    await userEvent.type(screen.getByLabelText(/confirmar senha/i), "P@ss1word");
    await userEvent.click(screen.getByRole("button", { name: /ativar/i }));
    await waitFor(() => {
      expect(screen.getByRole("alert")).toHaveTextContent(/convite inválido ou expirado/i);
    });
    expect(screen.getByRole("alert").textContent).not.toMatch(/expired/i);
  });

  it("shows expired-invite error when invalid_invite_token is returned with 401", async () => {
    mockAcceptInvite.mockRejectedValue(new ApiRequestError(401, "invalid_invite_token", "expired"));
    renderPage();
    await userEvent.type(screen.getByLabelText(/nome de exibição/i), "Alice");
    await userEvent.type(screen.getByLabelText(/^senha$/i), "P@ss1word");
    await userEvent.type(screen.getByLabelText(/confirmar senha/i), "P@ss1word");
    await userEvent.click(screen.getByRole("button", { name: /ativar/i }));
    await waitFor(() => {
      expect(screen.getByRole("alert")).toHaveTextContent(/convite inválido ou expirado/i);
    });
    expect(screen.getByRole("alert").textContent).not.toMatch(/requisitos de segurança|expired/i);
  });

  it("shows generic password policy error from backend on 400", async () => {
    mockAcceptInvite.mockRejectedValue(
      new ApiRequestError(400, "bad_request", "minimum length is 8 characters"),
    );
    renderPage();
    await userEvent.type(screen.getByLabelText(/nome de exibição/i), "Alice");
    await userEvent.type(screen.getByLabelText(/^senha$/i), "short");
    await userEvent.type(screen.getByLabelText(/confirmar senha/i), "short");
    await userEvent.click(screen.getByRole("button", { name: /ativar/i }));
    await waitFor(() => {
      expect(screen.getByRole("alert")).toHaveTextContent(/requisitos de segurança/i);
    });
    expect(screen.getByRole("alert").textContent).not.toMatch(/minimum length/i);
  });

  it("disables submit while loading", async () => {
    let reject: () => void;
    mockAcceptInvite.mockReturnValue(
      new Promise<never>((_, r) => {
        reject = () => r(new Error("cancelled"));
      }),
    );
    renderPage();
    await userEvent.type(screen.getByLabelText(/nome de exibição/i), "Alice");
    await userEvent.type(screen.getByLabelText(/^senha$/i), "P@ss1word");
    await userEvent.type(screen.getByLabelText(/confirmar senha/i), "P@ss1word");
    await userEvent.click(screen.getByRole("button", { name: /ativar/i }));
    expect(screen.getByRole("button", { name: /ativando/i })).toBeDisabled();
    reject!();
  });
});
