import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import RequireAuth from "../auth/RequireAuth";
import { clearTokens, setTokens } from "../lib/authSession";
import AdminAntiSpamPage from "./AdminAntiSpamPage";
import type { AntiSpamPolicy } from "./adminAntiSpamApi";

// ── Mock adminAntiSpamApi ──────────────────────────────────────────────────

const { mockWorkspaceId, mockFetchPolicy, mockUpdatePolicy } = vi.hoisted(() => ({
  mockWorkspaceId: vi.fn<() => Promise<string>>(),
  mockFetchPolicy: vi.fn<(id: string) => Promise<AntiSpamPolicy>>(),
  mockUpdatePolicy: vi.fn<(id: string, value: number) => Promise<AntiSpamPolicy>>(),
}));

vi.mock("./adminAntiSpamApi", () => ({
  fetchCurrentWorkspaceId: () => mockWorkspaceId(),
  fetchAntiSpamPolicy: (id: string) => mockFetchPolicy(id),
  updateAntiSpamPolicy: (id: string, value: number) => mockUpdatePolicy(id, value),
}));

// ── Helpers ────────────────────────────────────────────────────────────────

const policy = (messagesPerMinute: number): AntiSpamPolicy => ({
  workspaceId: "ws-1",
  messagesPerMinute,
  min: 1,
  max: 600,
});

function renderPage(authenticated = true) {
  if (authenticated) {
    setTokens("at");
  } else {
    clearTokens();
  }
  return render(
    <MemoryRouter initialEntries={["/admin/anti-spam"]}>
      <Routes>
        <Route path="/login" element={<div>Login page</div>} />
        <Route
          path="/admin/anti-spam"
          element={
            <RequireAuth>
              <AdminAntiSpamPage />
            </RequireAuth>
          }
        />
      </Routes>
    </MemoryRouter>,
  );
}

const input = () => screen.getByLabelText(/mensagens por usuário por minuto/i);
// The label switches to "Salvando…" while the request is in flight, so the
// query has to match both states.
const saveButton = () => screen.getByRole("button", { name: /salvar|salvando/i });

beforeEach(() => {
  mockWorkspaceId.mockReset().mockResolvedValue("ws-1");
  mockFetchPolicy.mockReset().mockResolvedValue(policy(60));
  mockUpdatePolicy.mockReset();
});

afterEach(() => {
  clearTokens();
});

// ── Loading and rendering ──────────────────────────────────────────────────

describe("AdminAntiSpamPage", () => {
  it("shows a loading state before the policy arrives", () => {
    renderPage();
    expect(screen.getByText(/carregando configuração/i)).toBeInTheDocument();
  });

  it("renders the current limit once loaded", async () => {
    renderPage();
    await waitFor(() => expect(input()).toHaveValue(60));
  });

  it("exposes the server bounds on the input and in the hint", async () => {
    mockFetchPolicy.mockResolvedValue(policy(45));
    renderPage();

    await waitFor(() => expect(input()).toHaveValue(45));
    expect(input()).toHaveAttribute("min", "1");
    expect(input()).toHaveAttribute("max", "600");
    expect(screen.getByText(/entre 1 e 600 mensagens por minuto/i)).toBeInTheDocument();
  });

  it("shows an error state when the policy cannot be read", async () => {
    mockFetchPolicy.mockRejectedValue(new Error("nope"));
    renderPage();

    await waitFor(() =>
      expect(screen.getByRole("alert")).toHaveTextContent(/não foi possível carregar/i),
    );
    expect(screen.queryByRole("button", { name: /salvar|salvando/i })).not.toBeInTheDocument();
  });

  // ── Submission ───────────────────────────────────────────────────────────

  it("saves a valid limit and confirms success", async () => {
    const user = userEvent.setup();
    mockUpdatePolicy.mockResolvedValue(policy(30));
    renderPage();
    await waitFor(() => expect(input()).toHaveValue(60));

    await user.clear(input());
    await user.type(input(), "30");
    await user.click(saveButton());

    await waitFor(() => expect(mockUpdatePolicy).toHaveBeenCalledWith("ws-1", 30));
    expect(await screen.findByRole("status")).toHaveTextContent(/limite atualizado/i);
    expect(input()).toHaveValue(30);
  });

  it("reports a validation error and never calls the API", async () => {
    const user = userEvent.setup();
    renderPage();
    await waitFor(() => expect(input()).toHaveValue(60));

    await user.clear(input());
    await user.type(input(), "0");
    await user.click(saveButton());

    expect(await screen.findByRole("alert")).toHaveTextContent(/entre 1 e 600/i);
    expect(mockUpdatePolicy).not.toHaveBeenCalled();
    expect(input()).toHaveAttribute("aria-invalid", "true");
  });

  it("rejects a value above the maximum", async () => {
    const user = userEvent.setup();
    renderPage();
    await waitFor(() => expect(input()).toHaveValue(60));

    await user.clear(input());
    await user.type(input(), "601");
    await user.click(saveButton());

    expect(await screen.findByRole("alert")).toHaveTextContent(/entre 1 e 600/i);
    expect(mockUpdatePolicy).not.toHaveBeenCalled();
  });

  it("surfaces a save failure without leaking the server message", async () => {
    const user = userEvent.setup();
    mockUpdatePolicy.mockRejectedValue(new Error("pq: relation chat.workspaces does not exist"));
    renderPage();
    await waitFor(() => expect(input()).toHaveValue(60));

    await user.clear(input());
    await user.type(input(), "30");
    await user.click(saveButton());

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent(/não foi possível salvar/i);
    expect(alert).not.toHaveTextContent(/chat\.workspaces/);
  });

  it("disables the button while saving so a double submit cannot fire twice", async () => {
    const user = userEvent.setup();
    let release: (value: AntiSpamPolicy) => void = () => {};
    mockUpdatePolicy.mockReturnValue(
      new Promise<AntiSpamPolicy>((resolve) => {
        release = resolve;
      }),
    );
    renderPage();
    await waitFor(() => expect(input()).toHaveValue(60));

    await user.clear(input());
    await user.type(input(), "30");
    await user.click(saveButton());

    await waitFor(() => expect(saveButton()).toBeDisabled());
    await user.click(saveButton());
    expect(mockUpdatePolicy).toHaveBeenCalledTimes(1);

    release(policy(30));
    await waitFor(() => expect(saveButton()).toBeEnabled());
  });

  // ── Route guard ──────────────────────────────────────────────────────────

  it("redirects an unauthenticated visitor to login", async () => {
    renderPage(false);
    expect(await screen.findByText("Login page")).toBeInTheDocument();
    expect(mockFetchPolicy).not.toHaveBeenCalled();
  });
});
