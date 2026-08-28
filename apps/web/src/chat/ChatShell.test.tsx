import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes, useOutletContext } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { clearTokens, setTokens } from "../lib/authSession";
import { issueCallToken, issueResourceCallToken } from "./callApi";
import { fetchSidebarData, leaveConversation } from "./chatApi";
import AppShell, { ROOT_LOCK_CLASS } from "./AppShell";
import ChatShell, { type ChatOutletContext } from "./ChatShell";
import CallSessionProvider from "../calls/CallSessionProvider";
import { _resetChatSocket } from "./chatSocket";
import { requestMediaPermission, type MediaPermissionResult } from "./mediaPermission";
import { NAV_DRAWER_QUERY } from "./useNavDrawer";
import { useCallMedia } from "./useCallMedia";

vi.mock("./chatApi", async () => {
  const actual = await vi.importActual<typeof import("./chatApi")>("./chatApi");
  return { ...actual, fetchSidebarData: vi.fn(), leaveConversation: vi.fn() };
});
// A stub, because what is under test here is which target the shell hands the
// panel — not what the panel then renders for it. The real one fetches.
vi.mock("./SidebarDetailsPanel", () => ({
  default: ({ target }: { target: { kind: string; id: string } | null }) =>
    target ? <div data-testid="sidebar-details">{`${target.kind}:${target.id}`}</div> : null,
}));
vi.mock("./callApi", () => ({ issueCallToken: vi.fn(), issueResourceCallToken: vi.fn() }));
vi.mock("./useChatWebSocket", () => ({ useChatWebSocket: vi.fn() }));
vi.mock("./useCallMedia", () => ({ useCallMedia: vi.fn() }));
vi.mock("./mediaPermission", () => ({ requestMediaPermission: vi.fn() }));

class FakeWebSocket {
  static readonly OPEN = 1;
  static readonly CLOSED = 3;
  static instances: FakeWebSocket[] = [];

  readyState = FakeWebSocket.OPEN;
  sentMessages: string[] = [];
  onopen: (() => void) | null = null;
  onmessage: ((event: MessageEvent) => void) | null = null;
  onerror: (() => void) | null = null;
  onclose: ((event: CloseEvent) => void) | null = null;

  constructor() {
    FakeWebSocket.instances.push(this);
  }

  send(data: string) {
    this.sentMessages.push(data);
    const message = JSON.parse(data) as Record<string, unknown>;
    // Auto-acks call.leave the way chat-service does: a direct reply to the
    // sender alone (see call_protocol.go's sendCallToClient). The RF-23 x
    // RF-24 arbitration suite below drives useResourceCallSession.leave()
    // for real (issue #569) and needs this to settle for the handoff to
    // proceed; its exact status is irrelevant to those tests, only that
    // leave() resolves.
    if (message.type === "call.leave" && typeof message.call_id === "string") {
      const callID = message.call_id;
      queueMicrotask(() => {
        this.simulateMessage({
          type: "call.left",
          operation: "call.leave",
          response_to: message.request_id,
          released: true,
          call: {
            ...call,
            call_id: callID,
            caller_id: currentUserId,
            callee_id: "",
            target_type: "channel",
            target_id: "chan-1",
            call_type: "audio",
            status: "ended",
          },
        });
      });
    }
  }

  close() {
    this.readyState = FakeWebSocket.CLOSED;
  }

  simulateOpen() {
    this.onopen?.();
  }

  simulateMessage(data: object) {
    this.onmessage?.(new MessageEvent("message", { data: JSON.stringify(data) }));
  }
}

function deferredValue<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason: unknown) => void;
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return { promise, resolve, reject };
}

const currentUserId = "00000000-0000-4000-8000-000000000401";
const callerId = "00000000-0000-4000-8000-000000000402";
const call = {
  call_id: "00000000-0000-4000-8000-000000000403",
  request_id: "00000000-0000-4000-8000-000000000404",
  caller_id: callerId,
  callee_id: currentUserId,
  call_type: "video",
  status: "ringing",
  version: 1,
  created_at: "2026-08-03T12:00:00Z",
  occurred_at: "2026-08-03T12:00:00Z",
  expires_at: "2026-08-03T12:00:30Z",
} as const;

const prepareMedia = vi.fn(async () => undefined);
const connectMedia = vi.fn(
  async (): Promise<{ microphone: boolean; camera: boolean } | undefined> => ({
    microphone: true,
    camera: true,
  }),
);
const stopMedia = vi.fn(async () => undefined);
const OriginalWebSocket = global.WebSocket;

beforeEach(() => {
  FakeWebSocket.instances = [];
  _resetChatSocket(() => 0);
  global.WebSocket = FakeWebSocket as unknown as typeof WebSocket;
  setTokens("test-token");
  // This file uses the REAL createOwnershipCoordinator (never mocked), which
  // persists ParticipationToken generations to real localStorage — issue
  // #594 adversarial follow-up, round 3's fix now makes ChatShell's own
  // join button actually allocate one. Without this, generations accumulate
  // across tests in this file (all sharing jsdom's one localStorage) instead
  // of each test starting from a clean, predictable floor.
  localStorage.clear();
  prepareMedia.mockClear();
  connectMedia.mockClear();
  stopMedia.mockClear();
  vi.mocked(issueCallToken).mockReset();
  vi.mocked(issueCallToken).mockResolvedValue({
    token: "media-token",
    expiresAt: "2026-08-03T12:05:00Z",
    serverUrl: "wss://livekit-dev.nic-labs.com",
  });
  vi.mocked(issueResourceCallToken).mockReset();
  vi.mocked(issueResourceCallToken).mockResolvedValue({
    token: "resource-token",
    expiresAt: "2026-08-03T12:05:00Z",
    serverUrl: "wss://livekit-dev.nic-labs.com",
  });
  vi.mocked(requestMediaPermission).mockReset();
  vi.mocked(requestMediaPermission).mockResolvedValue({ ok: true });
  vi.mocked(useCallMedia).mockReturnValue({
    status: "idle",
    microphoneEnabled: false,
    cameraEnabled: false,
    hasLocalVideo: false,
    hasRemoteMedia: false,
    hasRemoteVideo: false,
    mediaLoading: false,
    audioStarting: false,
    audioActivationRequired: false,
    error: null,
    pendingControl: null,
    activeSpeakerId: null,
    screenShareEnabled: false,
    remoteScreenShare: null,
    bindLocalMedia: vi.fn(),
    bindRemoteMedia: vi.fn(),
    bindLocalScreenShare: vi.fn(),
    participants: [],
    bindRemoteAudio: vi.fn(),
    toggleMicrophone: vi.fn(async () => undefined),
    toggleCamera: vi.fn(async () => undefined),
    toggleScreenShare: vi.fn(async () => undefined),
    activateAudio: vi.fn(async () => undefined),
    prepare: prepareMedia,
    startAudio: vi.fn(async () => undefined),
    connect: connectMedia,
    stop: stopMedia,
  });
});

afterEach(() => {
  _resetChatSocket();
  global.WebSocket = OriginalWebSocket;
  clearTokens();
  vi.clearAllMocks();
});

describe("ChatShell call identity bootstrap", () => {
  it("keeps ringing until the sidebar resolves without recreating call signaling", async () => {
    const sidebar = deferredValue<Awaited<ReturnType<typeof fetchSidebarData>>>();
    vi.mocked(fetchSidebarData).mockReturnValue(sidebar.promise);

    render(
      <MemoryRouter initialEntries={["/chat"]}>
        <CallSessionProvider>
          <Routes>
            <Route element={<AppShell />}>
              <Route path="/chat" element={<ChatShell />} />
            </Route>
          </Routes>
        </CallSessionProvider>
      </MemoryRouter>,
    );

    const socket = FakeWebSocket.instances[0];
    act(() => socket.simulateOpen());
    act(() =>
      socket.simulateMessage({
        type: "call.ringing",
        event_id: "00000000-0000-4000-8000-000000000405",
        target_type: "user",
        target_id: currentUserId,
        call,
      }),
    );

    const dialog = screen.getByRole("dialog", { name: "Chamada recebida" });
    expect(within(dialog).getByRole("status")).toHaveTextContent("Preparando chamada…");
    expect(
      screen.queryByRole("button", { name: "Atender com câmera a chamada de vídeo de Ana Lima" }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Recusar chamada de Ana Lima" }),
    ).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Cancelar chamada" })).not.toBeInTheDocument();
    expect(prepareMedia).not.toHaveBeenCalled();
    expect(syncCommands(socket)).toHaveLength(1);

    await act(async () =>
      sidebar.resolve({
        currentUserId,
        workspaceId: "workspace-1",
        channels: [],
        dms: [
          {
            id: "00000000-0000-4000-8000-000000000406",
            type: "1:1",
            name: "Ana Lima",
            participants: [],
            counterpart: { userId: callerId, displayName: "Ana Lima" },
          },
        ],
        categories: [],
      }),
    );

    expect(
      await screen.findByRole("button", {
        name: "Atender com câmera a chamada de vídeo de Ana Lima",
      }),
    ).not.toHaveFocus();
    expect(screen.getByRole("button", { name: "Recusar chamada de Ana Lima" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Cancelar chamada" })).not.toBeInTheDocument();
    expect(FakeWebSocket.instances).toHaveLength(1);
    expect(syncCommands(socket)).toHaveLength(1);
    await waitFor(() => expect(prepareMedia).toHaveBeenCalledOnce());
  });

  it("recovers identity inside the call dialog without recreating signaling", async () => {
    const user = userEvent.setup();
    const initialSidebar = deferredValue<Awaited<ReturnType<typeof fetchSidebarData>>>();
    const failedRetry = deferredValue<Awaited<ReturnType<typeof fetchSidebarData>>>();
    const successfulRetry = deferredValue<Awaited<ReturnType<typeof fetchSidebarData>>>();
    vi.mocked(fetchSidebarData)
      .mockReturnValueOnce(initialSidebar.promise)
      .mockReturnValueOnce(failedRetry.promise)
      .mockReturnValueOnce(successfulRetry.promise);

    render(
      <MemoryRouter initialEntries={["/chat"]}>
        <CallSessionProvider>
          <Routes>
            <Route element={<AppShell />}>
              <Route path="/chat" element={<ChatShell />} />
            </Route>
          </Routes>
        </CallSessionProvider>
      </MemoryRouter>,
    );

    const socket = FakeWebSocket.instances[0];
    act(() => socket.simulateOpen());
    act(() =>
      socket.simulateMessage({
        type: "call.ringing",
        event_id: "00000000-0000-4000-8000-000000000405",
        target_type: "user",
        target_id: currentUserId,
        call,
      }),
    );
    await act(async () => initialSidebar.reject(new Error("offline")));

    const dialog = screen.getByRole("dialog", { name: "Chamada recebida" });
    expect(within(dialog).getByRole("alert")).toHaveTextContent(
      "Não foi possível preparar a chamada",
    );
    const retry = within(dialog).getByRole("button", { name: "Tentar novamente" });
    expect(retry).not.toHaveFocus();
    await user.click(retry);
    expect(fetchSidebarData).toHaveBeenCalledTimes(2);
    expect(retry).toBeDisabled();
    await user.click(retry);
    expect(fetchSidebarData).toHaveBeenCalledTimes(2);
    expect(prepareMedia).not.toHaveBeenCalled();

    await act(async () => failedRetry.reject(new Error("still offline")));
    const retryAgain = within(dialog).getByRole("button", { name: "Tentar novamente" });
    expect(retryAgain).toBeEnabled();
    await user.click(retryAgain);
    expect(fetchSidebarData).toHaveBeenCalledTimes(3);
    expect(retry).toBeDisabled();

    await act(async () =>
      successfulRetry.resolve({
        currentUserId,
        workspaceId: "workspace-1",
        channels: [],
        dms: [
          {
            id: "00000000-0000-4000-8000-000000000406",
            type: "1:1",
            name: "Ana Lima",
            participants: [],
            counterpart: { userId: callerId, displayName: "Ana Lima" },
          },
        ],
        categories: [],
      }),
    );

    expect(
      await screen.findByRole("button", {
        name: "Atender com câmera a chamada de vídeo de Ana Lima",
      }),
    ).not.toHaveFocus();
    expect(screen.getByRole("button", { name: "Recusar chamada de Ana Lima" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Cancelar chamada" })).not.toBeInTheDocument();
    expect(FakeWebSocket.instances).toHaveLength(1);
    expect(syncCommands(socket)).toHaveLength(1);
    await waitFor(() => expect(prepareMedia).toHaveBeenCalledOnce());
  });

  it("keeps an active call without media until identity recovery succeeds", async () => {
    const user = userEvent.setup();
    const initialSidebar = deferredValue<Awaited<ReturnType<typeof fetchSidebarData>>>();
    const failedRetry = deferredValue<Awaited<ReturnType<typeof fetchSidebarData>>>();
    const successfulRetry = deferredValue<Awaited<ReturnType<typeof fetchSidebarData>>>();
    vi.mocked(fetchSidebarData)
      .mockReturnValueOnce(initialSidebar.promise)
      .mockReturnValueOnce(failedRetry.promise)
      .mockReturnValueOnce(successfulRetry.promise);

    render(
      <MemoryRouter initialEntries={["/chat"]}>
        <CallSessionProvider>
          <Routes>
            <Route element={<AppShell />}>
              <Route path="/chat" element={<ChatShell />} />
            </Route>
          </Routes>
        </CallSessionProvider>
      </MemoryRouter>,
    );

    const socket = FakeWebSocket.instances[0];
    act(() => socket.simulateOpen());
    act(() =>
      socket.simulateMessage({
        type: "call.accepted",
        event_id: "00000000-0000-4000-8000-000000000407",
        target_type: "user",
        target_id: currentUserId,
        call: { ...call, status: "active", version: 2, accepted_at: call.occurred_at },
      }),
    );

    const dialog = screen.getByTestId("floating-call-window");
    expect(within(dialog).getByText("Preparando chamada…")).toBeInTheDocument();
    expect(issueCallToken).not.toHaveBeenCalled();
    expect(connectMedia).not.toHaveBeenCalled();

    await act(async () => initialSidebar.reject(new Error("offline")));
    const retry = within(dialog).getByRole("button", { name: "Tentar novamente" });
    expect(retry).not.toHaveFocus();
    await user.click(retry);
    await user.click(retry);
    expect(fetchSidebarData).toHaveBeenCalledTimes(2);
    expect(issueCallToken).not.toHaveBeenCalled();

    await act(async () => failedRetry.reject(new Error("still offline")));
    const retryAgain = within(dialog).getByRole("button", { name: "Tentar novamente" });
    expect(retryAgain).toBeEnabled();
    await user.click(retryAgain);
    expect(fetchSidebarData).toHaveBeenCalledTimes(3);

    await act(async () =>
      successfulRetry.resolve({
        currentUserId,
        workspaceId: "workspace-1",
        channels: [],
        dms: [
          {
            id: "00000000-0000-4000-8000-000000000406",
            type: "1:1",
            name: "Ana Lima",
            participants: [],
            counterpart: { userId: callerId, displayName: "Ana Lima" },
          },
        ],
        categories: [],
      }),
    );

    const dialogAfterRecovery = await screen.findByLabelText("Chamada com Ana Lima");
    expect(dialogAfterRecovery).toBeVisible();
    expect(screen.getByRole("button", { name: "Encerrar chamada" })).not.toHaveFocus();

    // This call reached "active" via a raw push, never through this hook's
    // own start()/accept() preflight, so RF-23 requires an explicit gesture
    // before any getUserMedia/LiveKit connection — no auto-connect here.
    expect(issueCallToken).not.toHaveBeenCalled();
    expect(connectMedia).not.toHaveBeenCalled();
    const activate = screen.getByRole("button", { name: "Permitir câmera e microfone" });

    await user.click(activate);

    await waitFor(() => expect(requestMediaPermission).toHaveBeenCalledExactlyOnceWith("video"));
    await waitFor(() => expect(issueCallToken).toHaveBeenCalledOnce());
    await waitFor(() => expect(connectMedia).toHaveBeenCalledOnce());
    expect(FakeWebSocket.instances).toHaveLength(1);
    expect(syncCommands(socket)).toHaveLength(1);
  });
});

function syncCommands(socket: FakeWebSocket) {
  return socket.sentMessages
    .map((message) => JSON.parse(message) as { type: string })
    .filter((message) => message.type === "call.sync");
}

function declineCommands(socket: FakeWebSocket) {
  return socket.sentMessages
    .map((message) => JSON.parse(message) as { type: string; call_id?: string })
    .filter((message) => message.type === "call.decline");
}

// ── RF-24 code review achado 3: RF-23 x RF-24 arbitration ───────────────────

function JoinChannelButton() {
  const ctx = useOutletContext<ChatOutletContext>();
  return (
    <>
      <button
        type="button"
        disabled={!ctx.joinResourceCall}
        onClick={() => ctx.joinResourceCall?.({ kind: "channel", id: "chan-1", name: "Geral" })}
      >
        Entrar no canal
      </button>
      <button
        type="button"
        disabled={!ctx.startCall}
        onClick={() => ctx.startCall?.(callerId, "audio")}
      >
        Ligar direto
      </button>
    </>
  );
}

async function renderWithJoinButtonReady() {
  vi.mocked(fetchSidebarData).mockResolvedValue({
    currentUserId,
    workspaceId: "workspace-1",
    channels: [{ id: "chan-1", name: "Geral", type: "public", canWrite: true }],
    dms: [
      {
        id: "00000000-0000-4000-8000-000000000406",
        type: "1:1",
        name: "Ana Lima",
        participants: [],
        counterpart: { userId: callerId, displayName: "Ana Lima" },
      },
    ],
    categories: [],
  });
  render(
    <MemoryRouter initialEntries={["/chat"]}>
      <Routes>
        <Route element={<AppShell />}>
          <Route
            path="/chat"
            element={
              <CallSessionProvider>
                <ChatShell />
              </CallSessionProvider>
            }
          >
            <Route index element={<JoinChannelButton />} />
          </Route>
        </Route>
      </Routes>
    </MemoryRouter>,
  );
  const socket = FakeWebSocket.instances[0];
  await act(async () => socket.simulateOpen());
  await screen.findByRole("button", { name: "Entrar no canal" });
  return socket;
}

async function joinResource(user: ReturnType<typeof userEvent.setup>, socket: FakeWebSocket) {
  await user.click(screen.getByRole("button", { name: "Entrar no canal" }));
  let requestID = "";
  await waitFor(() => {
    const command = socket.sentMessages
      .map((message) => JSON.parse(message) as { type: string; request_id?: string })
      .find((message) => message.type === "call.start" && message.request_id);
    expect(command).toBeDefined();
    requestID = command!.request_id!;
  });
  // Issue #622 round 2: startResourceCall resolves solely on this
  // requester's own call.admitted, correlated by response_to — never on a
  // call.accepted broadcast matched by request_id (the historical bug this
  // issue fixed).
  act(() =>
    socket.simulateMessage({
      type: "call.admitted",
      operation: "call.start",
      response_to: requestID,
      participation_id: "00000000-0000-4000-8000-000000000709",
      call: {
        ...call,
        call_id: "00000000-0000-4000-8000-000000000550",
        request_id: requestID,
        caller_id: currentUserId,
        callee_id: "",
        target_type: "channel",
        target_id: "chan-1",
        call_type: "audio",
        status: "active",
      },
    }),
  );
}

describe("ChatShell RF-23 x RF-24 arbitration", () => {
  it("Caso A: an incoming RF-23 call stays visible and can be declined while a resource room is active", async () => {
    const user = userEvent.setup();
    const socket = await renderWithJoinButtonReady();

    await joinResource(user, socket);
    await waitFor(() => expect(connectMedia).toHaveBeenCalledOnce());
    expect(await screen.findByTestId("resource-call-panel")).toBeInTheDocument();

    act(() =>
      socket.simulateMessage({
        type: "call.ringing",
        event_id: "00000000-0000-4000-8000-000000000405",
        target_type: "user",
        target_id: currentUserId,
        call,
      }),
    );

    expect(await screen.findByRole("dialog", { name: "Chamada recebida" })).toBeInTheDocument();
    expect(screen.getByTestId("resource-call-panel")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Recusar chamada de Ana Lima" }));

    await waitFor(() =>
      expect(declineCommands(socket)).toContainEqual({
        type: "call.decline",
        call_id: call.call_id,
      }),
    );
    // The decline must never tear down the active resource room's media:
    // stop() only ever ran for the resource room's own join, never again.
    expect(stopMedia).not.toHaveBeenCalled();
    expect(screen.getByTestId("resource-call-panel")).toBeInTheDocument();
  });

  it("achado A: accept() that never manages to send (preflight stuck open) leaves RF-24 completely untouched", async () => {
    const user = userEvent.setup();
    const socket = await renderWithJoinButtonReady();
    await joinResource(user, socket);
    await waitFor(() => expect(connectMedia).toHaveBeenCalledOnce());
    connectMedia.mockClear();

    act(() =>
      socket.simulateMessage({
        type: "call.ringing",
        event_id: "00000000-0000-4000-8000-000000000405",
        target_type: "user",
        target_id: currentUserId,
        call,
      }),
    );
    await screen.findByRole("button", {
      name: "Atender com câmera a chamada de vídeo de Ana Lima",
    });

    // The permission prompt never resolves: call.accept is never sent, so
    // RF-23 never reaches "active" and never asks for the Room.
    let resolvePermission!: (value: MediaPermissionResult) => void;
    vi.mocked(requestMediaPermission).mockReturnValueOnce(
      new Promise((resolve) => {
        resolvePermission = resolve;
      }),
    );
    await user.click(
      screen.getByRole("button", { name: "Atender com câmera a chamada de vídeo de Ana Lima" }),
    );

    expect(syncCommands(socket)).toHaveLength(1); // only the initial call.sync
    expect(stopMedia).not.toHaveBeenCalled();
    expect(connectMedia).not.toHaveBeenCalled();
    expect(screen.getByTestId("resource-call-panel")).toBeInTheDocument();

    // Cleanup: let the pending preflight resolve so the test doesn't leak a
    // dangling promise/timer into the next test.
    await act(async () => resolvePermission({ ok: true }));
  });

  it("achado B: a denied permission preflight leaves RF-24 active and never calls stop", async () => {
    const user = userEvent.setup();
    const socket = await renderWithJoinButtonReady();
    await joinResource(user, socket);
    await waitFor(() => expect(connectMedia).toHaveBeenCalledOnce());
    connectMedia.mockClear();

    act(() =>
      socket.simulateMessage({
        type: "call.ringing",
        event_id: "00000000-0000-4000-8000-000000000405",
        target_type: "user",
        target_id: currentUserId,
        call,
      }),
    );
    await screen.findByRole("button", {
      name: "Atender com câmera a chamada de vídeo de Ana Lima",
    });
    vi.mocked(requestMediaPermission).mockResolvedValueOnce({
      ok: false,
      kind: "permission_denied",
      message: "Permissão negada.",
    });

    await user.click(
      screen.getByRole("button", { name: "Atender com câmera a chamada de vídeo de Ana Lima" }),
    );

    await waitFor(() => expect(requestMediaPermission).toHaveBeenCalled());
    expect(syncCommands(socket)).toHaveLength(1);
    expect(stopMedia).not.toHaveBeenCalled();
    expect(connectMedia).not.toHaveBeenCalled();
    expect(screen.getByTestId("resource-call-panel")).toBeInTheDocument();
  });

  it("achado C: accepting only hands the Room to RF-23 once the server confirms active, never on the local accept() call alone", async () => {
    const user = userEvent.setup();
    const socket = await renderWithJoinButtonReady();
    await joinResource(user, socket);
    await waitFor(() => expect(connectMedia).toHaveBeenCalledOnce());
    expect(await screen.findByTestId("resource-call-panel")).toBeInTheDocument();
    connectMedia.mockClear();

    act(() =>
      socket.simulateMessage({
        type: "call.ringing",
        event_id: "00000000-0000-4000-8000-000000000405",
        target_type: "user",
        target_id: currentUserId,
        call,
      }),
    );
    await screen.findByRole("button", {
      name: "Atender com câmera a chamada de vídeo de Ana Lima",
    });

    await user.click(
      screen.getByRole("button", { name: "Atender com câmera a chamada de vídeo de Ana Lima" }),
    );

    // The command was accepted locally (preflight granted, call.accept sent)
    // but the server has not confirmed "active" yet: RF-24 must still be
    // intact at this point.
    await waitFor(() => expect(requestMediaPermission).toHaveBeenCalledWith("video"));
    expect(stopMedia).not.toHaveBeenCalled();
    expect(screen.getByTestId("resource-call-panel")).toBeInTheDocument();

    act(() =>
      socket.simulateMessage({
        type: "call.accepted",
        event_id: "00000000-0000-4000-8000-000000000407",
        target_type: "user",
        target_id: currentUserId,
        call: { ...call, status: "active", version: 2, accepted_at: call.occurred_at },
      }),
    );

    // leave() calls the real, unguarded media.stop() directly — this is the
    // ownership handoff, distinct from the guarded stop() RF-23 itself uses.
    await waitFor(() => expect(stopMedia).toHaveBeenCalledOnce());
    await waitFor(() => expect(connectMedia).toHaveBeenCalledOnce());
    expect(screen.queryByTestId("resource-call-panel")).not.toBeInTheDocument();
  });

  it("achado D: resource cleanup resolves before the direct call's media.connect() runs, never after", async () => {
    const user = userEvent.setup();
    const socket = await renderWithJoinButtonReady();
    await joinResource(user, socket);
    await waitFor(() => expect(connectMedia).toHaveBeenCalledOnce());
    connectMedia.mockClear();

    act(() =>
      socket.simulateMessage({
        type: "call.ringing",
        event_id: "00000000-0000-4000-8000-000000000405",
        target_type: "user",
        target_id: currentUserId,
        call,
      }),
    );
    await screen.findByRole("button", {
      name: "Atender com câmera a chamada de vídeo de Ana Lima",
    });
    await user.click(
      screen.getByRole("button", { name: "Atender com câmera a chamada de vídeo de Ana Lima" }),
    );
    await waitFor(() => expect(requestMediaPermission).toHaveBeenCalledWith("video"));

    let resolveStop!: () => void;
    stopMedia.mockReturnValueOnce(
      new Promise<undefined>((resolve) => {
        resolveStop = () => resolve(undefined);
      }),
    );
    act(() =>
      socket.simulateMessage({
        type: "call.accepted",
        event_id: "00000000-0000-4000-8000-000000000407",
        target_type: "user",
        target_id: currentUserId,
        call: { ...call, status: "active", version: 2, accepted_at: call.occurred_at },
      }),
    );

    await waitFor(() => expect(stopMedia).toHaveBeenCalledOnce());
    // stop() is still pending: connect() must not have run yet.
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(connectMedia).not.toHaveBeenCalled();

    await act(async () => {
      resolveStop();
      await Promise.resolve();
    });

    await waitFor(() => expect(connectMedia).toHaveBeenCalledOnce());
  });

  it("achado 3 (round 2): Recusar stays enabled and sends call.decline with the right call_id while Atender's preflight is still pending", async () => {
    const user = userEvent.setup();
    const socket = await renderWithJoinButtonReady();
    await joinResource(user, socket);
    await waitFor(() => expect(connectMedia).toHaveBeenCalledOnce());
    expect(await screen.findByTestId("resource-call-panel")).toBeInTheDocument();

    act(() =>
      socket.simulateMessage({
        type: "call.ringing",
        event_id: "00000000-0000-4000-8000-000000000405",
        target_type: "user",
        target_id: currentUserId,
        call,
      }),
    );
    await screen.findByRole("button", {
      name: "Atender com câmera a chamada de vídeo de Ana Lima",
    });

    let resolvePermission!: (value: MediaPermissionResult) => void;
    vi.mocked(requestMediaPermission).mockReturnValueOnce(
      new Promise((resolve) => {
        resolvePermission = resolve;
      }),
    );
    await user.click(
      screen.getByRole("button", { name: "Atender com câmera a chamada de vídeo de Ana Lima" }),
    );
    await waitFor(() => expect(requestMediaPermission).toHaveBeenCalled());

    const recusar = screen.getByRole("button", { name: "Recusar chamada de Ana Lima" });
    expect(recusar).toBeEnabled();
    await user.click(recusar);

    expect(declineCommands(socket)).toContainEqual({
      type: "call.decline",
      call_id: call.call_id,
    });
    expect(stopMedia).not.toHaveBeenCalled();
    expect(screen.getByTestId("resource-call-panel")).toBeInTheDocument();

    // Cleanup: resolve the stale preflight so it doesn't leak into later tests.
    await act(async () => resolvePermission({ ok: true }));
  });

  it("shows the RF-23 dialog immediately once the call is confirmed active, even while its own token request is still pending", async () => {
    const user = userEvent.setup();
    const socket = await renderWithJoinButtonReady();
    await joinResource(user, socket);
    await waitFor(() => expect(connectMedia).toHaveBeenCalledOnce());
    expect(await screen.findByTestId("resource-call-panel")).toBeInTheDocument();
    connectMedia.mockClear();

    act(() =>
      socket.simulateMessage({
        type: "call.ringing",
        event_id: "00000000-0000-4000-8000-000000000405",
        target_type: "user",
        target_id: currentUserId,
        call,
      }),
    );
    await screen.findByRole("button", {
      name: "Atender com câmera a chamada de vídeo de Ana Lima",
    });
    await user.click(
      screen.getByRole("button", { name: "Atender com câmera a chamada de vídeo de Ana Lima" }),
    );
    await waitFor(() => expect(requestMediaPermission).toHaveBeenCalledWith("video"));

    const token = deferredValue<Awaited<ReturnType<typeof issueCallToken>>>();
    vi.mocked(issueCallToken).mockReturnValueOnce(token.promise);
    act(() =>
      socket.simulateMessage({
        type: "call.accepted",
        event_id: "00000000-0000-4000-8000-000000000407",
        target_type: "user",
        target_id: currentUserId,
        call: { ...call, status: "active", version: 2, accepted_at: call.occurred_at },
      }),
    );

    await waitFor(() => expect(stopMedia).toHaveBeenCalledOnce());
    // The token request hasn't resolved yet — RF-23 must already be showing,
    // never hidden behind the resource room while its own media is pending.
    expect(screen.queryByTestId("resource-call-panel")).not.toBeInTheDocument();
    expect(screen.getByLabelText("Chamada com Ana Lima")).toBeInTheDocument();
    expect(connectMedia).not.toHaveBeenCalled();

    await act(async () => {
      token.resolve({
        token: "media-token",
        expiresAt: "2026-08-03T12:05:00Z",
        serverUrl: "wss://livekit-dev.nic-labs.com",
      });
      await token.promise;
    });

    await waitFor(() => expect(connectMedia).toHaveBeenCalledOnce());
  });

  it("keeps the RF-23 dialog visible with a recoverable error when the handoff's RF-24 cleanup fails, never falling back to the resource room", async () => {
    const user = userEvent.setup();
    const socket = await renderWithJoinButtonReady();
    await joinResource(user, socket);
    await waitFor(() => expect(connectMedia).toHaveBeenCalledOnce());
    expect(await screen.findByTestId("resource-call-panel")).toBeInTheDocument();
    connectMedia.mockClear();

    act(() =>
      socket.simulateMessage({
        type: "call.ringing",
        event_id: "00000000-0000-4000-8000-000000000405",
        target_type: "user",
        target_id: currentUserId,
        call,
      }),
    );
    await screen.findByRole("button", {
      name: "Atender com câmera a chamada de vídeo de Ana Lima",
    });
    await user.click(
      screen.getByRole("button", { name: "Atender com câmera a chamada de vídeo de Ana Lima" }),
    );
    await waitFor(() => expect(requestMediaPermission).toHaveBeenCalledWith("video"));

    stopMedia.mockRejectedValueOnce(new Error("cleanup failed"));
    act(() =>
      socket.simulateMessage({
        type: "call.accepted",
        event_id: "00000000-0000-4000-8000-000000000407",
        target_type: "user",
        target_id: currentUserId,
        call: { ...call, status: "active", version: 2, accepted_at: call.occurred_at },
      }),
    );

    await waitFor(() => expect(stopMedia).toHaveBeenCalledOnce());
    vi.mocked(issueCallToken).mockClear();
    expect(issueCallToken).not.toHaveBeenCalled();
    expect(connectMedia).not.toHaveBeenCalled();
    // RF-23 stays visible and recoverable — never silently reverts to RF-24
    // just because the room it was trying to take over never actually left.
    expect(screen.queryByTestId("resource-call-panel")).not.toBeInTheDocument();
    await waitFor(() => expect(screen.getByLabelText("Chamada com Ana Lima")).toBeInTheDocument());
    await waitFor(() =>
      expect(
        within(screen.getByTestId("floating-call-window")).getByRole("alert"),
      ).toBeInTheDocument(),
    );
  });

  it("Caso C: joining a resource room stays unavailable while a direct call is ringing/active", async () => {
    const socket = await renderWithJoinButtonReady();
    act(() =>
      socket.simulateMessage({
        type: "call.ringing",
        event_id: "00000000-0000-4000-8000-000000000405",
        target_type: "user",
        target_id: currentUserId,
        call,
      }),
    );

    expect(await screen.findByRole("button", { name: "Entrar no canal" })).toBeDisabled();
  });

  it("keeps both a second resource join and a direct start unavailable while a resource call is active", async () => {
    const user = userEvent.setup();
    const socket = await renderWithJoinButtonReady();

    await joinResource(user, socket);
    await waitFor(() => expect(connectMedia).toHaveBeenCalledOnce());

    expect(screen.getByRole("button", { name: "Entrar no canal" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Ligar direto" })).toBeDisabled();
  });
});

// ── issue #594 adversarial follow-up, round 3: ChatShell's own fresh-join
// button (target.callId undefined — the server decides/reuses the call_id)
// is the primary real-world entry point for this bug, and previously called
// resourceCall.join() directly, bypassing BOTH the causal race protection
// AND beginResourceParticipation — meaning a join through this exact button
// never registered a ParticipationToken or broadcast "participating" at
// all. Drives the REAL CallSessionProvider/ownership coordinator end to
// end, including a genuine second BroadcastChannel instance standing in for
// another tab, since this file deliberately never mocks callOwnership.ts. ──

describe("ChatShell RF-24 fresh join — issue #594 adversarial follow-up (round 3)", () => {
  it("joins through the protected mechanism: registers a real ParticipationToken (previously never happened at all), and an old 'left' for the call_id the server reuses never aborts the in-flight join", async () => {
    const user = userEvent.setup();
    const socket = await renderWithJoinButtonReady();
    const reusedCallId = "00000000-0000-4000-8000-000000000550";
    const otherWriterId = "00000000-0000-4000-8000-000000009001";
    // Seeds the SAME shared, per-writer storage allocateParticipationGeneration
    // itself reads (issue #570 follow-up design) — mirrors what the OLD
    // participation's own real allocateParticipationGeneration() call would
    // already have written. Without this, the fresh join's own real
    // allocation (which reads this SAME storage) would independently also
    // land on generation 1, and the writerId tie-break in
    // compareParticipationTokens could go either way — never a realistic
    // simulation of "the storage already has a real, older generation on
    // record".
    localStorage.setItem(
      `nchat.call.participation.v2.${encodeURIComponent(reusedCallId)}:${otherWriterId}`,
      JSON.stringify({ v: 2, generation: 1 }),
    );

    let resolveConnect!: () => void;
    connectMedia.mockReturnValueOnce(
      new Promise<{ microphone: boolean; camera: boolean }>((resolve) => {
        resolveConnect = () => resolve({ microphone: true, camera: true });
      }),
    );

    const otherTab = new BroadcastChannel("nchat-call-ownership-v1");
    const receivedFromProvider: Array<Record<string, unknown>> = [];
    otherTab.onmessage = (event: MessageEvent) => {
      receivedFromProvider.push(event.data as Record<string, unknown>);
    };
    try {
      // Click through call.start -> call.accepted: target.callId was
      // undefined at the click, and is only now, synchronously inside
      // join(), resolved to reusedCallId — issueResourceCallToken (mocked)
      // resolves immediately after, leaving media.connect() as the one
      // genuinely still-pending step. Waiting for connectMedia's own call
      // (not just joinResource()'s return) is what makes this deterministic
      // — it can only have been reached after onCallIdResolved already ran.
      // (resource-call-panel itself is not a useful "still connecting"
      // signal here: it shows as soon as callId resolves, well before
      // media.connect() itself settles.)
      await joinResource(user, socket);
      await waitFor(() => expect(connectMedia).toHaveBeenCalledOnce());

      // An OLD participation for this EXACT call_id — the server reused it
      // for a brand-new join, exactly the scenario the ordering guard
      // cannot yet know differs from "the same one" — genuinely ended in
      // another tab. Its "left" (generation 1) arrives now, posted on a
      // real second BroadcastChannel instance the same way another tab's
      // coordinator would.
      act(() => {
        otherTab.postMessage({
          v: 1,
          type: "left",
          callId: reusedCallId,
          tabId: otherWriterId,
          epoch: 3,
          generation: 1,
          writerId: otherWriterId,
          sequence: 1,
        });
      });
      // Real BroadcastChannel dispatch is a genuine task, not a microtask —
      // let it actually run.
      await act(async () => {
        await new Promise((resolve) => setTimeout(resolve, 0));
      });

      // The in-flight join must not have been aborted by it: letting
      // media.connect() finally resolve still reaches "active" and shows
      // the panel — an aborted attempt would instead leave resource.status
      // stuck at "idle" (convergeRemoteLeave) with nothing left to await.
      resolveConnect();
      expect(await screen.findByTestId("resource-call-panel")).toBeInTheDocument();

      // The new participation is registered for real — generation 2, since
      // the shared storage already recorded generation 1 for this call_id
      // (issue #594 adversarial follow-up: was NEVER broadcast at all
      // before this fix, since resourceCall.join() bypassed
      // beginResourceParticipation entirely).
      await waitFor(() =>
        expect(receivedFromProvider).toContainEqual(
          expect.objectContaining({
            type: "participating",
            callId: reusedCallId,
            generation: 2,
            sequence: 0,
          }),
        ),
      );
    } finally {
      otherTab.close();
    }
  });
});

// ── issue #642 review, blocker 5: onLeave must swallow a rejection from
// leaveResourceParticipation (== CallSessionProvider's endResourceParticipation,
// which deliberately rethrows on failure — the error is already reflected
// through resource.status/resource.error, the existing retry authority).
// useCallSession is mocked ONLY for this one test (vi.doMock, not the
// hoisted vi.mock every other test in this file relies on the real
// CallSessionProvider for) so the rest of the suite is untouched. ──

// Deterministically proves whether a CALLER attached a rejection handler to
// the promise, without depending on a test runner/environment's own
// unhandled-rejection reporting (unreliable here — Vitest under this pool
// never surfaces it). Shadows .catch as an OWN property on each produced
// instance only — Promise.prototype and every other promise are untouched.
// The rejection is built lazily, INSIDE make() — never eagerly up front —
// so it comes into existence in the exact same tick the real async
// endResourceParticipation's returned promise would: the moment the mock is
// actually called, matching production's `fn().catch(...)` chaining
// exactly and avoiding Node's unrelated "handled asynchronously" warning a
// pre-built, only-later-caught promise would otherwise trigger.
function trackedRejectionFactory(error: Error) {
  let caught = false;
  const make = () => {
    const promise = Promise.reject(error);
    const originalCatch = promise.catch.bind(promise);
    Object.defineProperty(promise, "catch", {
      value: (...args: Parameters<typeof originalCatch>) => {
        caught = true;
        return originalCatch(...args);
      },
    });
    return promise;
  };
  return { make, wasCaught: () => caught };
}

describe("ChatShell — #642 review, blocker 5 (leave rejection)", () => {
  afterEach(() => {
    vi.doUnmock("../calls/CallSessionProvider");
    vi.doUnmock("./useChatSidebar");
    vi.doUnmock("./ChatSidebar");
    vi.resetModules();
  });

  it("calls .catch() on leaveResourceParticipation's rejection, and performs no false local cleanup", async () => {
    // ChatShell (and transitively CallSessionProvider) is already cached in
    // this file's module graph from the static import at the top — the
    // cache must be cleared BEFORE doMock + the dynamic import below, or
    // the fresh import still resolves to the already-evaluated real module.
    vi.resetModules();
    const { make: makeRejectedLeave, wasCaught } = trackedRejectionFactory(
      new Error("leave failed"),
    );
    const leaveResourceParticipation = vi.fn(() => makeRejectedLeave());
    const resourcePresentationCall = {
      call_id: "call-1",
      request_id: "req-1",
      caller_id: currentUserId,
      callee_id: "",
      target_type: "channel" as const,
      target_id: "chan-1",
      call_type: "audio" as const,
      status: "active" as const,
      version: 1,
      created_at: "2024-01-01T12:00:00.000Z",
      occurred_at: "2024-01-01T12:00:00.000Z",
      expires_at: "2024-01-01T13:00:00.000Z",
    };
    vi.doMock("../calls/CallSessionProvider", () => ({
      useCallSession: () => ({
        calls: { call: null, start: vi.fn() },
        resource: {
          active: { kind: "channel", id: "chan-1", name: "Geral" },
          callId: "call-1",
          status: "active",
          error: null,
        },
        joinResourceParticipation: vi.fn(),
        registerDirectory: vi.fn(),
        registerIdentity: vi.fn(),
        getResourceCall: vi.fn(() => null),
        media: {
          participants: [],
          activeSpeakerId: null,
          microphoneEnabled: true,
          pendingControl: null,
          toggleMicrophone: vi.fn(),
        },
        expand: vi.fn(),
        leaveResourceParticipation,
        localIdentity: { name: "Você", initials: "V" },
        resourcePresentationCall,
      }),
    }));
    vi.doMock("./useChatSidebar", () => ({
      useChatSidebar: () => ({
        state: { status: "ready", currentUserId, channels: [], dms: [] },
        retry: vi.fn(),
        setPinned: vi.fn(),
        markRead: vi.fn(),
        renameChannel: vi.fn(),
      }),
    }));
    // Partial: only the component is stubbed out. ChatSidebar also exports
    // chatNavigationId, which the shell's navigation toggle points
    // `aria-controls` at (issue #467) — replacing the whole module would take
    // that named export with it and break rendering for a reason that has
    // nothing to do with what this test is about.
    vi.doMock("./ChatSidebar", async () => {
      const actual = await vi.importActual<typeof import("./ChatSidebar")>("./ChatSidebar");
      return { ...actual, default: () => null };
    });

    const { default: ChatShellFresh } = await import("./ChatShell");
    const { default: AppShellFresh } = await import("./AppShell");

    function LeaveProbe() {
      const ctx = useOutletContext<ChatOutletContext>();
      return (
        <button type="button" onClick={() => ctx.resourceCallSession?.onLeave()}>
          Sair da chamada
        </button>
      );
    }

    render(
      <MemoryRouter initialEntries={["/chat"]}>
        <Routes>
          <Route element={<AppShellFresh />}>
            <Route path="/chat" element={<ChatShellFresh />}>
              <Route index element={<LeaveProbe />} />
            </Route>
          </Route>
        </Routes>
      </MemoryRouter>,
    );

    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Sair da chamada" }));

    await waitFor(() => expect(leaveResourceParticipation).toHaveBeenCalledOnce());
    await waitFor(() => expect(wasCaught()).toBe(true));
    // #642's onLeave never owned any local state to falsely clean up in the
    // first place — the error stays entirely CallSessionProvider's own
    // resource.status/resource.error retry authority.
  });

  it("wires resourceCallSession's onToggleMicrophone to media.toggleMicrophone() and onOpenFullCall to expand()", async () => {
    vi.resetModules();
    const toggleMicrophone = vi.fn();
    const expand = vi.fn();
    const resourcePresentationCall = {
      call_id: "call-2",
      request_id: "req-2",
      caller_id: currentUserId,
      callee_id: "",
      target_type: "channel" as const,
      target_id: "chan-1",
      call_type: "audio" as const,
      status: "active" as const,
      version: 1,
      created_at: "2024-01-01T12:00:00.000Z",
      occurred_at: "2024-01-01T12:00:00.000Z",
      expires_at: "2024-01-01T13:00:00.000Z",
    };
    vi.doMock("../calls/CallSessionProvider", () => ({
      useCallSession: () => ({
        calls: { call: null, start: vi.fn() },
        resource: {
          active: { kind: "channel", id: "chan-1", name: "Geral" },
          callId: "call-2",
          status: "active",
          error: null,
        },
        joinResourceParticipation: vi.fn(),
        registerDirectory: vi.fn(),
        registerIdentity: vi.fn(),
        getResourceCall: vi.fn(() => null),
        media: {
          participants: [],
          activeSpeakerId: null,
          microphoneEnabled: true,
          pendingControl: null,
          toggleMicrophone,
        },
        expand,
        leaveResourceParticipation: vi.fn(),
        localIdentity: { name: "Você", initials: "V" },
        resourcePresentationCall,
      }),
    }));
    vi.doMock("./useChatSidebar", () => ({
      useChatSidebar: () => ({
        state: { status: "ready", currentUserId, channels: [], dms: [] },
        retry: vi.fn(),
        setPinned: vi.fn(),
        markRead: vi.fn(),
        renameChannel: vi.fn(),
      }),
    }));
    vi.doMock("./ChatSidebar", async () => {
      const actual = await vi.importActual<typeof import("./ChatSidebar")>("./ChatSidebar");
      return { ...actual, default: () => null };
    });

    const { default: ChatShellFresh } = await import("./ChatShell");
    const { default: AppShellFresh } = await import("./AppShell");

    function ResourceSessionProbe() {
      const ctx = useOutletContext<ChatOutletContext>();
      return (
        <>
          <button type="button" onClick={() => ctx.resourceCallSession?.onToggleMicrophone()}>
            Alternar microfone
          </button>
          <button type="button" onClick={() => ctx.resourceCallSession?.onOpenFullCall()}>
            Abrir chamada
          </button>
        </>
      );
    }

    render(
      <MemoryRouter initialEntries={["/chat"]}>
        <Routes>
          <Route element={<AppShellFresh />}>
            <Route path="/chat" element={<ChatShellFresh />}>
              <Route index element={<ResourceSessionProbe />} />
            </Route>
          </Route>
        </Routes>
      </MemoryRouter>,
    );

    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Alternar microfone" }));
    expect(toggleMicrophone).toHaveBeenCalledOnce();
    await user.click(screen.getByRole("button", { name: "Abrir chamada" }));
    expect(expand).toHaveBeenCalledOnce();
  });
});

// ── issue #673: directCallSession is derived from directPresentationCall,
// mirroring resourceCallSession's own derivation exactly. useCallSession is
// mocked ONLY for these tests (vi.doMock, not the hoisted vi.mock every
// other test in this file relies on the real CallSessionProvider for) so the
// rest of the suite is untouched. ──

describe("ChatShell — #673 directCallSession derivation", () => {
  afterEach(() => {
    vi.doUnmock("../calls/CallSessionProvider");
    vi.doUnmock("./useChatSidebar");
    vi.doUnmock("./ChatSidebar");
    vi.resetModules();
  });

  function mockUseCallSession(overrides: Record<string, unknown> = {}) {
    vi.doMock("../calls/CallSessionProvider", () => ({
      useCallSession: () => ({
        calls: { call: null, start: vi.fn(), end: vi.fn() },
        resource: { active: null, callId: null, status: "idle", error: null },
        joinResourceParticipation: vi.fn(),
        registerDirectory: vi.fn(),
        registerIdentity: vi.fn(),
        getResourceCall: vi.fn(() => null),
        media: {
          participants: [],
          activeSpeakerId: null,
          microphoneEnabled: true,
          pendingControl: null,
          toggleMicrophone: vi.fn(),
        },
        expand: vi.fn(),
        leaveResourceParticipation: vi.fn(),
        localIdentity: { name: "Você", initials: "V" },
        resourcePresentationCall: null,
        directPresentationCall: null,
        ...overrides,
      }),
    }));
    vi.doMock("./useChatSidebar", () => ({
      useChatSidebar: () => ({
        state: { status: "ready", currentUserId, channels: [], dms: [] },
        retry: vi.fn(),
        setPinned: vi.fn(),
        markRead: vi.fn(),
        renameChannel: vi.fn(),
      }),
    }));
    vi.doMock("./ChatSidebar", async () => {
      const actual = await vi.importActual<typeof import("./ChatSidebar")>("./ChatSidebar");
      return { ...actual, default: () => null };
    });
  }

  function DirectSessionProbe() {
    const ctx = useOutletContext<ChatOutletContext>();
    if (!ctx.directCallSession) return <span>ausente</span>;
    return (
      <>
        <span data-testid="direct-call-id">{ctx.directCallSession.callId}</span>
        <span data-testid="direct-call-type">{ctx.directCallSession.callType}</span>
        <span data-testid="direct-peer-id">{ctx.directCallSession.peerUserId}</span>
        <span data-testid="direct-mic-enabled">
          {String(ctx.directCallSession.microphoneEnabled)}
        </span>
        <button type="button" onClick={() => ctx.directCallSession?.onLeave()}>
          Sair da chamada
        </button>
        <button type="button" onClick={() => ctx.directCallSession?.onOpenFullCall()}>
          Abrir chamada
        </button>
        <button type="button" onClick={() => ctx.directCallSession?.onToggleMicrophone()}>
          Alternar microfone
        </button>
      </>
    );
  }

  async function renderWithDirectSessionProbe() {
    const { default: ChatShellFresh } = await import("./ChatShell");
    const { default: AppShellFresh } = await import("./AppShell");
    return render(
      <MemoryRouter initialEntries={["/chat"]}>
        <Routes>
          <Route element={<AppShellFresh />}>
            <Route path="/chat" element={<ChatShellFresh />}>
              <Route index element={<DirectSessionProbe />} />
            </Route>
          </Route>
        </Routes>
      </MemoryRouter>,
    );
  }

  it("exposes no directCallSession when directPresentationCall is null (e.g. only ringing)", async () => {
    vi.resetModules();
    mockUseCallSession({ directPresentationCall: null });

    await renderWithDirectSessionProbe();

    expect(await screen.findByText("ausente")).toBeInTheDocument();
  });

  it("derives directCallSession from directPresentationCall, resolving peerUserId as the OTHER party", async () => {
    vi.resetModules();
    const directPresentationCall = {
      call_id: "call-direct-1",
      request_id: "req-1",
      caller_id: currentUserId,
      callee_id: "user-jl",
      target_type: "user" as const,
      call_type: "video" as const,
      status: "active" as const,
      version: 1,
      created_at: "2024-01-01T12:00:00.000Z",
      occurred_at: "2024-01-01T12:00:00.000Z",
      expires_at: "2024-01-01T13:00:00.000Z",
    };
    mockUseCallSession({ directPresentationCall });

    await renderWithDirectSessionProbe();

    expect(await screen.findByTestId("direct-call-id")).toHaveTextContent("call-direct-1");
    expect(screen.getByTestId("direct-call-type")).toHaveTextContent("video");
    // The local user (currentUserId) is the caller — the peer is the callee.
    expect(screen.getByTestId("direct-peer-id")).toHaveTextContent("user-jl");
    expect(screen.getByTestId("direct-mic-enabled")).toHaveTextContent("true");
  });

  it("resolves peerUserId as the caller when the local user is the callee", async () => {
    vi.resetModules();
    mockUseCallSession({
      directPresentationCall: {
        call_id: "call-direct-2",
        request_id: "req-2",
        caller_id: "user-jl",
        callee_id: currentUserId,
        target_type: "user" as const,
        call_type: "audio" as const,
        status: "active" as const,
        version: 1,
        created_at: "2024-01-01T12:00:00.000Z",
        occurred_at: "2024-01-01T12:00:00.000Z",
        expires_at: "2024-01-01T13:00:00.000Z",
      },
    });

    await renderWithDirectSessionProbe();

    expect(await screen.findByTestId("direct-peer-id")).toHaveTextContent("user-jl");
  });

  it("wires onLeave to calls.end() and onOpenFullCall to expand() — the same authoritative actions FloatingCallWindow uses", async () => {
    vi.resetModules();
    const end = vi.fn();
    const expand = vi.fn();
    mockUseCallSession({
      calls: { call: null, start: vi.fn(), end },
      expand,
      directPresentationCall: {
        call_id: "call-direct-3",
        request_id: "req-3",
        caller_id: currentUserId,
        callee_id: "user-jl",
        target_type: "user" as const,
        call_type: "audio" as const,
        status: "active" as const,
        version: 1,
        created_at: "2024-01-01T12:00:00.000Z",
        occurred_at: "2024-01-01T12:00:00.000Z",
        expires_at: "2024-01-01T13:00:00.000Z",
      },
    });

    await renderWithDirectSessionProbe();
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Sair da chamada" }));
    expect(end).toHaveBeenCalledOnce();
    await user.click(screen.getByRole("button", { name: "Abrir chamada" }));
    expect(expand).toHaveBeenCalledOnce();
  });

  it("wires onToggleMicrophone to media.toggleMicrophone()", async () => {
    vi.resetModules();
    const toggleMicrophone = vi.fn();
    mockUseCallSession({
      media: {
        participants: [],
        activeSpeakerId: null,
        microphoneEnabled: true,
        pendingControl: null,
        toggleMicrophone,
      },
      directPresentationCall: {
        call_id: "call-direct-4",
        request_id: "req-4",
        caller_id: currentUserId,
        callee_id: "user-jl",
        target_type: "user" as const,
        call_type: "audio" as const,
        status: "active" as const,
        version: 1,
        created_at: "2024-01-01T12:00:00.000Z",
        occurred_at: "2024-01-01T12:00:00.000Z",
        expires_at: "2024-01-01T13:00:00.000Z",
      },
    });

    await renderWithDirectSessionProbe();
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Alternar microfone" }));
    expect(toggleMicrophone).toHaveBeenCalledOnce();
  });
});

/**
 * ISSUE #527 (code review) — leaving the conversation that is on screen.
 *
 * The row disappearing is not the whole of it: the route still names the
 * conversation, and the details panel opened from its menu still has it as a
 * subject. Both are derived from the canonical list, so both must stop pointing
 * at a conversation this user is no longer in.
 */
describe("ChatShell — leaving the conversation on screen", () => {
  const readingId = "00000000-0000-4000-8000-0000000005a1";
  const otherId = "00000000-0000-4000-8000-0000000005a2";

  function renderShellAt(path: string) {
    return render(
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          <Route element={<AppShell />}>
            <Route
              path="/chat"
              element={
                <CallSessionProvider>
                  <ChatShell />
                </CallSessionProvider>
              }
            >
              <Route index element={<div>vazio</div>} />
              <Route path="channel/:channelId" element={<div>mensagens</div>} />
            </Route>
          </Route>
        </Routes>
      </MemoryRouter>,
    );
  }

  function sidebarWith(channels: { id: string; name: string }[]) {
    return {
      currentUserId,
      workspaceId: "workspace-1",
      channels: channels.map((channel) => ({
        ...channel,
        type: "public" as const,
        canWrite: true,
      })),
      dms: [],
      categories: [],
    };
  }

  beforeEach(() => {
    vi.mocked(leaveConversation).mockResolvedValue(undefined);
  });

  // The panel's subject is the row whose menu was used, which may be a
  // conversation other than the one being read. Leaving *that* one changes no
  // route at all, so the pathname guard cannot help: what closes the panel is
  // the conversation no longer being in the canonical list.
  it("closes a details panel for another conversation when that one is left", async () => {
    const user = userEvent.setup();
    vi.mocked(fetchSidebarData).mockResolvedValue(
      sidebarWith([
        { id: readingId, name: "Plataforma" },
        { id: otherId, name: "Infra" },
      ]),
    );
    renderShellAt(`/chat/channel/${readingId}`);
    await screen.findByRole("option", { name: /Infra/ });

    await user.click(screen.getByRole("button", { name: "Mais opções para canal Infra" }));
    await user.click(screen.getByRole("menuitem", { name: "Detalhes do canal" }));
    expect(screen.getByTestId("sidebar-details")).toHaveTextContent(`channel:${otherId}`);

    // The refetch that follows the departure no longer carries the channel,
    // which is what makes the panel's subject stop existing.
    vi.mocked(fetchSidebarData).mockResolvedValue(
      sidebarWith([{ id: readingId, name: "Plataforma" }]),
    );
    await user.click(screen.getByRole("button", { name: "Mais opções para canal Infra" }));
    await user.click(screen.getByRole("menuitem", { name: "Sair do canal" }));
    // The confirm button and the menu item share a label, so it is taken from
    // inside the dialog.
    await user.click(
      within(screen.getByRole("dialog")).getByRole("button", { name: "Sair do canal" }),
    );

    await waitFor(() => expect(screen.queryByTestId("sidebar-details")).not.toBeInTheDocument());
    expect(leaveConversation).toHaveBeenCalledWith("channel", otherId);
    // Nothing navigated: the conversation being read was not the one left.
    expect(screen.getByText("mensagens")).toBeInTheDocument();
  });

  it("returns to the neutral route when the conversation being read is left", async () => {
    const user = userEvent.setup();
    vi.mocked(fetchSidebarData).mockResolvedValue(
      sidebarWith([{ id: readingId, name: "Plataforma" }]),
    );
    renderShellAt(`/chat/channel/${readingId}`);
    await screen.findByRole("option", { name: /Plataforma/ });

    vi.mocked(fetchSidebarData).mockResolvedValue(sidebarWith([]));
    await user.click(screen.getByRole("button", { name: "Mais opções para canal Plataforma" }));
    await user.click(screen.getByRole("menuitem", { name: "Sair do canal" }));
    await user.click(
      within(screen.getByRole("dialog")).getByRole("button", { name: "Sair do canal" }),
    );

    await waitFor(() => expect(screen.getByText("vazio")).toBeInTheDocument());
    expect(leaveConversation).toHaveBeenCalledWith("channel", readingId);
  });
});

// AppShell's resolveDetailsTarget resolves a row menu's "details" target
// through the sidebar's own dms list — exercised so far only for channels
// above. A DM row (not a group) must resolve to the panel's "direct" kind.
describe("ChatShell — abre detalhes de uma conversa direta", () => {
  const dmId = "00000000-0000-4000-8000-0000000005d1";

  it("resolve o alvo de uma DM 1:1 como 'direct'", async () => {
    const user = userEvent.setup();
    vi.mocked(fetchSidebarData).mockResolvedValue({
      currentUserId,
      workspaceId: "workspace-1",
      channels: [],
      dms: [
        {
          id: dmId,
          type: "1:1",
          name: "Juliane Lino",
          participants: [],
          counterpart: { userId: callerId, displayName: "Juliane Lino" },
        },
      ],
      categories: [],
    });
    render(
      <MemoryRouter initialEntries={["/chat"]}>
        <Routes>
          <Route element={<AppShell />}>
            <Route
              path="/chat"
              element={
                <CallSessionProvider>
                  <ChatShell />
                </CallSessionProvider>
              }
            >
              <Route index element={<div>vazio</div>} />
            </Route>
          </Route>
        </Routes>
      </MemoryRouter>,
    );
    await screen.findByRole("option", { name: /Juliane Lino/ });

    await user.click(
      screen.getByRole("button", { name: "Mais opções para conversa com Juliane Lino" }),
    );
    await user.click(screen.getByRole("menuitem", { name: "Detalhes da conversa" }));

    expect(screen.getByTestId("sidebar-details")).toHaveTextContent(`direct:${dmId}`);
  });
});

/**
 * ISSUE #467 — the navigation is a column on wide viewports and a drawer below
 * them. The composition itself is CSS; what is asserted here is the behaviour a
 * stylesheet cannot carry: when the drawer is modal, what closes it, where focus
 * goes, and that changing width is a change of composition and never a remount.
 */
describe("ChatShell — trava de rolagem do documento", () => {
  const readingId = "00000000-0000-4000-8000-0000000005c1";

  function renderShell() {
    return render(
      <MemoryRouter initialEntries={[`/chat/channel/${readingId}`]}>
        <Routes>
          <Route element={<AppShell />}>
            <Route
              path="/chat"
              element={
                <CallSessionProvider>
                  <ChatShell />
                </CallSessionProvider>
              }
            >
              <Route path="channel/:channelId" element={<div>mensagens</div>} />
            </Route>
          </Route>
        </Routes>
      </MemoryRouter>,
    );
  }

  beforeEach(() => {
    vi.mocked(fetchSidebarData).mockResolvedValue({
      currentUserId,
      workspaceId: "workspace-1",
      channels: [{ id: readingId, name: "Plataforma", type: "public" as const, canWrite: true }],
      dms: [],
      categories: [],
    });
  });

  it("trava o documento enquanto está montado e devolve a rolagem ao desmontar", async () => {
    const { unmount } = renderShell();
    await screen.findByRole("option", { name: /Plataforma/ });

    expect(document.documentElement).toHaveClass(ROOT_LOCK_CLASS);
    expect(document.body).toHaveClass(ROOT_LOCK_CLASS);

    unmount();

    // The cleanup is the whole point: a class left on <html> would follow the
    // user to login, profile and admin, which are ordinary scrolling documents.
    expect(document.documentElement).not.toHaveClass(ROOT_LOCK_CLASS);
    expect(document.body).not.toHaveClass(ROOT_LOCK_CLASS);
  });

  it("normaliza a rolagem do documento ao montar", async () => {
    // `overflow: hidden` freezes whatever scroll position it finds, so arriving
    // from a scrolled route has to be reset rather than merely locked. Asserted
    // through the browser API the shell calls, not through the class it adds.
    const scrollTo = vi.spyOn(window, "scrollTo").mockImplementation(() => undefined);

    renderShell();
    await screen.findByRole("option", { name: /Plataforma/ });

    expect(scrollTo).toHaveBeenCalledWith(0, 0);
    scrollTo.mockRestore();
  });
});

describe("ChatShell — navegação responsiva", () => {
  const readingId = "00000000-0000-4000-8000-0000000005b1";
  const otherId = "00000000-0000-4000-8000-0000000005b2";

  /**
   * A MediaQueryList stand-in: jsdom does not implement matchMedia, so without
   * this every query simply never matches — which is exactly the wide-viewport
   * answer the tests that omit it rely on.
   */
  function stubViewport(startsAsDrawer: boolean) {
    let drawer = startsAsDrawer;
    const listeners = new Set<() => void>();
    window.matchMedia = ((query: string) => ({
      get matches() {
        return query === NAV_DRAWER_QUERY && drawer;
      },
      media: query,
      addEventListener: (_event: string, callback: () => void) => void listeners.add(callback),
      removeEventListener: (_event: string, callback: () => void) =>
        void listeners.delete(callback),
    })) as unknown as typeof window.matchMedia;
    return {
      resizeTo(nextIsDrawer: boolean) {
        drawer = nextIsDrawer;
        act(() => listeners.forEach((callback) => callback()));
      },
    };
  }

  function renderShellAt(path: string) {
    return render(
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          <Route element={<AppShell />}>
            <Route
              path="/chat"
              element={
                <CallSessionProvider>
                  <ChatShell />
                </CallSessionProvider>
              }
            >
              <Route index element={<div>vazio</div>} />
              <Route path="channel/:channelId" element={<div>mensagens</div>} />
            </Route>
          </Route>
        </Routes>
      </MemoryRouter>,
    );
  }

  function sidebarWithTwoChannels() {
    return {
      currentUserId,
      workspaceId: "workspace-1",
      channels: [
        { id: readingId, name: "Plataforma", type: "public" as const, canWrite: true },
        { id: otherId, name: "Infra", type: "public" as const, canWrite: true },
      ],
      dms: [],
      categories: [],
    };
  }

  const toggle = () => screen.getByRole("button", { name: "Conversas" });
  const main = () => screen.getByRole("main");

  beforeEach(() => {
    vi.mocked(fetchSidebarData).mockResolvedValue(sidebarWithTwoChannels());
  });

  afterEach(() => {
    // @ts-expect-error -- jsdom does not define this by default; restore that.
    delete window.matchMedia;
  });

  it("mantém a conversa interativa ao abrir a navegação em coluna", async () => {
    const user = userEvent.setup();
    renderShellAt(`/chat/channel/${readingId}`);
    await screen.findByRole("option", { name: /Plataforma/ });

    await user.click(toggle());

    // The disclosure still reports itself open — the sidebar is simply always on
    // screen at this width, so nothing about the conversation changes.
    expect(screen.getByRole("button", { name: "Fechar conversas" })).toHaveAttribute(
      "aria-expanded",
      "true",
    );
    expect(main()).not.toHaveAttribute("inert");
    expect(screen.queryByTestId("chat-nav-backdrop")).not.toBeInTheDocument();
  });

  it("abre a navegação como camada modal em larguras de drawer", async () => {
    stubViewport(true);
    const user = userEvent.setup();
    renderShellAt(`/chat/channel/${readingId}`);
    await screen.findByRole("option", { name: /Plataforma/ });

    expect(toggle()).toHaveAttribute("aria-expanded", "false");
    expect(toggle()).toHaveAttribute("aria-controls", "chat-navigation");
    expect(screen.getByTestId("chat-sidebar")).toHaveAttribute("id", "chat-navigation");
    expect(main()).not.toHaveAttribute("inert");

    await user.click(toggle());

    expect(screen.getByTestId("chat-shell")).toHaveAttribute("data-nav-open", "true");
    expect(main()).toHaveAttribute("inert");
    expect(screen.getByTestId("chat-nav-backdrop")).toBeInTheDocument();
  });

  it("fecha com Escape e devolve o foco ao acionador", async () => {
    stubViewport(true);
    const user = userEvent.setup();
    renderShellAt(`/chat/channel/${readingId}`);
    await screen.findByRole("option", { name: /Plataforma/ });

    await user.click(toggle());
    await user.keyboard("{Escape}");

    expect(screen.getByTestId("chat-shell")).not.toHaveAttribute("data-nav-open");
    expect(main()).not.toHaveAttribute("inert");
    expect(toggle()).toHaveFocus();
  });

  // React bubbles a portal's events to its React parent, not its DOM one, so
  // Escape inside a dialog opened from the drawer reaches the shell's handler.
  // Dismissing that dialog must leave the drawer exactly as it was.
  it("mantém a navegação aberta quando Escape fecha um diálogo aberto a partir dela", async () => {
    stubViewport(true);
    const user = userEvent.setup();
    renderShellAt(`/chat/channel/${readingId}`);
    await screen.findByRole("option", { name: /Infra/ });

    await user.click(toggle());
    await user.click(screen.getByRole("button", { name: "Mais opções para canal Infra" }));
    await user.click(screen.getByRole("menuitem", { name: "Sair do canal" }));
    expect(screen.getByRole("dialog")).toBeInTheDocument();

    await user.keyboard("{Escape}");

    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(screen.getByTestId("chat-shell")).toHaveAttribute("data-nav-open", "true");
  });

  it("fecha ao tocar fora e devolve o foco ao acionador", async () => {
    stubViewport(true);
    const user = userEvent.setup();
    renderShellAt(`/chat/channel/${readingId}`);
    await screen.findByRole("option", { name: /Plataforma/ });

    await user.click(toggle());
    await user.click(screen.getByTestId("chat-nav-backdrop"));

    expect(screen.getByTestId("chat-shell")).not.toHaveAttribute("data-nav-open");
    expect(toggle()).toHaveFocus();
  });

  it("mostra a conversa escolhida e fecha a navegação, preservando a seleção", async () => {
    stubViewport(true);
    const user = userEvent.setup();
    renderShellAt(`/chat/channel/${readingId}`);
    await screen.findByRole("option", { name: /Plataforma/ });

    await user.click(toggle());
    await user.click(screen.getByRole("option", { name: /Infra/ }));

    expect(screen.getByTestId("chat-shell")).not.toHaveAttribute("data-nav-open");
    expect(main()).not.toHaveAttribute("inert");
    expect(screen.getByText("mensagens")).toBeInTheDocument();
    expect(screen.getByRole("option", { name: /Infra/ })).toHaveAttribute("aria-selected", "true");
  });

  it("fecha a navegação ao abrir os detalhes de uma conversa pelo menu da linha", async () => {
    stubViewport(true);
    const user = userEvent.setup();
    renderShellAt(`/chat/channel/${readingId}`);
    await screen.findByRole("option", { name: /Infra/ });

    await user.click(toggle());
    await user.click(screen.getByRole("button", { name: "Mais opções para canal Infra" }));
    await user.click(screen.getByRole("menuitem", { name: "Detalhes do canal" }));

    expect(screen.getByTestId("sidebar-details")).toHaveTextContent(`channel:${otherId}`);
    expect(screen.getByTestId("chat-shell")).not.toHaveAttribute("data-nav-open");
  });

  it("redimensionar para coluna devolve a conversa sem recriar a conexão nem perder a seleção", async () => {
    const viewport = stubViewport(true);
    const user = userEvent.setup();
    renderShellAt(`/chat/channel/${readingId}`);
    await screen.findByRole("option", { name: /Plataforma/ });

    await user.click(toggle());
    expect(main()).toHaveAttribute("inert");
    const socketsWhileDrawerOpen = FakeWebSocket.instances.length;
    const sidebarFetches = vi.mocked(fetchSidebarData).mock.calls.length;

    viewport.resizeTo(false);

    // The drawer cannot stay modal over a sidebar that is a column again.
    expect(screen.getByTestId("chat-shell")).not.toHaveAttribute("data-nav-open");
    expect(main()).not.toHaveAttribute("inert");
    // Composition changed; nothing else did.
    expect(FakeWebSocket.instances).toHaveLength(socketsWhileDrawerOpen);
    expect(vi.mocked(fetchSidebarData).mock.calls).toHaveLength(sidebarFetches);
    expect(screen.getByText("mensagens")).toBeInTheDocument();
    expect(screen.getByRole("option", { name: /Plataforma/ })).toHaveAttribute(
      "aria-selected",
      "true",
    );
  });
});
