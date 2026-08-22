import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import App from "../App";
import type { AdminBootstrap } from "../api/adminApi";

const BOOTSTRAP: AdminBootstrap = {
  identity: {
    user_id: "u1",
    email: "admin@example.test",
    display_name: "Admin Master",
    avatar_url: "",
  },
  capabilities: ["admin.audit.read", "admin.config.read"],
  environment: "PRODUCTION",
  build: { service: "admin-service", version: "1.2.3", commit: "abc1234" },
  session: { idle_expires_at: "2026-08-20T12:00:00Z", absolute_expires_at: "2026-08-20T20:00:00Z" },
  csrf_token: "csrf-1",
};

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "content-type": "application/json" },
  });
}

function mockConsole(bootstrap: AdminBootstrap = BOOTSTRAP) {
  const fetchMock = vi.fn().mockImplementation((url: string) => {
    if (url.startsWith("/api/admin/bootstrap")) {
      return Promise.resolve(jsonResponse({ data: bootstrap }));
    }
    if (url.startsWith("/api/admin/audit/events")) {
      return Promise.resolve(jsonResponse({ data: { events: [] } }));
    }
    if (url.startsWith("/api/admin/session")) {
      return Promise.resolve(new Response(null, { status: 204 }));
    }
    return Promise.resolve(jsonResponse({ error: { code: "not_found", message: "x" } }, 404));
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

afterEach(() => {
  vi.restoreAllMocks();
  window.history.pushState({}, "", "/");
});

describe("AdminLayout", () => {
  it("renders the shell landmarks, identity and build", async () => {
    mockConsole();
    render(<App />);

    await screen.findByRole("heading", { name: "Visão geral" });
    expect(screen.getByRole("banner")).toBeInTheDocument();
    expect(screen.getByRole("navigation", { name: "Seções administrativas" })).toBeInTheDocument();
    expect(screen.getByRole("main")).toBeInTheDocument();
    expect(screen.getByTestId("admin-identity")).toHaveTextContent("Admin Master");
    expect(screen.getByTestId("admin-identity")).toHaveTextContent("admin@example.test");
    expect(screen.getByText(/admin-service 1\.2\.3 \(abc1234\)/)).toBeInTheDocument();
  });

  // The environment comes from the server payload. Nothing in the console reads
  // the hostname, and the label is text, not only a colour.
  it("shows the environment reported by the backend", async () => {
    mockConsole();
    render(<App />);

    const badge = await screen.findByTestId("admin-environment");
    expect(badge).toHaveTextContent("PRODUCTION");
    expect(badge).toHaveAttribute("data-environment", "PRODUCTION");
  });

  it("shows a different environment without any change of hostname", async () => {
    mockConsole({ ...BOOTSTRAP, environment: "DEVELOPMENT" });
    render(<App />);

    expect(await screen.findByTestId("admin-environment")).toHaveTextContent("DEVELOPMENT");
  });

  it("renders only the sections the session grants", async () => {
    mockConsole();
    render(<App />);

    const nav = await screen.findByRole("navigation", { name: "Seções administrativas" });
    expect(within(nav).getByText("Visão geral")).toBeInTheDocument();
    expect(within(nav).getByText("Auditoria")).toBeInTheDocument();
    expect(within(nav).queryByText("Usuários")).not.toBeInTheDocument();
  });

  // A section with no page is drawn as unavailable and cannot be activated, in
  // every environment: a control that looks live and does nothing is how an
  // operator ends up believing a policy was applied.
  it("marks unimplemented sections as unavailable and unfocusable", async () => {
    mockConsole({ ...BOOTSTRAP, capabilities: ["admin.superuser"] });
    render(<App />);

    const nav = await screen.findByRole("navigation", { name: "Seções administrativas" });
    const placeholder = within(nav).getByText("Health Center").closest("span");
    expect(placeholder).toHaveAttribute("aria-disabled", "true");
    expect(placeholder?.tagName).toBe("SPAN");
    expect(
      within(nav)
        .getAllByRole("link")
        .map((link) => link.textContent),
    ).toEqual([
      "Visão geral",
      "Usuários",
      "Canais e grupos",
      "Segurança e políticas",
      "Arquivos e armazenamento",
      "Configurações",
      "Auditoria",
    ]);
  });

  it("navigates between implemented sections and marks the current one", async () => {
    mockConsole();
    render(<App />);

    const nav = await screen.findByRole("navigation", { name: "Seções administrativas" });
    await userEvent.click(within(nav).getByRole("link", { name: "Auditoria" }));

    await screen.findByRole("heading", { name: "Auditoria" });
    expect(within(nav).getByRole("link", { name: "Auditoria" })).toHaveAttribute(
      "aria-current",
      "page",
    );
  });

  it("offers a skip link to the main region", async () => {
    mockConsole();
    render(<App />);

    const skip = await screen.findByRole("link", { name: "Ir para o conteúdo" });
    expect(skip).toHaveAttribute("href", "#admin-main");
  });

  it("signs out and returns to the sign-in screen", async () => {
    const fetchMock = mockConsole();
    render(<App />);

    await screen.findByRole("heading", { name: "Visão geral" });
    await userEvent.click(screen.getByRole("button", { name: "Sair" }));

    await screen.findByRole("heading", { name: "Console administrativo" });
    expect(
      fetchMock.mock.calls.some(
        (call) => call[0] === "/api/admin/session" && call[1]?.method === "DELETE",
      ),
    ).toBe(true);
  });
});

describe("shell without a session", () => {
  // The shell renders nothing rather than a half-populated header if it is ever
  // mounted before the bootstrap payload exists.
  it("renders nothing when there is no bootstrap payload", async () => {
    const { default: AdminLayout } = await import("./AdminLayout");
    const { AdminSessionContext } = await import("../session/AdminSessionContext");
    const { MemoryRouter } = await import("react-router");

    const { container } = render(
      <AdminSessionContext.Provider
        value={{
          status: "loading",
          bootstrap: null,
          message: "",
          reload: () => {},
          adopt: () => {},
          signOut: async () => {},
          can: () => false,
        }}
      >
        <MemoryRouter>
          <AdminLayout />
        </MemoryRouter>
      </AdminSessionContext.Provider>,
    );

    expect(container).toBeEmptyDOMElement();
  });
});
