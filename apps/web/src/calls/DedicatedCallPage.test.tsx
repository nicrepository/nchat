import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

import {
  fetchChannelCallParticipantProfiles,
  fetchGroupCallParticipantProfiles,
  fetchSidebarData,
} from "../chat/chatApi";
import { avatarColorFor } from "../chat/messageDisplay";
import { resolveCall } from "../chat/resourceCallSignaling";
import { _resetSelfProfile } from "../profile/selfProfile";
import type { SelfProfile } from "../profile/profileApi";
import DedicatedCallPage from "./DedicatedCallPage";
import { useCallSession } from "./CallSessionProvider";

vi.mock("../chat/chatApi", () => ({
  fetchSidebarData: vi.fn(),
  fetchChannelCallParticipantProfiles: vi.fn(),
  fetchGroupCallParticipantProfiles: vi.fn(),
}));
vi.mock("../chat/resourceCallSignaling", () => ({ resolveCall: vi.fn() }));
vi.mock("./CallSessionProvider", () => ({ useCallSession: vi.fn() }));

// ── Mock profileApi (the local participant's identity source, issue #612) ────

const { mockFetchMyProfile } = vi.hoisted(() => ({
  mockFetchMyProfile: vi.fn<(signal?: AbortSignal) => Promise<SelfProfile>>(),
}));

vi.mock("../profile/profileApi", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../profile/profileApi")>();
  return { ...actual, fetchMyProfile: (signal?: AbortSignal) => mockFetchMyProfile(signal) };
});

const callId = "00000000-0000-4000-8000-000000000546";
const registerDirectory = vi.fn();
const announceDedicated = vi.fn();
const acknowledgeDedicated = vi.fn();
// Mirrors the real hook: resolves with the joined call_id (every real call
// site always supplies target.callId), never rejects.
const join = vi.fn(async (target: { callId?: string }) => target.callId ?? callId);
const takeOver = vi.fn(async () => true);
const beginResourceParticipation = vi.fn();
// Mirrors CallSessionProvider's real activateResourceParticipation: runs
// join(), then registers the participation only once it actually resolves
// with a callId — the same causal path DedicatedCallPage now drives through
// (never joinResourceParticipation: that one is fresh-join-only).
const activateResourceParticipation = vi.fn(async (target: { callId?: string }) => {
  const joinedCallId = await join(target);
  if (joinedCallId) await beginResourceParticipation(joinedCallId);
  return joinedCallId;
});

const session = {
  ownerState: "local",
  registerDirectory,
  announceDedicated,
  acknowledgeDedicated,
  beginResourceParticipation,
  activateResourceParticipation,
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
    hasLocalVideo: true,
    microphoneEnabled: true,
    cameraEnabled: false,
    screenShareEnabled: false,
    pendingControl: null,
    toggleMicrophone: vi.fn(),
    toggleCamera: vi.fn(),
    toggleScreenShare: vi.fn(),
    bindLocalMedia: vi.fn(),
    bindLocalScreenShare: vi.fn(),
    bindRemoteAudio: vi.fn(),
  },
};

function pageTree(id = callId) {
  return (
    <MemoryRouter initialEntries={[`/call/${id}`]}>
      <Routes>
        <Route path="/call/:callId" element={<DedicatedCallPage />} />
        <Route path="/chat" element={<p>Chat</p>} />
      </Routes>
    </MemoryRouter>
  );
}

function renderPage(id = callId) {
  return render(pageTree(id));
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
    _resetSelfProfile();
    mockFetchMyProfile.mockResolvedValue({ id: "current-user", displayName: "" });
    vi.mocked(fetchChannelCallParticipantProfiles).mockResolvedValue([]);
    vi.mocked(fetchGroupCallParticipantProfiles).mockResolvedValue([]);
    session.ownerState = "local";
    session.dedicatedRecoveryFailed = false;
    session.calls.call = null;
    session.media.status = "connected";
    session.media.participants = [];
    session.media.remoteScreenShare = null;
    session.media.screenShareEnabled = false;
    session.media.hasLocalVideo = true;
    vi.mocked(useCallSession).mockReturnValue(session as never);
    vi.mocked(resolveCall).mockResolvedValue(resolvedCall);
    vi.mocked(fetchSidebarData).mockResolvedValue({
      currentUserId: "current-user",
      workspaceId: "workspace-1",
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
      workspaceId: "workspace-1",
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
      workspaceId: "workspace-1",
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

  it("wires local screen share as primary over a simultaneously active remote share (issue #611)", async () => {
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
      workspaceId: "workspace-1",
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
    session.media.screenShareEnabled = true;
    renderPage();

    expect(await screen.findByText("Sua tela")).toBeInTheDocument();
    expect(screen.queryByText("Tela de Ana")).not.toBeInTheDocument();
  });

  it("wires the local fallback avatar's seed to directory.currentUserId, never a fetched profile", async () => {
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
      workspaceId: "workspace-1",
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
    session.media.hasLocalVideo = false;
    renderPage();

    const avatar = await screen.findByText("Você", { selector: "span" });
    const tile = avatar.closest("article")!;
    const localAvatar = tile.querySelector(".dedicated-call__avatar")!;
    expect(localAvatar).toHaveClass(`call-avatar--${avatarColorFor("current-user")}`);
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
      workspaceId: "workspace-1",
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
      workspaceId: "workspace-1",
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
      workspaceId: "workspace-1",
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
    // The screen-share tile renders straight from session.media, but the
    // acknowledgement only fires once the activation effect has recorded this
    // callId — a separate async chain. Awaiting the tile says nothing about
    // that chain, so this has to wait for the acknowledgement itself, the same
    // way every other test here waits on join() before asserting it.
    await waitFor(() => expect(acknowledgeDedicated).toHaveBeenCalledWith(callId, false));
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

  it("shows the local participant's real profile name with (você)", async () => {
    mockFetchMyProfile.mockResolvedValue({ id: "current-user", displayName: "Ana Souza" });
    renderPage();
    await waitFor(() => screen.getByText("Ana Souza (você)"));
  });

  it("resolves each resource participant's own name and avatar in one batch request, not one per tile", async () => {
    session.media.participants = [
      { identity: "user-a", displayName: "Ana Souza", hasVideo: false, bindVideo: vi.fn() },
      { identity: "user-b", displayName: "Bruno Lima", hasVideo: false, bindVideo: vi.fn() },
    ];
    vi.mocked(fetchChannelCallParticipantProfiles).mockResolvedValue([
      { userId: "user-a", displayName: "Ana Souza", avatarUrl: "https://x/a.png" },
    ]);
    renderPage();
    await waitFor(() => expect(fetchChannelCallParticipantProfiles).toHaveBeenCalledTimes(1));
    expect(fetchChannelCallParticipantProfiles).toHaveBeenCalledWith(
      "channel-1",
      expect.arrayContaining(["user-a", "user-b"]),
    );
  });

  it("two distinct participants never share one resolved identity", async () => {
    session.media.participants = [
      { identity: "user-a", displayName: "Ana Souza", hasVideo: false, bindVideo: vi.fn() },
      { identity: "user-b", displayName: "Bruno Lima", hasVideo: false, bindVideo: vi.fn() },
    ];
    vi.mocked(fetchChannelCallParticipantProfiles).mockResolvedValue([
      { userId: "user-a", displayName: "Ana Souza", avatarUrl: "https://x/a.png" },
      { userId: "user-b", displayName: "Bruno Lima", avatarUrl: "https://x/b.png" },
    ]);
    const { container } = renderPage();
    await waitFor(() => {
      const imgs = container.querySelectorAll(".dedicated-call__tile img");
      expect(imgs.length).toBeGreaterThanOrEqual(2);
    });
    const imgs = Array.from(container.querySelectorAll(".dedicated-call__tile img"));
    expect(imgs.map((img) => img.getAttribute("src"))).toEqual(
      expect.arrayContaining(["https://x/a.png", "https://x/b.png"]),
    );
  });

  it("degrades safely when a participant's identity cannot be resolved", async () => {
    session.media.participants = [
      {
        identity: "00000000-0000-4000-8000-000000000999",
        displayName: "Participante",
        hasVideo: false,
        bindVideo: vi.fn(),
      },
    ];
    vi.mocked(fetchChannelCallParticipantProfiles).mockResolvedValue([]);
    const { container } = renderPage();
    await waitFor(() => screen.getByText("Participante"));
    expect(container.querySelector(".dedicated-call__tile")).not.toHaveTextContent(
      /[0-9a-f]{8}-[0-9a-f]{4}/,
    );
  });

  describe("batch-fetch lifecycle (issue #612 blocker review)", () => {
    it("does not re-fetch when the participant array is recreated but the identity set is unchanged", async () => {
      session.media.participants = [
        { identity: "user-a", displayName: "Ana Souza", hasVideo: false, bindVideo: vi.fn() },
      ];
      vi.mocked(fetchChannelCallParticipantProfiles).mockResolvedValue([
        { userId: "user-a", displayName: "Ana Souza", avatarUrl: "https://x/a.png" },
      ]);
      const view = renderPage();
      await waitFor(() => expect(fetchChannelCallParticipantProfiles).toHaveBeenCalledTimes(1));

      // A brand-new ParticipantMedia array/object for the SAME identity —
      // exactly what a hasVideo/hasAudio change produces upstream — must not
      // be read as "the roster changed".
      session.media.participants = [
        { identity: "user-a", displayName: "Ana Souza", hasVideo: true, bindVideo: vi.fn() },
      ];
      view.rerender(pageTree());
      await waitFor(() => screen.getByText("Ana Souza"));
      expect(fetchChannelCallParticipantProfiles).toHaveBeenCalledTimes(1);
    });

    it("does not re-fetch when the SAME roster arrives in a different order (SDK/event reorder)", async () => {
      session.media.participants = [
        { identity: "user-a", displayName: "Ana Souza", hasVideo: false, bindVideo: vi.fn() },
        { identity: "user-b", displayName: "Bruno Lima", hasVideo: false, bindVideo: vi.fn() },
      ];
      vi.mocked(fetchChannelCallParticipantProfiles).mockResolvedValue([
        { userId: "user-a", displayName: "Ana Souza", avatarUrl: "https://x/a.png" },
        { userId: "user-b", displayName: "Bruno Lima", avatarUrl: "https://x/b.png" },
      ]);
      const view = renderPage();
      await waitFor(() => expect(fetchChannelCallParticipantProfiles).toHaveBeenCalledTimes(1));

      // Same two identities, reversed order — no membership change at all.
      session.media.participants = [
        { identity: "user-b", displayName: "Bruno Lima", hasVideo: false, bindVideo: vi.fn() },
        { identity: "user-a", displayName: "Ana Souza", hasVideo: false, bindVideo: vi.fn() },
      ];
      view.rerender(pageTree());
      await waitFor(() => screen.getByText("Bruno Lima"));
      expect(fetchChannelCallParticipantProfiles).toHaveBeenCalledTimes(1);
    });

    it("does not re-fetch on camera, microphone, screen-share, or active-speaker changes alone", async () => {
      session.media.participants = [
        { identity: "user-a", displayName: "Ana Souza", hasVideo: false, bindVideo: vi.fn() },
      ];
      vi.mocked(fetchChannelCallParticipantProfiles).mockResolvedValue([
        { userId: "user-a", displayName: "Ana Souza", avatarUrl: "https://x/a.png" },
      ]);
      const view = renderPage();
      await waitFor(() => expect(fetchChannelCallParticipantProfiles).toHaveBeenCalledTimes(1));

      session.media.hasLocalVideo = !session.media.hasLocalVideo;
      view.rerender(pageTree());
      session.media.microphoneEnabled = !session.media.microphoneEnabled;
      view.rerender(pageTree());
      session.media.cameraEnabled = !session.media.cameraEnabled;
      view.rerender(pageTree());
      session.media.screenShareEnabled = !session.media.screenShareEnabled;
      view.rerender(pageTree());
      (session.media as unknown as Record<string, unknown>).activeSpeakerId = "user-a";
      view.rerender(pageTree());

      await waitFor(() => screen.getByText("Ana Souza"));
      expect(fetchChannelCallParticipantProfiles).toHaveBeenCalledTimes(1);
    });

    it("does not re-fetch when only a participant's displayName changes and the identity set is unchanged", async () => {
      session.media.participants = [
        { identity: "user-a", displayName: "Participante", hasVideo: false, bindVideo: vi.fn() },
      ];
      vi.mocked(fetchChannelCallParticipantProfiles).mockResolvedValue([
        { userId: "user-a", displayName: "Ana Souza", avatarUrl: "https://x/a.png" },
      ]);
      const view = renderPage();
      await waitFor(() => expect(fetchChannelCallParticipantProfiles).toHaveBeenCalledTimes(1));

      session.media.participants = [
        {
          identity: "user-a",
          displayName: "Ana Souza (LiveKit)",
          hasVideo: false,
          bindVideo: vi.fn(),
        },
      ];
      view.rerender(pageTree());
      await waitFor(() => screen.getByText("Ana Souza (LiveKit)"));
      expect(fetchChannelCallParticipantProfiles).toHaveBeenCalledTimes(1);
    });

    it("re-fetches once when the identity set actually changes (a participant joins)", async () => {
      session.media.participants = [
        { identity: "user-a", displayName: "Ana Souza", hasVideo: false, bindVideo: vi.fn() },
      ];
      vi.mocked(fetchChannelCallParticipantProfiles).mockResolvedValue([]);
      const view = renderPage();
      await waitFor(() => expect(fetchChannelCallParticipantProfiles).toHaveBeenCalledTimes(1));

      session.media.participants = [
        { identity: "user-a", displayName: "Ana Souza", hasVideo: false, bindVideo: vi.fn() },
        { identity: "user-b", displayName: "Bruno Lima", hasVideo: false, bindVideo: vi.fn() },
      ];
      view.rerender(pageTree());
      await waitFor(() => expect(fetchChannelCallParticipantProfiles).toHaveBeenCalledTimes(2));
      expect(fetchChannelCallParticipantProfiles).toHaveBeenLastCalledWith(
        "channel-1",
        expect.arrayContaining(["user-a", "user-b"]),
      );
    });

    it("fences a stale in-flight response: a late-resolving old request must not clobber a fresher one", async () => {
      // Request A (for user-a) is deliberately left unresolved while the
      // roster changes to user-c and request B resolves first — the
      // realistic ordering when a caller leaves right after joining.
      let resolveFirst!: (
        profiles: { userId: string; displayName: string; avatarUrl?: string }[],
      ) => void;
      const firstRequest = new Promise<
        { userId: string; displayName: string; avatarUrl?: string }[]
      >((resolve) => {
        resolveFirst = resolve;
      });
      vi.mocked(fetchChannelCallParticipantProfiles).mockReturnValueOnce(firstRequest);
      session.media.participants = [
        { identity: "user-a", displayName: "Ana Souza", hasVideo: false, bindVideo: vi.fn() },
      ];
      const view = renderPage();
      await waitFor(() => expect(fetchChannelCallParticipantProfiles).toHaveBeenCalledTimes(1));

      vi.mocked(fetchChannelCallParticipantProfiles).mockResolvedValueOnce([
        { userId: "user-c", displayName: "Carla Dias", avatarUrl: "https://x/c.png" },
      ]);
      session.media.participants = [
        { identity: "user-c", displayName: "Carla Dias", hasVideo: false, bindVideo: vi.fn() },
      ];
      view.rerender(pageTree());
      await waitFor(() => expect(fetchChannelCallParticipantProfiles).toHaveBeenCalledTimes(2));

      // Request B has already resolved and applied Carla's avatar.
      const { container } = view;
      await waitFor(() => {
        expect(container.querySelector(".dedicated-call__tile img")).toHaveAttribute(
          "src",
          "https://x/c.png",
        );
      });

      // Now the STALE request A finally resolves, naming a user (user-a)
      // who is no longer in the roster at all. An unfenced implementation
      // would call setParticipantProfiles with a map containing only
      // user-a, replacing (not merging into) the current map and wiping out
      // Carla's already-applied avatar.
      resolveFirst([{ userId: "user-a", displayName: "Ana Souza", avatarUrl: "https://x/a.png" }]);
      await new Promise((resolve) => setTimeout(resolve, 0));

      expect(container.querySelector(".dedicated-call__tile img")).toHaveAttribute(
        "src",
        "https://x/c.png",
      );
    });
  });

  describe("batch chunking beyond MaxCallParticipantProfileIDs (issue #612 blocker B)", () => {
    function participantsOf(count: number) {
      return Array.from({ length: count }, (_, i) => ({
        identity: `user-${String(i).padStart(3, "0")}`,
        displayName: `Participante ${i}`,
        hasVideo: false,
        bindVideo: vi.fn(),
      }));
    }

    function profilesFor(ids: string[]) {
      return ids.map((id) => ({ userId: id, displayName: id, avatarUrl: `https://x/${id}.png` }));
    }

    it("50 participants -> exactly one batch request", async () => {
      const participants = participantsOf(50);
      session.media.participants = participants;
      vi.mocked(fetchChannelCallParticipantProfiles).mockImplementation((_, ids) =>
        Promise.resolve(profilesFor(ids)),
      );
      renderPage();
      await waitFor(() => expect(fetchChannelCallParticipantProfiles).toHaveBeenCalledTimes(1));
      expect(vi.mocked(fetchChannelCallParticipantProfiles).mock.calls[0]![1]).toHaveLength(50);
    });

    it("51 participants -> exactly two batch requests, never a single oversized one", async () => {
      const participants = participantsOf(51);
      session.media.participants = participants;
      vi.mocked(fetchChannelCallParticipantProfiles).mockImplementation((_, ids) =>
        Promise.resolve(profilesFor(ids)),
      );
      renderPage();
      await waitFor(() => expect(fetchChannelCallParticipantProfiles).toHaveBeenCalledTimes(2));
      const callSizes = vi
        .mocked(fetchChannelCallParticipantProfiles)
        .mock.calls.map(([, ids]) => ids.length)
        .sort((a, b) => a - b);
      // Two chunks, neither of which is ever the full 51 (never exceeds the
      // server's MaxCallParticipantProfileIDs=50 cap) and together they
      // cover every participant exactly once.
      expect(callSizes).toEqual([1, 50]);
      const allIDsSent = vi
        .mocked(fetchChannelCallParticipantProfiles)
        .mock.calls.flatMap(([, ids]) => ids);
      expect(new Set(allIDsSent).size).toBe(51);
    });

    it("every resolvable participant from both chunks receives its own avatar (no N+1, no cross-contamination)", async () => {
      const participants = participantsOf(51);
      session.media.participants = participants;
      vi.mocked(fetchChannelCallParticipantProfiles).mockImplementation((_, ids) =>
        Promise.resolve(profilesFor(ids)),
      );
      const { container } = renderPage();
      await waitFor(() => expect(fetchChannelCallParticipantProfiles).toHaveBeenCalledTimes(2));
      await waitFor(() => {
        const imgs = container.querySelectorAll(".dedicated-call__tile img");
        expect(imgs.length).toBe(51);
      });
      const srcs = new Set(
        Array.from(container.querySelectorAll(".dedicated-call__tile img")).map((img) =>
          img.getAttribute("src"),
        ),
      );
      expect(srcs.size).toBe(51); // every tile has its OWN avatar, none shared/duplicated
      for (const participant of participants) {
        expect(srcs.has(`https://x/${participant.identity}.png`)).toBe(true);
      }
    });

    it("the same 51 identities in a different order do not trigger a new request", async () => {
      const participants = participantsOf(51);
      session.media.participants = participants;
      vi.mocked(fetchChannelCallParticipantProfiles).mockImplementation((_, ids) =>
        Promise.resolve(profilesFor(ids)),
      );
      const view = renderPage();
      await waitFor(() => expect(fetchChannelCallParticipantProfiles).toHaveBeenCalledTimes(2));

      session.media.participants = [...participants].reverse();
      view.rerender(pageTree());
      await waitFor(() => screen.getByText("Participante 0"));
      expect(fetchChannelCallParticipantProfiles).toHaveBeenCalledTimes(2);
    });

    it("one chunk failing degrades only that chunk to initials, never discards the sibling chunk's results", async () => {
      const participants = participantsOf(51);
      session.media.participants = participants;
      vi.mocked(fetchChannelCallParticipantProfiles).mockImplementation((_, ids) => {
        // The chunk containing user-050 (the 51st, lone id) fails; the
        // 50-sized chunk succeeds.
        if (ids.includes("user-050")) return Promise.reject(new Error("network"));
        return Promise.resolve(profilesFor(ids));
      });
      const { container } = renderPage();
      await waitFor(() => expect(fetchChannelCallParticipantProfiles).toHaveBeenCalledTimes(2));
      await waitFor(() => {
        const imgs = container.querySelectorAll(".dedicated-call__tile img");
        expect(imgs.length).toBe(50); // the failed chunk's 1 participant has no avatar...
      });
      const tiles = Array.from(container.querySelectorAll(".dedicated-call__tile"));
      const failedTile = tiles.find((tile) => tile.textContent?.includes("Participante 50"));
      expect(failedTile?.querySelector("img")).toBeNull(); // ...degrades to initials, not a crash
      expect(failedTile).toHaveTextContent(/P/); // deterministic initials still render
    });

    it("fences a stale delayed response from an old multi-chunk roster: it cannot overwrite the current call's identities", async () => {
      // The old call has 51 participants (2 chunks); one of those chunk
      // requests is deliberately left unresolved.
      let resolveStaleChunk!: (
        profiles: { userId: string; displayName: string; avatarUrl?: string }[],
      ) => void;
      const staleChunk = new Promise<{ userId: string; displayName: string; avatarUrl?: string }[]>(
        (resolve) => {
          resolveStaleChunk = resolve;
        },
      );
      const oldParticipants = participantsOf(51);
      session.media.participants = oldParticipants;
      vi.mocked(fetchChannelCallParticipantProfiles).mockImplementation((_, ids) => {
        if (ids.includes("user-050")) return staleChunk;
        return Promise.resolve(profilesFor(ids));
      });
      const view = renderPage();
      await waitFor(() => expect(fetchChannelCallParticipantProfiles).toHaveBeenCalledTimes(2));

      // Switch to a brand-new, unrelated roster before the stale chunk ever
      // resolves.
      vi.mocked(fetchChannelCallParticipantProfiles).mockImplementation((_, ids) =>
        Promise.resolve(profilesFor(ids)),
      );
      session.media.participants = [
        { identity: "user-c", displayName: "Carla Dias", hasVideo: false, bindVideo: vi.fn() },
      ];
      view.rerender(pageTree());
      const { container } = view;
      await waitFor(() => {
        expect(container.querySelector(".dedicated-call__tile img")).toHaveAttribute(
          "src",
          "https://x/user-c.png",
        );
      });

      // Now the stale chunk from the OLD 51-participant roster resolves.
      // It must not resurrect any of the old roster's tiles or otherwise
      // corrupt the new, single-participant call's state.
      resolveStaleChunk([
        { userId: "user-050", displayName: "Participante 50", avatarUrl: "https://x/stale.png" },
      ]);
      await new Promise((resolve) => setTimeout(resolve, 0));

      expect(
        Array.from(container.querySelectorAll(".dedicated-call__tile")).some((tile) =>
          tile.textContent?.includes("Participante 50"),
        ),
      ).toBe(false);
      expect(container.querySelector(".dedicated-call__tile img")).toHaveAttribute(
        "src",
        "https://x/user-c.png",
      );
    });
  });

  it("does not fetch resource participant identities for a direct call", async () => {
    session.calls.call = { call_id: callId, status: "active" };
    const directResolvedCall = {
      ...resolvedCall,
      target_type: "user" as const,
      target_id: undefined,
      caller_id: "current-user",
      callee_id: "peer-1",
    };
    vi.mocked(resolveCall).mockResolvedValue(directResolvedCall);
    vi.mocked(fetchSidebarData).mockResolvedValue({
      currentUserId: "current-user",
      workspaceId: "workspace-1",
      channels: [],
      dms: [
        {
          id: "dm-1",
          name: "Peer",
          type: "1:1",
          participants: [],
          counterpart: { userId: "peer-1", displayName: "Peer" },
        },
      ],
      categories: [],
    });
    session.media.participants = [];
    renderPage();
    await screen.findByRole("main", { name: "Chamada Peer" });
    expect(fetchChannelCallParticipantProfiles).not.toHaveBeenCalled();
    expect(fetchGroupCallParticipantProfiles).not.toHaveBeenCalled();
  });

  describe("dedicated direct header identity (issue #612 blocker fix)", () => {
    function setUpDirectCall(counterpart: {
      userId: string;
      displayName: string;
      avatarUrl?: string;
    }) {
      session.calls.call = { call_id: callId, status: "active" };
      vi.mocked(resolveCall).mockResolvedValue({
        ...resolvedCall,
        target_type: "user" as const,
        target_id: undefined,
        caller_id: "current-user",
        callee_id: counterpart.userId,
      });
      vi.mocked(fetchSidebarData).mockResolvedValue({
        currentUserId: "current-user",
        workspaceId: "workspace-1",
        channels: [],
        dms: [
          { id: "dm-1", name: counterpart.displayName, type: "1:1", participants: [], counterpart },
        ],
        categories: [],
      });
      session.media.participants = [];
    }

    it("shows the direct peer's real name and avatar in the dedicated header", async () => {
      setUpDirectCall({
        userId: "peer-1",
        displayName: "Ana Souza",
        avatarUrl: "https://x/peer.png",
      });
      const { container } = renderPage();
      await screen.findByRole("main", { name: "Chamada Ana Souza" });

      await waitFor(() => {
        const headerAvatar = container.querySelector(".dedicated-call__header-avatar");
        expect(headerAvatar?.querySelector("img")).toHaveAttribute("src", "https://x/peer.png");
      });
    });

    it("falls back to deterministic initials in the header when the peer has no avatar", async () => {
      setUpDirectCall({ userId: "peer-1", displayName: "Ana Souza" });
      const { container } = renderPage();
      await screen.findByRole("main", { name: "Chamada Ana Souza" });

      const headerAvatar = container.querySelector(".dedicated-call__header-avatar")!;
      expect(headerAvatar.querySelector("img")).not.toBeInTheDocument();
      expect(headerAvatar).toHaveTextContent("AS");
    });

    it("never shows a header avatar for a channel resource call", async () => {
      const { container } = renderPage();
      await screen.findByRole("main", { name: "Chamada Produto" });
      expect(container.querySelector(".dedicated-call__header-avatar")).not.toBeInTheDocument();
    });

    it("does not change existing dedicated resource header/media behavior", async () => {
      renderPage();
      const main = await screen.findByRole("main", { name: "Chamada Produto" });
      expect(main.querySelector(".dedicated-call__header strong")).toHaveTextContent("Produto");
      expect(registerDirectory).toHaveBeenCalledOnce();
    });
  });
});
