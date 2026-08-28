import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import RequireAuth from "../auth/RequireAuth";
import { clearTokens, setTokens } from "../lib/authSession";
import AdminUploadLimitPage from "./AdminUploadLimitPage";
import type { UploadLimitPolicy } from "./adminUploadLimitApi";

// ── Mock adminUploadLimitApi ───────────────────────────────────────────────

const { mockWorkspaceId, mockFetchPolicy, mockUpdatePolicy } = vi.hoisted(() => ({
  mockWorkspaceId: vi.fn<() => Promise<string>>(),
  mockFetchPolicy: vi.fn<(id: string) => Promise<UploadLimitPolicy>>(),
  mockUpdatePolicy: vi.fn<(id: string, value: number) => Promise<UploadLimitPolicy>>(),
}));

vi.mock("./adminAntiSpamApi", () => ({
  fetchCurrentWorkspaceId: () => mockWorkspaceId(),
}));

vi.mock("./adminUploadLimitApi", () => ({
  fetchUploadLimitPolicy: (id: string) => mockFetchPolicy(id),
  updateUploadLimitPolicy: (id: string, value: number) => mockUpdatePolicy(id, value),
}));

// ── Helpers ────────────────────────────────────────────────────────────────

const MIB = 1024 * 1024;

const policy = (maxUploadBytes: number): UploadLimitPolicy => ({
  workspaceId: "ws-1",
  maxUploadBytes,
  min: 1 * MIB,
  max: 512 * MIB,
});

function renderPage(authenticated = true) {
  if (authenticated) {
    setTokens("at");
  } else {
    clearTokens();
  }
  return render(
    <MemoryRouter initialEntries={["/admin/upload-limit"]}>
      <Routes>
        <Route path="/login" element={<div>Login page</div>} />
        <Route
          path="/admin/upload-limit"
          element={
            <RequireAuth>
              <AdminUploadLimitPage />
            </RequireAuth>
          }
        />
      </Routes>
    </MemoryRouter>,
  );
}

const input = () => screen.getByLabelText(/limite máximo por arquivo/i);
const saveButton = () => screen.getByRole("button", { name: /salvar|salvando/i });

beforeEach(() => {
  mockWorkspaceId.mockReset().mockResolvedValue("ws-1");
  mockFetchPolicy.mockReset().mockResolvedValue(policy(250 * MIB));
  mockUpdatePolicy.mockReset();
});

afterEach(() => {
  clearTokens();
});

describe("AdminUploadLimitPage", () => {
  it("shows the current limit in MiB and the server's bounds", async () => {
    renderPage();

    await waitFor(() => expect(input()).toHaveValue(250));
    expect(screen.getByText(/número inteiro entre 1 e 512 MiB/i)).toBeInTheDocument();
    expect(input()).toHaveAttribute("min", "1");
    expect(input()).toHaveAttribute("max", "512");
  });

  it("saves the value converted to bytes", async () => {
    mockUpdatePolicy.mockResolvedValue(policy(100 * MIB));
    renderPage();
    await waitFor(() => expect(input()).toHaveValue(250));

    await userEvent.clear(input());
    await userEvent.type(input(), "100");
    await userEvent.click(saveButton());

    await waitFor(() => expect(mockUpdatePolicy).toHaveBeenCalledWith("ws-1", 100 * MIB));
    expect(await screen.findByText("Limite atualizado.")).toBeInTheDocument();
    expect(input()).toHaveValue(100);
  });

  it("refuses an out-of-range value without calling the server", async () => {
    renderPage();
    await waitFor(() => expect(input()).toHaveValue(250));

    await userEvent.clear(input());
    await userEvent.type(input(), "513");
    await userEvent.click(saveButton());

    expect(await screen.findByRole("alert")).toHaveTextContent(/entre 1 e 512 MiB/i);
    expect(mockUpdatePolicy).not.toHaveBeenCalled();
  });

  it("reports a failed save and leaves the form usable", async () => {
    mockUpdatePolicy.mockRejectedValue(new Error("boom"));
    renderPage();
    await waitFor(() => expect(input()).toHaveValue(250));

    await userEvent.clear(input());
    await userEvent.type(input(), "100");
    await userEvent.click(saveButton());

    expect(await screen.findByRole("alert")).toHaveTextContent(/não foi possível salvar/i);
    // The button and the field come back, so a retry is possible.
    await waitFor(() => expect(saveButton()).toBeEnabled());
    expect(input()).toBeEnabled();
  });

  it("never rounds a fractional-MiB policy into a different limit", async () => {
    // 1572864 bytes is 1.5 MiB — the exact value the review flagged as being
    // displayed as 2 and saved as 2097152.
    mockFetchPolicy.mockResolvedValue({ ...policy(250 * MIB), maxUploadBytes: 1572864 });
    renderPage();

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent(/1572864 bytes/);
    expect(alert).toHaveTextContent(/não é um número inteiro de MiB/i);
    // No field, so no submit path can overwrite the stored value.
    expect(screen.queryByLabelText(/limite máximo por arquivo/i)).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /salvar/i })).not.toBeInTheDocument();
    expect(mockUpdatePolicy).not.toHaveBeenCalled();
  });

  it("rejects a decimal without calling the server", async () => {
    renderPage();
    await waitFor(() => expect(input()).toHaveValue(250));

    await userEvent.clear(input());
    await userEvent.type(input(), "1.5");
    await userEvent.click(saveButton());

    expect(await screen.findByRole("alert")).toHaveTextContent(/números inteiros de MiB/i);
    expect(mockUpdatePolicy).not.toHaveBeenCalled();
  });

  it("preserves the policy exactly when saved without a change", async () => {
    mockUpdatePolicy.mockResolvedValue(policy(250 * MIB));
    renderPage();
    await waitFor(() => expect(input()).toHaveValue(250));

    await userEvent.click(saveButton());

    await waitFor(() => expect(mockUpdatePolicy).toHaveBeenCalledWith("ws-1", 262144000));
    expect(input()).toHaveValue(250);
  });

  it("shows an error when the policy cannot be loaded", async () => {
    mockFetchPolicy.mockRejectedValue(new Error("boom"));
    renderPage();

    expect(await screen.findByRole("alert")).toHaveTextContent(/não foi possível carregar/i);
  });

  it("sends an unauthenticated visitor to the login page", async () => {
    renderPage(false);

    expect(await screen.findByText("Login page")).toBeInTheDocument();
    expect(mockFetchPolicy).not.toHaveBeenCalled();
  });
});
