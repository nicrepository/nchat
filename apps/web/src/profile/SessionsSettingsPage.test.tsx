import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import SessionsSettingsPage from "./SessionsSettingsPage";
import * as sessionsApi from "./sessionsApi";

vi.mock("./sessionsApi");

afterEach(() => {
  vi.clearAllMocks();
});

const sessions: sessionsApi.Session[] = [
  { id: "current", createdAt: "", lastSeenAt: "", ipAddress: "1.2.x.x", userAgent: "Firefox", current: true },
  { id: "other", createdAt: "", lastSeenAt: "", ipAddress: "3.4.x.x", userAgent: "Chrome", current: false },
];

describe("SessionsSettingsPage", () => {
  it("loads and lists sessions, current session first with its badge", async () => {
    vi.mocked(sessionsApi.listSessions).mockResolvedValueOnce(sessions);
    render(<SessionsSettingsPage />);
    await waitFor(() => expect(screen.getAllByTestId("session-row")).toHaveLength(2));
    expect(screen.getByText("Sessão atual")).toBeInTheDocument();
  });

  it("shows a retry-capable error independent of other sections", async () => {
    vi.mocked(sessionsApi.listSessions).mockRejectedValueOnce(new sessionsApi.SessionsApiError("unknown", "x"));
    render(<SessionsSettingsPage />);
    await waitFor(() => expect(screen.getByRole("button", { name: /tentar novamente/i })).toBeInTheDocument());
  });

  it("revokes a single session through the confirm dialog and relists", async () => {
    vi.mocked(sessionsApi.listSessions).mockResolvedValueOnce(sessions).mockResolvedValueOnce([sessions[0]]);
    vi.mocked(sessionsApi.revokeSession).mockResolvedValueOnce(undefined);
    const user = userEvent.setup();
    render(<SessionsSettingsPage />);
    await waitFor(() => expect(screen.getAllByTestId("session-row")).toHaveLength(2));
    await user.click(screen.getByRole("button", { name: "Revogar sessão" }));
    await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Revogar sessão" }));
    await waitFor(() => expect(sessionsApi.revokeSession).toHaveBeenCalledWith("other"));
    await waitFor(() => expect(screen.getAllByTestId("session-row")).toHaveLength(1));
  });

  it("revokes all others and preserves the current session", async () => {
    vi.mocked(sessionsApi.listSessions).mockResolvedValueOnce(sessions).mockResolvedValueOnce([sessions[0]]);
    vi.mocked(sessionsApi.revokeAllOtherSessions).mockResolvedValueOnce(undefined);
    const user = userEvent.setup();
    render(<SessionsSettingsPage />);
    await waitFor(() => expect(screen.getAllByTestId("session-row")).toHaveLength(2));
    await user.click(screen.getByRole("button", { name: /revogar todas as outras/i }));
    await user.click(screen.getByRole("button", { name: /revogar sessões/i }));
    await waitFor(() => expect(sessionsApi.revokeAllOtherSessions).toHaveBeenCalledTimes(1));
    await waitFor(() => {
      const rows = screen.getAllByTestId("session-row");
      expect(rows).toHaveLength(1);
      expect(screen.getByText("Sessão atual")).toBeInTheDocument();
    });
  });

  it("does not update state after unmount when a pending fetch resolves late", async () => {
    const consoleError = vi.spyOn(console, "error").mockImplementation(() => {});
    let resolveList!: (value: sessionsApi.Session[]) => void;
    vi.mocked(sessionsApi.listSessions).mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          resolveList = resolve;
        }),
    );
    const { unmount } = render(<SessionsSettingsPage />);
    unmount();
    resolveList(sessions);
    await Promise.resolve();
    await Promise.resolve();
    expect(consoleError).not.toHaveBeenCalled();
    consoleError.mockRestore();
  });

  it("does not update state after unmount when a pending fetch rejects late", async () => {
    const consoleError = vi.spyOn(console, "error").mockImplementation(() => {});
    let rejectList!: (error: Error) => void;
    vi.mocked(sessionsApi.listSessions).mockImplementationOnce(
      () =>
        new Promise((_resolve, reject) => {
          rejectList = reject;
        }),
    );
    const { unmount } = render(<SessionsSettingsPage />);
    unmount();
    rejectList(new Error("boom"));
    await Promise.resolve();
    await Promise.resolve();
    expect(consoleError).not.toHaveBeenCalled();
    consoleError.mockRestore();
  });

  it("does not reload after unmount when a pending revoke resolves late", async () => {
    vi.mocked(sessionsApi.listSessions).mockResolvedValueOnce(sessions);
    let resolveRevoke!: () => void;
    vi.mocked(sessionsApi.revokeSession).mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          resolveRevoke = resolve;
        }),
    );
    const user = userEvent.setup();
    const { unmount } = render(<SessionsSettingsPage />);
    await waitFor(() => expect(screen.getAllByTestId("session-row")).toHaveLength(2));
    await user.click(screen.getByRole("button", { name: "Revogar sessão" }));
    await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Revogar sessão" }));
    await waitFor(() => expect(sessionsApi.revokeSession).toHaveBeenCalledWith("other"));
    unmount();
    resolveRevoke();
    await Promise.resolve();
    await Promise.resolve();
    // Only the initial load; the post-revoke reload must not fire once unmounted.
    expect(sessionsApi.listSessions).toHaveBeenCalledTimes(1);
  });
});
