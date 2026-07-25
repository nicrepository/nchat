import { act, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { _resetListeners, clearTokens, setTokens } from "../lib/authSession";
import RequireAuth from "./RequireAuth";

const { mockRefresh, mockSetTokens } = vi.hoisted(() => ({
  mockRefresh: vi.fn(),
  mockSetTokens: vi.fn(),
}));

vi.mock("./authApi", () => ({
  refresh: (...args: unknown[]) => mockRefresh(...args),
}));

vi.mock("../lib/authSession", async () => {
  const actual = await vi.importActual<typeof import("../lib/authSession")>("../lib/authSession");
  return {
    ...actual,
    setTokens: (...args: unknown[]) => {
      actual.setTokens(...(args as [string]));
      mockSetTokens(...args);
    },
  };
});

function renderWithRouter(initialPath = "/protected") {
  return render(
    <MemoryRouter initialEntries={[initialPath]}>
      <Routes>
        <Route path="/login" element={<div>Login page</div>} />
        <Route
          path="/protected"
          element={
            <RequireAuth>
              <div>Protected content</div>
            </RequireAuth>
          }
        />
      </Routes>
    </MemoryRouter>,
  );
}

beforeEach(() => {
  clearTokens();
  _resetListeners();
  vi.clearAllMocks();
});

afterEach(() => {
  clearTokens();
  _resetListeners();
});

describe("RequireAuth", () => {
  it("renders children when access token is present in sessionStorage", () => {
    setTokens("at");
    renderWithRouter();
    expect(screen.getByText("Protected content")).toBeInTheDocument();
  });

  it("redirects to /login when no access token and refresh cookie is absent or expired", async () => {
    mockRefresh.mockRejectedValue(new Error("invalid_refresh_token"));
    renderWithRouter();
    await waitFor(() => {
      expect(screen.getByText("Login page")).toBeInTheDocument();
    });
    expect(screen.queryByText("Protected content")).not.toBeInTheDocument();
  });

  it("attempts silent refresh via HttpOnly cookie and renders children on success", async () => {
    mockRefresh.mockResolvedValue({
      accessToken: "new_at",
      tokenType: "Bearer",
      expiresIn: 900,
    });
    renderWithRouter();
    await waitFor(() => {
      expect(screen.getByText("Protected content")).toBeInTheDocument();
    });
    expect(mockRefresh).toHaveBeenCalledTimes(1);
    expect(mockSetTokens).toHaveBeenCalledWith("new_at");
  });

  it("redirects to /login when refresh fails", async () => {
    mockRefresh.mockRejectedValue(new Error("invalid_refresh_token"));
    renderWithRouter();
    await waitFor(() => {
      expect(screen.getByText("Login page")).toBeInTheDocument();
    });
  });

  it("shows nothing while refresh is in progress", () => {
    let resolveRefresh!: (value: { accessToken: string }) => void;
    mockRefresh.mockReturnValue(new Promise((r) => (resolveRefresh = r)));
    renderWithRouter();
    expect(screen.queryByText("Protected content")).not.toBeInTheDocument();
    expect(screen.queryByText("Login page")).not.toBeInTheDocument();
    resolveRefresh({ accessToken: "at" });
  });

  it("does not render protected content while auth check is in progress", () => {
    let resolveRefresh!: (value: { accessToken: string }) => void;
    mockRefresh.mockReturnValue(new Promise((r) => (resolveRefresh = r)));
    renderWithRouter();
    expect(screen.queryByText("Protected content")).not.toBeInTheDocument();
    expect(screen.queryByText("Login page")).not.toBeInTheDocument();
    resolveRefresh({ accessToken: "at" });
  });

  it("redirects to /login when clearTokens is called after mount", async () => {
    setTokens("at");
    renderWithRouter();
    expect(screen.getByText("Protected content")).toBeInTheDocument();

    act(() => {
      clearTokens();
    });

    await waitFor(() => {
      expect(screen.getByText("Login page")).toBeInTheDocument();
    });
    expect(screen.queryByText("Protected content")).not.toBeInTheDocument();
  });

  it("passes location.pathname as state.from when redirecting unauthenticated user", async () => {
    mockRefresh.mockRejectedValue(new Error("no cookie"));

    function LoginCapture() {
      const loc = useLocation();
      const from = (loc.state as { from?: string } | null)?.from ?? "none";
      return <div>login-from:{from}</div>;
    }

    render(
      <MemoryRouter initialEntries={["/protected"]}>
        <Routes>
          <Route path="/login" element={<LoginCapture />} />
          <Route
            path="/protected"
            element={
              <RequireAuth>
                <div>Protected content</div>
              </RequireAuth>
            }
          />
        </Routes>
      </MemoryRouter>,
    );

    await waitFor(() => {
      expect(screen.getByText("login-from:/protected")).toBeInTheDocument();
    });
  });
});
