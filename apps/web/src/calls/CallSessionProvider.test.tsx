import { StrictMode } from "react";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Outlet, Route, Routes, useNavigate } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { acquireChatSocket, type ChatSocketListener } from "../chat/chatSocket";
import { useCallMedia } from "../chat/useCallMedia";
import { useCallSignaling } from "../chat/useCallSignaling";
import { useResourceCallSession } from "../chat/useResourceCallSession";
import { createOwnershipCoordinator } from "./callOwnership";
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
  join: vi.fn(async () => undefined),
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
