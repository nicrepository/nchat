import { render, screen, waitFor } from "@testing-library/react";
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
  // default: resolves after explicit control
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

describe("AdminUsersPage — authenticated rendering", () => {
  it("renders the page heading when authenticated", async () => {
    mockListAdminUsers.mockResolvedValue([]);
    renderAdminUsersRoute();

    // Wait past loading state
    await waitFor(() => {
      expect(screen.queryByRole("table", { name: /lista de usuários/i })).toBeInTheDocument();
    });

    expect(screen.getByRole("heading", { name: /usuários/i })).toBeInTheDocument();
  });
});

describe("AdminUsersPage — loading state", () => {
  it("shows loading skeleton while request is pending", () => {
    let resolve: (v: AdminUser[]) => void;
    mockListAdminUsers.mockReturnValue(new Promise((r) => (resolve = r)));

    renderAdminUsersRoute();

    // Skeleton rows are aria-hidden; table has aria-busy
    const table = screen.getByRole("table", { name: /lista de usuários/i });
    expect(table).toHaveAttribute("aria-busy", "true");

    // Clean up pending promise
    resolve!([]);
  });
});

describe("AdminUsersPage — empty state", () => {
  it("shows empty state when API returns no users", async () => {
    mockListAdminUsers.mockResolvedValue([]);
    renderAdminUsersRoute();

    await waitFor(() => {
      expect(screen.getByText(/nenhum usuário encontrado/i)).toBeInTheDocument();
    });

    expect(screen.getByText(/usuários aparecerão aqui/i)).toBeInTheDocument();
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
});
