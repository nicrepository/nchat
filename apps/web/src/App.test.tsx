import { render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import App from "./App";
import { clearTokens } from "./lib/authSession";

// Mock adminUsersApi to prevent real fetch calls in App-level tests.
// When unauthenticated, RequireAuth redirects before AdminUsersPage mounts,
// so listAdminUsers should never be called.
const { mockListAdminUsers } = vi.hoisted(() => ({
  mockListAdminUsers: vi.fn(),
}));

vi.mock("./admin/adminUsersApi", () => ({
  listAdminUsers: () => mockListAdminUsers(),
}));

beforeEach(() => {
  clearTokens();
  window.history.pushState({}, "", "/");
  vi.clearAllMocks();
});

describe("App", () => {
  it("renders the login page when user is not authenticated", async () => {
    render(<App />);
    expect(await screen.findByRole("heading", { name: /entrar no nic chat/i })).toBeInTheDocument();
  });

  it("redirects unauthenticated user from /admin/users to login", async () => {
    window.history.pushState({}, "", "/admin/users");
    render(<App />);
    expect(await screen.findByRole("heading", { name: /entrar no nic chat/i })).toBeInTheDocument();
    expect(mockListAdminUsers).not.toHaveBeenCalled();
    expect(screen.queryByRole("heading", { name: /^usuários$/i })).not.toBeInTheDocument();
  });
});
