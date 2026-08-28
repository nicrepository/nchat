import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { ApiRequestError } from "../lib/api";
import { clearTokens, setTokens } from "../lib/authSession";
import RequireAuth from "../auth/RequireAuth";
import AdminUsersPage from "./AdminUsersPage";
import type { AdminUser } from "./adminUsersApi";

// ── Mock adminUsersApi ─────────────────────────────────────────────────────

const { mockListAdminUsers, mockCreateAdminInvite } = vi.hoisted(() => ({
  mockListAdminUsers: vi.fn(),
  mockCreateAdminInvite: vi.fn<() => Promise<void>>(),
}));

/** Builds one page of the paginated result the API client returns. */
function pageOf(users: AdminUser[], nextCursor: string | null = null) {
  return { users, nextCursor, hasMore: nextCursor !== null };
}

vi.mock("./adminUsersApi", async () => {
  // classifyAdminError is real: the page's error branching is part of what
  // these tests exercise, so stubbing it would test nothing.
  const actual = await vi.importActual<typeof import("./adminUsersApi")>("./adminUsersApi");
  return {
    ADMIN_USERS_PAGE_SIZE: actual.ADMIN_USERS_PAGE_SIZE,
    ERR_INVALID_RESPONSE: actual.ERR_INVALID_RESPONSE,
    classifyAdminError: actual.classifyAdminError,
    listAdminUsers: (...args: unknown[]) => mockListAdminUsers(...args),
    createAdminInvite: (...args: unknown[]) => mockCreateAdminInvite(...(args as [])),
  };
});

// ── Helpers ────────────────────────────────────────────────────────────────

function renderAdminUsersRoute(authenticated = true) {
  if (authenticated) {
    setTokens("at");
  } else {
    clearTokens();
  }

  return render(
    <MemoryRouter initialEntries={["/admin/users"]}>
      <Routes>
        <Route path="/login" element={<div>Login page</div>} />
        <Route
          path="/admin/users"
          element={
            <RequireAuth>
              <AdminUsersPage />
            </RequireAuth>
          }
        />
      </Routes>
    </MemoryRouter>,
  );
}

const SAMPLE_USERS: AdminUser[] = [
  {
    id: "u1",
    email: "alice@example.com",
    displayName: "Alice Andrade",
    status: "active",
    authSource: "local",
    createdAt: "2024-01-15T10:00:00Z",
  },
  {
    id: "u2",
    email: "bob@example.com",
    displayName: "Bob Bastos",
    status: "suspended",
    authSource: "admin",
    createdAt: "2024-03-22T08:30:00Z",
  },
];

// ── Setup / teardown ───────────────────────────────────────────────────────

beforeEach(() => {
  clearTokens();
  vi.clearAllMocks();
});

afterEach(() => {
  clearTokens();
});

// ── Tests ──────────────────────────────────────────────────────────────────

describe("AdminUsersPage — route protection", () => {
  it("redirects unauthenticated user to /login", async () => {
    renderAdminUsersRoute(false);

    expect(await screen.findByText("Login page")).toBeInTheDocument();
    expect(screen.queryByRole("table")).not.toBeInTheDocument();
  });

  it("does not render protected content for unauthenticated user", () => {
    mockListAdminUsers.mockResolvedValue(pageOf([]));
    renderAdminUsersRoute(false);

    expect(screen.queryByRole("heading", { name: /usuários/i })).not.toBeInTheDocument();
  });
});

describe("AdminUsersPage — admin shell structure", () => {
  it("renders the admin shell wrapper", async () => {
    mockListAdminUsers.mockResolvedValue(pageOf([]));
    renderAdminUsersRoute();

    await waitFor(() => {
      expect(screen.getByTestId("admin-shell")).toBeInTheDocument();
    });
  });

  it("renders the dark sidebar", async () => {
    mockListAdminUsers.mockResolvedValue(pageOf([]));
    renderAdminUsersRoute();

    await waitFor(() => {
      expect(screen.getByTestId("admin-sidebar")).toBeInTheDocument();
    });
  });

  it("renders the admin top navigation", async () => {
    mockListAdminUsers.mockResolvedValue(pageOf([]));
    renderAdminUsersRoute();

    await waitFor(() => {
      expect(screen.getByTestId("admin-topnav")).toBeInTheDocument();
    });
  });

  it("admin top nav contains expected tabs", async () => {
    mockListAdminUsers.mockResolvedValue(pageOf([]));
    renderAdminUsersRoute();

    await waitFor(() => {
      expect(screen.getByTestId("admin-topnav")).toBeInTheDocument();
    });

    const topnav = screen.getByTestId("admin-topnav");
    expect(topnav).toHaveTextContent("Visão geral");
    expect(topnav).toHaveTextContent("Usuários");
    expect(topnav).toHaveTextContent("Canais");
    expect(topnav).toHaveTextContent("Auditoria");
  });

  it("marks Usuários tab as active", async () => {
    mockListAdminUsers.mockResolvedValue(pageOf([]));
    renderAdminUsersRoute();

    await waitFor(() => {
      expect(screen.getByTestId("admin-topnav")).toBeInTheDocument();
    });

    const activeTab = screen.getByTestId("admin-topnav").querySelector('[aria-current="page"]');
    expect(activeTab).toHaveTextContent("Usuários");
  });

  it("sidebar shows NIC Chat branding", async () => {
    mockListAdminUsers.mockResolvedValue(pageOf([]));
    renderAdminUsersRoute();

    await waitFor(() => {
      expect(screen.getByTestId("admin-sidebar")).toBeInTheDocument();
    });

    const sidebar = screen.getByTestId("admin-sidebar");
    expect(sidebar).toHaveTextContent("NIC Chat");
    expect(sidebar).toHaveTextContent("Workspace NIC-Labs");
  });
});

describe("AdminUsersPage — invite button", () => {
  it("renders an enabled invite button", async () => {
    mockListAdminUsers.mockResolvedValue(pageOf([]));
    renderAdminUsersRoute();

    const inviteBtn = await screen.findByRole("button", { name: /convidar usuário/i });
    expect(inviteBtn).toBeEnabled();
    expect(inviteBtn).toHaveAttribute("type", "button");
  });

  it("opens the invite dialog on click", async () => {
    const user = userEvent.setup();
    mockListAdminUsers.mockResolvedValue(pageOf([]));
    renderAdminUsersRoute();

    await user.click(await screen.findByRole("button", { name: /convidar usuário/i }));

    const dialog = screen.getByRole("dialog");
    expect(dialog).toHaveAccessibleName(/convidar usuário/i);
    expect(screen.getByLabelText(/e-mail/i)).toBeInTheDocument();
    expect(screen.getByLabelText(/nome de exibição/i)).toBeInTheDocument();
  });

  it("closing the dialog returns focus to the invite button", async () => {
    const user = userEvent.setup();
    mockListAdminUsers.mockResolvedValue(pageOf([]));
    renderAdminUsersRoute();

    const inviteBtn = await screen.findByRole("button", { name: /convidar usuário/i });
    await user.click(inviteBtn);
    await user.click(screen.getByRole("button", { name: /cancelar/i }));

    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(inviteBtn).toHaveFocus();
  });
});

describe("AdminUsersPage — invite submission", () => {
  async function openInviteDialog(user: ReturnType<typeof userEvent.setup>) {
    await user.click(await screen.findByRole("button", { name: /convidar usuário/i }));
  }

  it("submits the invite and refreshes the list from the canonical source", async () => {
    const user = userEvent.setup();
    mockListAdminUsers.mockResolvedValue(pageOf([]));
    mockCreateAdminInvite.mockResolvedValue(undefined);
    renderAdminUsersRoute();
    await openInviteDialog(user);

    await user.type(screen.getByLabelText(/e-mail/i), "new@example.com");
    await user.type(screen.getByLabelText(/nome de exibição/i), "New User");
    await user.click(screen.getByRole("button", { name: /enviar convite/i }));

    await waitFor(() => {
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    });
    expect(mockCreateAdminInvite).toHaveBeenCalledWith({
      email: "new@example.com",
      displayName: "New User",
    });
    // Once on mount, once after the invite: the table is only ever filled from
    // the server, never from a row invented for an unaccepted invitation.
    expect(mockListAdminUsers).toHaveBeenCalledTimes(2);
  });

  it("announces success without fabricating a user row", async () => {
    const user = userEvent.setup();
    mockListAdminUsers.mockResolvedValue(pageOf([]));
    mockCreateAdminInvite.mockResolvedValue(undefined);
    renderAdminUsersRoute();
    await openInviteDialog(user);

    await user.type(screen.getByLabelText(/e-mail/i), "new@example.com");
    await user.type(screen.getByLabelText(/nome de exibição/i), "New User");
    await user.click(screen.getByRole("button", { name: /enviar convite/i }));

    expect(await screen.findByText(/convite enviado/i)).toBeInTheDocument();
    expect(screen.queryByText("new@example.com")).not.toBeInTheDocument();
  });

  it("rejects an invalid e-mail without calling the API", async () => {
    const user = userEvent.setup();
    mockListAdminUsers.mockResolvedValue(pageOf([]));
    renderAdminUsersRoute();
    await openInviteDialog(user);

    await user.type(screen.getByLabelText(/e-mail/i), "not-an-email");
    await user.type(screen.getByLabelText(/nome de exibição/i), "New User");
    await user.click(screen.getByRole("button", { name: /enviar convite/i }));

    expect(await screen.findByRole("alert")).toHaveTextContent(/e-mail válido/i);
    expect(mockCreateAdminInvite).not.toHaveBeenCalled();
  });

  it("requires both fields", async () => {
    const user = userEvent.setup();
    mockListAdminUsers.mockResolvedValue(pageOf([]));
    renderAdminUsersRoute();
    await openInviteDialog(user);

    await user.click(screen.getByRole("button", { name: /enviar convite/i }));

    expect(await screen.findByRole("alert")).toHaveTextContent(/informe e-mail e nome/i);
    expect(mockCreateAdminInvite).not.toHaveBeenCalled();
  });

  it("disables the submit button while sending, preventing a duplicate invite", async () => {
    const user = userEvent.setup();
    mockListAdminUsers.mockResolvedValue(pageOf([]));
    let resolveInvite: () => void;
    mockCreateAdminInvite.mockReturnValue(
      new Promise<void>((r) => {
        resolveInvite = r;
      }),
    );
    renderAdminUsersRoute();
    await openInviteDialog(user);

    await user.type(screen.getByLabelText(/e-mail/i), "new@example.com");
    await user.type(screen.getByLabelText(/nome de exibição/i), "New User");
    const submit = screen.getByRole("button", { name: /enviar convite/i });
    await user.click(submit);

    expect(screen.getByRole("button", { name: /enviando/i })).toBeDisabled();
    await user.click(screen.getByRole("button", { name: /enviando/i }));
    expect(mockCreateAdminInvite).toHaveBeenCalledTimes(1);

    resolveInvite!();
  });

  it("shows a recoverable error and keeps the dialog open when the invite fails", async () => {
    const user = userEvent.setup();
    mockListAdminUsers.mockResolvedValue(pageOf([]));
    mockCreateAdminInvite.mockRejectedValue(
      new ApiRequestError(409, "conflict", "invite conflict"),
    );
    renderAdminUsersRoute();
    await openInviteDialog(user);

    await user.type(screen.getByLabelText(/e-mail/i), "dup@example.com");
    await user.type(screen.getByLabelText(/nome de exibição/i), "Dup");
    await user.click(screen.getByRole("button", { name: /enviar convite/i }));

    expect(await screen.findByRole("alert")).toHaveTextContent(
      /não foi possível enviar o convite/i,
    );
    expect(screen.getByRole("dialog")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /enviar convite/i })).toBeEnabled();
  });

  it("reports a permissions failure distinctly", async () => {
    const user = userEvent.setup();
    mockListAdminUsers.mockResolvedValue(pageOf([]));
    mockCreateAdminInvite.mockRejectedValue(new ApiRequestError(403, "forbidden", "forbidden"));
    renderAdminUsersRoute();
    await openInviteDialog(user);

    await user.type(screen.getByLabelText(/e-mail/i), "new@example.com");
    await user.type(screen.getByLabelText(/nome de exibição/i), "New");
    await user.click(screen.getByRole("button", { name: /enviar convite/i }));

    expect(await screen.findByRole("alert")).toHaveTextContent(/não tem permissão/i);
  });
});

describe("AdminUsersPage — authenticated rendering", () => {
  it("renders the page heading when authenticated", async () => {
    mockListAdminUsers.mockResolvedValue(pageOf([]));
    renderAdminUsersRoute();

    await waitFor(() => {
      expect(screen.queryByRole("table", { name: /lista de usuários/i })).toBeInTheDocument();
    });

    expect(screen.getByRole("heading", { name: /usuários/i })).toBeInTheDocument();
  });

  it("renders filter chips", async () => {
    mockListAdminUsers.mockResolvedValue(pageOf([]));
    renderAdminUsersRoute();

    await waitFor(() => {
      expect(screen.getByRole("group", { name: /filtrar usuários/i })).toBeInTheDocument();
    });

    const filterGroup = screen.getByRole("group", { name: /filtrar usuários/i });
    expect(filterGroup).toHaveTextContent("Todos");
    expect(filterGroup).toHaveTextContent("Ativos");
    expect(filterGroup).toHaveTextContent("Suspensos");
    expect(filterGroup).toHaveTextContent("Admins");
    expect(filterGroup).toHaveTextContent("Convites pendentes");
  });

  it("renders search input", async () => {
    mockListAdminUsers.mockResolvedValue(pageOf([]));
    renderAdminUsersRoute();

    await waitFor(() => {
      expect(screen.getByRole("searchbox", { name: /buscar usuários/i })).toBeInTheDocument();
    });
  });
});

describe("AdminUsersPage — loading state", () => {
  it("shows loading skeleton while request is pending", () => {
    let resolve: (v: ReturnType<typeof pageOf>) => void;
    mockListAdminUsers.mockReturnValue(new Promise((r) => (resolve = r)));

    renderAdminUsersRoute();

    const table = screen.getByRole("table", { name: /lista de usuários/i });
    expect(table).toHaveAttribute("aria-busy", "true");

    resolve!(pageOf([]));
  });
});

describe("AdminUsersPage — empty state", () => {
  it("shows empty state when API returns no users", async () => {
    mockListAdminUsers.mockResolvedValue(pageOf([]));
    renderAdminUsersRoute();

    await waitFor(() => {
      expect(screen.getByText(/nenhum usuário disponível/i)).toBeInTheDocument();
    });

    expect(screen.getByText(/as contas registradas aparecerão aqui/i)).toBeInTheDocument();
  });

  it("empty state is rendered inside the admin shell layout", async () => {
    mockListAdminUsers.mockResolvedValue(pageOf([]));
    renderAdminUsersRoute();

    await waitFor(() => {
      expect(screen.getByText(/nenhum usuário disponível/i)).toBeInTheDocument();
    });

    // Admin shell must still be present when showing empty state
    expect(screen.getByTestId("admin-shell")).toBeInTheDocument();
    expect(screen.getByTestId("admin-topnav")).toBeInTheDocument();
  });
});

describe("AdminUsersPage — user rows", () => {
  it("renders a row for each user returned by the API", async () => {
    mockListAdminUsers.mockResolvedValue(pageOf(SAMPLE_USERS));
    renderAdminUsersRoute();

    await waitFor(() => {
      expect(screen.getByText("Alice Andrade")).toBeInTheDocument();
    });

    expect(screen.getByText("Bob Bastos")).toBeInTheDocument();
    expect(screen.getByText("alice@example.com")).toBeInTheDocument();
    expect(screen.getByText("bob@example.com")).toBeInTheDocument();
  });

  it("renders status badges for each user", async () => {
    mockListAdminUsers.mockResolvedValue(pageOf(SAMPLE_USERS));
    renderAdminUsersRoute();

    await waitFor(() => {
      expect(screen.getByText("Ativo")).toBeInTheDocument();
    });

    expect(screen.getByText("Suspenso")).toBeInTheDocument();
  });

  it("renders initials avatar derived from displayName", async () => {
    mockListAdminUsers.mockResolvedValue(pageOf([SAMPLE_USERS[0]]));
    renderAdminUsersRoute();

    await waitFor(() => {
      expect(screen.getByText("AA")).toBeInTheDocument();
    });
  });

  it("renders single-letter initial for single-word displayName", async () => {
    mockListAdminUsers.mockResolvedValue(
      pageOf([{ ...SAMPLE_USERS[0], displayName: "Alice", id: "u-single" }]),
    );
    renderAdminUsersRoute();

    await waitFor(() => {
      expect(screen.getByText("A")).toBeInTheDocument();
    });
  });

  it("renders origin badge showing auth source value", async () => {
    mockListAdminUsers.mockResolvedValue(pageOf([SAMPLE_USERS[0]]));
    renderAdminUsersRoute();

    await waitFor(() => {
      expect(screen.getByText("local")).toBeInTheDocument();
    });
  });

  it("renders authSource as origin/provider badge, not as role or function", async () => {
    mockListAdminUsers.mockResolvedValue(pageOf(SAMPLE_USERS));
    renderAdminUsersRoute();

    await waitFor(() => {
      expect(screen.getByText("local")).toBeInTheDocument();
    });

    // authSource values should appear as-is (origin, not inferred roles)
    expect(screen.getByText("admin")).toBeInTheDocument();

    // No "Função" column header — the table does not have a role column
    const headers = screen.getAllByRole("columnheader");
    const headerTexts = headers.map((h) => h.textContent?.toLowerCase() ?? "");
    expect(headerTexts.some((t) => t.includes("função"))).toBe(false);
    expect(headerTexts.some((t) => t.includes("cargo"))).toBe(false);
  });

  it("renders fullName as subtitle when it differs from displayName", async () => {
    mockListAdminUsers.mockResolvedValue(
      pageOf([
        {
          ...SAMPLE_USERS[0],
          displayName: "Alice",
          fullName: "Alice Andrade",
          id: "u-fullname",
        },
      ]),
    );
    renderAdminUsersRoute();

    await waitFor(() => {
      expect(screen.getByText("Alice Andrade")).toBeInTheDocument();
    });
  });

  it("renders table column headers: Nome, E-mail, Status, Origem, Criado em", async () => {
    mockListAdminUsers.mockResolvedValue(pageOf([]));
    renderAdminUsersRoute();

    await waitFor(() => {
      expect(screen.queryByRole("table", { name: /lista de usuários/i })).toBeInTheDocument();
    });

    const headers = screen.getAllByRole("columnheader");
    const texts = headers.map((h) => h.textContent?.toLowerCase() ?? "");
    expect(texts.some((t) => t.includes("nome"))).toBe(true);
    expect(texts.some((t) => t.includes("e-mail"))).toBe(true);
    expect(texts.some((t) => t.includes("status"))).toBe(true);
    expect(texts.some((t) => t.includes("origem"))).toBe(true);
    expect(texts.some((t) => t.includes("criado em"))).toBe(true);
  });
});

describe("AdminUsersPage — error state", () => {
  it("shows generic error state when API call fails", async () => {
    mockListAdminUsers.mockRejectedValue(new Error("network failure"));
    renderAdminUsersRoute();

    await waitFor(() => {
      expect(screen.getByText(/não foi possível carregar os usuários/i)).toBeInTheDocument();
    });

    expect(screen.getByText(/verifique sua conexão/i)).toBeInTheDocument();
  });

  it("error state is rendered inside the admin shell layout", async () => {
    mockListAdminUsers.mockRejectedValue(new Error("fail"));
    renderAdminUsersRoute();

    await waitFor(() => {
      expect(screen.getByText(/não foi possível carregar os usuários/i)).toBeInTheDocument();
    });

    expect(screen.getByTestId("admin-shell")).toBeInTheDocument();
  });

  // The regression this issue reports: a failing request rendered as though the
  // workspace simply had no users.
  it.each([
    [404, /não foi possível carregar os usuários/i],
    [500, /não foi possível carregar os usuários/i],
    [0, /não foi possível carregar os usuários/i],
  ])("renders an error, not the empty state, for HTTP %i", async (status, expected) => {
    mockListAdminUsers.mockRejectedValue(new ApiRequestError(status, "code", "message"));
    renderAdminUsersRoute();

    expect(await screen.findByText(expected)).toBeInTheDocument();
    expect(screen.queryByText(/nenhum usuário disponível/i)).not.toBeInTheDocument();
  });

  it("renders a session-expired state for 401", async () => {
    mockListAdminUsers.mockRejectedValue(new ApiRequestError(401, "unauthorized", "unauthorized"));
    renderAdminUsersRoute();

    expect(await screen.findByText(/sua sessão expirou/i)).toBeInTheDocument();
    expect(screen.queryByText(/nenhum usuário disponível/i)).not.toBeInTheDocument();
  });

  it("renders a permissions state for 403", async () => {
    mockListAdminUsers.mockRejectedValue(new ApiRequestError(403, "forbidden", "forbidden"));
    renderAdminUsersRoute();

    expect(
      await screen.findByText(/você não tem permissão para ver os usuários/i),
    ).toBeInTheDocument();
    expect(screen.queryByText(/nenhum usuário disponível/i)).not.toBeInTheDocument();
  });

  // A 200 whose body is malformed reaches the page as a rejected promise. It
  // must land in the error state: showing "no users" would report a broken
  // contract as an accurate, empty workspace.
  it("renders an error, not the empty state, for a 200 with an invalid envelope", async () => {
    mockListAdminUsers.mockRejectedValue(
      new ApiRequestError(200, "invalid_response", "Invalid API response: missing data array"),
    );
    renderAdminUsersRoute();

    expect(await screen.findByText(/não foi possível carregar os usuários/i)).toBeInTheDocument();
    expect(screen.queryByText(/nenhum usuário disponível/i)).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: /tentar novamente/i })).toBeInTheDocument();
  });

  it("error state is announced to assistive technology", async () => {
    mockListAdminUsers.mockRejectedValue(new Error("fail"));
    renderAdminUsersRoute();

    expect(await screen.findByRole("alert")).toHaveTextContent(
      /não foi possível carregar os usuários/i,
    );
  });

  // Retry is only offered where retrying can help. A 403 is not fixed by
  // asking again.
  it.each([
    [401, /sua sessão expirou/i],
    [403, /você não tem permissão para ver os usuários/i],
  ])("offers no retry for HTTP %i", async (status, expected) => {
    mockListAdminUsers.mockRejectedValue(new ApiRequestError(status, "code", "message"));
    renderAdminUsersRoute();

    await screen.findByText(expected);
    expect(screen.queryByRole("button", { name: /tentar novamente/i })).not.toBeInTheDocument();
  });
});

describe("AdminUsersPage — retry", () => {
  it("retry refetches and renders the recovered list", async () => {
    const user = userEvent.setup();
    mockListAdminUsers
      .mockRejectedValueOnce(new ApiRequestError(500, "internal_error", "boom"))
      .mockResolvedValueOnce(pageOf(SAMPLE_USERS));
    renderAdminUsersRoute();

    await user.click(await screen.findByRole("button", { name: /tentar novamente/i }));

    expect(await screen.findByText("Alice Andrade")).toBeInTheDocument();
    expect(mockListAdminUsers).toHaveBeenCalledTimes(2);
  });

  it("retry that fails again keeps the error state", async () => {
    const user = userEvent.setup();
    mockListAdminUsers.mockRejectedValue(new ApiRequestError(500, "internal_error", "boom"));
    renderAdminUsersRoute();

    await user.click(await screen.findByRole("button", { name: /tentar novamente/i }));

    expect(await screen.findByText(/não foi possível carregar os usuários/i)).toBeInTheDocument();
    expect(screen.queryByText(/nenhum usuário disponível/i)).not.toBeInTheDocument();
  });
});

describe("AdminUsersPage — filter chips", () => {
  it("clicking Ativos chip filters to active users only", async () => {
    const user = userEvent.setup();
    mockListAdminUsers.mockResolvedValue(pageOf(SAMPLE_USERS));
    renderAdminUsersRoute();

    await waitFor(() => {
      expect(screen.getByText("Alice Andrade")).toBeInTheDocument();
    });

    await user.click(screen.getByRole("button", { name: "Ativos" }));

    expect(screen.getByText("Alice Andrade")).toBeInTheDocument();
    expect(screen.queryByText("Bob Bastos")).not.toBeInTheDocument();
  });

  it("clicking Suspensos chip filters to suspended users only", async () => {
    const user = userEvent.setup();
    mockListAdminUsers.mockResolvedValue(pageOf(SAMPLE_USERS));
    renderAdminUsersRoute();

    await waitFor(() => {
      expect(screen.getByText("Bob Bastos")).toBeInTheDocument();
    });

    await user.click(screen.getByRole("button", { name: "Suspensos" }));

    expect(screen.queryByText("Alice Andrade")).not.toBeInTheDocument();
    expect(screen.getByText("Bob Bastos")).toBeInTheDocument();
  });

  it("clicking Admins chip shows empty state (no role data)", async () => {
    const user = userEvent.setup();
    mockListAdminUsers.mockResolvedValue(pageOf(SAMPLE_USERS));
    renderAdminUsersRoute();

    await waitFor(() => {
      expect(screen.getByText("Alice Andrade")).toBeInTheDocument();
    });

    await user.click(screen.getByRole("button", { name: "Admins" }));

    expect(screen.getByText(/nenhum usuário disponível/i)).toBeInTheDocument();
  });

  it("clicking Convites pendentes chip shows empty state", async () => {
    const user = userEvent.setup();
    mockListAdminUsers.mockResolvedValue(pageOf(SAMPLE_USERS));
    renderAdminUsersRoute();

    await waitFor(() => {
      expect(screen.getByText("Alice Andrade")).toBeInTheDocument();
    });

    await user.click(screen.getByRole("button", { name: "Convites pendentes" }));

    expect(screen.getByText(/nenhum usuário disponível/i)).toBeInTheDocument();
  });
});

describe("AdminUsersPage — search", () => {
  it("search filters users by name", async () => {
    const user = userEvent.setup();
    mockListAdminUsers.mockResolvedValue(pageOf(SAMPLE_USERS));
    renderAdminUsersRoute();

    await waitFor(() => {
      expect(screen.getByText("Alice Andrade")).toBeInTheDocument();
    });

    const searchInput = screen.getByRole("searchbox", { name: /buscar usuários/i });
    await user.type(searchInput, "bob");

    expect(screen.queryByText("Alice Andrade")).not.toBeInTheDocument();
    expect(screen.getByText("Bob Bastos")).toBeInTheDocument();
  });

  it("search filters users by email", async () => {
    const user = userEvent.setup();
    mockListAdminUsers.mockResolvedValue(pageOf(SAMPLE_USERS));
    renderAdminUsersRoute();

    await waitFor(() => {
      expect(screen.getByText("Alice Andrade")).toBeInTheDocument();
    });

    const searchInput = screen.getByRole("searchbox", { name: /buscar usuários/i });
    await user.type(searchInput, "alice@");

    expect(screen.getByText("Alice Andrade")).toBeInTheDocument();
    expect(screen.queryByText("Bob Bastos")).not.toBeInTheDocument();
  });
});

describe("AdminUsersPage — status action buttons", () => {
  it("shows disabled 'Suspender' button for active users", async () => {
    mockListAdminUsers.mockResolvedValue(
      pageOf([
        {
          id: "u1",
          email: "alice@example.com",
          displayName: "Alice",
          status: "active",
          authSource: "manual",
          createdAt: "2024-01-01T00:00:00Z",
        },
      ]),
    );

    renderAdminUsersRoute();
    const btn = await screen.findByRole("button", { name: "Suspender" });

    expect(btn).toBeDisabled();
    expect(btn).toHaveAttribute("aria-disabled", "true");
    expect(btn).toHaveAttribute("title", "Requer permissão de administrador");
  });

  it("shows disabled 'Ativar' button for suspended users", async () => {
    mockListAdminUsers.mockResolvedValue(
      pageOf([
        {
          id: "u2",
          email: "bob@example.com",
          displayName: "Bob",
          status: "suspended",
          authSource: "manual",
          createdAt: "2024-01-01T00:00:00Z",
        },
      ]),
    );

    renderAdminUsersRoute();
    const btn = await screen.findByRole("button", { name: "Ativar" });

    expect(btn).toBeDisabled();
    expect(btn).toHaveAttribute("aria-disabled", "true");
    expect(btn).toHaveAttribute("title", "Requer permissão de administrador");
  });

  it("shows no action button for non-active/suspended status", async () => {
    mockListAdminUsers.mockResolvedValue(
      pageOf([
        {
          id: "u3",
          email: "carol@example.com",
          displayName: "Carol",
          status: "invited",
          authSource: "manual",
          createdAt: "2024-01-01T00:00:00Z",
        },
      ]),
    );

    renderAdminUsersRoute();
    await screen.findByText("Carol");

    expect(screen.queryByRole("button", { name: "Suspender" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Ativar" })).toBeNull();
  });

  it("renders 'Ações' column header", async () => {
    mockListAdminUsers.mockResolvedValue(pageOf([]));

    renderAdminUsersRoute();
    await screen.findByText("Nenhum usuário disponível");

    expect(screen.getByRole("columnheader", { name: "Ações" })).toBeInTheDocument();
  });
});

describe("AdminUsersPage — invite rate limiting", () => {
  it("reports a 429 distinctly and does not resubmit", async () => {
    const user = userEvent.setup();
    mockListAdminUsers.mockResolvedValue(pageOf([]));
    mockCreateAdminInvite.mockRejectedValue(
      new ApiRequestError(429, "rate_limited", "rate limit exceeded"),
    );
    renderAdminUsersRoute();

    await user.click(await screen.findByRole("button", { name: /convidar usuário/i }));
    await user.type(screen.getByLabelText(/e-mail/i), "new@example.com");
    await user.type(screen.getByLabelText(/nome de exibição/i), "New");
    await user.click(screen.getByRole("button", { name: /enviar convite/i }));

    expect(await screen.findByRole("alert")).toHaveTextContent(/muitos convites/i);
    // Exactly one POST: the dialog never retries on its own.
    expect(mockCreateAdminInvite).toHaveBeenCalledTimes(1);
    // The list is not refreshed either — nothing was created.
    expect(mockListAdminUsers).toHaveBeenCalledTimes(1);
    expect(screen.getByRole("dialog")).toBeInTheDocument();
  });

  it("renders a rate-limit state without a retry button when the listing is throttled", async () => {
    mockListAdminUsers.mockRejectedValue(new ApiRequestError(429, "rate_limited", "slow down"));
    renderAdminUsersRoute();

    expect(await screen.findByText(/muitas solicitações/i)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /tentar novamente/i })).not.toBeInTheDocument();
    expect(screen.queryByText(/nenhum usuário disponível/i)).not.toBeInTheDocument();
  });
});

describe("AdminUsersPage — pagination", () => {
  const MORE: AdminUser[] = [
    {
      id: "u3",
      email: "carol@example.com",
      displayName: "Carol Costa",
      status: "active",
      authSource: "manual",
      createdAt: "2024-05-01T00:00:00Z",
    },
  ];

  it("hides the load-more control on the last page", async () => {
    mockListAdminUsers.mockResolvedValue(pageOf(SAMPLE_USERS));
    renderAdminUsersRoute();

    await screen.findByText("Alice Andrade");
    expect(screen.queryByRole("button", { name: /carregar mais/i })).not.toBeInTheDocument();
  });

  it("offers the load-more control when more pages exist", async () => {
    mockListAdminUsers.mockResolvedValue(pageOf(SAMPLE_USERS, "cursor-1"));
    renderAdminUsersRoute();

    expect(await screen.findByRole("button", { name: /carregar mais usuários/i })).toBeEnabled();
  });

  it("appends the next page and passes the cursor through", async () => {
    const user = userEvent.setup();
    mockListAdminUsers
      .mockResolvedValueOnce(pageOf(SAMPLE_USERS, "cursor-1"))
      .mockResolvedValueOnce(pageOf(MORE));
    renderAdminUsersRoute();

    await user.click(await screen.findByRole("button", { name: /carregar mais usuários/i }));

    expect(await screen.findByText("Carol Costa")).toBeInTheDocument();
    // The first page is still on screen — this appends, it does not replace.
    expect(screen.getByText("Alice Andrade")).toBeInTheDocument();
    expect(mockListAdminUsers.mock.calls[1][0]).toMatchObject({ cursor: "cursor-1" });
    // Last page reached: the control goes away.
    expect(screen.queryByRole("button", { name: /carregar mais/i })).not.toBeInTheDocument();
  });

  // A membership changing between two fetches can shift a row across the page
  // boundary; the same person must not appear twice.
  it("drops rows already loaded", async () => {
    const user = userEvent.setup();
    mockListAdminUsers
      .mockResolvedValueOnce(pageOf(SAMPLE_USERS, "cursor-1"))
      .mockResolvedValueOnce(pageOf([SAMPLE_USERS[1], ...MORE]));
    renderAdminUsersRoute();

    await user.click(await screen.findByRole("button", { name: /carregar mais usuários/i }));

    await screen.findByText("Carol Costa");
    expect(screen.getAllByText("Bob Bastos")).toHaveLength(1);
  });

  it("shows a busy label and blocks a second request while loading", async () => {
    const user = userEvent.setup();
    let resolveSecond: (v: ReturnType<typeof pageOf>) => void;
    mockListAdminUsers
      .mockResolvedValueOnce(pageOf(SAMPLE_USERS, "cursor-1"))
      .mockReturnValueOnce(new Promise((r) => (resolveSecond = r)));
    renderAdminUsersRoute();

    await user.click(await screen.findByRole("button", { name: /carregar mais usuários/i }));

    const busy = screen.getByRole("button", { name: /carregando mais usuários/i });
    expect(busy).toBeDisabled();
    await user.click(busy);
    // Two calls total: the initial page and one load-more.
    expect(mockListAdminUsers).toHaveBeenCalledTimes(2);

    resolveSecond!(pageOf(MORE));
  });

  // A later page failing must not discard what is already on screen.
  it("keeps loaded rows when the next page fails", async () => {
    const user = userEvent.setup();
    mockListAdminUsers
      .mockResolvedValueOnce(pageOf(SAMPLE_USERS, "cursor-1"))
      .mockRejectedValueOnce(new ApiRequestError(500, "internal_error", "boom"));
    renderAdminUsersRoute();

    await user.click(await screen.findByRole("button", { name: /carregar mais usuários/i }));

    expect(await screen.findByRole("alert")).toHaveTextContent(/não foi possível carregar/i);
    expect(screen.getByText("Alice Andrade")).toBeInTheDocument();
    expect(screen.getByText("Bob Bastos")).toBeInTheDocument();
    // No automatic retry.
    expect(mockListAdminUsers).toHaveBeenCalledTimes(2);
  });

  it("does not retry a rate-limited page on its own", async () => {
    const user = userEvent.setup();
    mockListAdminUsers
      .mockResolvedValueOnce(pageOf(SAMPLE_USERS, "cursor-1"))
      .mockRejectedValueOnce(new ApiRequestError(429, "rate_limited", "slow down"));
    renderAdminUsersRoute();

    await user.click(await screen.findByRole("button", { name: /carregar mais usuários/i }));

    expect(await screen.findByRole("alert")).toHaveTextContent(/muitas solicitações/i);
    expect(mockListAdminUsers).toHaveBeenCalledTimes(2);
  });

  // The filter box only sees what has been fetched, and says so.
  it("warns that filters only cover loaded users when pages remain", async () => {
    mockListAdminUsers.mockResolvedValue(pageOf(SAMPLE_USERS, "cursor-1"));
    renderAdminUsersRoute();

    expect(
      await screen.findByText(/os filtros consideram os usuários já carregados/i),
    ).toBeInTheDocument();
  });

  it("omits the filter warning when everything is loaded", async () => {
    mockListAdminUsers.mockResolvedValue(pageOf(SAMPLE_USERS));
    renderAdminUsersRoute();

    await screen.findByText("Alice Andrade");
    expect(screen.queryByText(/os filtros consideram/i)).not.toBeInTheDocument();
  });

  it("refresh after an invite returns to the first page", async () => {
    const user = userEvent.setup();
    mockListAdminUsers.mockResolvedValue(pageOf(SAMPLE_USERS, "cursor-1"));
    mockCreateAdminInvite.mockResolvedValue(undefined);
    renderAdminUsersRoute();

    await user.click(await screen.findByRole("button", { name: /convidar usuário/i }));
    await user.type(screen.getByLabelText(/e-mail/i), "new@example.com");
    await user.type(screen.getByLabelText(/nome de exibição/i), "New");
    await user.click(screen.getByRole("button", { name: /enviar convite/i }));

    await waitFor(() => expect(mockListAdminUsers).toHaveBeenCalledTimes(2));
    // No cursor: the refresh starts over from the canonical first page.
    expect(mockListAdminUsers.mock.calls[1][0]).not.toMatchObject({ cursor: "cursor-1" });
  });
});

// ── Session scope, driven through the real auth module ─────────────────────
//
// These are page-level on purpose. The hook's own tests pass the scope key as a
// string, which proves it reacts to a change but not that a change ever
// happens. What follows drives the actual `setTokens` / `clearTokens` the app
// calls, so it fails if the page ever stops deriving a key that moves with the
// session — which is exactly the defect a constant key had.
//
// The page is mounted without RequireAuth so it stays mounted across the
// switch. Under the real router a logout also navigates away; that unmount is
// the second line of defence, and it must not be the only one.

function renderAdminUsersPageBare() {
  return render(
    <MemoryRouter initialEntries={["/admin/users"]}>
      <AdminUsersPage />
    </MemoryRouter>,
  );
}

/** A promise whose settlement this test controls. */
function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((res) => {
    resolve = res;
  });
  return { promise, resolve };
}

const USERS_A: AdminUser[] = [
  {
    id: "a1",
    email: "ana.session-a@example.com",
    displayName: "Ana SessionA",
    status: "active",
    authSource: "local",
    createdAt: "2024-01-15T10:00:00Z",
  },
];

const USERS_B: AdminUser[] = [
  {
    id: "b1",
    email: "bruno.session-b@example.com",
    displayName: "Bruno SessionB",
    status: "active",
    authSource: "local",
    createdAt: "2024-02-15T10:00:00Z",
  },
];

/** Nothing belonging to session A may be on screen. */
function expectNoTraceOfSessionA() {
  expect(screen.queryByText("Ana SessionA")).not.toBeInTheDocument();
  expect(screen.queryByText("ana.session-a@example.com")).not.toBeInTheDocument();
}

describe("AdminUsersPage — session scope", () => {
  it("ignores a session A response that lands after session B was installed", async () => {
    const sessionA = deferred<ReturnType<typeof pageOf>>();
    const sessionB = deferred<ReturnType<typeof pageOf>>();
    mockListAdminUsers.mockReturnValueOnce(sessionA.promise).mockReturnValueOnce(sessionB.promise);

    setTokens("access-token-session-a");
    renderAdminUsersPageBare();
    await waitFor(() => expect(mockListAdminUsers).toHaveBeenCalledTimes(1));

    // Session A's listing is still in flight when a different session arrives.
    await act(async () => {
      setTokens("access-token-session-b");
    });

    // The key moved, so the page asked again — for B this time.
    await waitFor(() => expect(mockListAdminUsers).toHaveBeenCalledTimes(2));

    await act(async () => {
      sessionB.resolve(pageOf(USERS_B));
    });
    expect(await screen.findByText("Bruno SessionB")).toBeInTheDocument();

    // A's request finally answers. It belongs to a session that is gone.
    await act(async () => {
      sessionA.resolve(pageOf(USERS_A));
    });

    expectNoTraceOfSessionA();
    expect(screen.getByText("Bruno SessionB")).toBeInTheDocument();
  });

  it("clears a fully loaded session A the moment session B is installed", async () => {
    mockListAdminUsers.mockResolvedValueOnce(pageOf(USERS_A));
    setTokens("access-token-session-a");
    renderAdminUsersPageBare();

    expect(await screen.findByText("Ana SessionA")).toBeInTheDocument();

    // Session B's listing is left pending, so what the screen shows in the
    // meantime is entirely the result of the switch itself.
    const sessionB = deferred<ReturnType<typeof pageOf>>();
    mockListAdminUsers.mockReturnValueOnce(sessionB.promise);

    await act(async () => {
      setTokens("access-token-session-b");
    });

    // Immediately, with nothing resolved: A's rows are already gone.
    expectNoTraceOfSessionA();

    await act(async () => {
      sessionB.resolve(pageOf(USERS_B));
    });
    expect(await screen.findByText("Bruno SessionB")).toBeInTheDocument();
    expectNoTraceOfSessionA();
  });

  it("re-fetches for a new session even when the same user signs in again", async () => {
    mockListAdminUsers.mockResolvedValue(pageOf(USERS_A));
    setTokens("access-token-session-a");
    renderAdminUsersPageBare();
    await screen.findByText("Ana SessionA");

    // Same person, new session. The identity did not change, but the session
    // did, and anything fetched under the old one is no longer answerable for.
    await act(async () => {
      clearTokens();
      setTokens("access-token-session-a-again");
    });

    await waitFor(() => expect(mockListAdminUsers).toHaveBeenCalledTimes(2));
  });

  it("clears the table on logout and asks for nothing without a session", async () => {
    mockListAdminUsers.mockResolvedValueOnce(pageOf(USERS_A, "cursor-1"));
    setTokens("access-token-session-a");
    renderAdminUsersPageBare();

    await screen.findByText("Ana SessionA");
    // A cursor exists, so the page is offering to load more.
    expect(screen.getByRole("button", { name: /carregar mais usuários/i })).toBeInTheDocument();

    await act(async () => {
      clearTokens();
    });

    expectNoTraceOfSessionA();
    // The cursor went with the rows: there is nothing left to page through.
    expect(
      screen.queryByRole("button", { name: /carregar mais usuários/i }),
    ).not.toBeInTheDocument();
    expect(mockListAdminUsers).toHaveBeenCalledTimes(1);
  });

  it("ignores a response that arrives after logout", async () => {
    const sessionA = deferred<ReturnType<typeof pageOf>>();
    mockListAdminUsers.mockReturnValueOnce(sessionA.promise);

    setTokens("access-token-session-a");
    renderAdminUsersPageBare();
    await waitFor(() => expect(mockListAdminUsers).toHaveBeenCalledTimes(1));

    await act(async () => {
      clearTokens();
    });
    await act(async () => {
      sessionA.resolve(pageOf(USERS_A));
    });

    expectNoTraceOfSessionA();
    expect(mockListAdminUsers).toHaveBeenCalledTimes(1);
  });

  it("does not request the listing at all without a session", async () => {
    mockListAdminUsers.mockResolvedValue(pageOf(USERS_A));
    clearTokens();

    renderAdminUsersPageBare();

    // The page renders — it is simply empty, and nothing was asked of the API.
    await waitFor(() =>
      expect(screen.getByRole("heading", { name: "Usuários" })).toBeInTheDocument(),
    );
    expect(mockListAdminUsers).not.toHaveBeenCalled();
  });
});

// ── Paging by id, presented in name order ──────────────────────────────────
//
// The two orders are deliberately different: the server pages by user id
// because that is what its index covers, and the table presents by name
// because that is what a person reads. The fixture below makes them disagree —
// ids ascend while names descend — so a test cannot pass by accident.

/** The visible names, in the order the table renders them. */
function renderedNames() {
  return screen
    .getAllByRole("row")
    .slice(1) // drop the header row
    .map((row) => row.querySelector(".admin-users__user-name")?.textContent)
    .filter((name): name is string => Boolean(name));
}

function pagedUser(id: string, displayName: string): AdminUser {
  return {
    id,
    email: `${id}@example.com`,
    displayName,
    status: "active",
    authSource: "local",
    createdAt: "2024-01-01T00:00:00Z",
  };
}

describe("AdminUsersPage — id paging, name ordering", () => {
  it("keeps the table in name order as pages arrive in id order", async () => {
    const user = userEvent.setup();
    // Page 1 by id: id-1 (Zoe), id-2 (Nina). Page 2: id-3 (Ana), id-4 (Bruno).
    mockListAdminUsers
      .mockResolvedValueOnce(
        pageOf([pagedUser("id-1", "Zoe"), pagedUser("id-2", "Nina")], "cursor-1"),
      )
      .mockResolvedValueOnce(pageOf([pagedUser("id-3", "Ana"), pagedUser("id-4", "Bruno")]));

    renderAdminUsersRoute();
    await screen.findByText("Zoe");

    // First page alone is already presented by name, not by id.
    expect(renderedNames()).toEqual(["Nina", "Zoe"]);

    await user.click(screen.getByRole("button", { name: /carregar mais usuários/i }));
    await screen.findByText("Ana");

    // The second page's names sort above the first page's, and do.
    expect(renderedNames()).toEqual(["Ana", "Bruno", "Nina", "Zoe"]);
  });

  it("pages with the cursor the server gave and stops when it stops giving one", async () => {
    const user = userEvent.setup();
    mockListAdminUsers
      .mockResolvedValueOnce(pageOf([pagedUser("id-1", "Zoe")], "cursor-1"))
      .mockResolvedValueOnce(pageOf([pagedUser("id-2", "Ana")]));

    renderAdminUsersRoute();
    await screen.findByText("Zoe");

    await user.click(screen.getByRole("button", { name: /carregar mais usuários/i }));
    await screen.findByText("Ana");

    expect(mockListAdminUsers.mock.calls[1][0]).toMatchObject({ cursor: "cursor-1" });
    // No cursor came back, so there is nothing further to offer.
    expect(
      screen.queryByRole("button", { name: /carregar mais usuários/i }),
    ).not.toBeInTheDocument();
  });

  it("loses no row and repeats none when a page overlaps the previous one", async () => {
    const user = userEvent.setup();
    // A membership change between the two reads pushes id-2 across the page
    // boundary, so it arrives twice.
    mockListAdminUsers
      .mockResolvedValueOnce(
        pageOf([pagedUser("id-1", "Zoe"), pagedUser("id-2", "Nina")], "cursor-1"),
      )
      .mockResolvedValueOnce(pageOf([pagedUser("id-2", "Nina"), pagedUser("id-3", "Ana")]));

    renderAdminUsersRoute();
    await screen.findByText("Zoe");

    await user.click(screen.getByRole("button", { name: /carregar mais usuários/i }));
    await screen.findByText("Ana");

    expect(renderedNames()).toEqual(["Ana", "Nina", "Zoe"]);
  });
});
