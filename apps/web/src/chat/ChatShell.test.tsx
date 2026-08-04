import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { clearTokens, setTokens } from "../lib/authSession";
import { fetchSidebarData } from "./chatApi";
import ChatShell from "./ChatShell";
import { _resetChatSocket } from "./chatSocket";
import { useCallMedia } from "./useCallMedia";

vi.mock("./chatApi", async () => {
  const actual = await vi.importActual<typeof import("./chatApi")>("./chatApi");
  return { ...actual, fetchSidebarData: vi.fn() };
});
vi.mock("./useChatWebSocket", () => ({ useChatWebSocket: vi.fn() }));
vi.mock("./useCallMedia", () => ({ useCallMedia: vi.fn() }));

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
const OriginalWebSocket = global.WebSocket;

beforeEach(() => {
  FakeWebSocket.instances = [];
  _resetChatSocket(() => 0);
  global.WebSocket = FakeWebSocket as unknown as typeof WebSocket;
  setTokens("test-token");
  prepareMedia.mockClear();
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
    toggleMicrophone: vi.fn(async () => undefined),
    toggleCamera: vi.fn(async () => undefined),
    activateAudio: vi.fn(async () => undefined),
    prepare: prepareMedia,
    startAudio: vi.fn(async () => undefined),
    connect: vi.fn(async () => undefined),
    stop: vi.fn(async () => undefined),
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
      }),
    );

    expect(await screen.findByRole("button", { name: "Atender" })).toHaveFocus();
    expect(screen.getByRole("button", { name: "Recusar" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Cancelar chamada" })).not.toBeInTheDocument();
    expect(FakeWebSocket.instances).toHaveLength(1);
    expect(syncCommands(socket)).toHaveLength(1);
    await waitFor(() => expect(prepareMedia).toHaveBeenCalledOnce());
  });
});

function syncCommands(socket: FakeWebSocket) {
  return socket.sentMessages
    .map((message) => JSON.parse(message) as { type: string })
    .filter((message) => message.type === "call.sync");
}
