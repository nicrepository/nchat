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
});
