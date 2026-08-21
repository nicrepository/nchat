import { render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, useLocation } from "react-router";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import { _resetCSRFToken } from "../api/client";
import { AdminSessionContext } from "../session/AdminSessionContext";
import {
  errorResponse,
  jsonResponse,
  renderWithSession,
  sessionValue,
  TEST_USER_ID,
} from "../test/harness";
import UserDetailDialog from "./UserDetailDialog";

function detail(overrides: Record<string, unknown> = {}) {
  return {
    data: {
      id: "u-ana",
      email: "ana@example.test",
      display_name: "Ana",
      full_name: "Ana Lima",
      avatar_url: "",
      status: "active",
      auth_source: "manual",
      external_provider: "",
      identity_managed_externally: false,
      last_login_at: null,
      created_at: "2026-01-01T00:00:00Z",
      platform_admin: false,
      admin_roles: [],
      workspace_roles: [],
      active_sessions: 1,
      memberships: [
        {
          workspace_id: "w1",
          workspace_name: "NChat",
          role: "admin",
          status: "active",
          joined_at: "2026-01-01T00:00:00Z",
        },
      ],
      channel_count: 4,
      role_grants: [],
      available_roles: [
        {
          slug: "platform-auditor",
          description: "Somente leitura.",
          capabilities: ["admin.audit.read"],
        },
      ],
      ...overrides,
    },
  };
}

/** The same record, with the auditor role already held. */
function detailWithGrant() {
  return detail({
    role_grants: [
      {
        slug: "platform-auditor",
        description: "Somente leitura.",
        capabilities: ["admin.audit.read"],
        granted_at: "2026-02-01T00:00:00Z",
        granted_by: "root@example.test",
      },
    ],
  });
}

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  _resetCSRFToken();
});

describe("UserDetailDialog", () => {
  it("is a labelled modal dialog with focus moved into it", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(jsonResponse(detail())));
    renderWithSession(<UserDetailDialog userID="u-ana" onClose={vi.fn()} onChanged={vi.fn()} />, [
      "admin.users.read",
    ]);

    expect(screen.getByRole("dialog")).toHaveAttribute("aria-modal", "true");
    expect(screen.getByRole("button", { name: "Fechar" })).toHaveFocus();
    expect(await screen.findByRole("heading", { name: "Ana Lima" })).toBeInTheDocument();
  });

  it("shows memberships and the workspace role, not just a count", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(jsonResponse(detail())));
    renderWithSession(<UserDetailDialog userID="u-ana" onClose={vi.fn()} onChanged={vi.fn()} />, [
      "admin.users.read",
    ]);

    expect(await screen.findByRole("rowheader", { name: "NChat" })).toBeInTheDocument();
    expect(screen.getByText("admin")).toBeInTheDocument();
  });

  // The console must say which fields NChat does not own, and it offers no way
  // to write any of them.
  it("says when the identity belongs to an external provider", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        jsonResponse(
          detail({
            auth_source: "oidc",
            external_provider: "keycloak",
            identity_managed_externally: true,
          }),
        ),
      ),
    );
    renderWithSession(<UserDetailDialog userID="u-ana" onClose={vi.fn()} onChanged={vi.fn()} />, [
      "admin.users.read",
    ]);

    expect(
      await screen.findByText(/O NChat não altera senha nem atributos do IdP/),
    ).toBeInTheDocument();
  });

  it("hides role management from anyone below superuser and says why", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(jsonResponse(detail())));
    renderWithSession(<UserDetailDialog userID="u-ana" onClose={vi.fn()} onChanged={vi.fn()} />, [
      "admin.users.read",
      "admin.users.manage",
    ]);

    await screen.findByRole("heading", { name: "Ana Lima" });
    expect(screen.queryByRole("button", { name: "Conceder" })).not.toBeInTheDocument();
    expect(screen.getByText(/Somente um administrador com/)).toBeInTheDocument();
  });

  // Changing who administers the platform is the highest-impact operation the
  // console offers. A click opens a confirmation; only the confirmation calls
  // the API.
  it("does not grant a role until the confirmation is accepted", async () => {
    const onChanged = vi.fn();
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(detail()));
    vi.stubGlobal("fetch", fetchMock);
    renderWithSession(<UserDetailDialog userID="u-ana" onClose={vi.fn()} onChanged={onChanged} />, [
      "admin.superuser",
    ]);

    await userEvent.click(await screen.findByRole("button", { name: "Conceder" }));

    const dialog = await screen.findByRole("dialog", {
      name: "Conceder este papel administrativo?",
    });
    expect(within(dialog).getByText(/admin.audit.read/)).toBeInTheDocument();
    expect(fetchMock.mock.calls.filter((call) => call[1]?.method === "POST")).toHaveLength(0);

    await userEvent.click(within(dialog).getByRole("button", { name: "Cancelar" }));
    // Cancelling calls nothing and changes nothing.
    expect(fetchMock.mock.calls.filter((call) => call[1]?.method === "POST")).toHaveLength(0);
    expect(onChanged).not.toHaveBeenCalled();
    expect(
      screen.queryByRole("dialog", { name: "Conceder este papel administrativo?" }),
    ).not.toBeInTheDocument();
  });

  it("grants a role once the confirmation is accepted", async () => {
    const onChanged = vi.fn();
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(jsonResponse(detail()))
      .mockResolvedValueOnce(new Response(null, { status: 204 }))
      .mockResolvedValue(jsonResponse(detail()));
    vi.stubGlobal("fetch", fetchMock);
    renderWithSession(<UserDetailDialog userID="u-ana" onClose={vi.fn()} onChanged={onChanged} />, [
      "admin.superuser",
    ]);

    await userEvent.click(await screen.findByRole("button", { name: "Conceder" }));
    await userEvent.click(
      within(screen.getByRole("dialog", { name: "Conceder este papel administrativo?" })).getByRole(
        "button",
        { name: "Conceder" },
      ),
    );

    await waitFor(() => expect(onChanged).toHaveBeenCalled());
    expect(fetchMock.mock.calls.filter((call) => call[1]?.method === "POST")).toHaveLength(1);
  });

  it("does not revoke a role until the confirmation is accepted", async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(detailWithGrant()));
    vi.stubGlobal("fetch", fetchMock);
    renderWithSession(<UserDetailDialog userID="u-ana" onClose={vi.fn()} onChanged={vi.fn()} />, [
      "admin.superuser",
    ]);

    await userEvent.click(await screen.findByRole("button", { name: "Remover" }));
    const dialog = await screen.findByRole("dialog", {
      name: "Remover este papel administrativo?",
    });
    // The consequence is spelled out, including the invariant that may refuse it.
    expect(within(dialog).getByText(/sem administrador/)).toBeInTheDocument();
    expect(fetchMock.mock.calls.filter((call) => call[1]?.method === "DELETE")).toHaveLength(0);

    await userEvent.click(within(dialog).getByRole("button", { name: "Cancelar" }));
    expect(fetchMock.mock.calls.filter((call) => call[1]?.method === "DELETE")).toHaveLength(0);
  });

  it("revokes a held role once the confirmation is accepted", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(jsonResponse(detailWithGrant()))
      .mockResolvedValueOnce(new Response(null, { status: 204 }))
      .mockResolvedValue(jsonResponse(detail()));
    vi.stubGlobal("fetch", fetchMock);
    renderWithSession(<UserDetailDialog userID="u-ana" onClose={vi.fn()} onChanged={vi.fn()} />, [
      "admin.superuser",
    ]);

    expect(await screen.findByText(/Concedido em/)).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Remover" }));
    await userEvent.click(
      within(screen.getByRole("dialog", { name: "Remover este papel administrativo?" })).getByRole(
        "button",
        { name: "Remover" },
      ),
    );

    await waitFor(() =>
      expect(fetchMock.mock.calls.filter((call) => call[1]?.method === "DELETE")).toHaveLength(1),
    );
  });

  // The guard is the pending state, not only the disabled attribute: a second
  // click landing before React re-renders must not produce a second mutation.
  it("does not submit a confirmed role change twice", async () => {
    let settle: (value: Response) => void = () => {};
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(jsonResponse(detail()))
      .mockImplementationOnce(() => new Promise<Response>((resolve) => (settle = resolve)))
      .mockResolvedValue(jsonResponse(detail()));
    vi.stubGlobal("fetch", fetchMock);
    renderWithSession(<UserDetailDialog userID="u-ana" onClose={vi.fn()} onChanged={vi.fn()} />, [
      "admin.superuser",
    ]);

    await userEvent.click(await screen.findByRole("button", { name: "Conceder" }));
    const dialog = screen.getByRole("dialog", { name: "Conceder este papel administrativo?" });
    const confirm = within(dialog).getByRole("button", { name: "Conceder" });
    await userEvent.click(confirm);
    await userEvent.click(confirm).catch(() => {});

    expect(fetchMock.mock.calls.filter((call) => call[1]?.method === "POST")).toHaveLength(1);
    settle(new Response(null, { status: 204 }));
  });

  // The last-administrator invariant and the self-escalation guard both surface
  // here as a refusal the operator can read.
  it("reports a refused role change without claiming it applied", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(jsonResponse(detail()))
      .mockResolvedValue(errorResponse(409, "conflict"));
    vi.stubGlobal("fetch", fetchMock);
    renderWithSession(<UserDetailDialog userID="u-ana" onClose={vi.fn()} onChanged={vi.fn()} />, [
      "admin.superuser",
    ]);

    await userEvent.click(await screen.findByRole("button", { name: "Conceder" }));
    await userEvent.click(
      within(screen.getByRole("dialog", { name: "Conceder este papel administrativo?" })).getByRole(
        "button",
        { name: "Conceder" },
      ),
    );

    expect(await screen.findByRole("alert")).toBeInTheDocument();
  });

  it("does not offer an administrator their own roles", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(jsonResponse(detail({ id: TEST_USER_ID }))));
    renderWithSession(
      <UserDetailDialog userID={TEST_USER_ID} onClose={vi.fn()} onChanged={vi.fn()} />,
      ["admin.superuser"],
    );

    await screen.findByRole("heading", { name: "Ana Lima" });
    expect(screen.queryByRole("button", { name: "Conceder" })).not.toBeInTheDocument();
    expect(screen.getByText(/não altera os próprios papéis/)).toBeInTheDocument();
  });

  // Reading somebody's record does not imply reading the trail: the two are
  // separate capabilities.
  it("offers the audit history only with the audit capability", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(jsonResponse(detail())));
    const { unmount } = renderWithSession(
      <UserDetailDialog userID="u-ana" onClose={vi.fn()} onChanged={vi.fn()} />,
      ["admin.users.read", "admin.users.manage"],
    );
    await screen.findByRole("heading", { name: "Ana Lima" });
    expect(
      screen.queryByRole("button", { name: "Ver histórico de auditoria" }),
    ).not.toBeInTheDocument();
    unmount();

    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(jsonResponse(detail())));
    renderWithSession(<UserDetailDialog userID="u-ana" onClose={vi.fn()} onChanged={vi.fn()} />, [
      "admin.users.read",
      "admin.audit.read",
    ]);
    await screen.findByRole("heading", { name: "Ana Lima" });
    expect(screen.getByRole("button", { name: "Ver histórico de auditoria" })).toBeInTheDocument();
  });

  it("navigates to this person's history and closes the record", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(jsonResponse(detail())));
    const onClose = vi.fn();

    // A location probe rather than window.location: the console routes in
    // memory, and what matters is the destination the dialog asked for.
    function Probe() {
      const location = useLocation();
      return <span data-testid="location">{location.pathname + location.search}</span>;
    }
    render(
      <MemoryRouter initialEntries={["/users"]}>
        <AdminSessionContext.Provider
          value={sessionValue(["admin.users.read", "admin.audit.read"])}
        >
          <UserDetailDialog userID="u-ana" onClose={onClose} onChanged={vi.fn()} />
          <Probe />
        </AdminSessionContext.Provider>
      </MemoryRouter>,
    );

    await userEvent.click(
      await screen.findByRole("button", { name: "Ver histórico de auditoria" }),
    );

    expect(onClose).toHaveBeenCalledTimes(1);
    // The identifier travels in the URL so the history survives a refresh; the
    // operator never typed or read it.
    expect(screen.getByTestId("location")).toHaveTextContent("/audit?user=u-ana");
  });

  it("closes on Escape and on the close button", async () => {
    const onClose = vi.fn();
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(jsonResponse(detail())));
    renderWithSession(<UserDetailDialog userID="u-ana" onClose={onClose} onChanged={vi.fn()} />, [
      "admin.users.read",
    ]);

    await userEvent.keyboard("{Escape}");
    await userEvent.click(screen.getByRole("button", { name: "Fechar" }));
    expect(onClose).toHaveBeenCalledTimes(2);
  });

  it("surfaces a load failure instead of an empty record", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(errorResponse(403)));
    renderWithSession(<UserDetailDialog userID="u-ana" onClose={vi.fn()} onChanged={vi.fn()} />, [
      "admin.users.read",
    ]);

    expect(await screen.findByRole("alert")).toHaveTextContent("não tem permissão");
  });

  it("says plainly when somebody belongs to no workspace", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(jsonResponse(detail({ memberships: [] }))));
    renderWithSession(<UserDetailDialog userID="u-ana" onClose={vi.fn()} onChanged={vi.fn()} />, [
      "admin.users.read",
    ]);

    expect(await screen.findByText("Não participa de nenhum workspace.")).toBeInTheDocument();
  });
});
