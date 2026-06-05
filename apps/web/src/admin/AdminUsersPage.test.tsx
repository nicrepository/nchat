import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { clearTokens, setTokens } from "../lib/authSession";
import RequireAuth from "../auth/RequireAuth";
import AdminUsersPage from "./AdminUsersPage";
import type { AdminUser } from "./adminUsersApi";

// ── Mock adminUsersApi ─────────────────────────────────────────────────────

const { mockListAdminUsers } = vi.hoisted(() => ({
  mockListAdminUsers: vi.fn<() => Promise<AdminUser[]>>(),
}));

vi.mock("./adminUsersApi", () => ({
  listAdminUsers: () => mockListAdminUsers(),
}));

// ── Helpers ────────────────────────────────────────────────────────────────

function renderAdminUsersRoute(authenticated = true) {
  if (authenticated) {
    setTokens("at", "rt");
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
  it("redirects unauthenticated user to /login", () => {
    mockListAdminUsers.mockResolvedValue([]);
    renderAdminUsersRoute(false);

    expect(screen.getByText("Login page")).toBeInTheDocument();
    expect(screen.queryByRole("table")).not.toBeInTheDocument();
  });

  it("does not render protected content for unauthenticated user", () => {
    mockListAdminUsers.mockResolvedValue([]);
    renderAdminUsersRoute(false);

    expect(screen.queryByRole("heading", { name: /usuários/i })).not.toBeInTheDocument();
  });
});

describe("AdminUsersPage — admin shell structure", () => {
  it("renders the admin shell wrapper", async () => {
    mockListAdminUsers.mockResolvedValue([]);
    renderAdminUsersRoute();

    await waitFor(() => {
      expect(screen.getByTestId("admin-shell")).toBeInTheDocument();
    });
  });

  it("renders the dark sidebar", async () => {
    mockListAdminUsers.mockResolvedValue([]);
    renderAdminUsersRoute();

    await waitFor(() => {
      expect(screen.getByTestId("admin-sidebar")).toBeInTheDocument();
    });
  });

  it("renders the admin top navigation", async () => {
    mockListAdminUsers.mockResolvedValue([]);
    renderAdminUsersRoute();

    await waitFor(() => {
      expect(screen.getByTestId("admin-topnav")).toBeInTheDocument();
    });
  });

  it("admin top nav contains expected tabs", async () => {
    mockListAdminUsers.mockResolvedValue([]);
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
    mockListAdminUsers.mockResolvedValue([]);
    renderAdminUsersRoute();

    await waitFor(() => {
      expect(screen.getByTestId("admin-topnav")).toBeInTheDocument();
    });

    const activeTab = screen.getByTestId("admin-topnav").querySelector('[aria-current="page"]');
    expect(activeTab).toHaveTextContent("Usuários");
  });

  it("sidebar shows NIC Chat branding", async () => {
    mockListAdminUsers.mockResolvedValue([]);
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
  it("renders the invite button in a disabled state", async () => {
    mockListAdminUsers.mockResolvedValue([]);
    renderAdminUsersRoute();

    await waitFor(() => {
      expect(screen.getByRole("button", { name: /convidar usuário/i })).toBeInTheDocument();
    });

    const inviteBtn = screen.getByRole("button", { name: /convidar usuário/i });
    expect(inviteBtn).toBeDisabled();
    expect(inviteBtn).toHaveAttribute("aria-disabled", "true");
  });

  it("invite button does not submit a form or trigger navigation", async () => {
    mockListAdminUsers.mockResolvedValue([]);
    renderAdminUsersRoute();

    await waitFor(() => {
      expect(screen.getByRole("button", { name: /convidar usuário/i })).toBeInTheDocument();
    });

    const inviteBtn = screen.getByRole("button", { name: /convidar usuário/i });
    expect(inviteBtn).toHaveAttribute("type", "button");
    expect(inviteBtn).toBeDisabled();
  });
});

describe("AdminUsersPage — authenticated rendering", () => {
  it("renders the page heading when authenticated", async () => {
    mockListAdminUsers.mockResolvedValue([]);
    renderAdminUsersRoute();

    await waitFor(() => {
      expect(screen.queryByRole("table", { name: /lista de usuários/i })).toBeInTheDocument();
    });

    expect(screen.getByRole("heading", { name: /usuários/i })).toBeInTheDocument();
  });

  it("renders filter chips", async () => {
    mockListAdminUsers.mockResolvedValue([]);
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
    mockListAdminUsers.mockResolvedValue([]);
    renderAdminUsersRoute();

    await waitFor(() => {
      expect(screen.getByRole("searchbox", { name: /buscar usuários/i })).toBeInTheDocument();
    });
  });
});

describe("AdminUsersPage — loading state", () => {
  it("shows loading skeleton while request is pending", () => {
    let resolve: (v: AdminUser[]) => void;
    mockListAdminUsers.mockReturnValue(new Promise((r) => (resolve = r)));

    renderAdminUsersRoute();

    const table = screen.getByRole("table", { name: /lista de usuários/i });
    expect(table).toHaveAttribute("aria-busy", "true");

    resolve!([]);
  });
});

describe("AdminUsersPage — empty state", () => {
  it("shows empty state when API returns no users", async () => {
    mockListAdminUsers.mockResolvedValue([]);
    renderAdminUsersRoute();

    await waitFor(() => {
      expect(screen.getByText(/nenhum usuário disponível/i)).toBeInTheDocument();
    });

    expect(screen.getByText(/as contas registradas aparecerão aqui/i)).toBeInTheDocument();
  });

  it("empty state is rendered inside the admin shell layout", async () => {
    mockListAdminUsers.mockResolvedValue([]);
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
    mockListAdminUsers.mockResolvedValue(SAMPLE_USERS);
    renderAdminUsersRoute();

    await waitFor(() => {
      expect(screen.getByText("Alice Andrade")).toBeInTheDocument();
    });

    expect(screen.getByText("Bob Bastos")).toBeInTheDocument();
    expect(screen.getByText("alice@example.com")).toBeInTheDocument();
    expect(screen.getByText("bob@example.com")).toBeInTheDocument();
  });

  it("renders status badges for each user", async () => {
    mockListAdminUsers.mockResolvedValue(SAMPLE_USERS);
    renderAdminUsersRoute();

    await waitFor(() => {
      expect(screen.getByText("Ativo")).toBeInTheDocument();
    });

    expect(screen.getByText("Suspenso")).toBeInTheDocument();
  });

  it("renders initials avatar derived from displayName", async () => {
    mockListAdminUsers.mockResolvedValue([SAMPLE_USERS[0]]);
    renderAdminUsersRoute();

    await waitFor(() => {
      expect(screen.getByText("AA")).toBeInTheDocument();
    });
  });

  it("renders single-letter initial for single-word displayName", async () => {
    mockListAdminUsers.mockResolvedValue([
      { ...SAMPLE_USERS[0], displayName: "Alice", id: "u-single" },
    ]);
    renderAdminUsersRoute();

    await waitFor(() => {
      expect(screen.getByText("A")).toBeInTheDocument();
    });
  });

  it("renders origin badge showing auth source value", async () => {
    mockListAdminUsers.mockResolvedValue([SAMPLE_USERS[0]]);
    renderAdminUsersRoute();

    await waitFor(() => {
      expect(screen.getByText("local")).toBeInTheDocument();
    });
  });

  it("renders authSource as origin/provider badge, not as role or function", async () => {
    mockListAdminUsers.mockResolvedValue(SAMPLE_USERS);
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
    mockListAdminUsers.mockResolvedValue([
      {
        ...SAMPLE_USERS[0],
        displayName: "Alice",
        fullName: "Alice Andrade",
        id: "u-fullname",
      },
    ]);
    renderAdminUsersRoute();

    await waitFor(() => {
      expect(screen.getByText("Alice Andrade")).toBeInTheDocument();
    });
  });

  it("renders table column headers: Nome, E-mail, Status, Origem, Criado em", async () => {
    mockListAdminUsers.mockResolvedValue([]);
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
});

describe("AdminUsersPage — filter chips", () => {
  it("clicking Ativos chip filters to active users only", async () => {
    const user = userEvent.setup();
    mockListAdminUsers.mockResolvedValue(SAMPLE_USERS);
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
    mockListAdminUsers.mockResolvedValue(SAMPLE_USERS);
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
    mockListAdminUsers.mockResolvedValue(SAMPLE_USERS);
    renderAdminUsersRoute();

    await waitFor(() => {
      expect(screen.getByText("Alice Andrade")).toBeInTheDocument();
    });

    await user.click(screen.getByRole("button", { name: "Admins" }));

    expect(screen.getByText(/nenhum usuário disponível/i)).toBeInTheDocument();
  });

  it("clicking Convites pendentes chip shows empty state", async () => {
    const user = userEvent.setup();
    mockListAdminUsers.mockResolvedValue(SAMPLE_USERS);
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
    mockListAdminUsers.mockResolvedValue(SAMPLE_USERS);
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
    mockListAdminUsers.mockResolvedValue(SAMPLE_USERS);
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
