import { StrictMode } from "react";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Outlet, Route, Routes, useNavigate } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { acquireChatSocket, type ChatSocketListener } from "../chat/chatSocket";
import { useCallMedia } from "../chat/useCallMedia";
import { useCallSignaling } from "../chat/useCallSignaling";
import { useResourceCallSession } from "../chat/useResourceCallSession";
import { compareParticipationTokens, createOwnershipCoordinator } from "./callOwnership";
import CallSessionProvider, { useCallSession } from "./CallSessionProvider";

vi.mock("../chat/useCallMedia", () => ({ useCallMedia: vi.fn() }));
vi.mock("../chat/useCallSignaling", () => ({ useCallSignaling: vi.fn() }));
vi.mock("../chat/useResourceCallSession", () => ({ useResourceCallSession: vi.fn() }));
vi.mock("../chat/chatSocket", () => ({
  acquireChatSocket: vi.fn(),
  setConsumerSubscriptions: vi.fn(),
  releaseConsumerSubscriptions: vi.fn(),
}));
vi.mock("./callOwnership", async (importOriginal) => ({
  ...(await importOriginal<typeof import("./callOwnership")>()),
  createOwnershipCoordinator: vi.fn(),
}));

const callId = "00000000-0000-4000-8000-000000000546";
const userA = "00000000-0000-4000-8000-000000000547";
const userB = "00000000-0000-4000-8000-000000000548";
const channelId = "00000000-0000-4000-8000-000000000549";
const lease = {
  v: 1,
  callId,
  tabId: "tab-main",
  epoch: 2,
  role: "main",
  expiresAt: 999999,
} as const;

let ownershipListener: (message: never) => void;
let ownershipLost: () => void;
let socketListener: ChatSocketListener;
// Stands in for the real coordinator's persisted, PER-WRITER-KEY storage
// (issue #570 follow-up): production's nchat.call.participation.v2.
// <callId>.<writerId> scheme, one independent slot per (callId, writerId)
// pair — never a single shared slot a stale writer could regress. Shape:
// callId -> writerId -> generation. Deliberately NOT reset by
// vi.clearAllMocks() — only an explicit `participationStorage = {}` (in
// each describe's own beforeEach) represents "fresh browser storage";
// leaving it untouched within a single test is what lets that test simulate
// a reload, a second freshly-mounted tab, or a genuinely concurrent writer
// still sharing the same persisted floor.
let participationStorage: Record<string, Record<string, number>> = {};
function maxParticipationToken(id: string): { generation: number; writerId: string } | null {
  const writers = participationStorage[id];
  if (!writers) return null;
  let best: { generation: number; writerId: string } | null = null;
  for (const [writerId, generation] of Object.entries(writers)) {
    const candidate = { generation, writerId };
    if (!best || compareParticipationTokens(candidate, best) > 0) best = candidate;
  }
  return best;
}
const ownership = {
  tabId: "tab-main",
  claim: vi.fn<() => Promise<typeof lease | null>>(async () => lease),
  getLease: vi.fn<() => typeof lease | null>(() => null),
  getOwner: vi.fn<() => typeof lease | null>(() => null),
  release: vi.fn(),
  post: vi.fn(),
  subscribe: vi.fn((listener: typeof ownershipListener) => {
    ownershipListener = listener;
    return vi.fn();
  }),
  onOwnershipLost: vi.fn((listener: typeof ownershipLost) => {
    ownershipLost = listener;
    return vi.fn();
  }),
  // Mirrors production: reads the max across every writer's own key, then
  // writes ONLY this tab's own key ("tab-main") — never any other writer's.
  allocateParticipationGeneration: vi.fn((id: string) => {
    const generation = (maxParticipationToken(id)?.generation ?? 0) + 1;
    participationStorage = {
      ...participationStorage,
      [id]: { ...participationStorage[id], "tab-main": generation },
    };
    return { generation, writerId: "tab-main" };
  }),
  getParticipationToken: vi.fn((id: string) => maxParticipationToken(id)),
  close: vi.fn(),
};

const media = {
  status: "connected",
  prepare: vi.fn(async () => undefined),
  startAudio: vi.fn(async () => undefined),
  connect: vi.fn(async () => undefined),
  stop: vi.fn(async () => undefined),
  participants: [] as Array<{
    identity: string;
    displayName: string;
    hasVideo: boolean;
    bindVideo: ReturnType<typeof vi.fn>;
  }>,
  activeSpeakerId: null as string | null,
  remoteScreenShare: null as null | { identity: string; bindMedia: ReturnType<typeof vi.fn> },
  microphoneEnabled: false,
  cameraEnabled: false,
  screenShareEnabled: false,
  pendingControl: null,
  error: null as string | null,
  bindLocalMedia: vi.fn(),
  bindRemoteMedia: vi.fn(),
  bindRemoteAudio: vi.fn(),
  toggleMicrophone: vi.fn(),
  toggleCamera: vi.fn(),
  toggleScreenShare: vi.fn(),
};
const calls = {
  call: null as null | Record<string, unknown>,
  start: vi.fn(),
  accept: vi.fn(),
  decline: vi.fn(),
  end: vi.fn(),
  activateMedia: vi.fn(async () => undefined),
  retryMedia: vi.fn(async () => undefined),
  mediaActivationRequired: false,
  error: null as string | null,
};
const resource = {
  active: null as null | { kind: "channel" | "dm"; id: string; name: string },
  callId: null as string | null,
  status: "idle",
  error: null as string | null,
  // Mirrors the real hook: resolves with the joined call_id (target.callId
  // is always supplied by every real call site), never rejects.
  join: vi.fn(async (target: { callId?: string }) => target.callId),
  leave: vi.fn(async () => undefined),
  reconnect: vi.fn(async () => undefined),
};

function Probe() {
  const navigate = useNavigate();
  const session = useCallSession();
  return (
    <>
      <span data-testid="owner">{session.ownerState}</span>
      <span data-testid="presentation">{session.presentation.mode}</span>
      <span data-testid="dedicated-recovery-failed">{String(session.dedicatedRecoveryFailed)}</span>
      <button type="button" onClick={() => navigate("/profile")}>
        Perfil
      </button>
      <button type="button" onClick={() => navigate("/chat/channel/example")}>
        Canal
      </button>
      <button
        type="button"
        onClick={() =>
          session.registerDirectory({
            currentUserId: userB,
            channels: [{ id: channelId, name: "Produto", type: "public", canWrite: true }],
            dms: [
              {
                id: "dm-1",
                name: "Ana",
                type: "1:1",
                participants: [],
                counterpart: { userId: userA, displayName: "Ana" },
              },
            ],
          })
        }
      >
        Diretório
      </button>
      <button
        type="button"
        onClick={() => session.registerIdentity("ready", async () => undefined)}
      >
        Identidade
      </button>
      <button type="button" onClick={() => session.expand()}>
        Expandir probe
      </button>
      <button type="button" onClick={() => void session.takeOver()}>
        Takeover probe
      </button>
      <button type="button" onClick={() => session.announceDedicated(callId)}>
        Ready probe
      </button>
      <button type="button" onClick={() => session.acknowledgeDedicated(callId, true)}>
        Ack probe
      </button>
      <button type="button" onClick={() => session.acknowledgeDedicated(callId, false)}>
        Fail probe
      </button>
      <button type="button" onClick={() => session.releaseDedicated(callId)}>
        Release probe
      </button>
      <button type="button" onClick={() => void session.leaveDedicated(callId)}>
        Leave dedicated probe
      </button>
      <button type="button" onClick={() => void session.beginResourceParticipation(callId)}>
        Begin participation probe
      </button>
      <Outlet />
    </>
  );
}

function providerTree(path = "/chat") {
  return (
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route
          element={
            <CallSessionProvider>
              <Probe />
            </CallSessionProvider>
          }
        >
          <Route path="/chat" element={<p>Chat</p>} />
          <Route path="/chat/channel/:id" element={<p>Canal</p>} />
          <Route path="/profile" element={<p>Perfil</p>} />
          <Route path="/call/:id" element={<p>Dedicated</p>} />
        </Route>
      </Routes>
    </MemoryRouter>
  );
}

function renderProvider(path = "/chat") {
  return render(providerTree(path));
}

function activeDirect() {
  return {
    call_id: callId,
    request_id: "request",
    caller_id: userA,
    callee_id: userB,
    target_type: "user",
    call_type: "video",
    status: "active",
    version: 1,
    created_at: "2026-08-18T12:00:00Z",
    occurred_at: "2026-08-18T12:00:00Z",
    expires_at: "2026-08-18T13:00:00Z",
  };
}

describe("CallSessionProvider", () => {
  beforeEach(() => {
    vi.useRealTimers();
    vi.clearAllMocks();
    participationStorage = {};
    calls.call = null;
    calls.error = null;
    calls.mediaActivationRequired = false;
    resource.active = null;
    resource.callId = null;
    resource.status = "idle";
    resource.error = null;
    media.status = "connected";
    media.error = null;
    media.participants = [];
    ownership.getLease.mockReturnValue(null);
    ownership.getOwner.mockReturnValue(null);
    ownership.claim.mockResolvedValue(lease);
    vi.mocked(createOwnershipCoordinator).mockReturnValue(ownership as never);
    vi.mocked(useCallMedia).mockReturnValue(media as never);
    vi.mocked(useCallSignaling).mockReturnValue(calls as never);
    vi.mocked(useResourceCallSession).mockReturnValue(resource as never);
    vi.mocked(acquireChatSocket).mockImplementation((listener) => {
      socketListener = listener;
      return { send: vi.fn(), isOpen: vi.fn(), generation: vi.fn(), release: vi.fn() };
    });
  });

  it("keeps one media/signaling controller across authenticated route navigation", () => {
    renderProvider();
    fireEvent.click(screen.getByRole("button", { name: "Perfil" }));
    fireEvent.click(screen.getByRole("button", { name: "Canal" }));
    expect(screen.getByText("Canal", { selector: "p" })).toBeInTheDocument();
    expect(media.stop).not.toHaveBeenCalled();
  });

  it("claims media once, reuses its lease, and releases on connect failure", async () => {
    renderProvider();
    const owned = vi.mocked(useResourceCallSession).mock.calls[0]![0];
    await act(() => owned.connect(activeDirect() as never, "token", "wss://livekit"));
    expect(ownership.claim).toHaveBeenCalledWith(callId, "main");
    expect(screen.getByTestId("owner")).toHaveTextContent("local");

    ownership.getLease.mockReturnValue(lease);
    await act(() => owned.connect(activeDirect() as never, "token", "wss://livekit"));
    expect(ownership.claim).toHaveBeenCalledOnce();

    media.connect.mockRejectedValueOnce(new Error("connect"));
    await expect(owned.connect(activeDirect() as never, "token", "wss://livekit")).rejects.toThrow(
      "connect",
    );
    expect(ownership.release).toHaveBeenCalledWith(callId);
    await waitFor(() => expect(screen.getByTestId("owner")).toHaveTextContent("none"));
  });

  it("refuses media when another tab owns it and cleans tracks through the shared bridge", async () => {
    ownership.claim.mockResolvedValueOnce(null);
    renderProvider();
    const owned = vi.mocked(useResourceCallSession).mock.calls[0]![0];
    await act(async () => {
      await expect(
        owned.connect(activeDirect() as never, "token", "wss://livekit"),
      ).rejects.toThrow("another tab");
    });
    expect(screen.getByTestId("owner")).toHaveTextContent("remote");
    await act(() => owned.stop());
    expect(media.stop).toHaveBeenCalledOnce();
  });

  it("keeps direct media from stopping an active resource room and releases it before direct media", async () => {
    resource.active = { kind: "channel", id: channelId, name: "Produto" };
    renderProvider();
    const direct = vi.mocked(useCallSignaling).mock.calls[0]![0]!;
    const releaseResource = vi.mocked(useCallSignaling).mock.calls[0]![2]!;
    await act(() => direct.stop());
    expect(media.stop).not.toHaveBeenCalled();
    await act(() => releaseResource(activeDirect() as never));
    expect(resource.leave).toHaveBeenCalledOnce();
  });

  it("shows direct incoming controls, identity retry, and a persistent floating call", async () => {
    calls.call = { ...activeDirect(), status: "ringing" };
    const view = renderProvider();
    fireEvent.click(screen.getByRole("button", { name: "Diretório" }));
    fireEvent.click(screen.getByRole("button", { name: "Identidade" }));
    expect(await screen.findByRole("dialog", { name: "Chamada recebida" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Atender com câmera" }));
    expect(calls.accept).toHaveBeenCalledOnce();
    fireEvent.click(screen.getByRole("button", { name: "Recusar" }));
    expect(calls.decline).toHaveBeenCalledOnce();

    calls.call = activeDirect();
    view.rerender(
      <MemoryRouter initialEntries={["/chat"]}>
        <CallSessionProvider>
          <Probe />
        </CallSessionProvider>
      </MemoryRouter>,
    );
    expect(await screen.findByTestId("floating-call-window")).toBeInTheDocument();
  });

  it("handles resource incoming accept, reject, duplicate, and terminal cleanup", async () => {
    renderProvider();
    fireEvent.click(screen.getByRole("button", { name: "Diretório" }));
    const incoming = {
      ...activeDirect(),
      target_type: "channel",
      target_id: channelId,
      status: "active",
    };
    act(() =>
      socketListener.onMessage?.(
        {
          type: "call.accepted",
          event_id: "event",
          target_type: "channel",
          target_id: channelId,
          call: incoming,
        },
        1,
      ),
    );
    fireEvent.click(await screen.findByRole("button", { name: "Atender com câmera" }));
    expect(resource.join).toHaveBeenCalledWith(expect.objectContaining({ callId }));

    act(() =>
      socketListener.onMessage?.(
        {
          type: "call.accepted",
          event_id: "event-2",
          target_type: "channel",
          target_id: channelId,
          call: { ...incoming, call_id: `${callId.slice(0, -1)}7` },
        },
        1,
      ),
    );
    fireEvent.click(await screen.findByRole("button", { name: "Recusar" }));
    expect(screen.queryByRole("dialog", { name: "Chamada recebida" })).not.toBeInTheDocument();
  });

  it("performs main-to-dedicated handoff, ACK, timeout rollback, and failure rollback", async () => {
    vi.useFakeTimers();
    calls.call = activeDirect();
    ownership.getLease.mockReturnValue(lease);
    renderProvider();
    act(() =>
      ownershipListener({ v: 1, type: "ready", callId, tabId: "tab-dedicated", epoch: 2 } as never),
    );
    await act(async () => undefined);
    expect(ownership.post).toHaveBeenCalledWith(expect.objectContaining({ type: "handoff" }));
    act(() =>
      ownershipListener({ v: 1, type: "ack", callId, tabId: "tab-dedicated", epoch: 3 } as never),
    );
    expect(screen.getByTestId("presentation")).toHaveTextContent("active_dedicated_tab");

    act(() =>
      ownershipListener({ v: 1, type: "ready", callId, tabId: "tab-dedicated", epoch: 3 } as never),
    );
    await act(async () => vi.advanceTimersByTime(6000));
    expect(ownership.claim).toHaveBeenCalledWith(callId, "main", 2);
    act(() =>
      ownershipListener({
        v: 1,
        type: "failure",
        callId,
        tabId: "tab-dedicated",
        epoch: 3,
      } as never),
    );
    vi.useRealTimers();
  });

  it("claims a targeted handoff only in a dedicated tab and reports claim failure", async () => {
    // A healthy owner exists, so announceDedicated waits for a real handoff
    // reply instead of claiming immediately (achado #1's recovery path).
    ownership.getOwner.mockReturnValue(lease);
    renderProvider(`/call/${callId}`);
    fireEvent.click(screen.getByRole("button", { name: "Ready probe" }));
    act(() =>
      ownershipListener({
        v: 1,
        type: "handoff",
        callId,
        tabId: "tab-main",
        targetTabId: "other-tab",
        epoch: 2,
      } as never),
    );
    expect(ownership.claim).not.toHaveBeenCalled();
    act(() =>
      ownershipListener({
        v: 1,
        type: "handoff",
        callId,
        tabId: "tab-main",
        targetTabId: ownership.tabId,
        epoch: 2,
      } as never),
    );
    await waitFor(() => expect(ownership.claim).toHaveBeenCalledWith(callId, "dedicated", 2));
    ownership.claim.mockResolvedValueOnce(null);
    act(() =>
      ownershipListener({
        v: 1,
        type: "handoff",
        callId,
        tabId: "tab-main",
        targetTabId: ownership.tabId,
        epoch: 3,
      } as never),
    );
    await waitFor(() =>
      expect(ownership.post).toHaveBeenCalledWith(expect.objectContaining({ type: "failure" })),
    );
  });

  it("supports explicit coordinator actions and ownership-loss cleanup", async () => {
    calls.call = activeDirect();
    ownership.getOwner.mockReturnValue(lease);
    renderProvider();
    fireEvent.click(screen.getByRole("button", { name: "Ready probe" }));
    fireEvent.click(screen.getByRole("button", { name: "Ack probe" }));
    fireEvent.click(screen.getByRole("button", { name: "Fail probe" }));
    fireEvent.click(screen.getByRole("button", { name: "Release probe" }));
    expect(ownership.post).toHaveBeenCalledWith(
      expect.objectContaining({ type: "ready", epoch: 2 }),
    );
    expect(ownership.release).toHaveBeenCalledWith(callId);
    act(() => ownershipLost());
    await waitFor(() => expect(media.stop).toHaveBeenCalled());
  });

  it("covers action guards, floating controls, reconnect lifecycle, and terminal cleanup", async () => {
    const opened = vi.spyOn(window, "open").mockReturnValue(null);
    const view = renderProvider();
    fireEvent.click(screen.getByRole("button", { name: "Expandir probe" }));
    fireEvent.click(screen.getByRole("button", { name: "Takeover probe" }));
    expect(ownership.claim).not.toHaveBeenCalled();

    calls.call = activeDirect();
    calls.mediaActivationRequired = true;
    calls.error = "Falha de mídia";
    media.participants = [
      { identity: userA, displayName: "Ana", hasVideo: true, bindVideo: vi.fn() },
    ];
    media.activeSpeakerId = userB;
    media.remoteScreenShare = { identity: userA, bindMedia: vi.fn() };
    view.rerender(providerTree());
    const owned = vi.mocked(useResourceCallSession).mock.calls.at(-1)![0];
    await act(() => owned.connect(activeDirect() as never, "token", "wss://livekit"));
    await screen.findByTestId("floating-call-window");

    fireEvent.click(screen.getByRole("button", { name: "Expandir probe" }));
    opened.mockReturnValue({} as Window);
    fireEvent.click(screen.getByRole("button", { name: "Expandir em nova aba" }));
    expect(opened).toHaveBeenCalledWith(`/call/${callId}`, "_blank", "noopener");
    fireEvent.click(screen.getByRole("button", { name: "Permitir câmera e microfone" }));
    fireEvent.click(screen.getByRole("button", { name: "Tentar mídia novamente" }));
    fireEvent.click(screen.getByRole("button", { name: "Encerrar chamada" }));
    expect(calls.activateMedia).toHaveBeenCalled();
    expect(calls.retryMedia).toHaveBeenCalled();
    expect(calls.end).toHaveBeenCalled();

    media.status = "reconnecting";
    view.rerender(providerTree());
    await waitFor(() =>
      expect(screen.getByTestId("presentation")).toHaveTextContent("reconnecting"),
    );
    media.status = "connected";
    view.rerender(providerTree());
    calls.call = { ...activeDirect(), status: "ended" };
    view.rerender(providerTree());
    await waitFor(() => expect(screen.getByTestId("presentation")).toHaveTextContent("ended"));
  });

  it("does not treat a popup-blocked window.open as a loss of ownership", async () => {
    const opened = vi.spyOn(window, "open").mockReturnValue(null);
    const view = renderProvider();
    calls.call = activeDirect();
    view.rerender(providerTree());
    const owned = vi.mocked(useResourceCallSession).mock.calls.at(-1)![0];
    await act(() => owned.connect(activeDirect() as never, "token", "wss://livekit"));
    await screen.findByTestId("floating-call-window");
    expect(screen.getByTestId("owner")).toHaveTextContent("local");

    fireEvent.click(screen.getByRole("button", { name: "Expandir em nova aba" }));

    expect(opened).toHaveBeenCalledWith(`/call/${callId}`, "_blank", "noopener");
    // A blocked/opaque popup must never look like ownership was lost: no
    // release, no owner-state change — only the ready/handoff/ack protocol
    // (exercised in the recovery tests below) ever does that.
    expect(ownership.release).not.toHaveBeenCalled();
    expect(screen.getByTestId("owner")).toHaveTextContent("local");
  });

  it("recovers resource ownership, including activation and failed-claim paths", async () => {
    vi.useFakeTimers();
    resource.active = { kind: "channel", id: channelId, name: "Produto" };
    resource.callId = callId;
    resource.status = "connecting";
    ownership.getLease.mockReturnValue(lease);
    renderProvider();
    act(() =>
      ownershipListener({ v: 1, type: "ready", callId, tabId: "tab-dedicated", epoch: 2 } as never),
    );
    await act(async () => undefined);
    ownership.getOwner.mockReturnValue(lease);
    await act(async () => vi.advanceTimersByTime(1500));
    ownership.getOwner.mockReturnValue(null);
    await act(async () => vi.advanceTimersByTime(1500));
    expect(ownership.claim).toHaveBeenCalledWith(callId, "main", 2);
    expect(resource.reconnect).toHaveBeenCalled();

    ownership.claim.mockResolvedValueOnce(null);
    fireEvent.click(screen.getByRole("button", { name: "Takeover probe" }));
    await act(async () => undefined);
    vi.useRealTimers();
  });

  it("filters global resource events and clears a matching terminal popup", async () => {
    renderProvider();
    fireEvent.click(screen.getByRole("button", { name: "Diretório" }));
    fireEvent.click(screen.getByRole("button", { name: "Diretório" }));
    fireEvent.click(screen.getByRole("button", { name: "Identidade" }));
    fireEvent.click(screen.getByRole("button", { name: "Identidade" }));
    act(() => socketListener.onMessage?.({}, 1));
    act(() =>
      socketListener.onMessage?.(
        {
          type: "call.ringing",
          event_id: "direct",
          target_type: "user",
          target_id: userB,
          call: { ...activeDirect(), status: "ringing" },
        },
        1,
      ),
    );
    const incoming = { ...activeDirect(), target_type: "dm", target_id: "dm-1", status: "active" };
    act(() =>
      socketListener.onMessage?.(
        {
          type: "call.accepted",
          event_id: "resource",
          target_type: "dm",
          target_id: "dm-1",
          call: incoming,
        },
        1,
      ),
    );
    expect(await screen.findByRole("dialog", { name: "Chamada recebida" })).toBeInTheDocument();
    act(() =>
      socketListener.onMessage?.(
        {
          type: "call.ended",
          event_id: "ended",
          target_type: "dm",
          target_id: "dm-1",
          call: { ...incoming, status: "ended", version: 2 },
        },
        1,
      ),
    );
    expect(screen.queryByRole("dialog", { name: "Chamada recebida" })).not.toBeInTheDocument();
  });

  it("recovers direct media through retry, activation, failure, and exception paths", async () => {
    calls.call = activeDirect();
    renderProvider();

    fireEvent.click(screen.getByRole("button", { name: "Takeover probe" }));
    await waitFor(() => expect(calls.retryMedia).toHaveBeenCalledOnce());

    calls.mediaActivationRequired = true;
    fireEvent.click(screen.getByRole("button", { name: "Takeover probe" }));
    await waitFor(() => expect(calls.activateMedia).toHaveBeenCalledOnce());

    media.status = "error";
    fireEvent.click(screen.getByRole("button", { name: "Takeover probe" }));
    await waitFor(() => expect(screen.getByTestId("presentation")).toHaveTextContent("failed"));

    media.status = "connected";
    calls.mediaActivationRequired = false;
    calls.retryMedia.mockRejectedValueOnce(new Error("retry"));
    fireEvent.click(screen.getByRole("button", { name: "Takeover probe" }));
    await waitFor(() => expect(calls.retryMedia).toHaveBeenCalledTimes(2));
  });

  it("ignores unrelated ready messages and stale ack/failure with no attempt in flight (achado #3)", async () => {
    calls.call = activeDirect();
    renderProvider();
    act(() =>
      ownershipListener({
        v: 1,
        type: "ready",
        callId: "another-call",
        tabId: "tab-dedicated",
        epoch: 2,
      } as never),
    );
    act(() =>
      ownershipListener({ v: 1, type: "ready", callId, tabId: "tab-dedicated", epoch: 2 } as never),
    );
    expect(media.stop).not.toHaveBeenCalled();

    // No handoff attempt is in flight (getLease() is null), so these
    // ack/failure messages belong to no current attempt and must be
    // ignored rather than triggering a claim/recovery.
    act(() =>
      ownershipListener({ v: 1, type: "ack", callId, tabId: "tab-dedicated", epoch: 2 } as never),
    );
    act(() =>
      ownershipListener({
        v: 1,
        type: "failure",
        callId,
        tabId: "tab-dedicated",
        epoch: 2,
      } as never),
    );
    expect(ownership.claim).not.toHaveBeenCalled();
  });

  it("renders resource failure controls and performs resource-only retry and end", async () => {
    resource.active = { kind: "channel", id: channelId, name: "Produto" };
    resource.callId = callId;
    resource.status = "connecting";
    resource.error = "Falha no canal";
    media.status = "permission-denied";
    renderProvider();

    expect(await screen.findByTestId("resource-call-panel")).toBeInTheDocument();
    expect(screen.getByRole("status")).toHaveTextContent("Falha");
    fireEvent.click(screen.getByRole("button", { name: "Tentar mídia novamente" }));
    fireEvent.click(screen.getByRole("button", { name: "Encerrar chamada" }));
    await waitFor(() => expect(resource.reconnect).toHaveBeenCalled());
    expect(resource.leave).toHaveBeenCalled();
    // Issue #570: the main tab's own resource "leave" must announce the
    // same leaving/left participation signal a dedicated tab's leave does,
    // so a stale dedicated tab (if one exists) never reconnects this call.
    await waitFor(() =>
      expect(ownership.post).toHaveBeenCalledWith(
        expect.objectContaining({ type: "leaving", callId }),
      ),
    );
    await waitFor(() =>
      expect(ownership.post).toHaveBeenCalledWith(
        expect.objectContaining({ type: "left", callId }),
      ),
    );
  });

  it("uses the callee peer and empty participant fallback in a dedicated route", async () => {
    calls.call = { ...activeDirect(), caller_id: userB, callee_id: userA };
    media.participants = undefined as never;
    const view = renderProvider(`/call/${callId}`);
    fireEvent.click(screen.getByRole("button", { name: "Diretório" }));
    const owned = vi.mocked(useResourceCallSession).mock.calls.at(-1)![0];
    await act(() => owned.connect(calls.call as never, "token", "wss://livekit"));
    view.rerender(providerTree(`/call/${callId}`));
    await waitFor(() =>
      expect(screen.getByTestId("presentation")).toHaveTextContent("active_dedicated_tab"),
    );
  });

  it("shows start failures globally and labels restored audio activation accurately", () => {
    calls.error = "Permissão negada";
    const view = renderProvider();
    expect(screen.getByRole("alert")).toHaveTextContent("Permissão negada");

    calls.call = { ...activeDirect(), call_type: "audio" };
    calls.mediaActivationRequired = true;
    view.rerender(providerTree());
    expect(screen.getByRole("button", { name: "Permitir microfone" })).toBeInTheDocument();
    expect(document.querySelector(".call-global-error")).toBeNull();
  });
});

describe("dedicated tab reload/reopen recovery (achado #1)", () => {
  beforeEach(() => {
    vi.useRealTimers();
    vi.clearAllMocks();
    participationStorage = {};
    calls.call = null;
    resource.active = null;
    resource.callId = null;
    ownership.getLease.mockReturnValue(null);
    ownership.getOwner.mockReturnValue(null);
    ownership.claim.mockResolvedValue(lease);
    vi.mocked(createOwnershipCoordinator).mockReturnValue(ownership as never);
    vi.mocked(useCallMedia).mockReturnValue(media as never);
    vi.mocked(useCallSignaling).mockReturnValue(calls as never);
    vi.mocked(useResourceCallSession).mockReturnValue(resource as never);
    vi.mocked(acquireChatSocket).mockImplementation((listener) => {
      socketListener = listener;
      return { send: vi.fn(), isOpen: vi.fn(), generation: vi.fn(), release: vi.fn() };
    });
  });

  it("dedicated aberta diretamente: claims immediately when there is no owner at all", async () => {
    renderProvider(`/call/${callId}`);
    fireEvent.click(screen.getByRole("button", { name: "Ready probe" }));
    await waitFor(() => expect(ownership.claim).toHaveBeenCalledWith(callId, "dedicated", 0));
    await waitFor(() => expect(screen.getByTestId("owner")).toHaveTextContent("local"));
  });

  it("owner inexistente: never depends on another tab replying", async () => {
    renderProvider(`/call/${callId}`);
    fireEvent.click(screen.getByRole("button", { name: "Ready probe" }));
    await waitFor(() => expect(ownership.claim).toHaveBeenCalledTimes(1));
    expect(ownership.claim).toHaveBeenCalledWith(callId, "dedicated", 0);
  });

  it("timeout: waits the full bounded window before claiming when a lease is present", async () => {
    vi.useFakeTimers();
    ownership.getOwner.mockReturnValue(lease);
    renderProvider(`/call/${callId}`);
    fireEvent.click(screen.getByRole("button", { name: "Ready probe" }));
    await act(async () => vi.advanceTimersByTime(4_999));
    expect(ownership.claim).not.toHaveBeenCalled();
    await act(async () => vi.advanceTimersByTime(1));
    expect(ownership.claim).toHaveBeenCalledWith(callId, "dedicated", lease.epoch);
  });

  it("reload da dedicated owner: a stale lease from this tab's earlier life is reclaimed", async () => {
    vi.useFakeTimers();
    ownership.getOwner.mockReturnValue(lease);
    renderProvider(`/call/${callId}`);
    fireEvent.click(screen.getByRole("button", { name: "Ready probe" }));
    await act(async () => vi.advanceTimersByTime(5_000));
    expect(ownership.claim).toHaveBeenCalledWith(callId, "dedicated", lease.epoch);
    expect(screen.getByTestId("owner")).toHaveTextContent("local");
  });

  it("ready sem resposta: no handoff message ever arrives, recovery still completes", async () => {
    vi.useFakeTimers();
    ownership.getOwner.mockReturnValue(lease);
    renderProvider(`/call/${callId}`);
    fireEvent.click(screen.getByRole("button", { name: "Ready probe" }));
    await act(async () => vi.advanceTimersByTime(2_000));
    expect(ownership.claim).not.toHaveBeenCalled();
    await act(async () => vi.advanceTimersByTime(3_000));
    expect(ownership.claim).toHaveBeenCalledWith(callId, "dedicated", lease.epoch);
  });

  it("owner saudável existente: a real handoff reply wins and cancels the recovery timer", async () => {
    vi.useFakeTimers();
    ownership.getOwner.mockReturnValue(lease);
    renderProvider(`/call/${callId}`);
    fireEvent.click(screen.getByRole("button", { name: "Ready probe" }));
    act(() =>
      ownershipListener({
        v: 1,
        type: "handoff",
        callId,
        tabId: "tab-main",
        targetTabId: ownership.tabId,
        epoch: lease.epoch,
      } as never),
    );
    await act(async () => undefined);
    expect(ownership.claim).toHaveBeenCalledWith(callId, "dedicated", lease.epoch);
    expect(ownership.claim).toHaveBeenCalledTimes(1);
    // The recovery timer must have been cancelled: advancing past its
    // window never produces a second, redundant claim.
    await act(async () => vi.advanceTimersByTime(5_000));
    expect(ownership.claim).toHaveBeenCalledTimes(1);
  });

  it("recovery bem-sucedido: sets local ownership and never surfaces a recovery failure", async () => {
    vi.useFakeTimers();
    ownership.getOwner.mockReturnValue(lease);
    renderProvider(`/call/${callId}`);
    fireEvent.click(screen.getByRole("button", { name: "Ready probe" }));
    await act(async () => vi.advanceTimersByTime(5_000));
    expect(screen.getByTestId("owner")).toHaveTextContent("local");
    expect(screen.getByTestId("dedicated-recovery-failed")).toHaveTextContent("false");
  });

  it("recovery impossível: surfaces a visible, terminal fallback instead of spinning forever", async () => {
    vi.useFakeTimers();
    ownership.getOwner.mockReturnValue(lease);
    ownership.claim.mockResolvedValueOnce(null);
    renderProvider(`/call/${callId}`);
    fireEvent.click(screen.getByRole("button", { name: "Ready probe" }));
    await act(async () => vi.advanceTimersByTime(5_000));
    expect(screen.getByTestId("dedicated-recovery-failed")).toHaveTextContent("true");
    expect(screen.getByTestId("owner")).not.toHaveTextContent("local");
  });

  it("cleans up the recovery timer on unmount instead of firing a late claim", async () => {
    vi.useFakeTimers();
    ownership.getOwner.mockReturnValue(lease);
    const view = renderProvider(`/call/${callId}`);
    fireEvent.click(screen.getByRole("button", { name: "Ready probe" }));
    view.unmount();
    await act(async () => vi.advanceTimersByTime(5_000));
    expect(ownership.claim).not.toHaveBeenCalled();
  });
});

describe("stale handoff replies never move ownership after HANDOFF_TIMEOUT (achado #3)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    participationStorage = {};
    calls.call = activeDirect();
    resource.active = null;
    resource.callId = null;
    ownership.getLease.mockReturnValue(lease);
    ownership.getOwner.mockReturnValue(null);
    ownership.claim.mockResolvedValue(lease);
    vi.mocked(createOwnershipCoordinator).mockReturnValue(ownership as never);
    vi.mocked(useCallMedia).mockReturnValue(media as never);
    vi.mocked(useCallSignaling).mockReturnValue(calls as never);
    vi.mocked(useResourceCallSession).mockReturnValue(resource as never);
    vi.mocked(acquireChatSocket).mockImplementation((listener) => {
      socketListener = listener;
      return { send: vi.fn(), isOpen: vi.fn(), generation: vi.fn(), release: vi.fn() };
    });
  });

  function startHandoff() {
    act(() =>
      ownershipListener({ v: 1, type: "ready", callId, tabId: "tab-dedicated", epoch: 2 } as never),
    );
  }

  it("an ACK arriving immediately after the timeout is ignored: recovery already started", async () => {
    vi.useFakeTimers();
    renderProvider();
    startHandoff();
    await act(async () => undefined);
    await act(async () => vi.advanceTimersByTime(6_000));
    expect(ownership.claim).toHaveBeenCalledWith(callId, "main", 2);
    const claimsBeforeAck = ownership.claim.mock.calls.length;

    // The dedicated tab's epoch for this (now-abandoned) attempt.
    act(() =>
      ownershipListener({ v: 1, type: "ack", callId, tabId: "tab-dedicated", epoch: 3 } as never),
    );
    expect(ownership.claim).toHaveBeenCalledTimes(claimsBeforeAck);
    expect(screen.getByTestId("presentation")).not.toHaveTextContent("active_dedicated_tab");
    vi.useRealTimers();
  });

  it("a very late ACK (long after timeout/recovery) is still ignored", async () => {
    vi.useFakeTimers();
    renderProvider();
    startHandoff();
    await act(async () => undefined);
    await act(async () => vi.advanceTimersByTime(6_000));
    await act(async () => vi.advanceTimersByTime(30_000));
    const claimsBeforeAck = ownership.claim.mock.calls.length;

    act(() =>
      ownershipListener({ v: 1, type: "ack", callId, tabId: "tab-dedicated", epoch: 3 } as never),
    );
    expect(ownership.claim).toHaveBeenCalledTimes(claimsBeforeAck);
    vi.useRealTimers();
  });

  it("a late failure after timeout/recovery never re-triggers recovery", async () => {
    vi.useFakeTimers();
    renderProvider();
    startHandoff();
    await act(async () => undefined);
    await act(async () => vi.advanceTimersByTime(6_000));
    const claimsBeforeFailure = ownership.claim.mock.calls.length;

    act(() =>
      ownershipListener({
        v: 1,
        type: "failure",
        callId,
        tabId: "tab-dedicated",
        epoch: 3,
      } as never),
    );
    expect(ownership.claim).toHaveBeenCalledTimes(claimsBeforeFailure);
  });

  it("recovery starting before the ACK arrives still converges to a single owner", async () => {
    vi.useFakeTimers();
    renderProvider();
    startHandoff();
    await act(async () => undefined);
    // Recovery (timeout-triggered restoreOwnership) begins first...
    await act(async () => vi.advanceTimersByTime(6_000));
    expect(ownership.claim).toHaveBeenCalledWith(callId, "main", 2);
    // ...then the ACK for the original attempt finally shows up. It must
    // not flip presentation back to "active_dedicated_tab" once the main
    // tab has already moved into recovery for this call.
    act(() =>
      ownershipListener({ v: 1, type: "ack", callId, tabId: "tab-dedicated", epoch: 3 } as never),
    );
    expect(screen.getByTestId("presentation")).not.toHaveTextContent("active_dedicated_tab");
    vi.useRealTimers();
  });

  it("a fresh claim after recovery always uses a higher epoch than the abandoned attempt", async () => {
    vi.useFakeTimers();
    renderProvider();
    startHandoff();
    await act(async () => undefined);
    await act(async () => vi.advanceTimersByTime(6_000));
    expect(ownership.claim).toHaveBeenCalledWith(callId, "main", 2);
    vi.useRealTimers();
  });
});

describe("releaseDedicated stops media before releasing ownership (achado #4)", () => {
  beforeEach(() => {
    vi.useRealTimers();
    vi.clearAllMocks();
    participationStorage = {};
    calls.call = null;
    resource.active = null;
    resource.callId = null;
    ownership.getLease.mockReturnValue(null);
    ownership.getOwner.mockReturnValue(null);
    ownership.claim.mockResolvedValue(lease);
    vi.mocked(createOwnershipCoordinator).mockReturnValue(ownership as never);
    vi.mocked(useCallMedia).mockReturnValue(media as never);
    vi.mocked(useCallSignaling).mockReturnValue(calls as never);
    vi.mocked(useResourceCallSession).mockReturnValue(resource as never);
    vi.mocked(acquireChatSocket).mockImplementation((listener) => {
      socketListener = listener;
      return { send: vi.fn(), isOpen: vi.fn(), generation: vi.fn(), release: vi.fn() };
    });
  });

  it("awaits media.stop() before ownership.release(), leaving no residual media in this tab", async () => {
    const order: string[] = [];
    media.stop.mockImplementationOnce(async () => {
      order.push("stop");
    });
    ownership.release.mockImplementationOnce(() => {
      order.push("release");
    });
    renderProvider(`/call/${callId}`);
    fireEvent.click(screen.getByRole("button", { name: "Release probe" }));
    await waitFor(() => expect(ownership.release).toHaveBeenCalledWith(callId));
    expect(order).toEqual(["stop", "release"]);
    await waitFor(() => expect(screen.getByTestId("owner")).toHaveTextContent("remote"));
  });

  it("is safe to call twice (idempotent cleanup)", async () => {
    renderProvider(`/call/${callId}`);
    fireEvent.click(screen.getByRole("button", { name: "Release probe" }));
    fireEvent.click(screen.getByRole("button", { name: "Release probe" }));
    await waitFor(() => expect(ownership.release).toHaveBeenCalled());
    expect(media.stop).toHaveBeenCalled();
  });

  it("lets the main tab recover ownership normally once the dedicated tab releases", async () => {
    resource.active = { kind: "channel", id: channelId, name: "Produto" };
    resource.callId = callId;
    resource.status = "connecting";
    ownership.getLease.mockReturnValue(lease);
    vi.useFakeTimers();
    renderProvider();
    act(() =>
      ownershipListener({ v: 1, type: "ready", callId, tabId: "tab-dedicated", epoch: 2 } as never),
    );
    await act(async () => undefined);
    // Simulate the dedicated tab releasing the lease: the main tab's
    // recovery interval must reclaim ownership on its own next tick.
    ownership.getOwner.mockReturnValue(null);
    await act(async () => vi.advanceTimersByTime(1_500));
    expect(ownership.claim).toHaveBeenCalledWith(callId, "main", 2);
    expect(resource.reconnect).toHaveBeenCalled();
  });
});

// Issue #570: a dedicated tab's own "sair" must converge — real participant
// leave (#569), release ownership, and tell every other tab this
// participation is over — never just a minimize-style release, and never a
// resource-call call.end.
describe("dedicated participant leave converges ownership and main tab state (issue #570)", () => {
  beforeEach(() => {
    vi.useRealTimers();
    vi.clearAllMocks();
    participationStorage = {};
    calls.call = null;
    resource.active = { kind: "channel", id: channelId, name: "Produto" };
    resource.callId = callId;
    resource.status = "active";
    ownership.getLease.mockReturnValue(lease);
    ownership.getOwner.mockReturnValue(null);
    ownership.claim.mockResolvedValue(lease);
    vi.mocked(createOwnershipCoordinator).mockReturnValue(ownership as never);
    vi.mocked(useCallMedia).mockReturnValue(media as never);
    vi.mocked(useCallSignaling).mockReturnValue(calls as never);
    vi.mocked(useResourceCallSession).mockReturnValue(resource as never);
    vi.mocked(acquireChatSocket).mockImplementation((listener) => {
      socketListener = listener;
      return { send: vi.fn(), isOpen: vi.fn(), generation: vi.fn(), release: vi.fn() };
    });
  });

  it("leaveDedicated runs the real participant leave, releases ownership, and broadcasts leaving then left — never a resource call.end", async () => {
    renderProvider(`/call/${callId}`);
    fireEvent.click(screen.getByRole("button", { name: "Leave dedicated probe" }));

    await waitFor(() =>
      expect(ownership.post).toHaveBeenCalledWith(
        expect.objectContaining({ type: "leaving", callId }),
      ),
    );
    await waitFor(() => expect(resource.leave).toHaveBeenCalledOnce());
    expect(calls.end).not.toHaveBeenCalled();
    expect(ownership.release).toHaveBeenCalledWith(callId);
    await waitFor(() =>
      expect(ownership.post).toHaveBeenCalledWith(
        expect.objectContaining({ type: "left", callId }),
      ),
    );
    await waitFor(() => expect(screen.getByTestId("owner")).toHaveTextContent("remote"));
  });

  it("main tab converges once it observes the dedicated tab's 'left' broadcast: stops showing the call as open elsewhere and never reconnects (leave confirmado)", async () => {
    vi.useFakeTimers();
    renderProvider();
    act(() =>
      ownershipListener({ v: 1, type: "ready", callId, tabId: "tab-dedicated", epoch: 2 } as never),
    );
    await act(async () => undefined);
    expect(screen.getByTestId("owner")).toHaveTextContent("remote");
    expect(screen.getByText("Chamada aberta em outra aba")).toBeInTheDocument();

    // The dedicated tab really left: it broadcasts "left", not just a
    // released lease.
    act(() =>
      ownershipListener({
        v: 1,
        type: "left",
        callId,
        tabId: "tab-dedicated",
        epoch: 3,
        generation: 1,
        writerId: "tab-dedicated",
        sequence: 1,
      } as never),
    );

    expect(screen.queryByText("Chamada aberta em outra aba")).not.toBeInTheDocument();

    // Neither the automatic recovery interval nor an explicit "Abrir aqui"
    // click may reconnect a resource call whose participation is already
    // confirmed over.
    ownership.getOwner.mockReturnValue(null);
    await act(async () => vi.advanceTimersByTime(1_500));
    expect(resource.reconnect).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole("button", { name: "Takeover probe" }));
    await act(async () => undefined);
    expect(resource.reconnect).not.toHaveBeenCalled();
    expect(ownership.claim).not.toHaveBeenCalled();
    vi.useRealTimers();
  });

  it("a plain 'released' message (minimize, not leave) never blocks a legitimate reclaim", async () => {
    vi.useFakeTimers();
    renderProvider();
    act(() =>
      ownershipListener({ v: 1, type: "ready", callId, tabId: "tab-dedicated", epoch: 2 } as never),
    );
    await act(async () => undefined);

    act(() =>
      ownershipListener({
        v: 1,
        type: "released",
        callId,
        tabId: "tab-dedicated",
        epoch: 2,
      } as never),
    );
    ownership.getOwner.mockReturnValue(null);
    await act(async () => vi.advanceTimersByTime(1_500));
    expect(ownership.claim).toHaveBeenCalledWith(callId, "main", 2);
    expect(resource.reconnect).toHaveBeenCalled();
    vi.useRealTimers();
  });

  it("a leave in flight ('leaving', no ack yet) blocks any tab from reclaiming/reconnecting (corrida leave × Abrir aqui)", async () => {
    vi.useFakeTimers();
    renderProvider();
    act(() =>
      ownershipListener({ v: 1, type: "ready", callId, tabId: "tab-dedicated", epoch: 2 } as never),
    );
    await act(async () => undefined);
    expect(screen.getByText("Chamada aberta em outra aba")).toBeInTheDocument();

    // Dedicated started call.leave but the server hasn't confirmed yet.
    act(() =>
      ownershipListener({
        v: 1,
        type: "leaving",
        callId,
        tabId: "tab-dedicated",
        epoch: 2,
        generation: 1,
        writerId: "tab-dedicated",
        sequence: 1,
      } as never),
    );

    // The indicator itself must stop offering a reclaim while leaving is in
    // flight — reconnecting now would race the server-side leave and could
    // resurrect a participation the user is actively ending.
    expect(screen.queryByText("Chamada aberta em outra aba")).not.toBeInTheDocument();

    ownership.getOwner.mockReturnValue(null);
    await act(async () => vi.advanceTimersByTime(1_500));
    expect(resource.reconnect).not.toHaveBeenCalled();
    expect(ownership.claim).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole("button", { name: "Takeover probe" }));
    await act(async () => undefined);
    expect(resource.reconnect).not.toHaveBeenCalled();
    expect(ownership.claim).not.toHaveBeenCalled();
    vi.useRealTimers();
  });

  it("a failed leave ('leave-cancelled') restores this participation to reconnectable everywhere (leave falhou permite recovery)", async () => {
    vi.useFakeTimers();
    renderProvider();
    act(() =>
      ownershipListener({ v: 1, type: "ready", callId, tabId: "tab-dedicated", epoch: 2 } as never),
    );
    await act(async () => undefined);
    act(() =>
      ownershipListener({
        v: 1,
        type: "leaving",
        callId,
        tabId: "tab-dedicated",
        epoch: 2,
        generation: 1,
        writerId: "tab-dedicated",
        sequence: 1,
      } as never),
    );
    expect(screen.queryByText("Chamada aberta em outra aba")).not.toBeInTheDocument();

    // The leave attempt itself failed server-side; the same participation
    // is announced as participating/recoverable again.
    act(() =>
      ownershipListener({
        v: 1,
        type: "leave-cancelled",
        callId,
        tabId: "tab-dedicated",
        epoch: 2,
        generation: 1,
        writerId: "tab-dedicated",
        sequence: 2,
      } as never),
    );
    expect(screen.getByText("Chamada aberta em outra aba")).toBeInTheDocument();

    ownership.getOwner.mockReturnValue(null);
    await act(async () => vi.advanceTimersByTime(1_500));
    expect(ownership.claim).toHaveBeenCalledWith(callId, "main", 2);
    expect(resource.reconnect).toHaveBeenCalled();
    vi.useRealTimers();
  });

  it("a real join resolving to the same callId after a confirmed leave registers a fresh participation, even though resource.callId never observably changed (rejoin real)", async () => {
    // Main's own resource.callId is X from the very start and stays X
    // through the whole handoff -> dedicated-leave -> rejoin flow — the real
    // production shape: a handoff never nulls it, and useResourceCallSession
    // .join() resolving to the exact same call_id produces no React state
    // transition on it (Object.is bails), so nothing watching resource.callId
    // could ever detect this rejoin.
    // The original participation's generation (1) was itself allocated from
    // shared storage when the dedicated tab joined — seeding it here mirrors
    // that same shared floor a real allocateParticipationGeneration() call
    // would have produced, so the later real rejoin's own allocation is
    // exercised against realistic, not merely locally-injected, state.
    participationStorage = { [callId]: { "tab-dedicated": 1 } };
    renderProvider();
    fireEvent.click(screen.getByRole("button", { name: "Diretório" }));

    act(() =>
      ownershipListener({ v: 1, type: "ready", callId, tabId: "tab-dedicated", epoch: 2 } as never),
    );
    await act(async () => undefined);
    expect(screen.getByTestId("owner")).toHaveTextContent("remote");
    act(() =>
      ownershipListener({
        v: 1,
        type: "left",
        callId,
        tabId: "tab-dedicated",
        epoch: 3,
        generation: 1,
        writerId: "tab-dedicated",
        sequence: 2,
      } as never),
    );
    // While ownerState stays "remote" (this test never re-establishes local
    // ownership — a separate concern from participation, already covered
    // elsewhere), the resource-call indicator is what activeCallId gates.
    expect(screen.queryByText("Chamada aberta em outra aba")).not.toBeInTheDocument();

    // Others are still in call X: a fresh "active" event for the very same
    // call_id arrives. resource.callId is still X — untouched this whole
    // time — yet the affordance must still offer to join, because THIS
    // participation already ended.
    const incoming = {
      ...activeDirect(),
      call_id: callId,
      target_type: "channel",
      target_id: channelId,
      status: "active",
    };
    act(() =>
      socketListener.onMessage?.(
        {
          type: "call.accepted",
          event_id: "resource-rejoin",
          target_type: "channel",
          target_id: channelId,
          call: incoming,
        },
        1,
      ),
    );
    expect(await screen.findByRole("dialog", { name: "Chamada recebida" })).toBeInTheDocument();

    // The explicit, real join — resource.join() resolves with call_id X,
    // exactly like the real hook does when target.callId is supplied.
    fireEvent.click(screen.getByRole("button", { name: "Atender com câmera" }));
    expect(resource.join).toHaveBeenCalledWith(expect.objectContaining({ callId }));

    // The new participation lands with a fresh generation (2, one past the
    // old participation's 1) and activeCallId/UI converge back to X.
    await waitFor(() =>
      expect(ownership.post).toHaveBeenCalledWith(
        expect.objectContaining({ type: "participating", callId, generation: 2, sequence: 0 }),
      ),
    );
    await waitFor(() =>
      expect(screen.getByText("Chamada aberta em outra aba")).toBeInTheDocument(),
    );

    // Reconnect/control is genuinely restored, never refusing just because
    // this exact callId once appeared in a "left" message.
    ownership.getLease.mockReturnValue(lease);
    ownership.getOwner.mockReturnValue(null);
    fireEvent.click(screen.getByRole("button", { name: "Takeover probe" }));
    await waitFor(() => expect(resource.reconnect).toHaveBeenCalled());
  });

  it("dedicated crashing mid-leave leaves main stuck on 'leaving', but an explicit real join for the same callId still recovers it (dedicated morre durante leave)", async () => {
    // Mirrors the shared floor a real allocateParticipationGeneration() call
    // would have produced when the (now-crashed) dedicated tab originally
    // joined and posted "leaving" at generation 1.
    participationStorage = { [callId]: { "tab-dedicated": 1 } };
    renderProvider();
    fireEvent.click(screen.getByRole("button", { name: "Diretório" }));

    // Dedicated starts leaving but crashes before ever confirming — no
    // "left" or "leave-cancelled" ever arrives.
    act(() =>
      ownershipListener({
        v: 1,
        type: "leaving",
        callId,
        tabId: "tab-dedicated",
        epoch: 2,
        generation: 1,
        writerId: "tab-dedicated",
        sequence: 1,
      } as never),
    );
    expect(screen.queryByTestId("resource-call-panel")).not.toBeInTheDocument();

    // Automatic recovery stays blocked forever — that's expected, no ack ever
    // arrives to clear it — but this is NOT the thing under test.
    ownership.getOwner.mockReturnValue(null);
    fireEvent.click(screen.getByRole("button", { name: "Takeover probe" }));
    await act(async () => undefined);
    expect(resource.reconnect).not.toHaveBeenCalled();

    // Other participants are still in call X: a fresh "active" event for the
    // same call_id arrives. The affordance must reappear even though the
    // stuck record is "leaving", not merely "left".
    const incoming = {
      ...activeDirect(),
      call_id: callId,
      target_type: "channel",
      target_id: channelId,
      status: "active",
    };
    act(() =>
      socketListener.onMessage?.(
        {
          type: "call.accepted",
          event_id: "resource-crash-recovery",
          target_type: "channel",
          target_id: channelId,
          call: incoming,
        },
        1,
      ),
    );
    expect(await screen.findByRole("dialog", { name: "Chamada recebida" })).toBeInTheDocument();

    // No automatic recovery is required for this case — only that the
    // user's explicit join fully recovers the system.
    fireEvent.click(screen.getByRole("button", { name: "Atender com câmera" }));
    await waitFor(() => expect(screen.getByTestId("resource-call-panel")).toBeInTheDocument());

    ownership.getLease.mockReturnValue(lease);
    ownership.getOwner.mockReturnValue(null);
    fireEvent.click(screen.getByRole("button", { name: "Takeover probe" }));
    await waitFor(() => expect(resource.reconnect).toHaveBeenCalled());
  });
});

// Issue #570 follow-up: participation events are ordered by
// (generation, sequence), never Date.now() — two independent tabs can
// legitimately collide on the same wall-clock millisecond, and delivery
// order is not causal order.
describe("participation ordering is causal, not wall-clock (issue #570 follow-up)", () => {
  beforeEach(() => {
    vi.useRealTimers();
    vi.clearAllMocks();
    participationStorage = {};
    calls.call = null;
    resource.active = { kind: "channel", id: channelId, name: "Produto" };
    resource.callId = callId;
    resource.status = "active";
    ownership.getLease.mockReturnValue(lease);
    ownership.getOwner.mockReturnValue(null);
    ownership.claim.mockResolvedValue(lease);
    vi.mocked(createOwnershipCoordinator).mockReturnValue(ownership as never);
    vi.mocked(useCallMedia).mockReturnValue(media as never);
    vi.mocked(useCallSignaling).mockReturnValue(calls as never);
    vi.mocked(useResourceCallSession).mockReturnValue(resource as never);
    vi.mocked(acquireChatSocket).mockImplementation((listener) => {
      socketListener = listener;
      return { send: vi.fn(), isOpen: vi.fn(), generation: vi.fn(), release: vi.fn() };
    });
  });

  it("a new participation's higher generation always beats an old, delayed 'left' from a superseded participation, even at identical wall-clock time", async () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-08-19T12:00:00.000Z"));
    try {
      renderProvider();

      // The old participation (generation 1) already ended.
      act(() =>
        ownershipListener({
          v: 1,
          type: "left",
          callId,
          tabId: "tab-dedicated",
          epoch: 2,
          generation: 1,
          writerId: "tab-dedicated",
          sequence: 5,
        } as never),
      );
      expect(screen.queryByTestId("resource-call-panel")).not.toBeInTheDocument();

      // A brand-new participation begins (generation 2) — e.g. a fresh
      // dedicated tab reopening the same call.
      act(() =>
        ownershipListener({
          v: 1,
          type: "participating",
          callId,
          tabId: "tab-dedicated-2",
          epoch: 2,
          generation: 2,
          writerId: "tab-dedicated-2",
          sequence: 0,
        } as never),
      );
      // Synchronous: the ownershipListener call above already ran inside
      // act() and committed the re-render — no async wait needed.
      expect(screen.getByTestId("resource-call-panel")).toBeInTheDocument();

      // The OLD "left" (generation 1) is redelivered late, at the exact same
      // system time as everything above — it must never outrank generation 2.
      act(() =>
        ownershipListener({
          v: 1,
          type: "left",
          callId,
          tabId: "tab-dedicated",
          epoch: 2,
          generation: 1,
          writerId: "tab-dedicated",
          sequence: 6,
        } as never),
      );
      expect(screen.getByTestId("resource-call-panel")).toBeInTheDocument();
    } finally {
      vi.useRealTimers();
    }
  });

  it("within one participation, 'left' always wins over a reordered, later-arriving 'leaving' for an earlier step — even at identical wall-clock time", async () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-08-19T12:00:00.000Z"));
    try {
      renderProvider();

      act(() =>
        ownershipListener({
          v: 1,
          type: "participating",
          callId,
          tabId: "tab-dedicated",
          epoch: 2,
          generation: 1,
          writerId: "tab-dedicated",
          sequence: 0,
        } as never),
      );
      act(() =>
        ownershipListener({
          v: 1,
          type: "left",
          callId,
          tabId: "tab-dedicated",
          epoch: 2,
          generation: 1,
          writerId: "tab-dedicated",
          sequence: 2,
        } as never),
      );
      expect(screen.queryByTestId("resource-call-panel")).not.toBeInTheDocument();

      // "leaving" (an earlier step of the SAME participation) is redelivered
      // late, after "left" already applied — it must not resurrect the call.
      act(() =>
        ownershipListener({
          v: 1,
          type: "leaving",
          callId,
          tabId: "tab-dedicated",
          epoch: 2,
          generation: 1,
          writerId: "tab-dedicated",
          sequence: 1,
        } as never),
      );
      expect(screen.queryByTestId("resource-call-panel")).not.toBeInTheDocument();
    } finally {
      vi.useRealTimers();
    }
  });

  it("participating -> leaving -> leave-cancelled ends recoverable/participating, even at identical wall-clock time", async () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-08-19T12:00:00.000Z"));
    try {
      renderProvider();

      act(() =>
        ownershipListener({
          v: 1,
          type: "participating",
          callId,
          tabId: "tab-dedicated",
          epoch: 2,
          generation: 1,
          writerId: "tab-dedicated",
          sequence: 0,
        } as never),
      );
      act(() =>
        ownershipListener({
          v: 1,
          type: "leaving",
          callId,
          tabId: "tab-dedicated",
          epoch: 2,
          generation: 1,
          writerId: "tab-dedicated",
          sequence: 1,
        } as never),
      );
      expect(screen.queryByTestId("resource-call-panel")).not.toBeInTheDocument();

      act(() =>
        ownershipListener({
          v: 1,
          type: "leave-cancelled",
          callId,
          tabId: "tab-dedicated",
          epoch: 2,
          generation: 1,
          writerId: "tab-dedicated",
          sequence: 2,
        } as never),
      );
      expect(screen.getByTestId("resource-call-panel")).toBeInTheDocument();
    } finally {
      vi.useRealTimers();
    }
  });
});

// Issue #570 follow-up (HIGH): generation computed from this tab's own
// local participationRecordsRef is only monotonic WITHIN one
// CallSessionProvider instance. A brand-new tab, or the same tab after a
// reload, starts with that ref EMPTY and would restart at generation 1 —
// indistinguishable from, and rejected as older than, a real prior
// participation any other (or the same, pre-reload) tab still remembers.
// generation must instead come from the ownership coordinator's shared,
// persisted storage (allocateParticipationGeneration/
// getParticipationGeneration) — proven here via the `ownership` mock's
// `participationStorage`, which stands in for real localStorage: it is
// deliberately NOT cleared between renders within a single test, exactly
// like real storage survives a reload or a second tab opening.
describe("participation generation is cross-tab and reload safe (issue #570 follow-up, HIGH)", () => {
  beforeEach(() => {
    vi.useRealTimers();
    vi.clearAllMocks();
    participationStorage = {};
    calls.call = null;
    calls.error = null;
    calls.mediaActivationRequired = false;
    resource.active = null;
    resource.callId = null;
    resource.status = "idle";
    resource.error = null;
    media.status = "connected";
    media.error = null;
    media.participants = [];
    ownership.getLease.mockReturnValue(null);
    ownership.getOwner.mockReturnValue(null);
    ownership.claim.mockResolvedValue(lease);
    vi.mocked(createOwnershipCoordinator).mockReturnValue(ownership as never);
    vi.mocked(useCallMedia).mockReturnValue(media as never);
    vi.mocked(useCallSignaling).mockReturnValue(calls as never);
    vi.mocked(useResourceCallSession).mockReturnValue(resource as never);
    vi.mocked(acquireChatSocket).mockImplementation((listener) => {
      socketListener = listener;
      return { send: vi.fn(), isOpen: vi.fn(), generation: vi.fn(), release: vi.fn() };
    });
  });

  // Drives a real resource.join() through the incoming-call popup — the
  // same causal path production code uses, never a manual mock mutation.
  // targetCallId lets a test join a SECOND, distinct resource call (Y)
  // through the same directory-registered channel, to prove one callId's
  // activity never disturbs another's own generation history.
  async function joinViaPopup(targetCallId: string = callId) {
    fireEvent.click(screen.getByRole("button", { name: "Diretório" }));
    const incoming = {
      ...activeDirect(),
      call_id: targetCallId,
      target_type: "channel",
      target_id: channelId,
      status: "active",
    };
    act(() =>
      socketListener.onMessage?.(
        {
          type: "call.accepted",
          event_id: `join-${Math.random()}`,
          target_type: "channel",
          target_id: channelId,
          call: incoming,
        },
        1,
      ),
    );
    fireEvent.click(await screen.findByRole("button", { name: "Atender com câmera" }));
  }

  it("cross-tab fresh provider: a brand-new tab's real join allocates strictly past an old participation's generation, never restarting at 1 from its own empty local state", async () => {
    // This tab's own OLD participation: a real join (generation 1, from
    // empty shared storage), then a real leave (generation 1 stays, phase
    // -> left, sequence advances). Also proves "nova saída legítima": the
    // terminal leaving/left events of a real participation win normally.
    const first = renderProvider();
    await joinViaPopup();
    await waitFor(() =>
      expect(ownership.post).toHaveBeenCalledWith(
        expect.objectContaining({ type: "participating", callId, generation: 1, sequence: 0 }),
      ),
    );
    resource.active = { kind: "channel", id: channelId, name: "Produto" };
    resource.callId = callId;
    resource.status = "active";
    fireEvent.click(screen.getByRole("button", { name: "Leave dedicated probe" }));
    await waitFor(() =>
      expect(ownership.post).toHaveBeenCalledWith(
        expect.objectContaining({ type: "leaving", callId, generation: 1, sequence: 1 }),
      ),
    );
    await waitFor(() =>
      expect(ownership.post).toHaveBeenCalledWith(
        expect.objectContaining({ type: "left", callId, generation: 1, sequence: 2 }),
      ),
    );
    first.unmount();

    // A brand-new tab: fresh CallSessionProvider instance (fresh
    // participationRecordsRef, empty) — it never saw any of the events
    // above. Only the shared storage (participationStorage) survives, the
    // same way real localStorage would survive across a new tab opening.
    resource.active = null;
    resource.callId = null;
    resource.status = "idle";
    ownership.post.mockClear();
    renderProvider();
    await joinViaPopup();

    // The fresh tab's real join must allocate strictly past the OLD
    // participation's generation (1) — never restart at 1 from its own
    // empty local knowledge, which is exactly the HIGH finding's bug.
    await waitFor(() =>
      expect(ownership.post).toHaveBeenCalledWith(
        expect.objectContaining({ type: "participating", callId, generation: 2, sequence: 0 }),
      ),
    );
  });

  it("reload: this tab's own earlier participation ended, the provider is unmounted and recreated, and a delayed message from before the reload never outranks the post-reload rejoin", async () => {
    const first = renderProvider();
    await joinViaPopup();
    await waitFor(() =>
      expect(ownership.post).toHaveBeenCalledWith(
        expect.objectContaining({ type: "participating", callId, generation: 1, sequence: 0 }),
      ),
    );
    resource.active = { kind: "channel", id: channelId, name: "Produto" };
    resource.callId = callId;
    resource.status = "active";
    fireEvent.click(screen.getByRole("button", { name: "Leave dedicated probe" }));
    await waitFor(() =>
      expect(ownership.post).toHaveBeenCalledWith(
        expect.objectContaining({ type: "left", callId, generation: 1 }),
      ),
    );
    first.unmount();

    // Simulates a reload: same browser, same shared storage, but a
    // brand-new CallSessionProvider instance with no memory of the above.
    resource.active = null;
    resource.callId = null;
    resource.status = "idle";
    const second = renderProvider();
    await joinViaPopup();
    await waitFor(() =>
      expect(ownership.post).toHaveBeenCalledWith(
        expect.objectContaining({ type: "participating", callId, generation: 2, sequence: 0 }),
      ),
    );
    resource.active = { kind: "channel", id: channelId, name: "Produto" };
    resource.callId = callId;
    resource.status = "active";
    second.rerender(providerTree());
    expect(await screen.findByTestId("resource-call-panel")).toBeInTheDocument();

    // An old message from before the reload — this tab's own pre-reload
    // "left" at generation 1 — finally arrives late. It must not undo the
    // post-reload rejoin.
    act(() =>
      ownershipListener({
        v: 1,
        type: "left",
        callId,
        tabId: "tab-pre-reload",
        epoch: 2,
        generation: 1,
        writerId: "tab-main",
        sequence: 3,
      } as never),
    );
    expect(screen.getByTestId("resource-call-panel")).toBeInTheDocument();
  });

  it("a tab that took over an existing participation via handoff (never its own join()) still tags its own leaving/left with the shared generation floor, not restarting at 0", async () => {
    // Someone else (a different tab, or this same tab's earlier life)
    // already joined X and reached generation 1 — recorded only in shared
    // storage. This tab's own participationRecordsRef has no entry for X:
    // it took over via handoff/reconnect, never resource.join().
    ownership.allocateParticipationGeneration(callId);
    ownership.post.mockClear();
    resource.active = { kind: "channel", id: channelId, name: "Produto" };
    resource.callId = callId;
    resource.status = "active";
    renderProvider(`/call/${callId}`);

    fireEvent.click(screen.getByRole("button", { name: "Leave dedicated probe" }));
    await waitFor(() =>
      expect(ownership.post).toHaveBeenCalledWith(
        expect.objectContaining({ type: "leaving", callId, generation: 1, sequence: 1 }),
      ),
    );
    await waitFor(() =>
      expect(ownership.post).toHaveBeenCalledWith(
        expect.objectContaining({ type: "left", callId, generation: 1, sequence: 2 }),
      ),
    );
  });

  // HIGH finding: PARTICIPATION_KEY used to be a single global slot — a
  // different call's activity in between silently forgot the first call's
  // own generation, so a legitimate rejoin of it collided with its own
  // history instead of outranking it.
  it("X1 -> Y1 -> X2: another call's real join in between never resets X's own generation history", async () => {
    const otherCallId = "00000000-0000-4000-8000-000000000999";

    // X1: real join (generation 1), then a real leave — this tab's own
    // participation in X actually ends before Y ever begins.
    const first = renderProvider();
    await joinViaPopup(callId);
    await waitFor(() =>
      expect(ownership.post).toHaveBeenCalledWith(
        expect.objectContaining({ type: "participating", callId, generation: 1, sequence: 0 }),
      ),
    );
    resource.active = { kind: "channel", id: channelId, name: "Produto" };
    resource.callId = callId;
    resource.status = "active";
    first.rerender(providerTree());
    fireEvent.click(screen.getByRole("button", { name: "Leave dedicated probe" }));
    await waitFor(() =>
      expect(ownership.post).toHaveBeenCalledWith(
        expect.objectContaining({ type: "left", callId, generation: 1 }),
      ),
    );
    ownership.post.mockClear();

    // Y1: a DIFFERENT resource call is joined next, in the SAME tab.
    resource.active = null;
    resource.callId = null;
    resource.status = "idle";
    first.rerender(providerTree());
    await joinViaPopup(otherCallId);
    await waitFor(() =>
      expect(ownership.post).toHaveBeenCalledWith(
        expect.objectContaining({
          type: "participating",
          callId: otherCallId,
          generation: 1,
          sequence: 0,
        }),
      ),
    );
    ownership.post.mockClear();

    // X2: X is still active for other participants; this tab rejoins it.
    // X's own history — untouched by Y's activity — is what decides X2's
    // generation, strictly past X1's.
    resource.active = null;
    resource.callId = null;
    resource.status = "idle";
    first.rerender(providerTree());
    await joinViaPopup(callId);
    await waitFor(() =>
      expect(ownership.post).toHaveBeenCalledWith(
        expect.objectContaining({ type: "participating", callId, generation: 2, sequence: 0 }),
      ),
    );
  });

  it("X1 termina, Y usa o storage compartilhado, X2 começa, e um left(X1) atrasado nunca afeta X2", async () => {
    const otherCallId = "00000000-0000-4000-8000-000000000999";

    // X1: real join + real leave (generation 1, phase -> left).
    const first = renderProvider();
    await joinViaPopup(callId);
    await waitFor(() =>
      expect(ownership.post).toHaveBeenCalledWith(
        expect.objectContaining({ type: "participating", callId, generation: 1, sequence: 0 }),
      ),
    );
    resource.active = { kind: "channel", id: channelId, name: "Produto" };
    resource.callId = callId;
    resource.status = "active";
    first.rerender(providerTree());
    fireEvent.click(screen.getByRole("button", { name: "Leave dedicated probe" }));
    await waitFor(() =>
      expect(ownership.post).toHaveBeenCalledWith(
        expect.objectContaining({ type: "left", callId, generation: 1, sequence: 2 }),
      ),
    );
    const x1Left = ownership.post.mock.calls.find(
      (postCall) => (postCall[0] as { type?: string }).type === "left",
    )![0] as { generation: number; writerId: string; sequence: number };
    ownership.post.mockClear();

    // Y: another call's real join and leave uses the SAME shared storage.
    resource.active = null;
    resource.callId = null;
    resource.status = "idle";
    first.rerender(providerTree());
    await joinViaPopup(otherCallId);
    resource.active = { kind: "channel", id: channelId, name: "Produto" };
    resource.callId = otherCallId;
    resource.status = "active";
    first.rerender(providerTree());
    fireEvent.click(screen.getByRole("button", { name: "Leave dedicated probe" }));
    await waitFor(() =>
      expect(ownership.post).toHaveBeenCalledWith(
        expect.objectContaining({ type: "left", callId: otherCallId }),
      ),
    );

    // X2: a real rejoin of X begins — its own history (unaffected by Y).
    resource.active = null;
    resource.callId = null;
    resource.status = "idle";
    first.rerender(providerTree());
    await joinViaPopup(callId);
    await waitFor(() =>
      expect(ownership.post).toHaveBeenCalledWith(
        expect.objectContaining({ type: "participating", callId, generation: 2, sequence: 0 }),
      ),
    );
    // leaveDedicated (used to end X1 above) also releases ownership, so this
    // tab stays "remote" for the rest of the test — a separate concern from
    // participation, already covered elsewhere. The resource-call indicator
    // is what activeCallId gates while ownerState === "remote".
    resource.active = { kind: "channel", id: channelId, name: "Produto" };
    resource.callId = callId;
    resource.status = "active";
    first.rerender(providerTree());
    expect(await screen.findByText("Chamada aberta em outra aba")).toBeInTheDocument();

    // The OLD X1 "left" — sent before Y or X2 ever happened — finally
    // arrives late. It must not undo X2.
    act(() =>
      ownershipListener({
        v: 1,
        type: "left",
        callId,
        tabId: "tab-x1",
        epoch: 1,
        generation: x1Left.generation,
        writerId: x1Left.writerId,
        sequence: x1Left.sequence,
      } as never),
    );
    expect(screen.getByText("Chamada aberta em outra aba")).toBeInTheDocument();
  });

  it("a tie between two independently-raced tokens for the same callId still converges to one deterministic winner, and a later real join outranks both", async () => {
    // Two tabs raced to generation 1 for X with no shared-storage
    // synchronization at all (proven directly in callOwnership.test.ts) —
    // this tab receives both, in arbitrary order.
    const tokenA = { generation: 1, writerId: "tab-a" };
    const tokenB = { generation: 1, writerId: "tab-b" };
    resource.active = { kind: "channel", id: channelId, name: "Produto" };
    resource.callId = callId;
    resource.status = "active";
    renderProvider();
    act(() =>
      ownershipListener({
        v: 1,
        type: "participating",
        callId,
        tabId: "tab-a",
        epoch: 1,
        ...tokenA,
        sequence: 0,
      } as never),
    );
    act(() =>
      ownershipListener({
        v: 1,
        type: "participating",
        callId,
        tabId: "tab-b",
        epoch: 1,
        ...tokenB,
        sequence: 0,
      } as never),
    );
    expect(screen.getByTestId("resource-call-panel")).toBeInTheDocument();

    // Whichever of the two this tab settled on as "current" (deterministic,
    // same rule everywhere — proven directly in callOwnership.test.ts), the
    // OTHER one's own terminal message must never undo it: it belongs to a
    // participation this tab has already decided lost the tie.
    act(() =>
      ownershipListener({
        v: 1,
        type: "left",
        callId,
        tabId: "tab-a",
        epoch: 1,
        generation: tokenA.generation,
        writerId: tokenA.writerId,
        sequence: 1,
      } as never),
    );
    act(() =>
      ownershipListener({
        v: 1,
        type: "left",
        callId,
        tabId: "tab-b",
        epoch: 1,
        generation: tokenB.generation,
        writerId: tokenB.writerId,
        sequence: 1,
      } as never),
    );
    // At most one of the two "left" messages (the one for whichever token
    // actually won) could have applied; even so, a genuinely newer
    // participation (generation 2) must still outrank whatever is current.
    act(() =>
      ownershipListener({
        v: 1,
        type: "participating",
        callId,
        tabId: "tab-c",
        epoch: 1,
        generation: 2,
        writerId: "tab-c",
        sequence: 0,
      } as never),
    );
    expect(screen.getByTestId("resource-call-panel")).toBeInTheDocument();
  });

  // Issue #570 problem 3: DedicatedCallPage's activation effect calls
  // resource.join() on every successful ownerState -> "local" transition,
  // including a plain handoff continuation, not only a genuinely fresh
  // join — see the comment on beginResourceParticipation in
  // CallSessionProvider.tsx for the chosen, documented semantics.
  it("problem 3: re-registering an already-active participation (as a handoff continuation's resource.join() would) never posts leaving/left, and reconnect/minimize keep working", async () => {
    // The popup's own affordance gate (participationRecordsRef.phase !==
    // "participating") deliberately can't reoffer a call this tab is
    // already in — so this drives the SAME function
    // (beginResourceParticipation) DedicatedCallPage's activation effect
    // calls directly, on every ownerState -> "local" transition, without
    // going through that gate — exactly how a real handoff continuation's
    // resource.join() reaches it.
    const view = renderProvider();
    fireEvent.click(screen.getByRole("button", { name: "Begin participation probe" }));
    await waitFor(() =>
      expect(ownership.post).toHaveBeenCalledWith(
        expect.objectContaining({ type: "participating", callId, generation: 1, sequence: 0 }),
      ),
    );

    // A handoff continuation's resource.join() resolving again for the
    // exact same, still-active participation.
    fireEvent.click(screen.getByRole("button", { name: "Begin participation probe" }));
    await waitFor(() =>
      expect(ownership.post).toHaveBeenCalledWith(
        expect.objectContaining({ type: "participating", callId, generation: 2, sequence: 0 }),
      ),
    );
    expect(ownership.post).not.toHaveBeenCalledWith(expect.objectContaining({ type: "leaving" }));
    expect(ownership.post).not.toHaveBeenCalledWith(expect.objectContaining({ type: "left" }));

    // Reconnect/minimize continue functioning normally afterward.
    resource.active = { kind: "channel", id: channelId, name: "Produto" };
    resource.callId = callId;
    resource.status = "active";
    ownership.getLease.mockReturnValue(lease);
    ownership.getOwner.mockReturnValue(null);
    view.rerender(providerTree());
    fireEvent.click(screen.getByRole("button", { name: "Takeover probe" }));
    await waitFor(() => expect(resource.reconnect).toHaveBeenCalled());
  });

  // HIGH finding (per-writer keys): A and B both raced to generation 4
  // without Web Locks. Under the OLD single-shared-key design, whichever
  // wrote last could regress storage back over the other. Under per-writer
  // keys, both A's and B's own keys persist independently — nothing to
  // regress — so a fresh/reload tab with no local participationRecordsRef
  // entry, falling back to getParticipationToken, reads the true causal
  // winner among BOTH, never "whichever physically wrote last".
  it("A/B collide at the same generation; a fresh/reload tab's own leave still uses exactly the causal winner among both persisted writer keys", async () => {
    const tokenA = { generation: 4, writerId: "writer-z" };
    const tokenB = { generation: 4, writerId: "writer-a" };
    const winner = compareParticipationTokens(tokenB, tokenA) > 0 ? tokenB : tokenA;
    const loser = winner === tokenB ? tokenA : tokenB;
    expect(winner).not.toEqual(loser);

    // Both A's and B's own writer keys are independently persisted — the
    // real outcome of two tabs racing with zero synchronization, neither
    // ever overwriting the other's key.
    participationStorage = {
      [callId]: { [tokenA.writerId]: tokenA.generation, [tokenB.writerId]: tokenB.generation },
    };
    expect(ownership.getParticipationToken(callId)).toEqual(winner);

    // A brand-new/reloaded tab: fresh CallSessionProvider instance, EMPTY
    // local participationRecordsRef — it never itself observed either
    // participation being established.
    resource.active = { kind: "channel", id: channelId, name: "Produto" };
    resource.callId = callId;
    resource.status = "active";
    renderProvider();
    ownership.post.mockClear();

    fireEvent.click(screen.getByRole("button", { name: "Leave dedicated probe" }));
    await waitFor(() => expect(ownership.post).toHaveBeenCalled());

    // The fresh tab's own leaving/left is tagged with EXACTLY the causal
    // winner — never the loser, and never reset to a fresh generation 1.
    const leavingCall = ownership.post.mock.calls.find(
      (postCall) => (postCall[0] as { type?: string }).type === "leaving",
    );
    expect(leavingCall?.[0]).toMatchObject({
      type: "leaving",
      callId,
      generation: winner.generation,
      writerId: winner.writerId,
    });
    await waitFor(() =>
      expect(ownership.post).toHaveBeenCalledWith(
        expect.objectContaining({
          type: "left",
          callId,
          generation: winner.generation,
          writerId: winner.writerId,
        }),
      ),
    );

    // Every tab that already knows the winner as current accepts these
    // terminal events: the posted messages carry the SAME winning token
    // with a strictly increasing sequence, which is exactly what
    // compareParticipationTokens plus the sequence guard in
    // commitParticipation accepts (proven directly by the
    // "left always wins over a reordered leaving" and
    // "delivers '%s' to listeners unconditionally" tests) — never rejected
    // as belonging to an unrelated, older participation.
    const leftCall = ownership.post.mock.calls.find(
      (postCall) => (postCall[0] as { type?: string }).type === "left",
    )![0] as { generation: number; writerId: string };
    expect(compareParticipationTokens(leftCall, loser)).toBeGreaterThan(0);
    expect(compareParticipationTokens(leftCall, winner)).toBe(0);
  });
});

describe("React StrictMode ownership coordinator lifecycle (CALLS-546 regression)", () => {
  // Each call to createOwnershipCoordinator() returns a fresh, independently
  // tracked instance — unlike the single shared `ownership` mock used by
  // every other describe block above — because this suite's whole point is
  // to prove the provider ends up with a *different*, *open* instance after
  // React StrictMode's dev-only mount -> cleanup -> mount probe, never a
  // reused closed one.
  function createMockCoordinator(label: string) {
    let closed = false;
    const unsubscribe = vi.fn();
    const unsubscribeLost = vi.fn();
    const instance = {
      label,
      tabId: `tab-${label}`,
      claim: vi.fn(async (claimCallId: string, role: "main" | "dedicated") =>
        closed
          ? null
          : {
              v: 1 as const,
              callId: claimCallId,
              tabId: `tab-${label}`,
              epoch: 1,
              role,
              expiresAt: 999_999,
            },
      ),
      getLease: vi.fn(() => null),
      getOwner: vi.fn(() => null),
      release: vi.fn(),
      post: vi.fn(),
      subscribe: vi.fn((listener: typeof ownershipListener) => {
        ownershipListener = listener;
        return unsubscribe;
      }),
      onOwnershipLost: vi.fn((listener: typeof ownershipLost) => {
        ownershipLost = listener;
        return unsubscribeLost;
      }),
      close: vi.fn(() => {
        closed = true;
      }),
      isClosed: vi.fn(() => closed),
    };
    return { instance, unsubscribe, unsubscribeLost };
  }

  let created: Array<ReturnType<typeof createMockCoordinator>>;

  beforeEach(() => {
    vi.useRealTimers();
    vi.clearAllMocks();
    participationStorage = {};
    calls.call = null;
    resource.active = null;
    resource.callId = null;
    created = [];
    vi.mocked(createOwnershipCoordinator).mockImplementation(() => {
      const made = createMockCoordinator(`c${created.length}`);
      created.push(made);
      return made.instance as never;
    });
    vi.mocked(useCallMedia).mockReturnValue(media as never);
    vi.mocked(useCallSignaling).mockReturnValue(calls as never);
    vi.mocked(useResourceCallSession).mockReturnValue(resource as never);
    vi.mocked(acquireChatSocket).mockImplementation((listener) => {
      socketListener = listener;
      return { send: vi.fn(), isOpen: vi.fn(), generation: vi.fn(), release: vi.fn() };
    });
  });

  it("survives the StrictMode probe: closes the first instance and keeps a second, open one active", () => {
    render(<StrictMode>{providerTree()}</StrictMode>);

    // The probe creates exactly two coordinators for this one provider
    // mount — never more (no duplicate live coordinators) and never just
    // one reused across the cleanup (that reused-after-close instance is
    // exactly CALLS-546's bug).
    expect(created).toHaveLength(2);
    const [first, second] = created;
    expect(first.instance).not.toBe(second.instance);

    // The probe's cleanup closed the first instance...
    expect(first.instance.close).toHaveBeenCalledOnce();
    expect(first.instance.isClosed()).toBe(true);
    // ...and the provider is left with a second, functional, open one.
    expect(second.instance.close).not.toHaveBeenCalled();
    expect(second.instance.isClosed()).toBe(false);
  });

  it("routes announceDedicated/claim to the active (second) instance, never the closed first one", async () => {
    render(<StrictMode>{providerTree(`/call/${callId}`)}</StrictMode>);
    const [first, second] = created;

    fireEvent.click(screen.getByRole("button", { name: "Ready probe" }));
    // No owner on the active instance -> immediate claim (achado #1).
    await waitFor(() => expect(second.instance.claim).toHaveBeenCalledWith(callId, "dedicated", 0));
    expect(second.instance.post).toHaveBeenCalledWith(expect.objectContaining({ type: "ready" }));

    // The closed, discarded first instance must never see any of this.
    expect(first.instance.claim).not.toHaveBeenCalled();
    expect(first.instance.post).not.toHaveBeenCalled();
    await waitFor(() => expect(screen.getByTestId("owner")).toHaveTextContent("local"));
  });

  it("exercises claim-without-owner, handoff, and release against the active instance after the probe", async () => {
    render(<StrictMode>{providerTree(`/call/${callId}`)}</StrictMode>);
    const [, second] = created;

    // claim sem owner (direct dedicated recovery uses the same path).
    fireEvent.click(screen.getByRole("button", { name: "Ready probe" }));
    await waitFor(() => expect(second.instance.claim).toHaveBeenCalledWith(callId, "dedicated", 0));

    // handoff: a targeted "handoff" message reaching this tab claims on
    // the active instance.
    second.instance.claim.mockClear();
    act(() =>
      ownershipListener({
        v: 1,
        type: "handoff",
        callId,
        tabId: "tab-main",
        targetTabId: second.instance.tabId,
        epoch: 4,
      } as never),
    );
    await waitFor(() => expect(second.instance.claim).toHaveBeenCalledWith(callId, "dedicated", 4));

    // release: releaseDedicated always resolves the active instance too.
    fireEvent.click(screen.getByRole("button", { name: "Release probe" }));
    await waitFor(() => expect(second.instance.release).toHaveBeenCalledWith(callId));
  });

  it("final unmount closes the active instance and tears down its listeners — no dangling handle", () => {
    const view = render(<StrictMode>{providerTree()}</StrictMode>);
    const [, second] = created;
    expect(second.instance.subscribe).toHaveBeenCalledOnce();
    expect(second.instance.onOwnershipLost).toHaveBeenCalledOnce();

    view.unmount();

    expect(second.instance.close).toHaveBeenCalledOnce();
    expect(second.instance.isClosed()).toBe(true);
    // subscribe()/onOwnershipLost() returned unsubscribe functions — React
    // calling an effect's cleanup is what invokes them.
    expect(created[1].unsubscribe).toHaveBeenCalledOnce();
    expect(created[1].unsubscribeLost).toHaveBeenCalledOnce();
    // No coordinator ever left open after the real unmount.
    expect(created.every((c) => c.instance.isClosed())).toBe(true);
    expect(created).toHaveLength(2);
  });
});

describe("useCallSession", () => {
  it("rejects consumers outside the provider", () => {
    function InvalidConsumer() {
      useCallSession();
      return null;
    }
    expect(() => render(<InvalidConsumer />)).toThrow("useCallSession must be used within");
  });
});
