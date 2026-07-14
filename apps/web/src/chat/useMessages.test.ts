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
import type {
  WSClientErrorEvent,
  WSMessageCreatedEvent,
  WSMessageUpdatedEvent,
  WSMessagePayload,
  WSPinUpdatedEvent,
  WSReactionUpdatedEvent,
} from "./useChatWebSocket";
import { useMessages } from "./useMessages";
import type { Message, MessagePage } from "./chatTypes";

// ── Controllable useChatWebSocket stub ────────────────────────────────────────

// Captures the latest onMessageCreated callback so tests can fire WS events.
let capturedOnMessageCreated: ((evt: WSMessageCreatedEvent) => void) | null = null;
let capturedOnMessageUpdated: ((evt: WSMessageUpdatedEvent) => void) | null = null;
let capturedOnReactionUpdated: ((evt: WSReactionUpdatedEvent) => void) | null = null;
let capturedOnReactionError: ((evt: WSClientErrorEvent) => void) | null = null;
let capturedOnPinUpdated: ((evt: WSPinUpdatedEvent) => void) | null = null;
const mockToggleReaction = vi.fn(() => true);

vi.mock("./useChatWebSocket", () => ({
  useChatWebSocket: ({
    onMessageCreated,
    onMessageUpdated,
    onReactionUpdated,
    onPinUpdated,
    onReactionError,
  }: {
    kind: string;
    targetId: string;
    onMessageCreated: (evt: WSMessageCreatedEvent) => void;
    onMessageUpdated?: (evt: WSMessageUpdatedEvent) => void;
    onReactionUpdated?: (evt: WSReactionUpdatedEvent) => void;
    onPinUpdated?: (evt: WSPinUpdatedEvent) => void;
    onReactionError?: (evt: WSClientErrorEvent) => void;
  }) => {
    capturedOnMessageCreated = onMessageCreated;
    capturedOnMessageUpdated = onMessageUpdated ?? null;
    capturedOnReactionUpdated = onReactionUpdated ?? null;
    capturedOnPinUpdated = onPinUpdated ?? null;
    capturedOnReactionError = onReactionError ?? null;
    return { toggleReaction: mockToggleReaction };
  },
}));

// ── chatApi mocks ─────────────────────────────────────────────────────────────

const {
  mockFetchChannelMessages,
  mockFetchChannelMessage,
  mockFetchDMMessages,
  mockFetchDMMessage,
  mockFavoriteMessage,
  mockUnfavoriteMessage,
  mockEditMessage,
} = vi.hoisted(() => ({
  mockFavoriteMessage: vi.fn<(id: string) => Promise<void>>(),
  mockUnfavoriteMessage: vi.fn<(id: string) => Promise<void>>(),
  mockEditMessage: vi.fn<(id: string, body: string, bodyFormat: number) => Promise<Message>>(),
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
  favoriteMessage: (id: string) => mockFavoriteMessage(id),
  unfavoriteMessage: (id: string) => mockUnfavoriteMessage(id),
  editMessage: (id: string, body: string, bodyFormat: number) =>
    mockEditMessage(id, body, bodyFormat),
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
  bodyFormat: "v1",
  isRemoved: false,
  status: "active",
  createdAt: new Date().toISOString(),
  updatedAt: new Date().toISOString(),
  isEdited: false,
  editCount: 0,
  reactions: [],
  isFavorited: false,
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
  body_format: "v2",
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
  capturedOnMessageUpdated = null;
  capturedOnReactionUpdated = null;
  capturedOnReactionError = null;
  capturedOnPinUpdated = null;
  vi.clearAllMocks();
});

afterEach(() => {
  vi.useRealTimers();
  clearTokens();
});

// ── WS integration tests ──────────────────────────────────────────────────────

describe("useMessages — WS message.created integration", () => {
  it("replaces reaction counts from WS and marks the authenticated actor", async () => {
    mockFetchChannelMessages.mockResolvedValue({
      messages: [makeMessage({ id: "msg-1", reactions: [] })],
      nextCursor: "",
    });
    const { result } = renderHook(() =>
      useMessages({
        kind: "channel",
        targetId: "ch-1",
        currentUserId: "user-me",
      }),
    );
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() =>
      capturedOnReactionUpdated?.({
        type: "reaction.updated",
        target_type: "channel",
        target_id: "ch-1",
        message_id: "msg-1",
        reaction: {
          message_id: "msg-1",
          actor_user_id: "user-me",
          emoji: "👍",
          added: true,
          reactions: [{ emoji: "👍", count: 2 }],
        },
      }),
    );

    expect(result.current.state.messages[0].reactions).toEqual([
      { emoji: "👍", count: 2, reactedByMe: true },
    ]);
    act(() => result.current.toggleReaction("msg-1", "👍"));
    expect(mockToggleReaction).toHaveBeenCalledWith("msg-1", "👍");
  });

  it("shows an error when the reaction socket is unavailable", async () => {
    mockFetchChannelMessages.mockResolvedValue({
      messages: [makeMessage({ id: "msg-1" })],
      nextCursor: "",
    });
    mockToggleReaction.mockReturnValueOnce(false);
    const { result } = renderHook(() =>
      useMessages({ kind: "channel", targetId: "ch-1", currentUserId: "user-me" }),
    );
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() => result.current.toggleReaction("msg-1", "👍"));

    expect(result.current.state.actionError).toMatch(/tempo real/i);
  });

  it("forwards pin.updated only for the active target type and id", async () => {
    mockFetchChannelMessages.mockResolvedValue(emptyPage);
    const onPinUpdated = vi.fn();
    renderHook(() =>
      useMessages({
        kind: "channel",
        targetId: "same-id",
        currentUserId: "user-me",
        onPinUpdated,
      }),
    );
    await waitFor(() => expect(capturedOnPinUpdated).not.toBeNull());

    act(() =>
      capturedOnPinUpdated?.({
        type: "pin.updated",
        target_type: "dm",
        target_id: "same-id",
        message_id: "msg-1",
      }),
    );
    expect(onPinUpdated).not.toHaveBeenCalled();

    act(() =>
      capturedOnPinUpdated?.({
        type: "pin.updated",
        target_type: "channel",
        target_id: "same-id",
        message_id: "msg-1",
      }),
    );
    expect(onPinUpdated).toHaveBeenCalledTimes(1);
  });

  it("maps structured reaction errors to visible state", async () => {
    mockFetchChannelMessages.mockResolvedValue(emptyPage);
    const { result } = renderHook(() =>
      useMessages({ kind: "channel", targetId: "ch-1", currentUserId: "user-me" }),
    );
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() => capturedOnReactionError?.({ type: "error", code: "rate_limited", retry_after: 60 }));

    expect(result.current.state.actionError).toMatch(/muitas reações/i);

    act(() => capturedOnReactionError?.({ type: "error", code: "temporarily_unavailable" }));
    expect(result.current.state.actionError).toMatch(/temporariamente indisponíveis/i);
  });

  it("reverts pending optimistic reactions when the server reports a reaction error", async () => {
    const originalReactions = [{ emoji: "👍", count: 1, reactedByMe: false }];
    mockFetchChannelMessages.mockResolvedValue({
      messages: [makeMessage({ id: "msg-1", reactions: originalReactions })],
      nextCursor: "",
    });
    const { result } = renderHook(() =>
      useMessages({ kind: "channel", targetId: "ch-1", currentUserId: "user-me" }),
    );
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() => result.current.toggleReaction("msg-1", "👍"));
    expect(result.current.state.messages[0].reactions).toEqual([
      { emoji: "👍", count: 2, reactedByMe: true },
    ]);

    act(() => capturedOnReactionError?.({ type: "error", code: "temporarily_unavailable" }));

    expect(result.current.state.messages[0].reactions).toEqual(originalReactions);
    expect(result.current.state.actionError).toMatch(/temporariamente indisponíveis/i);
  });

  it("reverts an optimistic reaction when confirmation times out", async () => {
    const originalReactions = [{ emoji: "👍", count: 1, reactedByMe: false }];
    mockFetchChannelMessages.mockResolvedValue({
      messages: [makeMessage({ id: "msg-1", reactions: originalReactions })],
      nextCursor: "",
    });
    const { result } = renderHook(() =>
      useMessages({ kind: "channel", targetId: "ch-1", currentUserId: "user-me" }),
    );
    await waitFor(() => expect(result.current.state.status).toBe("ready"));
    vi.useFakeTimers();

    act(() => result.current.toggleReaction("msg-1", "👍"));
    act(() => vi.advanceTimersByTime(8_000));

    expect(result.current.state.messages[0].reactions).toEqual(originalReactions);
    expect(result.current.state.actionError).toMatch(/confirmar a reação/i);
  });

  it("clears a temporary reaction error instead of leaving a stale banner", async () => {
    mockFetchChannelMessages.mockResolvedValue(emptyPage);
    const { result } = renderHook(() =>
      useMessages({ kind: "channel", targetId: "ch-1", currentUserId: "user-me" }),
    );
    await waitFor(() => expect(result.current.state.status).toBe("ready"));
    vi.useFakeTimers();

    act(() => capturedOnReactionError?.({ type: "error", code: "temporarily_unavailable" }));
    expect(result.current.state.actionError).toMatch(/temporariamente indisponíveis/i);
    act(() => vi.advanceTimersByTime(5_000));

    expect(result.current.state.actionError).toBeNull();
    vi.useRealTimers();
  });

  it("reloads an authorized message for route-only remote reaction events", async () => {
    mockFetchChannelMessages.mockResolvedValue({
      messages: [makeMessage({ id: "msg-remote", reactions: [] })],
      nextCursor: "",
    });
    mockFetchChannelMessage.mockResolvedValue(
      makeMessage({ id: "msg-remote", reactions: [{ emoji: "🔥", count: 4, reactedByMe: true }] }),
    );
    const { result } = renderHook(() =>
      useMessages({ kind: "channel", targetId: "ch-1", currentUserId: "user-me" }),
    );
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() =>
      capturedOnReactionUpdated?.({
        type: "reaction.updated",
        target_type: "channel",
        target_id: "ch-1",
        message_id: "msg-remote",
      }),
    );

    await waitFor(() => expect(result.current.state.messages[0].reactions).toHaveLength(1));
    expect(mockFetchChannelMessage).toHaveBeenCalledWith(
      "ch-1",
      "msg-remote",
      expect.any(AbortSignal),
    );
  });

  it("reports a failed remote reaction snapshot without exposing details", async () => {
    mockFetchChannelMessages.mockResolvedValue({
      messages: [makeMessage({ id: "msg-remote-error", reactions: [] })],
      nextCursor: "",
    });
    mockFetchChannelMessage.mockRejectedValue(new Error("sensitive backend failure"));
    const { result } = renderHook(() =>
      useMessages({ kind: "channel", targetId: "ch-1", currentUserId: "user-me" }),
    );
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() =>
      capturedOnReactionUpdated?.({
        type: "reaction.updated",
        target_type: "channel",
        target_id: "ch-1",
        message_id: "msg-remote-error",
      }),
    );

    await waitFor(() => expect(result.current.state.realtimeError).toMatch(/tempo real/i));
    expect(result.current.state.realtimeError).not.toContain("sensitive backend failure");
  });

  it("reloads a DM message for route-only remote reaction events", async () => {
    mockFetchDMMessages.mockResolvedValue({
      messages: [makeMessage({ id: "msg-dm-remote", reactions: [] })],
      nextCursor: "",
    });
    mockFetchDMMessage.mockResolvedValue(
      makeMessage({
        id: "msg-dm-remote",
        reactions: [{ emoji: "🔥", count: 2, reactedByMe: false }],
      }),
    );
    const { result } = renderHook(() =>
      useMessages({ kind: "dm", targetId: "dm-1", currentUserId: "user-me" }),
    );
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() =>
      capturedOnReactionUpdated?.({
        type: "reaction.updated",
        target_type: "dm",
        target_id: "dm-1",
        message_id: "msg-dm-remote",
      }),
    );

    await waitFor(() => expect(result.current.state.messages[0].reactions).toHaveLength(1));
    expect(mockFetchDMMessage).toHaveBeenCalledWith(
      "dm-1",
      "msg-dm-remote",
      expect.any(AbortSignal),
    );
  });

  it("does not mark another user's reaction as mine", async () => {
    mockFetchChannelMessages.mockResolvedValue({
      messages: [makeMessage({ id: "msg-other", reactions: [] })],
      nextCursor: "",
    });
    const onOwnReactionConfirmed = vi.fn();
    const { result } = renderHook(() =>
      useMessages({
        kind: "channel",
        targetId: "ch-1",
        currentUserId: "user-me",
        onOwnReactionConfirmed,
      }),
    );
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() =>
      capturedOnReactionUpdated?.({
        type: "reaction.updated",
        target_type: "channel",
        target_id: "ch-1",
        message_id: "msg-other",
        reaction: {
          message_id: "msg-other",
          actor_user_id: "user-other",
          emoji: "👍",
          added: true,
          reactions: [{ emoji: "👍", count: 1 }],
        },
      }),
    );

    expect(result.current.state.messages[0].reactions).toEqual([
      { emoji: "👍", count: 1, reactedByMe: false },
    ]);
    expect(onOwnReactionConfirmed).not.toHaveBeenCalled();
  });

  it("does not fetch a remote reaction for a message outside the rendered page", async () => {
    mockFetchChannelMessages.mockResolvedValue({
      messages: [makeMessage({ id: "msg-visible" })],
      nextCursor: "",
    });
    const { result } = renderHook(() =>
      useMessages({ kind: "channel", targetId: "ch-1", currentUserId: "user-me" }),
    );
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() =>
      capturedOnReactionUpdated?.({
        type: "reaction.updated",
        target_type: "channel",
        target_id: "ch-1",
        message_id: "msg-not-rendered",
      }),
    );

    expect(mockFetchChannelMessage).not.toHaveBeenCalled();
  });

  it("aborts the remote reaction fallback when the main effect cleans up", async () => {
    let fallbackSignal: AbortSignal | undefined;
    mockFetchChannelMessages.mockResolvedValue({
      messages: [makeMessage({ id: "msg-remote" })],
      nextCursor: "",
    });
    mockFetchChannelMessage.mockImplementation((_targetId, _messageId, signal) => {
      fallbackSignal = signal;
      return new Promise<Message>(() => undefined);
    });
    const { result, unmount } = renderHook(() =>
      useMessages({ kind: "channel", targetId: "ch-1", currentUserId: "user-me" }),
    );
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() =>
      capturedOnReactionUpdated?.({
        type: "reaction.updated",
        target_type: "channel",
        target_id: "ch-1",
        message_id: "msg-remote",
      }),
    );
    await waitFor(() => expect(fallbackSignal).toBeDefined());

    unmount();
    expect(fallbackSignal?.aborted).toBe(true);
  });

  it("inserts channel message directly from payload without fetch", async () => {
    const payload = makePayload({ id: "msg-payload-ch" });
    mockFetchChannelMessages.mockResolvedValue(emptyPage);

    const { result } = renderHook(() =>
      useMessages({ kind: "channel", targetId: "ch-1", currentUserId: "user-me" }),
    );

    await waitFor(() => expect(result.current.state.status).toBe("ready"));
    expect(result.current.state.messages).toHaveLength(0);

    act(() => {
      fireWsEventWithPayload("channel", "ch-1", payload);
    });

    await waitFor(() => expect(result.current.state.messages).toHaveLength(1));
    expect(result.current.state.messages[0].id).toBe("msg-payload-ch");
    expect(result.current.state.messages[0].senderDisplayName).toBe("Alice");
    expect(result.current.state.messages[0].bodyText).toBe("Hello from WS");
    expect(result.current.state.messages[0].bodyFormat).toBe("v2");
    // Payload path must NOT call fetchChannelMessage.
    expect(mockFetchChannelMessage).not.toHaveBeenCalled();
    expect(result.current.state.lastMutation).toBe("ws_append");
  });

  it("preserves v3 mention format from a realtime payload", async () => {
    const payload = makePayload({ id: "msg-v3", body_format: "v3" });
    mockFetchChannelMessages.mockResolvedValue(emptyPage);
    const { result } = renderHook(() =>
      useMessages({ kind: "channel", targetId: "ch-1", currentUserId: "user-me" }),
    );
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() => fireWsEventWithPayload("channel", "ch-1", payload));

    await waitFor(() => expect(result.current.state.messages).toHaveLength(1));
    expect(result.current.state.messages[0].bodyFormat).toBe("v3");
  });

  it("maps quoted previews from realtime payloads", async () => {
    const payload = makePayload({
      id: "msg-reply",
      quoted: {
        id: "msg-parent",
        author_id: "user-parent",
        body: "texto citado",
        body_format: "v3",
        created_at: "2024-01-15T09:00:00Z",
      },
    });
    mockFetchChannelMessages.mockResolvedValue(emptyPage);
    const { result } = renderHook(() =>
      useMessages({ kind: "channel", targetId: "ch-1", currentUserId: "user-me" }),
    );
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() => fireWsEventWithPayload("channel", "ch-1", payload));

    await waitFor(() => expect(result.current.state.messages).toHaveLength(1));
    expect(result.current.state.messages[0].quoted).toEqual({
      id: "msg-parent",
      authorId: "user-parent",
      bodyText: "texto citado",
      bodyFormat: "v3",
      isRemoved: false,
      deletedAt: null,
      createdAt: "2024-01-15T09:00:00Z",
    });
  });

  it("renders realtime payload without sender email", async () => {
    const payload = {
      ...makePayload({ id: "msg-no-email", sender_display_name: "Display Name" }),
      sender_email: undefined,
    } as unknown as WSMessagePayload;
    mockFetchChannelMessages.mockResolvedValue(emptyPage);

    const { result } = renderHook(() =>
      useMessages({ kind: "channel", targetId: "ch-no-email", currentUserId: "user-me" }),
    );

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

    const { result } = renderHook(() =>
      useMessages({ kind: "dm", targetId: "conv-1", currentUserId: "user-me" }),
    );

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

    const { result } = renderHook(() =>
      useMessages({ kind: "channel", targetId: "ch-fb", currentUserId: "user-me" }),
    );

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

    const { result } = renderHook(() =>
      useMessages({ kind: "dm", targetId: "conv-fb", currentUserId: "user-me" }),
    );

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

    const { result } = renderHook(() =>
      useMessages({ kind: "channel", targetId: "ch-fb-error", currentUserId: "user-me" }),
    );

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

    const { result } = renderHook(() =>
      useMessages({ kind: "channel", targetId: "ch-dup", currentUserId: "user-me" }),
    );

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

    const { result } = renderHook(() =>
      useMessages({ kind: "channel", targetId: "ch-active", currentUserId: "user-me" }),
    );

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

    const { result } = renderHook(() =>
      useMessages({ kind: "dm", targetId: "conv-active", currentUserId: "user-me" }),
    );

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

    const { result } = renderHook(() =>
      useMessages({ kind: "channel", targetId: "ch-order", currentUserId: "user-me" }),
    );

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

    const { result } = renderHook(() =>
      useMessages({ kind: "channel", targetId: "ch-tiebreak", currentUserId: "user-me" }),
    );

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

    const { result } = renderHook(() =>
      useMessages({ kind: "channel", targetId: "ch-latest", currentUserId: "user-me" }),
    );

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
      ({ targetId }: { targetId: string }) =>
        useMessages({ kind: "channel", targetId, currentUserId: "user-me" }),
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

// ── Quote reply state (RF-07) ─────────────────────────────────────────────────

describe("useMessages — reply state", () => {
  it("selectReply exposes the selected message as replyTo", async () => {
    const msg = makeMessage({ id: "msg-reply-parent" });
    mockFetchChannelMessages.mockResolvedValue({ messages: [msg], nextCursor: "" });

    const { result } = renderHook(() =>
      useMessages({ kind: "channel", targetId: "ch-1", currentUserId: "user-me" }),
    );
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() => result.current.selectReply(msg));

    expect(result.current.state.replyTo).toEqual(msg);
  });

  it("cancelReply clears replyTo without changing loaded messages", async () => {
    const msg = makeMessage({ id: "msg-reply-parent" });
    mockFetchChannelMessages.mockResolvedValue({ messages: [msg], nextCursor: "" });

    const { result } = renderHook(() =>
      useMessages({ kind: "channel", targetId: "ch-1", currentUserId: "user-me" }),
    );
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() => result.current.selectReply(msg));
    act(() => result.current.cancelReply());

    expect(result.current.state.replyTo).toBeNull();
    expect(result.current.state.messages).toEqual([msg]);
  });

  it("resets replyTo when switching targets", async () => {
    const msg = makeMessage({ id: "msg-reply-parent" });
    mockFetchChannelMessages.mockResolvedValue({ messages: [msg], nextCursor: "" });

    const { result, rerender } = renderHook(
      ({ targetId }: { targetId: string }) =>
        useMessages({ kind: "channel", targetId, currentUserId: "user-me" }),
      { initialProps: { targetId: "ch-1" } },
    );
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() => result.current.selectReply(msg));
    expect(result.current.state.replyTo).toEqual(msg);

    mockFetchChannelMessages.mockResolvedValueOnce(emptyPage);
    rerender({ targetId: "ch-2" });

    await waitFor(() =>
      expect(mockFetchChannelMessages).toHaveBeenCalledWith(
        "ch-2",
        undefined,
        expect.any(AbortSignal),
      ),
    );
    await waitFor(() => expect(result.current.state.replyTo).toBeNull());
  });
});

// ── Favorites (RF-06) ─────────────────────────────────────────────────────────

describe("useMessages — toggleFavorite", () => {
  it("marks the message favorited after the REST call confirms", async () => {
    mockFetchChannelMessages.mockResolvedValue({
      messages: [makeMessage({ id: "msg-1", isFavorited: false })],
      nextCursor: "",
    });
    mockFavoriteMessage.mockResolvedValue(undefined);
    const { result } = renderHook(() =>
      useMessages({ kind: "channel", targetId: "ch-1", currentUserId: "user-me" }),
    );
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() => result.current.toggleFavorite("msg-1", true));

    await waitFor(() => expect(result.current.state.messages[0].isFavorited).toBe(true));
    expect(mockFavoriteMessage).toHaveBeenCalledWith("msg-1");
    expect(mockUnfavoriteMessage).not.toHaveBeenCalled();
  });

  it("clears the flag after unfavoriting", async () => {
    mockFetchChannelMessages.mockResolvedValue({
      messages: [makeMessage({ id: "msg-1", isFavorited: true })],
      nextCursor: "",
    });
    mockUnfavoriteMessage.mockResolvedValue(undefined);
    const { result } = renderHook(() =>
      useMessages({ kind: "channel", targetId: "ch-1", currentUserId: "user-me" }),
    );
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() => result.current.toggleFavorite("msg-1", false));

    await waitFor(() => expect(result.current.state.messages[0].isFavorited).toBe(false));
    expect(mockUnfavoriteMessage).toHaveBeenCalledWith("msg-1");
  });

  it("keeps state and surfaces an error when the call fails", async () => {
    mockFetchChannelMessages.mockResolvedValue({
      messages: [makeMessage({ id: "msg-1", isFavorited: false })],
      nextCursor: "",
    });
    mockFavoriteMessage.mockRejectedValue(new Error("boom"));
    const { result } = renderHook(() =>
      useMessages({ kind: "channel", targetId: "ch-1", currentUserId: "user-me" }),
    );
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() => result.current.toggleFavorite("msg-1", true));

    await waitFor(() => expect(result.current.state.actionError).toMatch(/favorito/i));
    expect(result.current.state.messages[0].isFavorited).toBe(false);
  });
});

describe("useMessages — message editing", () => {
  it("confirms server edit fields without clearing reactions or favorite state", async () => {
    const initial = makeMessage({
      id: "msg-edit",
      reactions: [{ emoji: "👍", count: 2, reactedByMe: true }],
      isFavorited: true,
    });
    mockFetchChannelMessages.mockResolvedValue({ messages: [initial], nextCursor: "" });
    mockEditMessage.mockResolvedValue(
      makeMessage({
        id: "msg-edit",
        bodyText: "persistida",
        bodyFormat: "v3",
        editCount: 1,
        isEdited: true,
        editedAt: "2026-07-13T12:00:00Z",
        reactions: [],
        isFavorited: false,
      }),
    );
    const { result } = renderHook(() =>
      useMessages({ kind: "channel", targetId: "ch-1", currentUserId: "user-me" }),
    );
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    await act(() => result.current.editMessageLocal("msg-edit", "persistida", "v3"));

    expect(result.current.state.messages[0]).toMatchObject({
      bodyText: "persistida",
      reactions: initial.reactions,
      isFavorited: true,
    });
  });

  it("applies an optimistic edit and restores the exact message when PATCH fails", async () => {
    const original = makeMessage({ id: "msg-edit", bodyText: "original", bodyFormat: "v2" });
    mockFetchChannelMessages.mockResolvedValue({ messages: [original], nextCursor: "" });
    let rejectEdit!: (error: Error) => void;
    mockEditMessage.mockImplementation(
      () => new Promise((_resolve, reject) => (rejectEdit = reject)),
    );
    const { result } = renderHook(() =>
      useMessages({ kind: "channel", targetId: "ch-1", currentUserId: "user-me" }),
    );
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    let request!: Promise<Message>;
    act(() => {
      request = result.current.editMessageLocal("msg-edit", "rascunho otimista", "v3");
    });

    expect(result.current.state.messages[0]).toMatchObject({
      bodyText: "rascunho otimista",
      bodyFormat: "v3",
      isEdited: true,
      editCount: 1,
    });
    const settled = request.catch((error: unknown) => error);
    act(() => rejectEdit(new Error("PATCH failed")));
    await settled;

    await waitFor(() => expect(result.current.state.messages[0]).toEqual(original));
    expect(mockEditMessage).toHaveBeenCalledWith("msg-edit", "rascunho otimista", 3);
  });

  it("sends legacy v1 edits using the backend body-format version", async () => {
    const original = makeMessage({ id: "msg-edit", bodyText: "original", bodyFormat: "v1" });
    const updated = makeMessage({
      ...original,
      bodyText: "texto legado editado",
      bodyFormat: "v1",
      isEdited: true,
      editCount: 1,
    });
    mockFetchChannelMessages.mockResolvedValue({ messages: [original], nextCursor: "" });
    mockEditMessage.mockResolvedValue(updated);
    const { result } = renderHook(() =>
      useMessages({ kind: "channel", targetId: "ch-1", currentUserId: "user-me" }),
    );
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    await act(() => result.current.editMessageLocal("msg-edit", "texto legado editado", "v1"));

    expect(mockEditMessage).toHaveBeenCalledWith("msg-edit", "texto legado editado", 1);
    expect(result.current.state.messages[0]).toMatchObject(updated);
  });

  it("does not let a late PATCH failure overwrite a newer authoritative WS edit", async () => {
    mockFetchChannelMessages.mockResolvedValue({
      messages: [makeMessage({ id: "msg-edit", bodyText: "original" })],
      nextCursor: "",
    });
    let rejectEdit!: (error: Error) => void;
    mockEditMessage.mockImplementation(
      () => new Promise((_resolve, reject) => (rejectEdit = reject)),
    );
    const { result } = renderHook(() =>
      useMessages({ kind: "channel", targetId: "ch-1", currentUserId: "user-me" }),
    );
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    let request!: Promise<Message>;
    act(() => {
      request = result.current.editMessageLocal("msg-edit", "otimista", "v2");
    });
    act(() =>
      capturedOnMessageUpdated?.({
        type: "message.updated",
        target_type: "channel",
        target_id: "ch-1",
        message_update: {
          message_id: "msg-edit",
          channel_id: "ch-1",
          body: "versão mais nova",
          body_format: "v3",
          edited_at: "2026-07-13T13:00:00Z",
          edit_count: 2,
          is_edited: true,
        },
      }),
    );
    const settled = request.catch((error: unknown) => error);
    act(() => rejectEdit(new Error("late failure")));
    await settled;

    expect(result.current.state.messages[0]).toMatchObject({
      bodyText: "versão mais nova",
      editCount: 2,
      editedAt: "2026-07-13T13:00:00Z",
    });
  });

  it("does not let a stale PATCH success overwrite a newer authoritative WS edit", async () => {
    mockFetchChannelMessages.mockResolvedValue({
      messages: [makeMessage({ id: "msg-edit", bodyText: "original" })],
      nextCursor: "",
    });
    let resolveEdit!: (message: Message) => void;
    mockEditMessage.mockImplementation(() => new Promise((resolve) => (resolveEdit = resolve)));
    const { result } = renderHook(() =>
      useMessages({ kind: "channel", targetId: "ch-1", currentUserId: "user-me" }),
    );
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    let request!: Promise<Message>;
    act(() => {
      request = result.current.editMessageLocal("msg-edit", "resposta atrasada", "v2");
    });
    act(() =>
      capturedOnMessageUpdated?.({
        type: "message.updated",
        target_type: "channel",
        target_id: "ch-1",
        message_update: {
          message_id: "msg-edit",
          channel_id: "ch-1",
          body: "versão mais nova",
          body_format: "v3",
          edited_at: "2026-07-13T13:00:00Z",
          edit_count: 3,
          is_edited: true,
        },
      }),
    );
    act(() =>
      resolveEdit(
        makeMessage({
          id: "msg-edit",
          bodyText: "resposta atrasada",
          editCount: 1,
          isEdited: true,
        }),
      ),
    );
    await request;

    expect(result.current.state.messages[0]).toMatchObject({
      bodyText: "versão mais nova",
      bodyFormat: "v3",
      editCount: 3,
      editedAt: "2026-07-13T13:00:00Z",
    });
  });

  it("reconciles message.updated with every authoritative server field", async () => {
    mockFetchChannelMessages.mockResolvedValue({
      messages: [makeMessage({ id: "msg-edit", bodyText: "original" })],
      nextCursor: "",
    });
    const { result } = renderHook(() =>
      useMessages({ kind: "channel", targetId: "ch-1", currentUserId: "user-me" }),
    );
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() =>
      capturedOnMessageUpdated?.({
        type: "message.updated",
        target_type: "channel",
        target_id: "ch-1",
        message_update: {
          message_id: "msg-edit",
          channel_id: "ch-1",
          body: "versão persistida",
          body_format: "v3",
          edited_at: "2026-07-13T12:00:00Z",
          edit_count: 4,
          is_edited: true,
        },
      }),
    );

    expect(result.current.state.messages[0]).toMatchObject({
      bodyText: "versão persistida",
      bodyFormat: "v3",
      editedAt: "2026-07-13T12:00:00Z",
      editCount: 4,
      isEdited: true,
    });
  });

  it("ignores an older message.updated version for the rendered message", async () => {
    const current = makeMessage({
      id: "msg-edit",
      bodyText: "versão atual",
      bodyFormat: "v3",
      editCount: 4,
      isEdited: true,
      editedAt: "2026-07-13T12:00:00Z",
    });
    mockFetchChannelMessages.mockResolvedValue({ messages: [current], nextCursor: "" });
    const { result } = renderHook(() =>
      useMessages({ kind: "channel", targetId: "ch-1", currentUserId: "user-me" }),
    );
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() =>
      capturedOnMessageUpdated?.({
        type: "message.updated",
        target_type: "channel",
        target_id: "ch-1",
        message_update: {
          message_id: "msg-edit",
          channel_id: "ch-1",
          body: "versão antiga",
          body_format: "v2",
          edited_at: "2026-07-13T11:00:00Z",
          edit_count: 3,
          is_edited: true,
        },
      }),
    );

    expect(result.current.state.messages[0]).toEqual(current);
  });

  it("ignores message.updated for another target or an unknown message", async () => {
    const original = makeMessage({ id: "msg-edit", bodyText: "original" });
    mockFetchChannelMessages.mockResolvedValue({ messages: [original], nextCursor: "" });
    const { result } = renderHook(() =>
      useMessages({ kind: "channel", targetId: "ch-1", currentUserId: "user-me" }),
    );
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    const update: WSMessageUpdatedEvent = {
      type: "message.updated",
      target_type: "channel",
      target_id: "ch-2",
      message_update: {
        message_id: "msg-edit",
        channel_id: "ch-2",
        body: "não aplicar",
        body_format: "v3",
        edited_at: "2026-07-13T12:00:00Z",
        edit_count: 1,
        is_edited: true,
      },
    };

    act(() => capturedOnMessageUpdated?.(update));
    act(() =>
      capturedOnMessageUpdated?.({
        ...update,
        target_id: "ch-1",
        message_update: { ...update.message_update, message_id: "missing", channel_id: "ch-1" },
      }),
    );

    expect(result.current.state.messages).toEqual([original]);
  });
});
