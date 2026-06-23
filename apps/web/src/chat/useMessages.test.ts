/**
 * useMessages — WS integration tests.
 *
 * These tests verify the end-to-end flow of a WebSocket message.created event:
 *   1. useChatWebSocket delivers the event to onMessageCreated.
 *   2. useMessages inserts the message directly from evt.payload (primary path),
 *      or falls back to fetchChannelMessage / fetchDMMessage if payload is absent.
 *   3. The message is dispatched into state (ws_received) and deduplicated by ID.
 *   4. Events from a different target are ignored by the target check in handler.
 *   5. Out-of-order messages are inserted at the correct (createdAt, id) position.
 *
 * useChatWebSocket is NOT mocked as a no-op here. Instead it is replaced with a
 * controllable fake that captures the onMessageCreated callback so tests can fire
 * it directly — exercising the real callback logic in useMessages.
 */

import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { clearTokens, setTokens } from "../lib/authSession";
import type { WSMessageCreatedEvent, WSMessagePayload } from "./useChatWebSocket";
import { useMessages } from "./useMessages";
import type { Message, MessagePage } from "./chatTypes";

// ── Controllable useChatWebSocket stub ────────────────────────────────────────

// Captures the latest onMessageCreated callback so tests can fire WS events.
let capturedOnMessageCreated: ((evt: WSMessageCreatedEvent) => void) | null = null;

vi.mock("./useChatWebSocket", () => ({
  useChatWebSocket: ({
    onMessageCreated,
  }: {
    kind: string;
    targetId: string;
    onMessageCreated: (evt: WSMessageCreatedEvent) => void;
  }) => {
    capturedOnMessageCreated = onMessageCreated;
  },
}));

// ── chatApi mocks ─────────────────────────────────────────────────────────────

const {
  mockFetchChannelMessages,
  mockFetchChannelMessage,
  mockFetchDMMessages,
  mockFetchDMMessage,
} = vi.hoisted(() => ({
  mockFetchChannelMessages:
    vi.fn<(id: string, cursor?: string, signal?: AbortSignal) => Promise<MessagePage>>(),
  mockFetchChannelMessage:
    vi.fn<(id: string, msgId: string, signal?: AbortSignal) => Promise<Message>>(),
  mockFetchDMMessages:
    vi.fn<(id: string, cursor?: string, signal?: AbortSignal) => Promise<MessagePage>>(),
  mockFetchDMMessage:
    vi.fn<(id: string, msgId: string, signal?: AbortSignal) => Promise<Message>>(),
}));

vi.mock("./chatApi", () => ({
  fetchChannelMessages: (id: string, cursor?: string, signal?: AbortSignal) =>
    mockFetchChannelMessages(id, cursor, signal),
  fetchChannelMessage: (id: string, msgId: string, signal?: AbortSignal) =>
    mockFetchChannelMessage(id, msgId, signal),
  fetchDMMessages: (id: string, cursor?: string, signal?: AbortSignal) =>
    mockFetchDMMessages(id, cursor, signal),
  fetchDMMessage: (id: string, msgId: string, signal?: AbortSignal) =>
    mockFetchDMMessage(id, msgId, signal),
  postChannelMessage: vi.fn(),
  postDMMessage: vi.fn(),
}));

// ── Fixtures ──────────────────────────────────────────────────────────────────

const emptyPage: MessagePage = { messages: [], nextCursor: "" };

const makeMessage = (overrides: Partial<Message> = {}): Message => ({
  id: "msg-ws-1",
  senderId: "user-sender",
  senderDisplayName: "Alice",
  senderEmail: "alice@example.com",
  kind: "user",
  bodyText: "Hello from WS",
  isRemoved: false,
  status: "active",
  createdAt: new Date().toISOString(),
  updatedAt: new Date().toISOString(),
  ...overrides,
});

const makePayload = (overrides: Partial<WSMessagePayload> = {}): WSMessagePayload => ({
  id: "msg-ws-1",
  workspace_id: "ws-1",
  sender_id: "user-sender",
  sender_display_name: "Alice",
  sender_email: "alice@example.com",
  kind: "user",
  body_text: "Hello from WS",
  is_removed: false,
  status: "active",
  created_at: new Date().toISOString(),
  updated_at: new Date().toISOString(),
  ...overrides,
});

/** Fire a message.created event with a full payload (primary path). */
function fireWsEventWithPayload(
  targetType: "channel" | "dm",
  targetId: string,
  payload: WSMessagePayload,
): void {
  capturedOnMessageCreated?.({
    type: "message.created",
    event_id: "evt-1",
    created_at: new Date().toISOString(),
    workspace_id: "ws-1",
    target_type: targetType,
    target_id: targetId,
    message_id: payload.id,
    payload,
  });
}

/** Fire a message.created event WITHOUT a payload (fallback path). */
function fireWsEventNoPayload(
  targetType: "channel" | "dm",
  targetId: string,
  messageId: string,
): void {
  capturedOnMessageCreated?.({
    type: "message.created",
    event_id: "evt-1",
    created_at: new Date().toISOString(),
    workspace_id: "ws-1",
    target_type: targetType,
    target_id: targetId,
    message_id: messageId,
  });
}

// ── Setup / teardown ──────────────────────────────────────────────────────────

beforeEach(() => {
  setTokens("test-access-token");
  capturedOnMessageCreated = null;
  vi.clearAllMocks();
});

afterEach(() => {
  clearTokens();
});

// ── WS integration tests ──────────────────────────────────────────────────────

describe("useMessages — WS message.created integration", () => {
  it("inserts channel message directly from payload without fetch", async () => {
    const payload = makePayload({ id: "msg-payload-ch" });
    mockFetchChannelMessages.mockResolvedValue(emptyPage);

    const { result } = renderHook(() => useMessages({ kind: "channel", targetId: "ch-1" }));

    await waitFor(() => expect(result.current.state.status).toBe("ready"));
    expect(result.current.state.messages).toHaveLength(0);

    act(() => {
      fireWsEventWithPayload("channel", "ch-1", payload);
    });

    await waitFor(() => expect(result.current.state.messages).toHaveLength(1));
    expect(result.current.state.messages[0].id).toBe("msg-payload-ch");
    expect(result.current.state.messages[0].senderDisplayName).toBe("Alice");
    expect(result.current.state.messages[0].bodyText).toBe("Hello from WS");
    // Payload path must NOT call fetchChannelMessage.
    expect(mockFetchChannelMessage).not.toHaveBeenCalled();
    expect(result.current.state.lastMutation).toBe("ws_append");
  });

  it("renders realtime payload without sender email", async () => {
    const payload = {
      ...makePayload({ id: "msg-no-email", sender_display_name: "Display Name" }),
      sender_email: undefined,
    } as unknown as WSMessagePayload;
    mockFetchChannelMessages.mockResolvedValue(emptyPage);

    const { result } = renderHook(() => useMessages({ kind: "channel", targetId: "ch-no-email" }));

    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() => {
      fireWsEventWithPayload("channel", "ch-no-email", payload);
    });

    await waitFor(() => expect(result.current.state.messages).toHaveLength(1));
    expect(result.current.state.messages[0]).toEqual(
      expect.objectContaining({
        id: "msg-no-email",
        senderDisplayName: "Display Name",
        senderEmail: "",
      }),
    );
  });

  it("inserts DM message directly from payload without fetch", async () => {
    const payload = makePayload({ id: "msg-payload-dm", dm_conversation_id: "conv-1" });
    mockFetchDMMessages.mockResolvedValue(emptyPage);

    const { result } = renderHook(() => useMessages({ kind: "dm", targetId: "conv-1" }));

    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() => {
      fireWsEventWithPayload("dm", "conv-1", payload);
    });

    await waitFor(() => expect(result.current.state.messages).toHaveLength(1));
    expect(result.current.state.messages[0].id).toBe("msg-payload-dm");
    expect(mockFetchDMMessage).not.toHaveBeenCalled();
  });

  it("falls back to GET fetch when payload is absent (channel)", async () => {
    const msg = makeMessage({ id: "msg-fallback-ch" });
    mockFetchChannelMessages.mockResolvedValue(emptyPage);
    mockFetchChannelMessage.mockResolvedValue(msg);

    const { result } = renderHook(() => useMessages({ kind: "channel", targetId: "ch-fb" }));

    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() => {
      fireWsEventNoPayload("channel", "ch-fb", "msg-fallback-ch");
    });

    await waitFor(() =>
      expect(mockFetchChannelMessage).toHaveBeenCalledWith(
        "ch-fb",
        "msg-fallback-ch",
        expect.any(AbortSignal),
      ),
    );
    await waitFor(() => expect(result.current.state.messages).toHaveLength(1));
    expect(result.current.state.messages[0]).toEqual(msg);
  });

  it("falls back to GET fetch when payload is absent (DM)", async () => {
    const msg = makeMessage({ id: "msg-fallback-dm" });
    mockFetchDMMessages.mockResolvedValue(emptyPage);
    mockFetchDMMessage.mockResolvedValue(msg);

    const { result } = renderHook(() => useMessages({ kind: "dm", targetId: "conv-fb" }));

    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() => {
      fireWsEventNoPayload("dm", "conv-fb", "msg-fallback-dm");
    });

    await waitFor(() =>
      expect(mockFetchDMMessage).toHaveBeenCalledWith(
        "conv-fb",
        "msg-fallback-dm",
        expect.any(AbortSignal),
      ),
    );
    await waitFor(() => expect(result.current.state.messages).toHaveLength(1));
    expect(result.current.state.messages[0]).toEqual(msg);
  });

  it("records a recoverable realtime error when fallback GET fails", async () => {
    mockFetchChannelMessages.mockResolvedValue(emptyPage);
    mockFetchChannelMessage.mockRejectedValue(new Error("rate limited"));

    const { result } = renderHook(() => useMessages({ kind: "channel", targetId: "ch-fb-error" }));

    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() => {
      fireWsEventNoPayload("channel", "ch-fb-error", "msg-fallback-error");
    });

    await waitFor(() =>
      expect(mockFetchChannelMessage).toHaveBeenCalledWith(
        "ch-fb-error",
        "msg-fallback-error",
        expect.any(AbortSignal),
      ),
    );
    await waitFor(() =>
      expect((result.current.state as { realtimeError?: string | null }).realtimeError).toBe(
        "Não foi possível atualizar mensagens em tempo real.",
      ),
    );
    expect(result.current.state.messages).toHaveLength(0);
    expect(result.current.state.lastMutation).toBe("none");
  });

  it("deduplicates a message already present in state (payload path)", async () => {
    const createdAt = new Date().toISOString();
    const existingMsg = makeMessage({ id: "msg-dup", createdAt });
    const initialPage: MessagePage = { messages: [existingMsg], nextCursor: "" };
    mockFetchChannelMessages.mockResolvedValue(initialPage);

    const { result } = renderHook(() => useMessages({ kind: "channel", targetId: "ch-dup" }));

    await waitFor(() => expect(result.current.state.messages).toHaveLength(1));

    act(() => {
      fireWsEventWithPayload(
        "channel",
        "ch-dup",
        makePayload({ id: "msg-dup", created_at: createdAt }),
      );
    });

    // Must NOT add a duplicate.
    await waitFor(() => {
      expect(result.current.state.messages).toHaveLength(1);
    });
    expect(mockFetchChannelMessage).not.toHaveBeenCalled();
  });

  it("ignores event for a different channel (cross-target filter)", async () => {
    mockFetchChannelMessages.mockResolvedValue(emptyPage);

    const { result } = renderHook(() => useMessages({ kind: "channel", targetId: "ch-active" }));

    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    // Fire an event whose target_id is ch-OTHER, not ch-active.
    // handleWsMessageCreated checks evt.target_id !== targetId and returns early.
    act(() => {
      fireWsEventWithPayload("channel", "ch-other", makePayload({ id: "msg-from-other" }));
    });

    // No fetch, no state change.
    expect(mockFetchChannelMessage).not.toHaveBeenCalled();
    expect(result.current.state.messages).toHaveLength(0);
  });

  it("ignores event for a different DM conversation (cross-target filter)", async () => {
    mockFetchDMMessages.mockResolvedValue(emptyPage);

    const { result } = renderHook(() => useMessages({ kind: "dm", targetId: "conv-active" }));

    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() => {
      fireWsEventWithPayload("dm", "conv-other", makePayload({ id: "msg-from-other-dm" }));
    });

    expect(mockFetchDMMessage).not.toHaveBeenCalled();
    expect(result.current.state.messages).toHaveLength(0);
  });

  it("inserts out-of-order message at correct (createdAt, id) position", async () => {
    const t1 = "2024-01-01T00:00:01.000Z";
    const t3 = "2024-01-01T00:00:03.000Z";
    const initial = [
      makeMessage({ id: "msg-a", createdAt: t1 }),
      makeMessage({ id: "msg-c", createdAt: t3 }),
    ];
    mockFetchChannelMessages.mockResolvedValue({ messages: initial, nextCursor: "" });

    const { result } = renderHook(() => useMessages({ kind: "channel", targetId: "ch-order" }));

    await waitFor(() => expect(result.current.state.messages).toHaveLength(2));

    // Arrive an older message that belongs between msg-a and msg-c.
    const t2 = "2024-01-01T00:00:02.000Z";
    act(() => {
      fireWsEventWithPayload(
        "channel",
        "ch-order",
        makePayload({ id: "msg-b", created_at: t2, updated_at: t2 }),
      );
    });

    await waitFor(() => expect(result.current.state.messages).toHaveLength(3));
    const ids = result.current.state.messages.map((m) => m.id);
    expect(ids).toEqual(["msg-a", "msg-b", "msg-c"]);
    // Mid-list insertion: no auto-scroll.
    expect(result.current.state.lastMutation).toBe("none");
  });

  it("sorts by id when createdAt timestamps are equal", async () => {
    const ts = "2024-06-01T00:00:01.000Z";
    const initial = [
      makeMessage({ id: "msg-a", createdAt: ts }),
      makeMessage({ id: "msg-c", createdAt: ts }),
    ];
    mockFetchChannelMessages.mockResolvedValue({ messages: initial, nextCursor: "" });

    const { result } = renderHook(() => useMessages({ kind: "channel", targetId: "ch-tiebreak" }));

    await waitFor(() => expect(result.current.state.messages).toHaveLength(2));

    // msg-b has the same createdAt and an id that falls between msg-a and msg-c.
    // isNewer check: ts === ts && "msg-b" > "msg-c" → false → goes to sort.
    act(() => {
      fireWsEventWithPayload(
        "channel",
        "ch-tiebreak",
        makePayload({ id: "msg-b", created_at: ts, updated_at: ts }),
      );
    });

    await waitFor(() => expect(result.current.state.messages).toHaveLength(3));
    expect(result.current.state.messages.map((m) => m.id)).toEqual(["msg-a", "msg-b", "msg-c"]);
    expect(result.current.state.lastMutation).toBe("none");
  });

  it("sets lastMutation to ws_append when message is newest", async () => {
    mockFetchChannelMessages.mockResolvedValue(emptyPage);
    const payload = makePayload({ id: "msg-newest" });

    const { result } = renderHook(() => useMessages({ kind: "channel", targetId: "ch-latest" }));

    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() => {
      fireWsEventWithPayload("channel", "ch-latest", payload);
    });

    await waitFor(() => expect(result.current.state.messages).toHaveLength(1));
    expect(result.current.state.lastMutation).toBe("ws_append");
  });

  it("does not insert message when target changes before fetch resolves (stale guard)", async () => {
    const msg = makeMessage({ id: "msg-stale" });
    let fallbackSignal: AbortSignal | undefined;

    // Delay the fetch so the target changes first.
    let resolveMsg!: (m: Message) => void;
    mockFetchChannelMessages.mockResolvedValue(emptyPage);
    mockFetchChannelMessage.mockImplementation((_id, _msgId, signal) => {
      fallbackSignal = signal;
      return new Promise((r) => (resolveMsg = r));
    });

    const { result, rerender } = renderHook(
      ({ targetId }: { targetId: string }) => useMessages({ kind: "channel", targetId }),
      { initialProps: { targetId: "ch-original" } },
    );

    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    // Fire a fallback event (no payload) to trigger the delayed fetch.
    act(() => {
      fireWsEventNoPayload("channel", "ch-original", "msg-stale");
    });
    await waitFor(() => expect(fallbackSignal).toBeDefined());
    expect(fallbackSignal?.aborted).toBe(false);

    // Switch to a different channel before the fetch resolves.
    mockFetchChannelMessages.mockResolvedValue(emptyPage);
    rerender({ targetId: "ch-new" });
    expect(fallbackSignal?.aborted).toBe(true);

    // Resolve the delayed fetch.
    act(() => {
      resolveMsg(msg);
    });

    // Message must NOT be inserted — staleRef guard fired.
    await waitFor(() => expect(result.current.state.status).toBe("ready"));
    expect(result.current.state.messages).toHaveLength(0);
  });
});
