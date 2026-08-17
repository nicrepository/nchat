import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes, useOutletContext } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { clearTokens, setTokens } from "../lib/authSession";
import { issueCallToken, issueResourceCallToken } from "./callApi";
import { fetchSidebarData } from "./chatApi";
import ChatShell, { type ChatOutletContext } from "./ChatShell";
import { _resetChatSocket } from "./chatSocket";
import { requestMediaPermission, type MediaPermissionResult } from "./mediaPermission";
import { useCallMedia } from "./useCallMedia";

vi.mock("./chatApi", async () => {
  const actual = await vi.importActual<typeof import("./chatApi")>("./chatApi");
  return { ...actual, fetchSidebarData: vi.fn() };
});
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
const connectMedia = vi.fn(async () => undefined);
const stopMedia = vi.fn(async () => undefined);
const OriginalWebSocket = global.WebSocket;

beforeEach(() => {
  FakeWebSocket.instances = [];
  _resetChatSocket(() => 0);
  global.WebSocket = FakeWebSocket as unknown as typeof WebSocket;
  setTokens("test-token");
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
    bindLocalMedia: vi.fn(),
    bindRemoteMedia: vi.fn(),
    participants: [],
    bindRemoteAudio: vi.fn(),
    toggleMicrophone: vi.fn(async () => undefined),
    toggleCamera: vi.fn(async () => undefined),
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
        <ChatShell />
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

    const dialog = screen.getByRole("dialog", { name: "Chamada de vídeo com Participante" });
    expect(within(dialog).getByRole("status")).toHaveTextContent("Preparando chamada…");
    expect(screen.queryByRole("button", { name: "Atender" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Recusar" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Cancelar chamada" })).not.toBeInTheDocument();
    expect(prepareMedia).not.toHaveBeenCalled();
    expect(syncCommands(socket)).toHaveLength(1);

    await act(async () =>
      sidebar.resolve({
        currentUserId,
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

    expect(await screen.findByRole("button", { name: "Atender" })).toHaveFocus();
    expect(screen.getByRole("button", { name: "Recusar" })).toBeInTheDocument();
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
        <ChatShell />
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

    const dialog = screen.getByRole("dialog", { name: "Chamada de vídeo com Participante" });
    expect(within(dialog).getByRole("alert")).toHaveTextContent(
      "Não foi possível preparar a chamada",
    );
    const retry = within(dialog).getByRole("button", { name: "Tentar novamente" });
    expect(retry).toHaveFocus();
    await user.keyboard("{Enter}");
    expect(fetchSidebarData).toHaveBeenCalledTimes(2);
    expect(retry).toBeDisabled();
    await user.keyboard("{Enter}");
    expect(fetchSidebarData).toHaveBeenCalledTimes(2);
    expect(prepareMedia).not.toHaveBeenCalled();

    await act(async () => failedRetry.reject(new Error("still offline")));
    expect(retry).toBeEnabled();
    await user.keyboard("{Enter}");
    expect(fetchSidebarData).toHaveBeenCalledTimes(3);
    expect(retry).toBeDisabled();

    await act(async () =>
      successfulRetry.resolve({
        currentUserId,
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

    expect(await screen.findByRole("button", { name: "Atender" })).toHaveFocus();
    expect(screen.getByRole("button", { name: "Recusar" })).toBeInTheDocument();
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
        <ChatShell />
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

    const dialog = screen.getByRole("dialog", { name: "Chamada de vídeo com Participante" });
    expect(within(dialog).getByRole("status")).toHaveTextContent("Preparando chamada…");
    expect(
      within(dialog).queryByRole("button", { name: "Encerrar chamada" }),
    ).not.toBeInTheDocument();
    expect(issueCallToken).not.toHaveBeenCalled();
    expect(connectMedia).not.toHaveBeenCalled();

    await act(async () => initialSidebar.reject(new Error("offline")));
    const retry = within(dialog).getByRole("button", { name: "Tentar novamente" });
    expect(retry).toHaveFocus();
    await user.keyboard("{Enter}");
    await user.keyboard("{Enter}");
    expect(fetchSidebarData).toHaveBeenCalledTimes(2);
    expect(issueCallToken).not.toHaveBeenCalled();

    await act(async () => failedRetry.reject(new Error("still offline")));
    expect(retry).toBeEnabled();
    await user.keyboard("{Enter}");
    expect(fetchSidebarData).toHaveBeenCalledTimes(3);

    await act(async () =>
      successfulRetry.resolve({
        currentUserId,
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

    const dialogAfterRecovery = await screen.findByRole("dialog", {
      name: "Chamada de vídeo com Ana Lima",
    });
    expect(dialogAfterRecovery).toBeVisible();
    expect(screen.getByRole("button", { name: "Encerrar chamada" })).toHaveFocus();

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
    <button
      type="button"
      disabled={!ctx.joinResourceCall}
      onClick={() =>
        ctx.joinResourceCall?.({ kind: "channel", id: "chan-1", name: "Geral", callType: "audio" })
      }
    >
      Entrar no canal
    </button>
  );
}

async function renderWithJoinButtonReady() {
  vi.mocked(fetchSidebarData).mockResolvedValue({
    currentUserId,
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
        <Route path="/chat" element={<ChatShell />}>
          <Route index element={<JoinChannelButton />} />
        </Route>
      </Routes>
    </MemoryRouter>,
  );
  const socket = FakeWebSocket.instances[0];
  await act(async () => socket.simulateOpen());
  await screen.findByRole("button", { name: "Entrar no canal" });
  return socket;
}

describe("ChatShell RF-23 x RF-24 arbitration", () => {
  it("Caso A: an incoming RF-23 call stays visible and can be declined while a resource room is active", async () => {
    const user = userEvent.setup();
    const socket = await renderWithJoinButtonReady();

    await user.click(screen.getByRole("button", { name: "Entrar no canal" }));
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

    expect(
      await screen.findByRole("alert", { name: "Chamada de vídeo de Ana Lima" }),
    ).toBeInTheDocument();
    expect(screen.getByTestId("resource-call-panel")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Recusar" }));

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
    await user.click(screen.getByRole("button", { name: "Entrar no canal" }));
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
    await screen.findByRole("button", { name: "Atender" });

    // The permission prompt never resolves: call.accept is never sent, so
    // RF-23 never reaches "active" and never asks for the Room.
    let resolvePermission!: (value: MediaPermissionResult) => void;
    vi.mocked(requestMediaPermission).mockReturnValueOnce(
      new Promise((resolve) => {
        resolvePermission = resolve;
      }),
    );
    await user.click(screen.getByRole("button", { name: "Atender" }));

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
    await user.click(screen.getByRole("button", { name: "Entrar no canal" }));
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
    await screen.findByRole("button", { name: "Atender" });
    vi.mocked(requestMediaPermission).mockResolvedValueOnce({
      ok: false,
      kind: "permission_denied",
      message: "Permissão negada.",
    });

    await user.click(screen.getByRole("button", { name: "Atender" }));

    await waitFor(() => expect(requestMediaPermission).toHaveBeenCalled());
    expect(syncCommands(socket)).toHaveLength(1);
    expect(stopMedia).not.toHaveBeenCalled();
    expect(connectMedia).not.toHaveBeenCalled();
    expect(screen.getByTestId("resource-call-panel")).toBeInTheDocument();
  });

  it("achado C: accepting only hands the Room to RF-23 once the server confirms active, never on the local accept() call alone", async () => {
    const user = userEvent.setup();
    const socket = await renderWithJoinButtonReady();
    await user.click(screen.getByRole("button", { name: "Entrar no canal" }));
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
    await screen.findByRole("button", { name: "Atender" });

    await user.click(screen.getByRole("button", { name: "Atender" }));

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
    await user.click(screen.getByRole("button", { name: "Entrar no canal" }));
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
    await screen.findByRole("button", { name: "Atender" });
    await user.click(screen.getByRole("button", { name: "Atender" }));
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
    await user.click(screen.getByRole("button", { name: "Entrar no canal" }));
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
    await screen.findByRole("button", { name: "Atender" });

    let resolvePermission!: (value: MediaPermissionResult) => void;
    vi.mocked(requestMediaPermission).mockReturnValueOnce(
      new Promise((resolve) => {
        resolvePermission = resolve;
      }),
    );
    await user.click(screen.getByRole("button", { name: "Atender" }));
    await waitFor(() => expect(requestMediaPermission).toHaveBeenCalled());

    const recusar = screen.getByRole("button", { name: "Recusar" });
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
    await user.click(screen.getByRole("button", { name: "Entrar no canal" }));
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
    await screen.findByRole("button", { name: "Atender" });
    await user.click(screen.getByRole("button", { name: "Atender" }));
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
    expect(
      screen.getByRole("dialog", { name: "Chamada de vídeo com Ana Lima" }),
    ).toBeInTheDocument();
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
    await user.click(screen.getByRole("button", { name: "Entrar no canal" }));
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
    await screen.findByRole("button", { name: "Atender" });
    await user.click(screen.getByRole("button", { name: "Atender" }));
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
    expect(issueCallToken).not.toHaveBeenCalled();
    expect(connectMedia).not.toHaveBeenCalled();
    // RF-23 stays visible and recoverable — never silently reverts to RF-24
    // just because the room it was trying to take over never actually left.
    expect(screen.queryByTestId("resource-call-panel")).not.toBeInTheDocument();
    await waitFor(() =>
      expect(
        screen.getByRole("dialog", { name: "Chamada de vídeo com Ana Lima" }),
      ).toBeInTheDocument(),
    );
    await waitFor(() =>
      expect(
        screen.getByText("Não foi possível preparar a mídia da chamada. Tente novamente."),
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
});
