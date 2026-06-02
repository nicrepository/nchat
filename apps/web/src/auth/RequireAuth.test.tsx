import { render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { clearTokens, setTokens } from "../lib/authSession";
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
      actual.setTokens(...(args as [string, string]));
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
  vi.clearAllMocks();
});

afterEach(() => {
  clearTokens();
});

describe("RequireAuth", () => {
  it("renders children when access token is present in sessionStorage", () => {
    setTokens("at", "rt");
    renderWithRouter();
    expect(screen.getByText("Protected content")).toBeInTheDocument();
  });

  it("redirects to /login when no tokens are stored", () => {
    renderWithRouter();
    expect(screen.getByText("Login page")).toBeInTheDocument();
    expect(screen.queryByText("Protected content")).not.toBeInTheDocument();
  });

  it("attempts refresh when only refresh token is present and renders children on success", async () => {
    sessionStorage.setItem("nchat_rt", "rt_only");
    mockRefresh.mockResolvedValue({
      accessToken: "new_at",
      refreshToken: "new_rt",
      tokenType: "Bearer",
      expiresIn: 900,
    });
    renderWithRouter();
    await waitFor(() => {
      expect(screen.getByText("Protected content")).toBeInTheDocument();
    });
    expect(mockRefresh).toHaveBeenCalledWith("rt_only");
    expect(mockSetTokens).toHaveBeenCalledWith("new_at", "new_rt");
  });

  it("redirects to /login when refresh fails", async () => {
    sessionStorage.setItem("nchat_rt", "expired_rt");
    mockRefresh.mockRejectedValue(new Error("invalid_refresh_token"));
    renderWithRouter();
    await waitFor(() => {
      expect(screen.getByText("Login page")).toBeInTheDocument();
    });
  });

  it("shows nothing while refresh is in progress", () => {
    sessionStorage.setItem("nchat_rt", "rt_only");
    let resolve: (value: { accessToken: string; refreshToken: string }) => void;
    mockRefresh.mockReturnValue(new Promise((r) => (resolve = r)));
    renderWithRouter();
    expect(screen.queryByText("Protected content")).not.toBeInTheDocument();
    expect(screen.queryByText("Login page")).not.toBeInTheDocument();
    resolve!({ accessToken: "at", refreshToken: "rt" });
  });
});
