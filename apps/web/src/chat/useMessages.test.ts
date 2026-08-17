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

import { ApiRequestError } from "../lib/api";
import { clearTokens, setTokens } from "../lib/authSession";
import type {
  WSClientErrorEvent,
  WSMessageBlockedEvent,
  WSMessageCreatedEvent,
  WSMessageUpdatedEvent,
  WSMessagePayload,
  WSPinUpdatedEvent,
  WSReactionUpdatedEvent,
  WSSubscribedEvent,
} from "./useChatWebSocket";
import { useMessages } from "./useMessages";
import type { Message, MessagePage } from "./chatTypes";

// ── Controllable useChatWebSocket stub ────────────────────────────────────────

// Captures the latest onMessageCreated callback so tests can fire WS events.
let capturedOnMessageCreated: ((evt: WSMessageCreatedEvent) => void) | null = null;
let capturedOnMessageBlocked: ((evt: WSMessageBlockedEvent) => void) | null = null;
let capturedOnMessageUpdated: ((evt: WSMessageUpdatedEvent) => void) | null = null;
let capturedOnReactionUpdated: ((evt: WSReactionUpdatedEvent) => void) | null = null;
let capturedOnReactionError: ((evt: WSClientErrorEvent) => void) | null = null;
let capturedOnSubscriptionError: ((evt: WSClientErrorEvent) => void) | null = null;
let capturedOnSubscribed: ((evt: WSSubscribedEvent) => void) | null = null;
let capturedOnPinUpdated: ((evt: WSPinUpdatedEvent) => void) | null = null;
const mockToggleReaction = vi.fn(() => true);

vi.mock("./useChatWebSocket", () => ({
  useChatWebSocket: ({
    onMessageCreated,
    onMessageBlocked,
    onMessageUpdated,
    onReactionUpdated,
    onPinUpdated,
    onReactionError,
    onSubscriptionError,
    onSubscribed,
  }: {
    kind: string;
    targetId: string;
    onMessageCreated: (evt: WSMessageCreatedEvent) => void;
    onMessageBlocked?: (evt: WSMessageBlockedEvent) => void;
    onMessageUpdated?: (evt: WSMessageUpdatedEvent) => void;
    onReactionUpdated?: (evt: WSReactionUpdatedEvent) => void;
    onPinUpdated?: (evt: WSPinUpdatedEvent) => void;
    onReactionError?: (evt: WSClientErrorEvent) => void;
    onSubscriptionError?: (evt: WSClientErrorEvent) => void;
    onSubscribed?: (evt: WSSubscribedEvent) => void;
  }) => {
    capturedOnMessageCreated = onMessageCreated;
    capturedOnMessageBlocked = onMessageBlocked ?? null;
    capturedOnMessageUpdated = onMessageUpdated ?? null;
    capturedOnReactionUpdated = onReactionUpdated ?? null;
    capturedOnPinUpdated = onPinUpdated ?? null;
    capturedOnReactionError = onReactionError ?? null;
    capturedOnSubscriptionError = onSubscriptionError ?? null;
    capturedOnSubscribed = onSubscribed ?? null;
    return { toggleReaction: mockToggleReaction };
  },
}));

// ── chatApi mocks ─────────────────────────────────────────────────────────────

const {
  mockFetchLinkSafetyStatuses,
  mockFetchChannelMessages,
  mockFetchChannelMessage,
  mockFetchDMMessages,
  mockFetchDMMessage,
  mockResolveChannelMessageReferences,
  mockResolveDMMessageReferences,
  mockFavoriteMessage,
  mockUnfavoriteMessage,
  mockPostChannelMessage,
  mockEditMessage,
  mockDeleteMessage,
} = vi.hoisted(() => ({
  mockFavoriteMessage: vi.fn<(id: string) => Promise<void>>(),
  mockUnfavoriteMessage: vi.fn<(id: string) => Promise<void>>(),
  mockPostChannelMessage:
    vi.fn<(id: string, body: string, parentMessageId?: string) => Promise<Message>>(),
  mockEditMessage: vi.fn<(id: string, body: string, bodyFormat: number) => Promise<Message>>(),
  mockDeleteMessage: vi.fn<(id: string) => Promise<Message>>(),
  mockFetchLinkSafetyStatuses:
    vi.fn<
      (
        messageIds: string[],
        signal?: AbortSignal,
      ) => Promise<{ messageId: string; state: string; reason?: string }[]>
    >(),
  mockFetchChannelMessages:
    vi.fn<(id: string, cursor?: string, signal?: AbortSignal) => Promise<MessagePage>>(),
  mockFetchChannelMessage:
    vi.fn<(id: string, msgId: string, signal?: AbortSignal) => Promise<Message>>(),
  mockFetchDMMessages:
    vi.fn<(id: string, cursor?: string, signal?: AbortSignal) => Promise<MessagePage>>(),
  mockFetchDMMessage:
    vi.fn<(id: string, msgId: string, signal?: AbortSignal) => Promise<Message>>(),
  mockResolveChannelMessageReferences:
    vi.fn<
      (
        id: string,
        messageIds: string[],
        signal?: AbortSignal,
      ) => Promise<Record<string, NonNullable<Message["reference"]>>>
    >(),
  mockResolveDMMessageReferences:
    vi.fn<
      (
        id: string,
        messageIds: string[],
        signal?: AbortSignal,
      ) => Promise<Record<string, NonNullable<Message["reference"]>>>
    >(),
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
  resolveChannelMessageReferences: (id: string, messageIds: string[], signal?: AbortSignal) =>
    mockResolveChannelMessageReferences(id, messageIds, signal),
  resolveDMMessageReferences: (id: string, messageIds: string[], signal?: AbortSignal) =>
    mockResolveDMMessageReferences(id, messageIds, signal),
  postChannelMessage: (id: string, body: string, parentMessageId?: string) =>
    mockPostChannelMessage(id, body, parentMessageId),
  postDMMessage: vi.fn(),
  favoriteMessage: (id: string) => mockFavoriteMessage(id),
  unfavoriteMessage: (id: string) => mockUnfavoriteMessage(id),
  editMessage: (id: string, body: string, bodyFormat: number) =>
    mockEditMessage(id, body, bodyFormat),
  deleteMessage: (id: string) => mockDeleteMessage(id),
  fetchLinkSafetyStatuses: (messageIds: string[], signal?: AbortSignal) =>
    mockFetchLinkSafetyStatuses(messageIds, signal),
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
  isForwarded: false,
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

/** Fire the RF-21 terminal refusal, which is addressed to the author alone. */
function fireWsBlockedEvent(messageId: string, reason?: string): void {
  capturedOnMessageBlocked?.({
    type: "message.blocked",
    message_id: messageId,
    ...(reason === undefined ? {} : { reason }),
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

function fireFullDelete(messageId: string, targetId = "ch-1"): void {
  capturedOnMessageUpdated?.({
    type: "message.updated",
    target_type: "channel",
    target_id: targetId,
    message_update: {
      message_id: messageId,
      channel_id: targetId,
      body: "",
      body_format: "v1",
      edited_at: "",
      edit_count: 0,
      is_edited: false,
      status: "deleted",
      is_removed: true,
      deleted_at: "2026-07-14T12:00:00Z",
      updated_at: "2026-07-14T12:00:00Z",
    },
  });
}

// ── Setup / teardown ──────────────────────────────────────────────────────────

beforeEach(() => {
  setTokens("test-access-token");
  capturedOnMessageCreated = null;
  capturedOnMessageBlocked = null;
  capturedOnMessageUpdated = null;
  capturedOnReactionUpdated = null;
  capturedOnReactionError = null;
  capturedOnSubscriptionError = null;
  capturedOnSubscribed = null;
  capturedOnPinUpdated = null;
  vi.clearAllMocks();
});

afterEach(() => {
  vi.useRealTimers();
  clearTokens();
});

// ── WS integration tests ──────────────────────────────────────────────────────

describe("useMessages — WS message.created integration", () => {
  it("does not refetch a focused message already present in the page", async () => {
    mockFetchChannelMessages.mockResolvedValue({
      messages: [makeMessage({ id: "focused" })],
      nextCursor: "",
    });

    const { result } = renderHook(() =>
      useMessages({
        kind: "channel",
        targetId: "ch-1",
        currentUserId: "user-me",
        focusMessageId: "focused",
      }),
    );

    await waitFor(() => expect(result.current.state.status).toBe("ready"));
    expect(mockFetchChannelMessage).not.toHaveBeenCalled();
  });

  it("loads a focused DM message outside the current page", async () => {
    mockFetchDMMessages.mockResolvedValue(emptyPage);
    mockFetchDMMessage.mockResolvedValue(makeMessage({ id: "focused-dm" }));

    const { result } = renderHook(() =>
      useMessages({
        kind: "dm",
        targetId: "dm-1",
        currentUserId: "user-me",
        focusMessageId: "focused-dm",
      }),
    );

    await waitFor(() => expect(result.current.state.status).toBe("ready"));
    expect(result.current.state.messages.map((message) => message.id)).toEqual(["focused-dm"]);
    expect(mockFetchDMMessage).toHaveBeenCalledWith("dm-1", "focused-dm", expect.any(AbortSignal));
  });

  it("keeps the page generic when a focused message cannot be read", async () => {
    mockFetchChannelMessages.mockResolvedValue(emptyPage);
    mockFetchChannelMessage.mockRejectedValue(new Error("not found"));

    const { result } = renderHook(() =>
      useMessages({
        kind: "channel",
        targetId: "ch-1",
        currentUserId: "user-me",
        focusMessageId: "protected",
      }),
    );

    await waitFor(() => expect(result.current.state.status).toBe("ready"));
    expect(result.current.state.messages).toEqual([]);
    expect(result.current.state.realtimeError).toBeNull();
  });

  it("revalidates a mounted reference on focus and removes a revoked preview", async () => {
    const destination = makeMessage({
      id: "destination-message",
      reference: {
        available: true,
        messageId: "source-message",
        targetType: "channel",
        targetId: "private-source",
        targetLabel: "Privado",
        authorDisplayName: "Ana",
        bodyText: "segredo",
        bodyFormat: "v3",
        createdAt: "2026-07-21T12:00:00Z",
      },
    });
    mockFetchChannelMessages.mockResolvedValue({ messages: [destination], nextCursor: "" });
    mockResolveChannelMessageReferences.mockResolvedValue({
      "destination-message": { available: false },
    });

    const { result } = renderHook(() =>
      useMessages({ kind: "channel", targetId: "destination", currentUserId: "user-me" }),
    );
    await waitFor(() => expect(result.current.state.status).toBe("ready"));
    expect(result.current.state.messages[0].reference).toMatchObject({ available: true });

    act(() => window.dispatchEvent(new Event("focus")));

    await waitFor(() =>
      expect(result.current.state.messages[0].reference).toEqual({ available: false }),
    );
    expect(mockResolveChannelMessageReferences).toHaveBeenCalledWith(
      "destination",
      ["destination-message"],
      expect.any(AbortSignal),
    );

    mockResolveChannelMessageReferences.mockRejectedValueOnce(new Error("revoked"));
    act(() => window.dispatchEvent(new Event("focus")));
    await waitFor(() => expect(mockResolveChannelMessageReferences).toHaveBeenCalledTimes(2));
    expect(result.current.state.messages[0].reference).toEqual({ available: false });
  });

  it("revalidates mounted DM references through the authorized DM endpoint", async () => {
    const destination = makeMessage({
      id: "destination-dm-message",
      reference: { available: false },
    });
    mockFetchDMMessages.mockResolvedValue({ messages: [destination], nextCursor: "" });
    mockResolveDMMessageReferences.mockResolvedValue({
      "destination-dm-message": { available: false },
    });

    const { result } = renderHook(() =>
      useMessages({ kind: "dm", targetId: "dm-destination", currentUserId: "me" }),
    );
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() => window.dispatchEvent(new Event("focus")));

    await waitFor(() =>
      expect(mockResolveDMMessageReferences).toHaveBeenCalledWith(
        "dm-destination",
        ["destination-dm-message"],
        expect.any(AbortSignal),
      ),
    );
  });

  it("coalesces fifty mounted references into one authorized batch", async () => {
    const messages = Array.from({ length: 50 }, (_, index) =>
      makeMessage({ id: `destination-${index}`, reference: { available: false } }),
    );
    mockFetchChannelMessages.mockResolvedValue({ messages, nextCursor: "" });
    mockResolveChannelMessageReferences.mockResolvedValue(
      Object.fromEntries(messages.map((message) => [message.id, { available: false }])),
    );

    const { result } = renderHook(() =>
      useMessages({ kind: "channel", targetId: "destination", currentUserId: "user-me" }),
    );
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    const visibility = vi.spyOn(document, "visibilityState", "get");
    visibility.mockReturnValue("hidden");
    act(() => document.dispatchEvent(new Event("visibilitychange")));
    expect(mockResolveChannelMessageReferences).not.toHaveBeenCalled();
    visibility.mockReturnValue("visible");
    act(() => {
      document.dispatchEvent(new Event("visibilitychange"));
      window.dispatchEvent(new Event("focus"));
    });
    await waitFor(() => expect(mockResolveChannelMessageReferences).toHaveBeenCalledTimes(1));
    expect(mockResolveChannelMessageReferences.mock.calls[0][1]).toHaveLength(50);
    visibility.mockRestore();
  });

  it("fails closed beyond the bounded one-request revalidation window", async () => {
    const protectedReference: NonNullable<Message["reference"]> = {
      available: true,
      messageId: "protected-source",
      targetType: "channel",
      targetId: "private-source",
      targetLabel: "Privado",
      authorDisplayName: "Ana",
      bodyText: "segredo",
      bodyFormat: "v3",
      createdAt: "2026-07-21T12:00:00Z",
    };
    const messages = Array.from({ length: 101 }, (_, index) =>
      makeMessage({
        id: `destination-${index}`,
        reference: index === 0 ? protectedReference : { available: false },
      }),
    );
    mockFetchChannelMessages.mockResolvedValue({ messages, nextCursor: "" });
    mockResolveChannelMessageReferences.mockResolvedValue({});
    const { result } = renderHook(() =>
      useMessages({ kind: "channel", targetId: "destination", currentUserId: "user-me" }),
    );

    await waitFor(() =>
      expect(result.current.state.messages[0].reference).toEqual({ available: false }),
    );
    act(() => window.dispatchEvent(new Event("focus")));
    await waitFor(() => expect(mockResolveChannelMessageReferences).toHaveBeenCalledTimes(1));
    expect(mockResolveChannelMessageReferences.mock.calls[0][1]).toHaveLength(100);
    expect(mockResolveChannelMessageReferences.mock.calls[0][1]).not.toContain("destination-0");
  });

  it("ignores an older authorized batch after a newer unavailable result", async () => {
    const destination = makeMessage({ id: "destination-message", reference: { available: false } });
    mockFetchChannelMessages.mockResolvedValue({ messages: [destination], nextCursor: "" });
    let resolveFirst!: (value: Record<string, NonNullable<Message["reference"]>>) => void;
    let resolveSecond!: (value: Record<string, NonNullable<Message["reference"]>>) => void;
    mockResolveChannelMessageReferences
      .mockReturnValueOnce(new Promise((resolve) => (resolveFirst = resolve)))
      .mockReturnValueOnce(new Promise((resolve) => (resolveSecond = resolve)));

    const { result } = renderHook(() =>
      useMessages({ kind: "channel", targetId: "destination", currentUserId: "user-me" }),
    );
    await waitFor(() => expect(result.current.state.status).toBe("ready"));
    act(() => window.dispatchEvent(new Event("focus")));
    await waitFor(() => expect(mockResolveChannelMessageReferences).toHaveBeenCalledTimes(1));
    act(() => window.dispatchEvent(new Event("focus")));
    await waitFor(() => expect(mockResolveChannelMessageReferences).toHaveBeenCalledTimes(2));

    act(() => resolveSecond({ "destination-message": { available: false } }));
    await waitFor(() =>
      expect(result.current.state.messages[0].reference).toEqual({ available: false }),
    );
    act(() =>
      resolveFirst({
        "destination-message": {
          available: true,
          messageId: "protected-source",
          targetType: "channel",
          targetId: "private-source",
          targetLabel: "Privado",
          authorDisplayName: "Ana",
          bodyText: "segredo",
          bodyFormat: "v3",
          createdAt: "2026-07-21T12:00:00Z",
        },
      }),
    );
    await act(async () => Promise.resolve());
    expect(result.current.state.messages[0].reference).toEqual({ available: false });
  });

  it("refreshes only the reference and preserves the destination snapshot", async () => {
    const destination = makeMessage({
      id: "destination-message",
      bodyText: "edited body",
      editedAt: "2026-07-21T13:00:00Z",
      reactions: [{ emoji: "👍", count: 4, reactedByMe: true }],
      isFavorited: true,
      reference: { available: false },
    });
    mockFetchChannelMessages.mockResolvedValue({ messages: [destination], nextCursor: "" });
    mockResolveChannelMessageReferences.mockResolvedValue({
      "destination-message": { available: false },
    });
    const { result } = renderHook(() =>
      useMessages({ kind: "channel", targetId: "destination", currentUserId: "user-me" }),
    );
    await waitFor(() => expect(result.current.state.status).toBe("ready"));
    act(() => window.dispatchEvent(new Event("focus")));
    await waitFor(() => expect(mockResolveChannelMessageReferences).toHaveBeenCalled());
    expect(result.current.state.messages[0]).toMatchObject({
      bodyText: "edited body",
      editedAt: "2026-07-21T13:00:00Z",
      reactions: [{ emoji: "👍", count: 4, reactedByMe: true }],
      isFavorited: true,
      reference: { available: false },
    });
  });

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

    act(() => capturedOnReactionError?.({ type: "error", code: "reaction_limit_reached", limit: 5 }));
    expect(result.current.state.actionError).toBe("Você pode adicionar no máximo 5 reações por mensagem.");
  });

  it("maps subscribe errors to realtime state without showing a reaction failure", async () => {
    mockFetchChannelMessages.mockResolvedValue(emptyPage);
    const { result } = renderHook(() =>
      useMessages({ kind: "channel", targetId: "ch-1", currentUserId: "user-me" }),
    );
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() =>
      capturedOnSubscriptionError?.({
        type: "error",
        operation: "subscribe",
        code: "room_access_denied",
      }),
    );

    expect(result.current.state.realtimeError).toMatch(/tempo real/i);
    expect(result.current.state.actionError).toBeNull();
  });

  it("clears a technical realtime error when the current subscription is confirmed", async () => {
    mockFetchChannelMessages.mockResolvedValue(emptyPage);
    const { result } = renderHook(() =>
      useMessages({ kind: "channel", targetId: "ch-1", currentUserId: "user-me" }),
    );
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() =>
      capturedOnSubscriptionError?.({
        type: "error",
        operation: "subscribe",
        code: "room_subscription_unavailable",
      }),
    );
    expect(result.current.state.realtimeError).toMatch(/tempo real/i);

    act(() =>
      capturedOnSubscribed?.({
        type: "subscribed",
        operation: "subscribe",
        target_type: "channel",
        target_id: "ch-1",
      }),
    );

    expect(result.current.state.realtimeError).toBeNull();
    expect(result.current.state.actionError).toBeNull();
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

  it("preserves the forwarded marker from a realtime payload", async () => {
    mockFetchChannelMessages.mockResolvedValue(emptyPage);
    const { result } = renderHook(() =>
      useMessages({ kind: "channel", targetId: "ch-1", currentUserId: "user-me" }),
    );
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() =>
      fireWsEventWithPayload(
        "channel",
        "ch-1",
        makePayload({ id: "forwarded", is_forwarded: true }),
      ),
    );

    await waitFor(() => expect(result.current.state.messages).toHaveLength(1));
    expect(result.current.state.messages[0].isForwarded).toBe(true);
  });

  it("normalizes false, missing, and unexpected realtime forwarding markers to false", async () => {
    mockFetchChannelMessages.mockResolvedValue(emptyPage);
    const { result } = renderHook(() =>
      useMessages({ kind: "channel", targetId: "ch-1", currentUserId: "user-me" }),
    );
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() => {
      fireWsEventWithPayload("channel", "ch-1", makePayload({ id: "normal", is_forwarded: false }));
      fireWsEventWithPayload("channel", "ch-1", makePayload({ id: "legacy" }));
      fireWsEventWithPayload(
        "channel",
        "ch-1",
        makePayload({ id: "unexpected", is_forwarded: "true" }),
      );
    });

    await waitFor(() => expect(result.current.state.messages).toHaveLength(3));
    expect(result.current.state.messages.every((message) => !message.isForwarded)).toBe(true);
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
        message_update: { ...update.message_update!, message_id: "missing", channel_id: "ch-1" },
      }),
    );

    expect(result.current.state.messages).toEqual([original]);
  });
});

describe("useMessages — message deletion", () => {
  it("reconciles the DELETE response in place and sanitizes loaded replies", async () => {
    const createdAt = "2026-07-14T10:00:00Z";
    const deletedAt = "2026-07-14T12:00:00Z";
    const original = makeMessage({
      id: "msg-delete",
      bodyText: "segredo",
      createdAt,
      reactions: [{ emoji: "👍", count: 1, reactedByMe: true }],
    });
    const reply = makeMessage({
      id: "msg-reply",
      bodyText: "resposta",
      quoted: {
        id: original.id,
        authorId: original.senderId,
        bodyText: original.bodyText,
        bodyFormat: "v1",
        isRemoved: false,
        deletedAt: null,
        createdAt,
      },
    });
    mockFetchChannelMessages.mockResolvedValue({ messages: [original, reply], nextCursor: "" });
    mockDeleteMessage.mockResolvedValue(
      makeMessage({
        ...original,
        bodyText: "",
        status: "deleted",
        isRemoved: true,
        deletedAt,
        updatedAt: deletedAt,
        reactions: [],
      }),
    );
    const onMessageRemoved = vi.fn();
    const { result } = renderHook(() =>
      useMessages({
        kind: "channel",
        targetId: "ch-1",
        currentUserId: "user-me",
        onMessageRemoved,
      }),
    );
    await waitFor(() => expect(result.current.state.status).toBe("ready"));
    act(() => result.current.selectReply(original));

    await act(() => result.current.deleteMessageLocal(original.id));

    expect(mockDeleteMessage).toHaveBeenCalledWith(original.id);
    expect(result.current.state.messages.map((message) => message.id)).toEqual([
      "msg-delete",
      "msg-reply",
    ]);
    expect(result.current.state.messages[0]).toMatchObject({
      bodyText: "",
      createdAt,
      status: "deleted",
      isRemoved: true,
      deletedAt,
      reactions: [],
    });
    expect(result.current.state.messages[1].quoted).toMatchObject({
      bodyText: "",
      isRemoved: true,
      deletedAt,
    });
    expect(result.current.state.replyTo).toBeNull();
    expect(onMessageRemoved).toHaveBeenCalledOnce();
  });

  it("preserves the message and surfaces generic feedback when DELETE fails", async () => {
    const original = makeMessage({ id: "msg-delete", bodyText: "permanece" });
    mockFetchChannelMessages.mockResolvedValue({ messages: [original], nextCursor: "" });
    mockDeleteMessage.mockRejectedValue(new Error("forbidden details"));
    const { result } = renderHook(() =>
      useMessages({ kind: "channel", targetId: "ch-1", currentUserId: "user-me" }),
    );
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    let rejected: unknown;
    await act(async () => {
      try {
        await result.current.deleteMessageLocal(original.id);
      } catch (error) {
        rejected = error;
      }
    });

    expect(rejected).toEqual(new Error("forbidden details"));
    expect(result.current.state.messages).toEqual([original]);
    await waitFor(() =>
      expect(result.current.state.actionError).toBe(
        "Não foi possível excluir a mensagem. Tente novamente.",
      ),
    );
  });

  it("applies repeated sanitized WS deletions idempotently and ignores a late edit", async () => {
    mockFetchChannelMessages.mockResolvedValue({
      messages: [makeMessage({ id: "msg-delete", bodyText: "original" })],
      nextCursor: "",
    });
    const { result } = renderHook(() =>
      useMessages({ kind: "channel", targetId: "ch-1", currentUserId: "user-me" }),
    );
    await waitFor(() => expect(result.current.state.status).toBe("ready"));
    const deletion: WSMessageUpdatedEvent = {
      type: "message.updated",
      target_type: "channel",
      target_id: "ch-1",
      message_update: {
        message_id: "msg-delete",
        channel_id: "ch-1",
        body: "",
        body_format: "v1",
        edited_at: "",
        edit_count: 0,
        is_edited: false,
        status: "deleted",
        is_removed: true,
        deleted_at: "2026-07-14T12:00:00Z",
        updated_at: "2026-07-14T12:00:00Z",
      },
    };

    act(() => {
      capturedOnMessageUpdated?.(deletion);
      capturedOnMessageUpdated?.(deletion);
      capturedOnMessageUpdated?.({
        ...deletion,
        message_update: {
          ...deletion.message_update!,
          body: "edição atrasada",
          status: "active",
          is_removed: false,
          edit_count: 2,
          is_edited: true,
        },
      });
    });

    expect(result.current.state.messages).toHaveLength(1);
    expect(result.current.state.messages[0]).toMatchObject({
      bodyText: "",
      status: "deleted",
      isRemoved: true,
      deletedAt: "2026-07-14T12:00:00Z",
    });
  });

  it("reloads a sanitized snapshot for route-only distributed updates", async () => {
    mockFetchChannelMessages.mockResolvedValue({
      messages: [makeMessage({ id: "msg-delete", bodyText: "original" })],
      nextCursor: "",
    });
    mockFetchChannelMessage.mockResolvedValue(
      makeMessage({
        id: "msg-delete",
        bodyText: "",
        isRemoved: true,
        status: "deleted",
        deletedAt: "2026-07-14T12:00:00Z",
      }),
    );
    const { result } = renderHook(() =>
      useMessages({ kind: "channel", targetId: "ch-1", currentUserId: "user-me" }),
    );
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() =>
      capturedOnMessageUpdated?.({
        type: "message.updated",
        target_type: "channel",
        target_id: "ch-1",
        message_id: "msg-delete",
      }),
    );

    await waitFor(() => expect(result.current.state.messages[0].isRemoved).toBe(true));
    expect(mockFetchChannelMessage).toHaveBeenCalledWith(
      "ch-1",
      "msg-delete",
      expect.any(AbortSignal),
    );
    expect(result.current.state.messages[0].bodyText).toBe("");
  });

  it("reconciles concurrent route-only updates for different messages", async () => {
    mockFetchChannelMessages.mockResolvedValue({
      messages: [
        makeMessage({ id: "msg-first", bodyText: "first original" }),
        makeMessage({ id: "msg-second", bodyText: "second original" }),
      ],
      nextCursor: "",
    });
    const snapshots = new Map<string, (message: Message) => void>();
    mockFetchChannelMessage.mockImplementation(
      (_targetId, messageId) =>
        new Promise((resolve) => {
          snapshots.set(messageId, resolve);
        }),
    );
    const { result } = renderHook(() =>
      useMessages({ kind: "channel", targetId: "ch-1", currentUserId: "user-me" }),
    );
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() => {
      capturedOnMessageUpdated?.({
        type: "message.updated",
        target_type: "channel",
        target_id: "ch-1",
        message_id: "msg-first",
      });
      capturedOnMessageUpdated?.({
        type: "message.updated",
        target_type: "channel",
        target_id: "ch-1",
        message_id: "msg-second",
      });
    });

    expect(mockFetchChannelMessage).toHaveBeenCalledTimes(2);
    act(() => {
      snapshots.get("msg-second")?.(makeMessage({ id: "msg-second", bodyText: "second updated" }));
      snapshots.get("msg-first")?.(
        makeMessage({ id: "msg-first", bodyText: "", isRemoved: true, status: "deleted" }),
      );
    });

    await waitFor(() =>
      expect(result.current.state.messages).toEqual([
        expect.objectContaining({ id: "msg-first", bodyText: "", isRemoved: true }),
        expect.objectContaining({ id: "msg-second", bodyText: "second updated" }),
      ]),
    );
  });

  it("keeps a route-only delete when creation for the same message is still pending", async () => {
    mockFetchChannelMessages.mockResolvedValue(emptyPage);
    let resolveCreated!: (message: Message) => void;
    let resolveDeleted!: (message: Message) => void;
    mockFetchChannelMessage
      .mockImplementationOnce(() => new Promise((resolve) => (resolveCreated = resolve)))
      .mockImplementationOnce(() => new Promise((resolve) => (resolveDeleted = resolve)));
    const { result } = renderHook(() =>
      useMessages({ kind: "channel", targetId: "ch-1", currentUserId: "user-me" }),
    );
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() => {
      fireWsEventNoPayload("channel", "ch-1", "msg-race");
      capturedOnMessageUpdated?.({
        type: "message.updated",
        target_type: "channel",
        target_id: "ch-1",
        message_id: "msg-race",
      });
    });
    expect(mockFetchChannelMessage).toHaveBeenCalledTimes(2);

    act(() =>
      resolveDeleted(
        makeMessage({ id: "msg-race", bodyText: "", isRemoved: true, status: "deleted" }),
      ),
    );
    await waitFor(() =>
      expect(result.current.state.messages).toEqual([
        expect.objectContaining({ id: "msg-race", bodyText: "", isRemoved: true }),
      ]),
    );

    act(() => resolveCreated(makeMessage({ id: "msg-race", bodyText: "stale original" })));
    await waitFor(() => expect(result.current.state.messages).toHaveLength(1));
    expect(result.current.state.messages[0]).toMatchObject({
      id: "msg-race",
      bodyText: "",
      isRemoved: true,
      status: "deleted",
    });
  });

  it("reconciles a full delete while route-only creation for the same ID is pending", async () => {
    mockFetchChannelMessages.mockResolvedValue(emptyPage);
    let resolveCreated!: (message: Message) => void;
    let resolveDeleted!: (message: Message) => void;
    mockFetchChannelMessage
      .mockImplementationOnce(() => new Promise((resolve) => (resolveCreated = resolve)))
      .mockImplementationOnce(() => new Promise((resolve) => (resolveDeleted = resolve)));
    const { result } = renderHook(() =>
      useMessages({ kind: "channel", targetId: "ch-1", currentUserId: "user-me" }),
    );
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() => {
      fireWsEventNoPayload("channel", "ch-1", "msg-full-race");
      capturedOnMessageUpdated?.({
        type: "message.updated",
        target_type: "channel",
        target_id: "ch-1",
        message_update: {
          message_id: "msg-full-race",
          channel_id: "ch-1",
          body: "",
          body_format: "v1",
          edited_at: "",
          edit_count: 0,
          is_edited: false,
          status: "deleted",
          is_removed: true,
          deleted_at: "2026-07-14T12:00:00Z",
          updated_at: "2026-07-14T12:00:00Z",
        },
      });
    });
    expect(mockFetchChannelMessage).toHaveBeenCalledTimes(2);

    act(() => resolveCreated(makeMessage({ id: "msg-full-race", bodyText: "stale original" })));
    await waitFor(() => expect(result.current.state.messages).toEqual([]));

    act(() =>
      resolveDeleted(
        makeMessage({
          id: "msg-full-race",
          bodyText: "",
          isRemoved: true,
          status: "deleted",
          deletedAt: "2026-07-14T12:00:00Z",
        }),
      ),
    );
    await waitFor(() =>
      expect(result.current.state.messages).toEqual([
        expect.objectContaining({
          id: "msg-full-race",
          bodyText: "",
          isRemoved: true,
          status: "deleted",
        }),
      ]),
    );
  });

  it("keeps a full delete terminal when a full create arrives later", async () => {
    mockFetchChannelMessages.mockResolvedValue(emptyPage);
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
          message_id: "msg-reordered",
          channel_id: "ch-1",
          body: "",
          body_format: "v1",
          edited_at: "",
          edit_count: 0,
          is_edited: false,
          status: "deleted",
          is_removed: true,
          deleted_at: "2026-07-14T12:00:00Z",
          updated_at: "2026-07-14T12:00:00Z",
        },
      }),
    );
    expect(result.current.state.messages).toEqual([]);

    act(() =>
      fireWsEventWithPayload(
        "channel",
        "ch-1",
        makePayload({ id: "msg-reordered", body_text: "body that must stay hidden" }),
      ),
    );

    expect(result.current.state.messages).toEqual([
      expect.objectContaining({
        id: "msg-reordered",
        bodyText: "",
        isRemoved: true,
        status: "deleted",
        deletedAt: "2026-07-14T12:00:00Z",
      }),
    ]);
    expect(mockFetchChannelMessage).not.toHaveBeenCalled();
  });

  it("sanitizes a stale initial page that resolves after a full delete", async () => {
    let resolvePage!: (page: MessagePage) => void;
    mockFetchChannelMessages.mockImplementation(
      () => new Promise((resolve) => (resolvePage = resolve)),
    );
    const { result } = renderHook(() =>
      useMessages({ kind: "channel", targetId: "ch-1", currentUserId: "user-me" }),
    );

    act(() => fireFullDelete("msg-stale-page"));
    act(() =>
      resolvePage({
        messages: [
          makeMessage({ id: "msg-stale-page", bodyText: "stale body" }),
          makeMessage({
            id: "msg-stale-reply",
            quoted: {
              id: "msg-stale-page",
              authorId: "user-sender",
              bodyText: "stale quote",
              bodyFormat: "v1",
              isRemoved: false,
              deletedAt: null,
              createdAt: "2026-07-14T11:00:00Z",
            },
          }),
        ],
        nextCursor: "",
      }),
    );

    await waitFor(() => expect(result.current.state.status).toBe("ready"));
    expect(result.current.state.messages[0]).toMatchObject({
      id: "msg-stale-page",
      bodyText: "",
      isRemoved: true,
      status: "deleted",
    });
    expect(result.current.state.messages[1].quoted).toMatchObject({
      id: "msg-stale-page",
      bodyText: "",
      isRemoved: true,
    });
  });

  it("sanitizes a stale older page that resolves after a full delete", async () => {
    let resolveOlderPage!: (page: MessagePage) => void;
    mockFetchChannelMessages
      .mockResolvedValueOnce({
        messages: [makeMessage({ id: "msg-visible" })],
        nextCursor: "older-cursor",
      })
      .mockImplementationOnce(() => new Promise((resolve) => (resolveOlderPage = resolve)));
    const { result } = renderHook(() =>
      useMessages({ kind: "channel", targetId: "ch-1", currentUserId: "user-me" }),
    );
    await waitFor(() => expect(result.current.state.status).toBe("ready"));
    act(() => result.current.loadMore());

    act(() => fireFullDelete("msg-stale-older"));
    act(() =>
      resolveOlderPage({
        messages: [makeMessage({ id: "msg-stale-older", bodyText: "stale older body" })],
        nextCursor: "",
      }),
    );

    await waitFor(() => expect(result.current.state.loadingMore).toBe(false));
    expect(
      result.current.state.messages.find((message) => message.id === "msg-stale-older"),
    ).toMatchObject({
      bodyText: "",
      isRemoved: true,
      status: "deleted",
    });
  });

  it("sanitizes a stale send response that resolves after a full delete", async () => {
    mockFetchChannelMessages.mockResolvedValue(emptyPage);
    let resolveSend!: (message: Message) => void;
    mockPostChannelMessage.mockImplementation(
      () => new Promise((resolve) => (resolveSend = resolve)),
    );
    const { result } = renderHook(() =>
      useMessages({ kind: "channel", targetId: "ch-1", currentUserId: "user-me" }),
    );
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    let send!: ReturnType<typeof result.current.sendMessage>;
    act(() => {
      send = result.current.sendMessage("stale send body");
      fireFullDelete("msg-stale-send");
    });
    await act(async () => {
      resolveSend(makeMessage({ id: "msg-stale-send", bodyText: "stale send body" }));
      await send;
    });

    expect(result.current.state.messages).toEqual([
      expect.objectContaining({
        id: "msg-stale-send",
        bodyText: "",
        isRemoved: true,
        status: "deleted",
      }),
    ]);
  });

  it("does not revert a deleted message when an earlier edit fails", async () => {
    const original = makeMessage({ id: "msg-edit-delete", bodyText: "original" });
    mockFetchChannelMessages.mockResolvedValue({ messages: [original], nextCursor: "" });
    let rejectEdit!: (error: Error) => void;
    mockEditMessage.mockImplementation(
      () => new Promise((_resolve, reject) => (rejectEdit = reject)),
    );
    const { result } = renderHook(() =>
      useMessages({ kind: "channel", targetId: "ch-1", currentUserId: "user-me" }),
    );
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    let edit!: ReturnType<typeof result.current.editMessageLocal>;
    act(() => {
      edit = result.current.editMessageLocal(original.id, "optimistic body", "v1");
    });
    act(() => fireFullDelete(original.id));
    act(() => rejectEdit(new Error("edit failed after delete")));
    await expect(edit).rejects.toThrow("edit failed after delete");

    expect(result.current.state.messages[0]).toMatchObject({
      id: original.id,
      bodyText: "",
      isRemoved: true,
      status: "deleted",
    });
  });

  it("does not insert an absent historical message from a route-only update", async () => {
    mockFetchChannelMessages.mockResolvedValue({
      messages: [makeMessage({ id: "msg-visible" })],
      nextCursor: "older-cursor",
    });
    mockFetchChannelMessage.mockResolvedValue(
      makeMessage({ id: "msg-historical", bodyText: "edited outside the loaded page" }),
    );
    const { result } = renderHook(() =>
      useMessages({ kind: "channel", targetId: "ch-1", currentUserId: "user-me" }),
    );
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() =>
      capturedOnMessageUpdated?.({
        type: "message.updated",
        target_type: "channel",
        target_id: "ch-1",
        message_id: "msg-historical",
      }),
    );

    await waitFor(() => expect(mockFetchChannelMessage).toHaveBeenCalledOnce());
    expect(result.current.state.messages).toEqual([expect.objectContaining({ id: "msg-visible" })]);
    expect(result.current.state.nextCursor).toBe("older-cursor");
  });

  it("handles status-only deletion events using the authoritative updated timestamp", async () => {
    mockFetchChannelMessages.mockResolvedValue({
      messages: [makeMessage({ id: "msg-delete", bodyText: "original" })],
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
          message_id: "msg-delete",
          channel_id: "ch-1",
          body: "",
          body_format: "v1",
          edited_at: "",
          edit_count: 0,
          is_edited: false,
          status: "deleted",
          is_removed: false,
          updated_at: "2026-07-14T12:00:00Z",
        },
      }),
    );

    expect(result.current.state.messages[0]).toMatchObject({
      bodyText: "",
      isRemoved: true,
      deletedAt: "2026-07-14T12:00:00Z",
      updatedAt: "2026-07-14T12:00:00Z",
    });
  });

  it("reports route-only snapshot failures and silently ignores aborts", async () => {
    mockFetchChannelMessages.mockResolvedValue({
      messages: [makeMessage({ id: "msg-delete" })],
      nextCursor: "",
    });
    mockFetchChannelMessage
      .mockRejectedValueOnce(new Error("backend detail"))
      .mockRejectedValueOnce(new DOMException("aborted", "AbortError"));
    const { result } = renderHook(() =>
      useMessages({ kind: "channel", targetId: "ch-1", currentUserId: "user-me" }),
    );
    await waitFor(() => expect(result.current.state.status).toBe("ready"));
    const routeOnly: WSMessageUpdatedEvent = {
      type: "message.updated",
      target_type: "channel",
      target_id: "ch-1",
      message_id: "msg-delete",
    };

    act(() => capturedOnMessageUpdated?.(routeOnly));
    await waitFor(() =>
      expect(result.current.state.realtimeError).toBe(
        "Não foi possível atualizar mensagens em tempo real.",
      ),
    );
    act(() => capturedOnMessageUpdated?.(routeOnly));

    await waitFor(() => expect(mockFetchChannelMessage).toHaveBeenCalledTimes(2));
    expect(result.current.state.realtimeError).toBe(
      "Não foi possível atualizar mensagens em tempo real.",
    );
  });

  it("reloads a deleted DM snapshot from a route-only update", async () => {
    mockFetchDMMessages.mockResolvedValue({
      messages: [makeMessage({ id: "msg-delete", bodyText: "dm original" })],
      nextCursor: "",
    });
    mockFetchDMMessage.mockResolvedValue(
      makeMessage({ id: "msg-delete", bodyText: "", isRemoved: true, status: "deleted" }),
    );
    const { result } = renderHook(() =>
      useMessages({ kind: "dm", targetId: "dm-1", currentUserId: "user-me" }),
    );
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() =>
      capturedOnMessageUpdated?.({
        type: "message.updated",
        target_type: "dm",
        target_id: "dm-1",
        message_id: "msg-delete",
      }),
    );

    await waitFor(() => expect(result.current.state.messages[0].isRemoved).toBe(true));
    expect(mockFetchDMMessage).toHaveBeenCalledWith("dm-1", "msg-delete", expect.any(AbortSignal));
  });

  it("discards stale DELETE outcomes after the user changes target", async () => {
    const original = makeMessage({ id: "msg-delete", bodyText: "original" });
    mockFetchChannelMessages
      .mockResolvedValueOnce({ messages: [original], nextCursor: "" })
      .mockResolvedValue({ messages: [], nextCursor: "" });
    let resolveDelete!: (message: Message) => void;
    let rejectDelete!: (error: Error) => void;
    mockDeleteMessage
      .mockImplementationOnce(() => new Promise((resolve) => (resolveDelete = resolve)))
      .mockImplementationOnce(() => new Promise((_resolve, reject) => (rejectDelete = reject)));
    const { result, rerender } = renderHook(
      ({ targetId }) => useMessages({ kind: "channel", targetId, currentUserId: "user-me" }),
      { initialProps: { targetId: "ch-1" } },
    );
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    let success!: Promise<void>;
    act(() => {
      success = result.current.deleteMessageLocal(original.id);
    });
    rerender({ targetId: "ch-2" });
    await waitFor(() => expect(result.current.state.messages).toEqual([]));
    act(() =>
      resolveDelete(makeMessage({ ...original, bodyText: "", isRemoved: true, status: "deleted" })),
    );
    await success;

    let failure!: Promise<void>;
    act(() => {
      failure = result.current.deleteMessageLocal("missing");
    });
    rerender({ targetId: "ch-3" });
    act(() => rejectDelete(new Error("stale failure")));
    await expect(failure).rejects.toThrow("stale failure");
    expect(result.current.state.actionError).toBeNull();
  });

  it("ignores route-only updates without an ID and stale snapshot outcomes", async () => {
    mockFetchChannelMessages.mockResolvedValue({
      messages: [makeMessage({ id: "msg-delete" })],
      nextCursor: "",
    });
    let resolveSnapshot!: (message: Message) => void;
    let rejectSnapshot!: (error: Error) => void;
    mockFetchChannelMessage
      .mockImplementationOnce(() => new Promise((resolve) => (resolveSnapshot = resolve)))
      .mockImplementationOnce(() => new Promise((_resolve, reject) => (rejectSnapshot = reject)));
    const { result, rerender } = renderHook(
      ({ targetId }) => useMessages({ kind: "channel", targetId, currentUserId: "user-me" }),
      { initialProps: { targetId: "ch-1" } },
    );
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() =>
      capturedOnMessageUpdated?.({
        type: "message.updated",
        target_type: "channel",
        target_id: "ch-1",
      }),
    );
    expect(mockFetchChannelMessage).not.toHaveBeenCalled();

    act(() =>
      capturedOnMessageUpdated?.({
        type: "message.updated",
        target_type: "channel",
        target_id: "ch-1",
        message_id: "msg-delete",
      }),
    );
    rerender({ targetId: "ch-2" });
    await waitFor(() => expect(result.current.state.status).toBe("ready"));
    act(() =>
      resolveSnapshot(makeMessage({ id: "msg-delete", isRemoved: true, status: "deleted" })),
    );

    act(() =>
      capturedOnMessageUpdated?.({
        type: "message.updated",
        target_type: "channel",
        target_id: "ch-2",
        message_id: "msg-delete",
      }),
    );
    rerender({ targetId: "ch-3" });
    await waitFor(() => expect(result.current.state.status).toBe("ready"));
    act(() => rejectSnapshot(new Error("stale snapshot failure")));
    await waitFor(() => expect(mockFetchChannelMessage).toHaveBeenCalledTimes(2));
    expect(result.current.state.realtimeError).toBeNull();
  });

  it("does not resurrect a deleted message from an active route-only snapshot", async () => {
    const deleted = makeMessage({
      id: "msg-delete",
      bodyText: "",
      isRemoved: true,
      status: "deleted",
    });
    mockFetchChannelMessages.mockResolvedValue({ messages: [deleted], nextCursor: "" });
    mockFetchChannelMessage.mockResolvedValue(
      makeMessage({ id: "msg-delete", bodyText: "stale body", isRemoved: false, status: "active" }),
    );
    const { result } = renderHook(() =>
      useMessages({ kind: "channel", targetId: "ch-1", currentUserId: "user-me" }),
    );
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() =>
      capturedOnMessageUpdated?.({
        type: "message.updated",
        target_type: "channel",
        target_id: "ch-1",
        message_id: "msg-delete",
      }),
    );

    await waitFor(() => expect(mockFetchChannelMessage).toHaveBeenCalledOnce());
    expect(result.current.state.messages[0]).toEqual(deleted);
  });
});

// ── RF-21: a link the backend refused ────────────────────────────────────────
//
// The backend is the authority. These tests assert what the client does with
// its answer: recognise the code, say something a person can act on, and leave
// the timeline showing that nothing was sent.

describe("useMessages — blocked links", () => {
  beforeEach(() => {
    mockFetchChannelMessages.mockResolvedValue(emptyPage);
  });

  async function readyHook() {
    const { result } = renderHook(() =>
      useMessages({ kind: "channel", targetId: "ch-1", currentUserId: "user-me" }),
    );
    await waitFor(() => expect(result.current.state.status).toBe("ready"));
    return result;
  }

  // The capacity refusal, which is a different claim entirely: the backend
  // declined to start a new scan right now and decided nothing about the link.
  // Showing the security warning for it would tell someone their link looks
  // dangerous because a queue was full.
  it("shows a retry message, not a security warning, for a capacity refusal", async () => {
    mockPostChannelMessage.mockRejectedValue(
      new ApiRequestError(429, "link_check_capacity", "links could not be checked right now"),
    );
    const result = await readyHook();

    await act(async () => {
      await expect(result.current.sendMessage("https://novo.example/a")).rejects.toBeTruthy();
    });

    expect(result.current.state.sendError).toBe(
      "Não foi possível verificar os links agora. Tente novamente em instantes.",
    );
    expect(result.current.state.sendError).not.toMatch(/bloqueado por segurança/);
    // The backend refused before creating anything, so there is no pending
    // bubble to show and nothing is stuck sending.
    expect(result.current.state.messages).toEqual([]);
    expect(result.current.state.sending).toBe(false);
  });

  it("shows the security warning for a malicious_url refusal", async () => {
    mockPostChannelMessage.mockRejectedValue(
      new ApiRequestError(
        403,
        "malicious_url",
        "this message contains a link blocked for security reasons",
      ),
    );
    const result = await readyHook();

    await act(async () => {
      await expect(result.current.sendMessage("https://evil.example")).rejects.toBeTruthy();
    });

    expect(result.current.state.sendError).toBe("Este link foi bloqueado por segurança.");
    // Nothing may look sent: no optimistic message, and not stuck sending.
    expect(result.current.state.messages).toEqual([]);
    expect(result.current.state.sending).toBe(false);
  });

  // RF-21 is asynchronous: a link nobody has scanned makes the backend accept
  // the message and withhold it. The sender's own copy comes back with that
  // status, and the client must render it rather than claim a delivery.
  it("keeps a withheld message visible to its sender as pending", async () => {
    mockPostChannelMessage.mockResolvedValue({
      id: "msg-pending",
      senderId: "user-me",
      senderDisplayName: "Me",
      senderEmail: "me@example.com",
      kind: "user",
      bodyText: "veja https://novo.example/x",
      bodyFormat: "v2",
      isRemoved: false,
      status: "pending_link_scan",
      deletedAt: null,
      createdAt: new Date().toISOString(),
      updatedAt: new Date().toISOString(),
      isEdited: false,
      editCount: 0,
      reactions: [],
      isFavorited: false,
      isForwarded: false,
    });
    const result = await readyHook();

    await act(async () => {
      await result.current.sendMessage("veja https://novo.example/x");
    });

    const sent = result.current.state.messages.at(-1);
    expect(sent?.status).toBe("pending_link_scan");
    // No error: it was accepted, not refused.
    expect(result.current.state.sendError).toBeFalsy();
  });

  // The reconciliation the review found missing. A withheld message is returned
  // to its own sender with the same id it will keep, so when the scan clears and
  // the backend broadcasts message.created for that id, discarding the event as
  // "already present" left the sender stuck on "checking links…" while everyone
  // else saw the message.
  it("replaces a pending message with the published one on message.created", async () => {
    const createdAt = new Date().toISOString();
    mockPostChannelMessage.mockResolvedValue({
      id: "msg-pending",
      senderId: "user-me",
      senderDisplayName: "Me",
      senderEmail: "me@example.com",
      kind: "user",
      bodyText: "veja https://novo.example/x",
      bodyFormat: "v2",
      isRemoved: false,
      status: "pending_link_scan",
      deletedAt: null,
      createdAt,
      updatedAt: createdAt,
      isEdited: false,
      editCount: 0,
      reactions: [],
      isFavorited: false,
      isForwarded: false,
    });
    const result = await readyHook();

    await act(async () => {
      await result.current.sendMessage("veja https://novo.example/x");
    });
    expect(result.current.state.messages.filter((m) => m.id === "msg-pending")).toHaveLength(1);
    expect(result.current.state.messages.at(-1)?.status).toBe("pending_link_scan");

    // The scan cleared: the backend promoted it and broadcast the published row.
    await act(async () => {
      fireWsEventWithPayload(
        "channel",
        "ch-1",
        makePayload({
          id: "msg-pending",
          sender_id: "user-me",
          body_text: "veja https://novo.example/x",
          status: "active",
          created_at: createdAt,
        }),
      );
    });

    // One message, not two, and it is now the authoritative published version.
    const matching = result.current.state.messages.filter((m) => m.id === "msg-pending");
    expect(matching).toHaveLength(1);
    expect(matching[0].status).toBe("active");
    expect(matching[0].bodyText).toBe("veja https://novo.example/x");
  });

  // Delivery from the outbox is at-least-once, so the same event may arrive
  // twice. The second one must stay the no-op it always was.
  it("keeps a single message when the published event is delivered twice", async () => {
    const result = await readyHook();
    const payload = makePayload({ id: "msg-dup", status: "active" });

    await act(async () => {
      fireWsEventWithPayload("channel", "ch-1", payload);
      fireWsEventWithPayload("channel", "ch-1", payload);
    });

    expect(result.current.state.messages.filter((m) => m.id === "msg-dup")).toHaveLength(1);
  });

  // RF-21: the refusal must reach the author, or the composer sits on
  // "checking links…" forever — the backend took the message to a terminal
  // state and, before this event existed, told nobody.
  it("removes a pending message when the scan refuses it", async () => {
    const createdAt = new Date().toISOString();
    mockPostChannelMessage.mockResolvedValue({
      id: "msg-blocked",
      senderId: "user-me",
      senderDisplayName: "Me",
      senderEmail: "me@example.com",
      kind: "user",
      bodyText: "veja https://evil.example/x",
      bodyFormat: "v2",
      isRemoved: false,
      status: "pending_link_scan",
      deletedAt: null,
      createdAt,
      updatedAt: createdAt,
      isEdited: false,
      editCount: 0,
      reactions: [],
      isFavorited: false,
      isForwarded: false,
    });
    const result = await readyHook();

    await act(async () => {
      await result.current.sendMessage("veja https://evil.example/x");
    });
    expect(result.current.state.messages.some((m) => m.id === "msg-blocked")).toBe(true);

    await act(async () => {
      fireWsBlockedEvent("msg-blocked");
    });

    // The message was never published, so nothing of it remains in the
    // transcript — and the author is told why.
    expect(result.current.state.messages.some((m) => m.id === "msg-blocked")).toBe(false);
    expect(result.current.state.sendError).toBe("Este link foi bloqueado por segurança.");
    expect(result.current.state.sending).toBe(false);
  });

  // A refusal for something this view never held, or already resolved, changes
  // nothing: outbox delivery is at-least-once, so a repeat must be harmless.
  it("ignores a blocked event for a message it is not holding as pending", async () => {
    const result = await readyHook();
    const before = result.current.state.messages.length;

    await act(async () => {
      fireWsBlockedEvent("msg-unknown");
      fireWsBlockedEvent("msg-unknown");
    });

    expect(result.current.state.messages).toHaveLength(before);
    expect(result.current.state.sendError).toBeFalsy();
  });

  it("tells the user to retry when the check could not run", async () => {
    mockPostChannelMessage.mockRejectedValue(
      new ApiRequestError(
        503,
        "link_check_unavailable",
        "the link could not be checked for safety, try again",
      ),
    );
    const result = await readyHook();

    await act(async () => {
      await expect(result.current.sendMessage("https://example.com")).rejects.toBeTruthy();
    });

    expect(result.current.state.sendError).toContain("Tente novamente");
    expect(result.current.state.sendError).not.toContain("bloqueado");
    expect(result.current.state.messages).toEqual([]);
  });

  it("rethrows so the composer keeps the draft", async () => {
    mockPostChannelMessage.mockRejectedValue(new ApiRequestError(403, "malicious_url", "blocked"));
    const result = await readyHook();

    // The composer clears its editor only on a resolved "sent"; a rejection is
    // what preserves what the person typed.
    await act(async () => {
      await expect(result.current.sendMessage("https://evil.example")).rejects.toBeInstanceOf(
        ApiRequestError,
      );
    });
  });

  it("leaves every other send failure exactly as it was", async () => {
    mockPostChannelMessage.mockRejectedValue(
      new ApiRequestError(500, "internal_error", "internal error"),
    );
    const result = await readyHook();

    await act(async () => {
      await expect(result.current.sendMessage("olá")).rejects.toBeTruthy();
    });

    expect(result.current.state.sendError).toBe("internal error");
  });
});

// ── RF-21: recovering a verdict that realtime never delivered ────────────────
//
// The gap this closes, stated plainly: a withheld message resolves
// asynchronously and the verdict is announced over the websocket. If the author
// is disconnected at that moment the announcement reaches nobody, and for a
// refusal nothing else is ever coming — the message no longer exists to be
// fetched, no further event will be emitted, and the composer sits on
// "checking segurança dos links…" for good.
//
// So on every subscription that comes back ready, the client asks the server
// what became of the messages it still holds as pending. Absence of an event is
// never read as a verdict; only an answer is acted on.

describe("useMessages — reconnect reconciliation of withheld messages", () => {
  const pendingCreatedAt = "2026-01-01T10:00:00.000Z";

  beforeEach(() => {
    mockFetchChannelMessages.mockResolvedValue(emptyPage);
    mockFetchLinkSafetyStatuses.mockReset();
  });

  /** Renders the hook with one message already withheld, as after a send. */
  async function hookWithPendingMessage(id = "msg-pending") {
    mockPostChannelMessage.mockResolvedValue(
      makeMessage({
        id,
        senderId: "user-me",
        bodyText: "veja https://novo.example/x",
        status: "pending_link_scan",
        createdAt: pendingCreatedAt,
        updatedAt: pendingCreatedAt,
      }),
    );
    const { result } = renderHook(() =>
      useMessages({ kind: "channel", targetId: "ch-1", currentUserId: "user-me" }),
    );
    await waitFor(() => expect(result.current.state.status).toBe("ready"));
    await act(async () => {
      await result.current.sendMessage("veja https://novo.example/x");
    });
    expect(result.current.state.messages.some((m) => m.id === id)).toBe(true);
    return result;
  }

  /** Fires the acknowledgement a resubscribe produces after a reconnect. */
  async function reconnect() {
    await act(async () => {
      capturedOnSubscribed?.({
        type: "subscribed",
        operation: "subscribe",
        target_type: "channel",
        target_id: "ch-1",
      });
      await Promise.resolve();
    });
  }

  it.each([
    ["malicious_link", "Este link foi bloqueado por segurança."],
    [undefined, "Este link foi bloqueado por segurança."],
    ["future_block_reason", "Este link foi bloqueado por segurança."],
    ["link_check_inconclusive", "Não foi possível verificar a segurança deste link."],
  ])("uses the blocked wording for realtime reason %s", async (reason, wording) => {
    const result = await hookWithPendingMessage();

    act(() => fireWsBlockedEvent("msg-pending", reason));

    await waitFor(() =>
      expect(result.current.state.messages.some((m) => m.id === "msg-pending")).toBe(false),
    );
    expect(result.current.state.sendError).toBe(wording);
  });

  // [B] The finding itself: the refusal happened while the socket was down.
  it("clears a pending message the server refused while the socket was down", async () => {
    const result = await hookWithPendingMessage();
    mockFetchLinkSafetyStatuses.mockResolvedValue([
      { messageId: "msg-pending", state: "blocked", reason: "malicious_link" },
    ]);

    // No message.blocked ever arrives — that is the whole scenario.
    await reconnect();

    await waitFor(() =>
      expect(result.current.state.messages.some((m) => m.id === "msg-pending")).toBe(false),
    );
    expect(result.current.state.sendError).toBe("Este link foi bloqueado por segurança.");
    expect(result.current.state.sending).toBe(false);
    // Recovered without the user reloading anything.
    expect(mockFetchChannelMessages).toHaveBeenCalledTimes(1);
  });

  // RF-21 inconclusive: the scan itself finished terminal-but-undecided, so the
  // server never announces a promotion for it — the same "nothing else is ever
  // coming" gap as [B], with the inconclusive reason instead of malicious_link,
  // and a distinct message from the one shown for a malicious link.
  it("clears a pending message the server refused as inconclusive while the socket was down", async () => {
    const result = await hookWithPendingMessage();
    mockFetchLinkSafetyStatuses.mockResolvedValue([
      { messageId: "msg-pending", state: "blocked", reason: "link_check_inconclusive" },
    ]);

    // No message.blocked ever arrives — the checking state must not poll forever.
    await reconnect();

    await waitFor(() =>
      expect(result.current.state.messages.some((m) => m.id === "msg-pending")).toBe(false),
    );
    expect(result.current.state.sendError).toBe(
      "Não foi possível verificar a segurança deste link.",
    );
    expect(result.current.state.sendError).not.toBe("Este link foi bloqueado por segurança.");
    expect(result.current.state.sending).toBe(false);
  });

  it.each([undefined, "future_block_reason"])(
    "keeps the historical malicious wording for reconnect blocked reason %s",
    async (reason) => {
      const result = await hookWithPendingMessage();
      mockFetchLinkSafetyStatuses.mockResolvedValue([
        { messageId: "msg-pending", state: "blocked", ...(reason === undefined ? {} : { reason }) },
      ]);

      await reconnect();

      await waitFor(() =>
        expect(result.current.state.messages.some((m) => m.id === "msg-pending")).toBe(false),
      );
      expect(result.current.state.sendError).toBe("Este link foi bloqueado por segurança.");
    },
  );

  // [C] The same loss in the other direction: the promotion was missed.
  it("promotes a pending message the server published while the socket was down", async () => {
    const result = await hookWithPendingMessage();
    mockFetchLinkSafetyStatuses.mockResolvedValue([{ messageId: "msg-pending", state: "active" }]);
    mockFetchChannelMessage.mockResolvedValue(
      makeMessage({
        id: "msg-pending",
        senderId: "user-me",
        bodyText: "veja https://novo.example/x",
        status: "active",
        createdAt: pendingCreatedAt,
        updatedAt: pendingCreatedAt,
      }),
    );

    await reconnect();

    await waitFor(() => {
      const matching = result.current.state.messages.filter((m) => m.id === "msg-pending");
      expect(matching).toHaveLength(1);
      expect(matching[0].status).toBe("active");
    });
    // The authoritative row, not a locally patched status.
    expect(mockFetchChannelMessage).toHaveBeenCalledWith("ch-1", "msg-pending", expect.anything());
  });

  // [D] Nothing has been decided yet, so nothing may change.
  it("leaves a message pending when the server says it is still being scanned", async () => {
    const result = await hookWithPendingMessage();
    mockFetchLinkSafetyStatuses.mockResolvedValue([{ messageId: "msg-pending", state: "pending" }]);

    await reconnect();
    await act(async () => {
      await Promise.resolve();
    });

    const message = result.current.state.messages.find((m) => m.id === "msg-pending");
    expect(message?.status).toBe("pending_link_scan");
    expect(result.current.state.sendError).toBeFalsy();
  });

  // [E] A failed reconciliation says nothing about the message. Removing the
  // bubble or reporting a block would be inventing an answer nobody gave.
  it("keeps the message pending when reconciliation fails", async () => {
    const result = await hookWithPendingMessage();
    mockFetchLinkSafetyStatuses.mockRejectedValue(new Error("network"));

    await reconnect();
    await act(async () => {
      await Promise.resolve();
    });

    const message = result.current.state.messages.find((m) => m.id === "msg-pending");
    expect(message?.status).toBe("pending_link_scan");
    expect(result.current.state.sendError).toBeFalsy();
  });

  // An id the server will not talk about is absent from the reply, and absence
  // is not a verdict — the same conservative outcome as a failure.
  it("keeps the message pending when the server omits it from the answer", async () => {
    const result = await hookWithPendingMessage();
    mockFetchLinkSafetyStatuses.mockResolvedValue([]);

    await reconnect();
    await act(async () => {
      await Promise.resolve();
    });

    expect(result.current.state.messages.find((m) => m.id === "msg-pending")?.status).toBe(
      "pending_link_scan",
    );
    expect(result.current.state.sendError).toBeFalsy();
  });

  // §20: gone is not the same claim as blocked.
  it("removes a message that is simply gone without blaming a malicious link", async () => {
    const result = await hookWithPendingMessage();
    mockFetchLinkSafetyStatuses.mockResolvedValue([{ messageId: "msg-pending", state: "deleted" }]);

    await reconnect();

    await waitFor(() =>
      expect(result.current.state.messages.some((m) => m.id === "msg-pending")).toBe(false),
    );
    expect(result.current.state.sendError).toBe("Esta mensagem não está mais disponível.");
    expect(result.current.state.sendError).not.toMatch(/bloqueado por segurança/);
  });

  // [F] Realtime and reconciliation are two channels carrying the same fact.
  // Whichever order they land in, the result is one transition.
  it("is idempotent when the blocked event and reconciliation both arrive", async () => {
    for (const realtimeFirst of [true, false]) {
      mockFetchLinkSafetyStatuses.mockResolvedValue([
        { messageId: "msg-pending", state: "blocked", reason: "malicious_link" },
      ]);
      const result = await hookWithPendingMessage();

      if (realtimeFirst) {
        await act(async () => {
          capturedOnMessageBlocked?.({ type: "message.blocked", message_id: "msg-pending" });
        });
        await reconnect();
      } else {
        await reconnect();
        await waitFor(() =>
          expect(result.current.state.messages.some((m) => m.id === "msg-pending")).toBe(false),
        );
        await act(async () => {
          capturedOnMessageBlocked?.({ type: "message.blocked", message_id: "msg-pending" });
        });
      }

      await waitFor(() =>
        expect(result.current.state.messages.filter((m) => m.id === "msg-pending")).toHaveLength(0),
      );
      expect(result.current.state.sendError).toBe("Este link foi bloqueado por segurança.");
    }
  });

  it("converges after duplicate inconclusive realtime and reconnect events", async () => {
    const result = await hookWithPendingMessage();
    mockFetchLinkSafetyStatuses.mockResolvedValue([
      { messageId: "msg-pending", state: "blocked", reason: "link_check_inconclusive" },
    ]);

    act(() => {
      fireWsBlockedEvent("msg-pending", "link_check_inconclusive");
      fireWsBlockedEvent("msg-pending", "link_check_inconclusive");
    });
    await reconnect();

    await waitFor(() =>
      expect(result.current.state.messages.filter((m) => m.id === "msg-pending")).toHaveLength(0),
    );
    expect(result.current.state.sendError).toBe(
      "Não foi possível verificar a segurança deste link.",
    );
    expect(result.current.state.sending).toBe(false);
  });

  it("is idempotent when the published event and reconciliation both arrive", async () => {
    const published = makeMessage({
      id: "msg-pending",
      senderId: "user-me",
      bodyText: "veja https://novo.example/x",
      status: "active",
      createdAt: pendingCreatedAt,
      updatedAt: pendingCreatedAt,
    });
    mockFetchLinkSafetyStatuses.mockResolvedValue([{ messageId: "msg-pending", state: "active" }]);
    mockFetchChannelMessage.mockResolvedValue(published);
    const result = await hookWithPendingMessage();

    await act(async () => {
      fireWsEventWithPayload(
        "channel",
        "ch-1",
        makePayload({
          id: "msg-pending",
          sender_id: "user-me",
          body_text: "veja https://novo.example/x",
          status: "active",
          created_at: pendingCreatedAt,
        }),
      );
    });
    await reconnect();

    await waitFor(() => {
      const matching = result.current.state.messages.filter((m) => m.id === "msg-pending");
      expect(matching).toHaveLength(1);
      expect(matching[0].status).toBe("active");
    });
  });

  // The overwhelmingly common case: nothing is pending, so nothing is asked.
  it("makes no request when nothing is being scanned", async () => {
    const { result } = renderHook(() =>
      useMessages({ kind: "channel", targetId: "ch-1", currentUserId: "user-me" }),
    );
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    await reconnect();

    expect(mockFetchLinkSafetyStatuses).not.toHaveBeenCalled();
  });

  // Only the ids this client is actually waiting on, never the transcript.
  it("asks only about the messages it is holding as pending", async () => {
    mockFetchChannelMessages.mockResolvedValue({
      messages: [makeMessage({ id: "msg-old", status: "active" })],
      nextCursor: "",
    });
    mockFetchLinkSafetyStatuses.mockResolvedValue([]);
    await hookWithPendingMessage();

    await reconnect();

    await waitFor(() => expect(mockFetchLinkSafetyStatuses).toHaveBeenCalled());
    expect(mockFetchLinkSafetyStatuses.mock.calls[0][0]).toEqual(["msg-pending"]);
  });
});
