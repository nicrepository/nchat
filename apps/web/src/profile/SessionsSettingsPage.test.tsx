import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import { setTokens } from "../lib/authSession";
import SessionsSettingsPage from "./SessionsSettingsPage";
import * as sessionsApi from "./sessionsApi";

vi.mock("./sessionsApi");

afterEach(() => {
  cleanup();
  sessionStorage.clear();
  vi.clearAllMocks();
});

const FIREFOX_WINDOWS =
  "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:142.0) Gecko/20100101 Firefox/142.0";
const CHROME_WINDOWS =
  "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/152.0.0.0 Safari/537.36";
const EDGE_WINDOWS =
  "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/153.0.0.0 Safari/537.36 Edg/153.0.0.0";

const sessions: sessionsApi.Session[] = [
  {
    id: "current",
    createdAt: "",
    lastSeenAt: "",
    ipAddress: "1.2.x.x",
    userAgent: FIREFOX_WINDOWS,
    current: true,
  },
  {
    id: "other",
    createdAt: "",
    lastSeenAt: "",
    ipAddress: "3.4.x.x",
    userAgent: CHROME_WINDOWS,
    current: false,
  },
];

describe("SessionsSettingsPage", () => {
  it("loads and lists sessions, current session first with its badge", async () => {
    vi.mocked(sessionsApi.listSessions).mockResolvedValueOnce(sessions);
    render(<SessionsSettingsPage />);
    await waitFor(() => expect(screen.getAllByTestId("session-row")).toHaveLength(2));
    expect(screen.getByRole("heading", { name: "Sessões" })).toHaveProperty("tagName", "H2");
    expect(screen.getByText("Sessão atual")).toBeInTheDocument();
  });

  it("groups the rows and the revoke-all action in a 'Dispositivos conectados' card", async () => {
    vi.mocked(sessionsApi.listSessions).mockResolvedValueOnce(sessions);
    render(<SessionsSettingsPage />);
    const card = await screen.findByRole("region", { name: "Dispositivos conectados" });
    expect(within(card).getAllByTestId("session-row")).toHaveLength(2);
    expect(within(card).getByRole("button", { name: "Revogar todas as outras" })).toBeVisible();
    expect(within(card).getAllByRole("button", { name: "Revogar sessão" })).toHaveLength(1);
    expect(within(card).getByText(/IP é exibido parcialmente e é aproximado/)).toBeInTheDocument();
  });

  it("offers no revoke-all when the current session is the only one", async () => {
    vi.mocked(sessionsApi.listSessions).mockResolvedValueOnce([sessions[0]]);
    render(<SessionsSettingsPage />);
    await screen.findByText("Sessão atual");
    expect(screen.queryByRole("button", { name: /revogar/i })).not.toBeInTheDocument();
  });

  it("states that revocation affects NChat sessions, not the identity provider", async () => {
    vi.mocked(sessionsApi.listSessions).mockResolvedValueOnce(sessions);
    render(<SessionsSettingsPage />);
    expect(
      await screen.findByText(/não encerra a sessão no provedor de identidade/i),
    ).toBeInTheDocument();
  });

  it("shows a retry-capable error independent of other sections", async () => {
    vi.mocked(sessionsApi.listSessions).mockRejectedValueOnce(
      new sessionsApi.SessionsApiError("unknown", "x"),
    );
    render(<SessionsSettingsPage />);
    await waitFor(() =>
      expect(screen.getByRole("button", { name: /tentar novamente/i })).toBeInTheDocument(),
    );
  });

  it("ignores a stale retry response that finishes after a newer one", async () => {
    let resolveOlder!: (value: sessionsApi.Session[]) => void;
    let resolveNewer!: (value: sessionsApi.Session[]) => void;
    vi.mocked(sessionsApi.listSessions)
      .mockRejectedValueOnce(new Error("initial failure"))
      .mockImplementationOnce(() => new Promise((resolve) => (resolveOlder = resolve)))
      .mockImplementationOnce(() => new Promise((resolve) => (resolveNewer = resolve)));
    render(<SessionsSettingsPage />);
    const retry = await screen.findByRole("button", { name: /tentar novamente/i });

    act(() => {
      fireEvent.click(retry);
      fireEvent.click(retry);
    });
    await act(async () => resolveNewer([sessions[0]]));
    await waitFor(() => expect(screen.getAllByTestId("session-row")).toHaveLength(1));
    await act(async () => resolveOlder(sessions));

    expect(screen.getAllByTestId("session-row")).toHaveLength(1);
    expect(screen.queryByText("Chrome 152")).not.toBeInTheDocument();
  });

  it("keeps session B data when session A resolves after the auth generation changes", async () => {
    let resolveA!: (value: sessionsApi.Session[]) => void;
    let resolveB!: (value: sessionsApi.Session[]) => void;
    let signalA: AbortSignal | undefined;
    vi.mocked(sessionsApi.listSessions)
      .mockImplementationOnce(
        (signal) =>
          new Promise((resolve) => {
            signalA = signal;
            resolveA = resolve;
          }),
      )
      .mockImplementationOnce(
        () =>
          new Promise((resolve) => {
            resolveB = resolve;
          }),
      );

    setTokens("session-a");
    render(<SessionsSettingsPage />);
    await waitFor(() => expect(sessionsApi.listSessions).toHaveBeenCalledTimes(1));

    await act(async () => setTokens("session-b"));
    await waitFor(() => expect(sessionsApi.listSessions).toHaveBeenCalledTimes(2));
    expect(signalA?.aborted).toBe(true);

    await act(async () => resolveB([sessions[0]]));
    await waitFor(() => expect(screen.getAllByTestId("session-row")).toHaveLength(1));

    await act(async () => resolveA(sessions));
    expect(screen.getAllByTestId("session-row")).toHaveLength(1);
    expect(screen.queryByText("Chrome 152")).not.toBeInTheDocument();
  });

  it("revokes a single session through the confirm dialog and relists", async () => {
    vi.mocked(sessionsApi.listSessions)
      .mockResolvedValueOnce(sessions)
      .mockResolvedValueOnce([sessions[0]]);
    vi.mocked(sessionsApi.revokeSession).mockResolvedValueOnce(undefined);
    const user = userEvent.setup();
    render(<SessionsSettingsPage />);
    await waitFor(() => expect(screen.getAllByTestId("session-row")).toHaveLength(2));
    await user.click(screen.getByRole("button", { name: "Revogar sessão" }));
    await user.click(
      within(screen.getByRole("dialog")).getByRole("button", { name: "Revogar sessão" }),
    );
    await waitFor(() => expect(sessionsApi.revokeSession).toHaveBeenCalledWith("other"));
    await waitFor(() => expect(screen.getAllByTestId("session-row")).toHaveLength(1));
  });

  it("does not let a revoke from session A restart loading after session B is active", async () => {
    let resolveRevoke!: () => void;
    let resolveB!: (value: sessionsApi.Session[]) => void;
    vi.mocked(sessionsApi.listSessions)
      .mockResolvedValueOnce(sessions)
      .mockImplementationOnce(
        () =>
          new Promise((resolve) => {
            resolveB = resolve;
          }),
      )
      .mockImplementation(() => new Promise(() => {}));
    vi.mocked(sessionsApi.revokeSession).mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          resolveRevoke = resolve;
        }),
    );

    setTokens("session-a");
    const user = userEvent.setup();
    render(<SessionsSettingsPage />);
    await screen.findByText("Chrome 152");
    await user.click(screen.getByRole("button", { name: "Revogar sessão" }));
    await user.click(
      within(screen.getByRole("dialog")).getByRole("button", { name: "Revogar sessão" }),
    );
    await waitFor(() => expect(sessionsApi.revokeSession).toHaveBeenCalledWith("other"));

    await act(async () => setTokens("session-b"));
    await waitFor(() => expect(sessionsApi.listSessions).toHaveBeenCalledTimes(2));
    await act(async () => resolveB([{ ...sessions[0], userAgent: EDGE_WINDOWS }]));
    await screen.findByText("Microsoft Edge 153");

    await act(async () => resolveRevoke());
    expect(sessionsApi.listSessions).toHaveBeenCalledTimes(2);
    expect(screen.getByText("Microsoft Edge 153")).toBeInTheDocument();
  });

  it("does not submit a revoke-all confirmation opened by an older auth generation", async () => {
    vi.mocked(sessionsApi.listSessions).mockResolvedValue(sessions);
    vi.mocked(sessionsApi.revokeAllOtherSessions).mockResolvedValue(undefined);

    setTokens("session-a");
    const user = userEvent.setup();
    render(<SessionsSettingsPage />);
    await screen.findByText("Chrome 152");
    await user.click(screen.getByRole("button", { name: /revogar todas as outras/i }));
    const staleConfirm = within(screen.getByRole("dialog")).getByRole("button", {
      name: "Revogar sessões",
    });

    act(() => {
      setTokens("session-b");
      fireEvent.click(staleConfirm);
    });

    expect(sessionsApi.revokeAllOtherSessions).not.toHaveBeenCalled();
  });

  it("revokes all others and preserves the current session", async () => {
    vi.mocked(sessionsApi.listSessions)
      .mockResolvedValueOnce(sessions)
      .mockResolvedValueOnce([sessions[0]]);
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
    await user.click(
      within(screen.getByRole("dialog")).getByRole("button", { name: "Revogar sessão" }),
    );
    await waitFor(() => expect(sessionsApi.revokeSession).toHaveBeenCalledWith("other"));
    unmount();
    resolveRevoke();
    await Promise.resolve();
    await Promise.resolve();
    // Only the initial load; the post-revoke reload must not fire once unmounted.
    expect(sessionsApi.listSessions).toHaveBeenCalledTimes(1);
  });
});
