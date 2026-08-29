import { render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import SecuritySettingsPage from "./SecuritySettingsPage";

describe("SecuritySettingsPage", () => {
  const originalUrl = import.meta.env.VITE_KEYCLOAK_ACCOUNT_URL;

  afterEach(() => {
    vi.stubEnv("VITE_KEYCLOAK_ACCOUNT_URL", originalUrl ?? "");
  });

  it("never implements a local password/MFA form", () => {
    vi.stubEnv("VITE_KEYCLOAK_ACCOUNT_URL", "https://id.nchat.local/realms/nchat/account");
    render(<SecuritySettingsPage />);
    expect(screen.queryByLabelText(/senha/i)).not.toBeInTheDocument();
    expect(screen.queryByText(/totp|autenticador|passkey/i)).not.toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Segurança" })).toHaveProperty("tagName", "H2");
  });

  it("links to the configured Keycloak account URL, opened in a new tab safely", () => {
    vi.stubEnv("VITE_KEYCLOAK_ACCOUNT_URL", "https://id.nchat.local/realms/nchat/account");
    render(<SecuritySettingsPage />);
    const link = screen.getByRole("link", { name: /gerenciar segurança da conta/i });
    expect(link).toHaveAttribute("href", "https://id.nchat.local/realms/nchat/account");
    expect(link).toHaveAttribute("target", "_blank");
    expect(link).toHaveAttribute("rel", expect.stringContaining("noopener"));
  });

  it.each([
    "javascript:alert(document.domain)",
    "data:text/html,<script>alert(1)</script>",
    "http://id.nchat.local/realms/nchat/account",
    "https://user:password@id.nchat.local/realms/nchat/account",
  ])("does not render an unsafe account-management URL: %s", (unsafeUrl) => {
    vi.stubEnv("VITE_KEYCLOAK_ACCOUNT_URL", unsafeUrl);
    render(<SecuritySettingsPage />);

    expect(
      screen.queryByRole("link", { name: /gerenciar segurança da conta/i }),
    ).not.toBeInTheDocument();
    expect(screen.getByText(/não está configurado neste ambiente/i)).toBeInTheDocument();
  });

  it("shows an honest message with no dead link when the URL is not configured", () => {
    vi.stubEnv("VITE_KEYCLOAK_ACCOUNT_URL", "");
    render(<SecuritySettingsPage />);
    expect(screen.queryByRole("link", { name: /gerenciar segurança/i })).not.toBeInTheDocument();
    expect(
      screen.getByText(/gerencie senha e autenticação no provedor de identidade/i),
    ).toBeInTheDocument();
  });

  it("does not claim MFA is enabled or disabled — no fabricated status", () => {
    vi.stubEnv("VITE_KEYCLOAK_ACCOUNT_URL", "https://id.nchat.local/realms/nchat/account");
    render(<SecuritySettingsPage />);
    expect(screen.queryByText(/ativada|desativada/i)).not.toBeInTheDocument();
  });

  it("does not claim an account identity provider without account-level authority", () => {
    vi.stubEnv("VITE_KEYCLOAK_ACCOUNT_URL", "https://id.nchat.local/realms/nchat/account");
    render(<SecuritySettingsPage />);
    expect(screen.queryByText("Keycloak")).not.toBeInTheDocument();
    expect(screen.queryByText("Provedor de identidade")).not.toBeInTheDocument();
  });
});
