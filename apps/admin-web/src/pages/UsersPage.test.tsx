import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import { _resetCSRFToken } from "../api/client";
import {
  errorResponse,
  jsonResponse,
  renderWithSession,
  requestedURLs,
  TEST_USER_ID,
} from "../test/harness";
import UsersPage from "./UsersPage";

const READ = ["admin.users.read"];
const MANAGE = ["admin.users.read", "admin.users.manage"];

function user(overrides: Record<string, unknown> = {}) {
  return {
    id: "u-ana",
    email: "ana@example.test",
    display_name: "Ana",
    full_name: "Ana Lima",
    avatar_url: "",
    status: "active",
    auth_source: "manual",
    external_provider: "",
    identity_managed_externally: false,
    last_login_at: "2026-08-01T10:00:00Z",
    created_at: "2026-01-01T00:00:00Z",
    platform_admin: false,
    admin_roles: [],
    workspace_roles: [
      {
        workspace_id: "w1",
        workspace_name: "NChat",
        role: "member",
        status: "active",
        joined_at: "2026-01-01T00:00:00Z",
      },
    ],
    active_sessions: 2,
    ...overrides,
  };
}

function page(users: unknown[], nextCursor: string | null = null) {
  return {
    data: {
      users,
      pagination: { next_cursor: nextCursor, has_more: nextCursor !== null },
    },
  };
}

function stubList(users: unknown[], nextCursor: string | null = null) {
  const fetchMock = vi.fn().mockResolvedValue(jsonResponse(page(users, nextCursor)));
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  _resetCSRFToken();
});

describe("UsersPage", () => {
  it("shows a loading skeleton before the first page arrives", () => {
    vi.stubGlobal("fetch", vi.fn().mockReturnValue(new Promise(() => {})));
    renderWithSession(<UsersPage />, READ);

    expect(screen.getByRole("status")).toHaveTextContent("Carregando…");
  });

  it("renders one server-side page as a semantic table", async () => {
    stubList([user()]);
    renderWithSession(<UsersPage />, READ);

    expect(await screen.findByRole("table")).toBeInTheDocument();
    expect(screen.getByRole("columnheader", { name: "Pessoa" })).toBeInTheDocument();
    // The label is the person's name, never the technical identifier.
    expect(screen.getByRole("rowheader", { name: /Ana Lima/ })).toBeInTheDocument();
    expect(screen.queryByText("u-ana")).not.toBeInTheDocument();
  });

  it("says plainly when nothing matches, without implying a failure", async () => {
    stubList([]);
    renderWithSession(<UsersPage />, READ);

    expect(
      await screen.findByText("Nenhum usuário corresponde aos filtros aplicados."),
    ).toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("separates a permission failure from a network failure", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(errorResponse(403)));
    const { unmount } = renderWithSession(<UsersPage />, READ);
    expect(await screen.findByRole("alert")).toHaveTextContent("não tem permissão");
    expect(screen.queryByRole("button", { name: "Tentar novamente" })).not.toBeInTheDocument();
    unmount();

    vi.stubGlobal("fetch", vi.fn().mockRejectedValue(new TypeError("offline")));
    renderWithSession(<UsersPage />, READ);
    expect(await screen.findByRole("alert")).toHaveTextContent("Falha de rede");
    expect(screen.getByRole("button", { name: "Tentar novamente" })).toBeInTheDocument();
  });

  it("marks an identity the platform is not the source of truth for", async () => {
    stubList([
      user({
        auth_source: "oidc",
        external_provider: "keycloak",
        identity_managed_externally: true,
      }),
    ]);
    renderWithSession(<UsersPage />, READ);

    expect(await screen.findByText("Gerenciado pelo keycloak")).toBeInTheDocument();
  });

  it("sends the filters to the server instead of filtering in the browser", async () => {
    const fetchMock = stubList([user()]);
    renderWithSession(<UsersPage />, READ);
    await screen.findByRole("table");

    await userEvent.selectOptions(screen.getByLabelText("Status"), "suspended");
    await waitFor(() =>
      expect(requestedURLs(fetchMock).some((url) => url.includes("status=suspended"))).toBe(true),
    );

    await userEvent.selectOptions(screen.getByLabelText("Origem da identidade"), "oidc");
    await waitFor(() =>
      expect(requestedURLs(fetchMock).some((url) => url.includes("auth_source=oidc"))).toBe(true),
    );
  });

  // One settled request rather than one per keystroke.
  it("debounces the search", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const fetchMock = stubList([user()]);
    renderWithSession(<UsersPage />, READ);
    await screen.findByRole("table");
    const before = fetchMock.mock.calls.length;

    const input = screen.getByLabelText("Buscar por nome, e-mail ou login");
    await userEvent.type(input, "ana", { delay: null });
    expect(fetchMock.mock.calls.length).toBe(before);

    await vi.advanceTimersByTimeAsync(400);
    await waitFor(() =>
      expect(requestedURLs(fetchMock).some((url) => url.includes("q=ana"))).toBe(true),
    );
    vi.useRealTimers();
  });

  it("pages forward and back with the server's cursor", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(jsonResponse(page([user()], "cursor-2")))
      .mockResolvedValueOnce(jsonResponse(page([user({ id: "u-bruno", full_name: "Bruno Dias" })])))
      .mockResolvedValue(jsonResponse(page([user()], "cursor-2")));
    vi.stubGlobal("fetch", fetchMock);
    renderWithSession(<UsersPage />, READ);
    await screen.findByRole("rowheader", { name: /Ana Lima/ });

    await userEvent.click(screen.getByRole("button", { name: "Próxima página" }));
    await screen.findByRole("rowheader", { name: /Bruno Dias/ });
    expect(requestedURLs(fetchMock).some((url) => url.includes("cursor=cursor-2"))).toBe(true);

    await userEvent.click(screen.getByRole("button", { name: "Página anterior" }));
    await screen.findByRole("rowheader", { name: /Ana Lima/ });
  });

  // A cursor from a previous filter names a position in a different result set.
  it("restarts paging when a filter changes", async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(page([user()], "cursor-2")));
    vi.stubGlobal("fetch", fetchMock);
    renderWithSession(<UsersPage />, READ);
    await screen.findByRole("table");

    await userEvent.click(screen.getByRole("button", { name: "Próxima página" }));
    await waitFor(() =>
      expect(requestedURLs(fetchMock).some((url) => url.includes("cursor="))).toBe(true),
    );

    await userEvent.selectOptions(screen.getByLabelText("Status"), "active");
    await waitFor(() => {
      const last = requestedURLs(fetchMock).at(-1) ?? "";
      expect(last).toContain("status=active");
      expect(last).not.toContain("cursor=");
    });
  });

  // Hiding a control is courtesy; the API refuses regardless. What matters here
  // is that the console does not draw an action it knows will be refused.
  it("offers management actions only with the capability", async () => {
    stubList([user()]);
    const { unmount } = renderWithSession(<UsersPage />, READ);
    await screen.findByRole("table");
    expect(screen.queryByRole("button", { name: "Desativar" })).not.toBeInTheDocument();
    unmount();

    stubList([user()]);
    renderWithSession(<UsersPage />, MANAGE);
    await screen.findByRole("table");
    expect(screen.getByRole("button", { name: "Desativar" })).toBeInTheDocument();
  });

  // An administrator acting on their own account is refused by the API; the
  // console does not offer it either, so the operator is never one click from
  // locking themselves out.
  it("does not offer actions on the operator's own account", async () => {
    stubList([user({ id: TEST_USER_ID })]);
    renderWithSession(<UsersPage />, MANAGE);
    await screen.findByRole("table");

    expect(screen.queryByRole("button", { name: "Desativar" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Detalhes" })).toBeInTheDocument();
  });

  it("confirms a deactivation, states its impact, and reports the result", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(jsonResponse(page([user()])))
      .mockResolvedValueOnce(
        jsonResponse({
          data: {
            user_id: "u-ana",
            from_status: "active",
            to_status: "suspended",
            revoked_sessions: 2,
          },
        }),
      )
      .mockResolvedValue(jsonResponse(page([user({ status: "suspended", active_sessions: 0 })])));
    vi.stubGlobal("fetch", fetchMock);
    renderWithSession(<UsersPage />, MANAGE);
    await screen.findByRole("table");

    await userEvent.click(screen.getByRole("button", { name: "Desativar" }));
    const dialog = screen.getByRole("dialog");
    expect(within(dialog).getByText(/Todas as sessões ativas são encerradas/)).toBeInTheDocument();

    await userEvent.click(within(dialog).getByRole("button", { name: "Desativar" }));
    expect(await screen.findByTestId("admin-feedback")).toHaveTextContent(
      "2 sessão(ões) encerrada(s)",
    );
  });

  it("lets a confirmation be abandoned without changing anything", async () => {
    const fetchMock = stubList([user()]);
    renderWithSession(<UsersPage />, MANAGE);
    await screen.findByRole("table");
    const before = fetchMock.mock.calls.length;

    await userEvent.click(screen.getByRole("button", { name: "Desativar" }));
    await userEvent.click(screen.getByRole("button", { name: "Cancelar" }));

    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(fetchMock.mock.calls.length).toBe(before);
  });

  it("reports a refused mutation without claiming it succeeded", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(jsonResponse(page([user()])))
      .mockResolvedValue(errorResponse(409, "conflict"));
    vi.stubGlobal("fetch", fetchMock);
    renderWithSession(<UsersPage />, MANAGE);
    await screen.findByRole("table");

    await userEvent.click(screen.getByRole("button", { name: "Desativar" }));
    await userEvent.click(
      within(screen.getByRole("dialog")).getByRole("button", { name: "Desativar" }),
    );

    expect(await screen.findByRole("alert")).toBeInTheDocument();
  });

  // The guard is the state, not only the disabled attribute: a second click
  // arriving before React re-renders finds the mutation already in flight.
  it("does not submit a mutation twice", async () => {
    let resolveMutation: (value: Response) => void = () => {};
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(jsonResponse(page([user()])))
      .mockImplementationOnce(() => new Promise<Response>((resolve) => (resolveMutation = resolve)))
      .mockResolvedValue(jsonResponse(page([user()])));
    vi.stubGlobal("fetch", fetchMock);
    renderWithSession(<UsersPage />, MANAGE);
    await screen.findByRole("table");

    await userEvent.click(screen.getByRole("button", { name: "Desativar" }));
    const confirm = within(screen.getByRole("dialog")).getByRole("button", { name: "Desativar" });
    await userEvent.click(confirm);
    await userEvent.click(confirm).catch(() => {});

    expect(fetchMock.mock.calls.filter((call) => call[1]?.method === "PATCH")).toHaveLength(1);
    resolveMutation(
      jsonResponse({
        data: {
          user_id: "u-ana",
          from_status: "active",
          to_status: "suspended",
          revoked_sessions: 0,
        },
      }),
    );
  });

  it("ends every session of somebody else when asked", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(jsonResponse(page([user()])))
      .mockResolvedValueOnce(jsonResponse({ data: { user_id: "u-ana", revoked_sessions: 2 } }))
      .mockResolvedValue(jsonResponse(page([user({ active_sessions: 0 })])));
    vi.stubGlobal("fetch", fetchMock);
    renderWithSession(<UsersPage />, MANAGE);
    await screen.findByRole("table");

    await userEvent.click(screen.getByRole("button", { name: "Encerrar sessões" }));
    await userEvent.click(
      within(screen.getByRole("dialog")).getByRole("button", { name: "Encerrar sessões" }),
    );

    expect(await screen.findByTestId("admin-feedback")).toHaveTextContent("2 sessão(ões)");
  });

  it("does not offer to end sessions of somebody who has none", async () => {
    stubList([user({ active_sessions: 0 })]);
    renderWithSession(<UsersPage />, MANAGE);
    await screen.findByRole("table");

    expect(screen.getByRole("button", { name: "Encerrar sessões" })).toBeDisabled();
  });

  it("opens the detail record only when asked for it", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(jsonResponse(page([user()])))
      .mockResolvedValue(
        jsonResponse({
          data: {
            ...user(),
            memberships: [],
            channel_count: 0,
            role_grants: [],
            available_roles: [],
          },
        }),
      );
    vi.stubGlobal("fetch", fetchMock);
    renderWithSession(<UsersPage />, READ);
    await screen.findByRole("table");
    expect(requestedURLs(fetchMock).some((url) => /\/users\/u-ana(\?|$)/.test(url))).toBe(false);

    await userEvent.click(screen.getByRole("button", { name: "Detalhes" }));
    await waitFor(() =>
      expect(requestedURLs(fetchMock).some((url) => url.includes("/users/u-ana"))).toBe(true),
    );
  });

  it("reactivates a suspended account with its own wording", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(jsonResponse(page([user({ status: "suspended" })])))
      .mockResolvedValueOnce(
        jsonResponse({
          data: {
            user_id: "u-ana",
            from_status: "suspended",
            to_status: "active",
            revoked_sessions: 0,
          },
        }),
      )
      .mockResolvedValue(jsonResponse(page([user()])));
    vi.stubGlobal("fetch", fetchMock);
    renderWithSession(<UsersPage />, MANAGE);
    await screen.findByRole("table");

    await userEvent.click(screen.getByRole("button", { name: "Ativar" }));
    const dialog = screen.getByRole("dialog");
    expect(within(dialog).getByText(/Nenhuma sessão anterior é restaurada/)).toBeInTheDocument();
    await userEvent.click(within(dialog).getByRole("button", { name: "Ativar" }));

    expect(await screen.findByTestId("admin-feedback")).toHaveTextContent("reativado");
  });
});

describe("UsersPage rendering details", () => {
  it("shows administrative roles, or says there are none", async () => {
    stubList([
      user({ platform_admin: true, admin_roles: ["platform-auditor"] }),
      user({ id: "u-sem", full_name: "Sem Papel", platform_admin: true, admin_roles: [] }),
    ]);
    renderWithSession(<UsersPage />, READ);

    expect(await screen.findByText("platform-auditor")).toBeInTheDocument();
    // A principal with no role still shows as administrative, honestly labelled
    // rather than silently blank.
    expect(screen.getByText("sem papel")).toBeInTheDocument();
  });

  it("renders an em dash where there is no membership and no last access", async () => {
    stubList([user({ workspace_roles: [], last_login_at: null, platform_admin: false })]);
    renderWithSession(<UsersPage />, READ);

    await screen.findByRole("table");
    expect(screen.getAllByText("—").length).toBeGreaterThanOrEqual(2);
  });

  it("renders a status the console does not have a label for, as sent", async () => {
    stubList([user({ status: "locked" }), user({ id: "u-x", full_name: "Xis", status: "novo" })]);
    renderWithSession(<UsersPage />, READ);

    expect(await screen.findByText("Bloqueado")).toBeInTheDocument();
    expect(screen.getByText("novo")).toBeInTheDocument();
  });

  // The domain supports exactly one transition pair. Offering "Ativar" to an
  // invited or locked account announced an operation whose only possible
  // outcome was a refusal.
  it("offers a status action only where the platform supports one", async () => {
    const table: { status: string; button: string | null; reason?: string }[] = [
      { status: "active", button: "Desativar" },
      { status: "suspended", button: "Ativar" },
      { status: "invited", button: null, reason: "convite pendente" },
      { status: "locked", button: null, reason: "bloqueado por segurança" },
      { status: "novo", button: null, reason: "estado não gerenciado aqui" },
    ];
    for (const row of table) {
      const { unmount } = (() => {
        stubList([user({ status: row.status })]);
        return renderWithSession(<UsersPage />, MANAGE);
      })();
      await screen.findByRole("table");

      if (row.button === null) {
        expect(screen.queryByRole("button", { name: "Ativar" })).not.toBeInTheDocument();
        expect(screen.queryByRole("button", { name: "Desativar" })).not.toBeInTheDocument();
        expect(screen.getByText(row.reason ?? "")).toBeInTheDocument();
      } else {
        expect(screen.getByRole("button", { name: row.button })).toBeInTheDocument();
      }
      unmount();
    }
  });

  // Ending sessions is a different operation and stays available: a locked
  // account can still hold live sessions worth revoking.
  it("still offers session revocation for a state with no status action", async () => {
    stubList([user({ status: "locked", active_sessions: 2 })]);
    renderWithSession(<UsersPage />, MANAGE);
    await screen.findByRole("table");

    expect(screen.getByRole("button", { name: "Encerrar sessões" })).toBeEnabled();
  });

  it("falls back to the e-mail when a person has no name", async () => {
    stubList([user({ full_name: "", display_name: "" })]);
    renderWithSession(<UsersPage />, READ);

    expect(await screen.findByRole("rowheader", { name: /ana@example.test/ })).toBeInTheDocument();
  });
});

describe("UsersPage workspace role filter", () => {
  it("sends the selected role to the server rather than filtering locally", async () => {
    const fetchMock = stubList([user()]);
    renderWithSession(<UsersPage />, READ);
    await screen.findByRole("table");

    for (const role of ["owner", "admin", "moderator", "member", "guest"]) {
      await userEvent.selectOptions(screen.getByLabelText("Papel de workspace"), role);
      await waitFor(() =>
        expect(requestedURLs(fetchMock).some((url) => url.includes(`workspace_role=${role}`))).toBe(
          true,
        ),
      );
    }
  });

  it("removes the filter again when 'any role' is chosen", async () => {
    const fetchMock = stubList([user()]);
    renderWithSession(<UsersPage />, READ);
    await screen.findByRole("table");

    const select = screen.getByLabelText("Papel de workspace");
    await userEvent.selectOptions(select, "owner");
    await waitFor(() =>
      expect(requestedURLs(fetchMock).some((url) => url.includes("workspace_role=owner"))).toBe(
        true,
      ),
    );

    await userEvent.selectOptions(select, "");
    await waitFor(() => {
      const last = requestedURLs(fetchMock).at(-1) ?? "";
      expect(last).not.toContain("workspace_role");
    });
  });

  // The two are different questions: a workspace owner holds no platform
  // authority, and asking for both must narrow rather than replace.
  it("combines with the platform administrator filter", async () => {
    const fetchMock = stubList([user()]);
    renderWithSession(<UsersPage />, READ);
    await screen.findByRole("table");

    await userEvent.selectOptions(screen.getByLabelText("Papel de workspace"), "owner");
    await userEvent.selectOptions(screen.getByLabelText("Papel administrativo"), "true");

    await waitFor(() => {
      const last = requestedURLs(fetchMock).at(-1) ?? "";
      expect(last).toContain("workspace_role=owner");
      expect(last).toContain("platform_admin=true");
    });
  });

  it("restarts paging when the role changes", async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(page([user()], "cursor-2")));
    vi.stubGlobal("fetch", fetchMock);
    renderWithSession(<UsersPage />, READ);
    await screen.findByRole("table");

    await userEvent.click(screen.getByRole("button", { name: "Próxima página" }));
    await waitFor(() =>
      expect(requestedURLs(fetchMock).some((url) => url.includes("cursor="))).toBe(true),
    );

    await userEvent.selectOptions(screen.getByLabelText("Papel de workspace"), "guest");
    await waitFor(() => {
      const last = requestedURLs(fetchMock).at(-1) ?? "";
      expect(last).toContain("workspace_role=guest");
      expect(last).not.toContain("cursor=");
    });
  });
});
