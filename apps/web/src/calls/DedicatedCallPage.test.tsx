import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { fetchSidebarData } from "../chat/chatApi";
import { resolveCall } from "../chat/resourceCallSignaling";
import DedicatedCallPage from "./DedicatedCallPage";
import { useCallSession } from "./CallSessionProvider";

vi.mock("../chat/chatApi", () => ({ fetchSidebarData: vi.fn() }));
vi.mock("../chat/resourceCallSignaling", () => ({ resolveCall: vi.fn() }));
vi.mock("./CallSessionProvider", () => ({ useCallSession: vi.fn() }));

const callId = "00000000-0000-4000-8000-000000000546";
const registerDirectory = vi.fn();
const announceDedicated = vi.fn();
const acknowledgeDedicated = vi.fn();
// Mirrors the real hook: resolves with the joined call_id (every real call
// site always supplies target.callId), never rejects.
const join = vi.fn(async () => callId);
const takeOver = vi.fn(async () => true);
const beginResourceParticipation = vi.fn();

const session = {
  ownerState: "local",
  registerDirectory,
  announceDedicated,
  acknowledgeDedicated,
  beginResourceParticipation,
  takeOver,
  releaseDedicated: vi.fn(async () => undefined),
  leaveDedicated: vi.fn(async () => undefined),
  dedicatedRecoveryFailed: false,
  presentation: { mode: "active_dedicated_tab" },
  enableMedia: vi.fn(),
  registerIdentity: vi.fn(),
  expand: vi.fn(),
  calls: { call: null as unknown, activateMedia: vi.fn(), end: vi.fn() },
  resource: { join, leave: vi.fn(async () => undefined) },
  media: {
    status: "connected" as string,
    participants: [] as unknown[],
    remoteScreenShare: null as unknown,
    microphoneEnabled: true,
    cameraEnabled: false,
    screenShareEnabled: false,
    pendingControl: null,
    toggleMicrophone: vi.fn(),
    toggleCamera: vi.fn(),
    toggleScreenShare: vi.fn(),
    bindLocalMedia: vi.fn(),
    bindRemoteAudio: vi.fn(),
  },
};

function renderPage(id = callId) {
  return render(
    <MemoryRouter initialEntries={[`/call/${id}`]}>
      <Routes>
        <Route path="/call/:callId" element={<DedicatedCallPage />} />
        <Route path="/chat" element={<p>Chat</p>} />
      </Routes>
    </MemoryRouter>,
  );
}

const resolvedCall = {
  call_id: callId,
  request_id: "request",
  caller_id: "caller",
  callee_id: "",
  target_type: "channel" as const,
  target_id: "channel-1",
  call_type: "video" as const,
  status: "active" as const,
  version: 1,
  created_at: "2026-08-18T12:00:00Z",
  occurred_at: "2026-08-18T12:00:00Z",
  expires_at: "2026-08-18T13:00:00Z",
};

describe("DedicatedCallPage", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    session.ownerState = "local";
    session.dedicatedRecoveryFailed = false;
    session.calls.call = null;
    session.media.status = "connected";
    session.media.participants = [];
    session.media.remoteScreenShare = null;
    vi.mocked(useCallSession).mockReturnValue(session as never);
    vi.mocked(resolveCall).mockResolvedValue(resolvedCall);
    vi.mocked(fetchSidebarData).mockResolvedValue({
      currentUserId: "current-user",
      channels: [{ id: "channel-1", name: "Produto", type: "public", canWrite: true }],
      dms: [],
      categories: [],
    });
  });

  it("reauthorizes by call id, joins as owner, and ACKs only after media connects", async () => {
    renderPage();

    expect(await screen.findByRole("main", { name: "Chamada Produto" })).toBeInTheDocument();
    expect(resolveCall).toHaveBeenCalledWith(callId);
    expect(registerDirectory).toHaveBeenCalledOnce();
    expect(announceDedicated).toHaveBeenCalledWith(callId);
    await waitFor(() =>
      expect(join).toHaveBeenCalledWith({
        kind: "channel",
        id: "channel-1",
        name: "Produto",
        callId,
      }),
    );
    expect(acknowledgeDedicated).toHaveBeenCalledWith(callId, true);
    // Issue #570 follow-up: registers the new participation only once
    // join() itself resolves with the callId it actually joined — never
    // inferred from resource.callId, which a rejoin of this exact tab's own
    // callId would never observably change.
    await waitFor(() => expect(beginResourceParticipation).toHaveBeenCalledWith(callId));
  });

  it("does not publish while another tab owns media and offers explicit takeover", async () => {
    vi.mocked(useCallSession).mockReturnValue({ ...session, ownerState: "remote" } as never);
    renderPage();

    expect(await screen.findByText("Chamada aberta em outra aba")).toBeInTheDocument();
    expect(join).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "Trazer chamada para esta aba" }));
    expect(takeOver).toHaveBeenCalledOnce();
  });

  it("rejects malformed ids locally and reports authenticated resolution failure", async () => {
    const invalid = renderPage("invalid");
    expect(screen.getByRole("alert")).toHaveTextContent("Chamada inválida");
    expect(resolveCall).not.toHaveBeenCalled();
    invalid.unmount();

    vi.mocked(resolveCall).mockRejectedValueOnce(new Error("denied"));
    renderPage();
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Não foi possível abrir esta chamada",
    );
  });

  it("opens a group DM, reports failed media only after activation, and leaves explicitly", async () => {
    // leaveDedicated resolves immediately (the default mock), so the real
    // window.close() this test doesn't otherwise care about must be
    // neutralized here too — same as every other test in this file whose
    // click can reach that convergence path.
    vi.spyOn(window, "close").mockImplementation(() => undefined);
    vi.mocked(resolveCall).mockResolvedValueOnce({
      ...(await vi.mocked(resolveCall).getMockImplementation()!(callId)),
      target_type: "dm",
      target_id: "dm-group",
    });
    vi.mocked(fetchSidebarData).mockResolvedValueOnce({
      currentUserId: "current-user",
      channels: [],
      dms: [{ id: "dm-group", name: "Equipe", type: "group", participants: [] }],
      categories: [],
    });
    session.media.status = "permission-denied";
    renderPage();

    expect(await screen.findByRole("main", { name: "Chamada Equipe" })).toBeInTheDocument();
    await waitFor(() => expect(join).toHaveBeenCalledWith(expect.objectContaining({ kind: "dm" })));
    expect(acknowledgeDedicated).toHaveBeenCalledWith(callId, false);
    fireEvent.click(screen.getByRole("button", { name: "Encerrar chamada" }));
    // Issue #570: exiting a resource/group call converges through
    // leaveDedicated (participant leave + ownership release + "ended"),
    // never a bare resource.leave() the dedicated tab never follows up on.
    await waitFor(() => expect(session.leaveDedicated).toHaveBeenCalledWith(callId));
    expect(session.resource.leave).not.toHaveBeenCalled();
  });

  it("converges fully on resource/group call exit: leaveDedicated, then window.close, then /chat fallback — never a global call.end (issue #570)", async () => {
    const close = vi.spyOn(window, "close").mockImplementation(() => undefined);
    Object.defineProperty(window, "closed", { configurable: true, value: false });
    let resolveLeave!: () => void;
    session.leaveDedicated.mockImplementationOnce(
      () =>
        new Promise<undefined>((resolve) => {
          resolveLeave = () => resolve(undefined);
        }),
    );
    renderPage();
    await screen.findByRole("main", { name: "Chamada Produto" });

    fireEvent.click(screen.getByRole("button", { name: "Encerrar chamada" }));
    expect(session.leaveDedicated).toHaveBeenCalledWith(callId);
    // window.close() must never be attempted before the participant leave
    // (and ownership release inside leaveDedicated) has actually resolved.
    expect(close).not.toHaveBeenCalled();
    expect(session.calls.end).not.toHaveBeenCalled();

    await act(async () => resolveLeave());
    expect(close).toHaveBeenCalledOnce();
    expect(await screen.findByText("Chat")).toBeInTheDocument();
  });

  it("never closes or navigates away when the participant leave itself fails, leaving the retry state visible (issue #570)", async () => {
    const close = vi.spyOn(window, "close").mockImplementation(() => undefined);
    session.leaveDedicated.mockRejectedValueOnce(new Error("leave failed"));
    renderPage();
    await screen.findByRole("main", { name: "Chamada Produto" });

    fireEvent.click(screen.getByRole("button", { name: "Encerrar chamada" }));
    await waitFor(() => expect(session.leaveDedicated).toHaveBeenCalledWith(callId));
    await act(async () => undefined);

    expect(close).not.toHaveBeenCalled();
    expect(screen.queryByText("Chat")).not.toBeInTheDocument();
    expect(await screen.findByRole("main", { name: "Chamada Produto" })).toBeInTheDocument();
  });

  it("activates an authorized direct call, renders remote screen share, and ends it", async () => {
    const direct = {
      call_id: callId,
      request_id: "request",
      caller_id: "caller",
      callee_id: "current-user",
      target_type: "user" as const,
      call_type: "video" as const,
      status: "active" as const,
      version: 1,
      created_at: "2026-08-18T12:00:00Z",
      occurred_at: "2026-08-18T12:00:00Z",
      expires_at: "2026-08-18T13:00:00Z",
    };
    vi.mocked(resolveCall).mockResolvedValueOnce(direct);
    vi.mocked(fetchSidebarData).mockResolvedValueOnce({
      currentUserId: "current-user",
      channels: [],
      dms: [
        {
          id: "dm-direct",
          name: "Ana",
          type: "1:1",
          participants: [],
          counterpart: { userId: "caller", displayName: "Ana" },
        },
      ],
      categories: [],
    });
    session.calls.call = direct;
    session.media.participants = [
      { identity: "caller", displayName: "Ana", hasVideo: true, bindVideo: vi.fn() },
    ];
    session.media.remoteScreenShare = { identity: "caller", bindMedia: vi.fn() };
    renderPage();

    expect(await screen.findByText("Tela de Ana")).toBeInTheDocument();
    // activateMedia() runs in a passive effect that commits after the DOM
    // update above, not synchronously with it — same reasoning as the
    // join() wait a few tests up for the channel/dm activation branch.
    await waitFor(() => expect(session.calls.activateMedia).toHaveBeenCalledOnce());
    fireEvent.click(screen.getByRole("button", { name: "Encerrar chamada" }));
    expect(session.calls.end).toHaveBeenCalledOnce();
  });

  it("releases (stopping media first) before attempting window.close, then falls back to /chat when it is blocked", async () => {
    const close = vi.spyOn(window, "close").mockImplementation(() => undefined);
    Object.defineProperty(window, "closed", { configurable: true, value: false });
    let releaseResolve!: () => void;
    session.releaseDedicated.mockImplementationOnce(
      () =>
        new Promise<undefined>((resolve) => {
          releaseResolve = () => resolve(undefined);
        }),
    );
    renderPage();
    await screen.findByRole("main", { name: "Chamada Produto" });
    fireEvent.click(screen.getByRole("button", { name: "Minimizar para janela flutuante" }));
    // window.close() must never be attempted before release() (media stop)
    // has actually resolved.
    expect(session.releaseDedicated).toHaveBeenCalledWith(callId);
    expect(close).not.toHaveBeenCalled();
    await act(async () => releaseResolve());
    expect(close).toHaveBeenCalledOnce();
    expect(await screen.findByText("Chat")).toBeInTheDocument();
  });

  it("keeps unresolved channel and group targets in the preparing state", async () => {
    vi.mocked(fetchSidebarData).mockResolvedValueOnce({
      currentUserId: "current-user",
      channels: [],
      dms: [],
      categories: [],
    });
    const missingChannel = renderPage();
    await waitFor(() => expect(resolveCall).toHaveBeenCalled());
    expect(screen.getByRole("status")).toHaveTextContent("Preparando chamada");
    missingChannel.unmount();

    vi.mocked(resolveCall).mockResolvedValueOnce({
      ...resolvedCall,
      target_type: "dm",
      target_id: "missing-dm",
    });
    vi.mocked(fetchSidebarData).mockResolvedValueOnce({
      currentUserId: "current-user",
      channels: [],
      dms: [],
      categories: [],
    });
    renderPage();
    await waitFor(() => expect(resolveCall).toHaveBeenCalledTimes(2));
    expect(screen.getByRole("status")).toHaveTextContent("Preparando chamada");
  });

  it("does not activate a different direct call", async () => {
    const direct = {
      ...resolvedCall,
      target_type: "user" as const,
      target_id: undefined,
      caller_id: "current-user",
      callee_id: "peer",
    };
    vi.mocked(resolveCall).mockResolvedValueOnce(direct);
    vi.mocked(fetchSidebarData).mockResolvedValueOnce({
      currentUserId: "current-user",
      channels: [],
      dms: [],
      categories: [],
    });
    session.calls.call = { ...direct, call_id: "another-call" };
    renderPage();

    expect(await screen.findByRole("main", { name: "Chamada Participante" })).toBeInTheDocument();
    expect(session.calls.activateMedia).not.toHaveBeenCalled();
  });

  it("maps media errors and an unknown screen sharer without inventing identity", async () => {
    session.media.status = "error";
    session.media.remoteScreenShare = { identity: "unknown", bindMedia: vi.fn() };
    renderPage();

    expect(await screen.findByText("Tela de Participante")).toBeInTheDocument();
    expect(acknowledgeDedicated).toHaveBeenCalledWith(callId, false);
  });

  it("ignores asynchronous resolution after unmount", async () => {
    let resolveRequest!: (value: typeof resolvedCall) => void;
    vi.mocked(resolveCall).mockReturnValueOnce(
      new Promise((resolve) => {
        resolveRequest = resolve;
      }),
    );
    const view = renderPage();
    view.unmount();
    resolveRequest(resolvedCall);
    await act(async () => undefined);
    expect(registerDirectory).not.toHaveBeenCalled();
  });

  it("shows reconnecting state and does not navigate when the dedicated window closes", async () => {
    session.media.status = "reconnecting";
    Object.defineProperty(window, "closed", { configurable: true, value: true });
    renderPage();
    expect(await screen.findByText("Reconectando")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Minimizar para janela flutuante" }));
    await waitFor(() => expect(session.releaseDedicated).toHaveBeenCalledWith(callId));
    expect(screen.queryByText("Chat")).not.toBeInTheDocument();
    Object.defineProperty(window, "closed", { configurable: true, value: false });
  });

  it("shows a visible, actionable fallback when dedicated ownership recovery is impossible (achado #1)", async () => {
    session.ownerState = "remote";
    session.dedicatedRecoveryFailed = true;
    renderPage();

    const message = await screen.findByRole("alert");
    expect(message).toHaveTextContent("Não foi possível recuperar esta chamada");
    fireEvent.click(screen.getByRole("button", { name: "Voltar para o chat" }));
    expect(await screen.findByText("Chat")).toBeInTheDocument();
  });
});
