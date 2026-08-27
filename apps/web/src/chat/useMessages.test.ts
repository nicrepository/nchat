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
  WSMessageLinkSafetyChangedEvent,
} from "./useChatWebSocket";
import { useMessages } from "./useMessages";
import type { Message, MessagePage } from "./chatTypes";

// ── Controllable useChatWebSocket stub ────────────────────────────────────────

// Captures the latest onMessageCreated callback so tests can fire WS events.
let capturedOnMessageCreated: ((evt: WSMessageCreatedEvent) => void) | null = null;
let capturedOnMessageBlocked: ((evt: WSMessageBlockedEvent) => void) | null = null;
let capturedOnLinkSafetyChanged: ((evt: WSMessageLinkSafetyChangedEvent) => void) | null = null;
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
    onMessageLinkSafetyChanged,
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
    onMessageLinkSafetyChanged?: (evt: WSMessageLinkSafetyChangedEvent) => void;
    onMessageUpdated?: (evt: WSMessageUpdatedEvent) => void;
    onReactionUpdated?: (evt: WSReactionUpdatedEvent) => void;
    onPinUpdated?: (evt: WSPinUpdatedEvent) => void;
    onReactionError?: (evt: WSClientErrorEvent) => void;
    onSubscriptionError?: (evt: WSClientErrorEvent) => void;
    onSubscribed?: (evt: WSSubscribedEvent) => void;
  }) => {
    capturedOnMessageCreated = onMessageCreated;
    capturedOnMessageBlocked = onMessageBlocked ?? null;
    capturedOnLinkSafetyChanged = onMessageLinkSafetyChanged ?? null;
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
  mockReconcileMessageLinkSafety,
  mockFetchChannelMessages,
  mockFetchChannelMessage,
  mockFetchDMMessages,
  mockFetchDMMessage,
  mockFetchChannelMessageSecuritySnapshots,
  mockFetchDMMessageSecuritySnapshots,
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
  mockReconcileMessageLinkSafety: vi.fn<
    (
      messageId: string,
      signal?: AbortSignal,
    ) => Promise<{
      state: Message["linkSafetyState"];
      updatedAt: string;
      retryAfterSeconds: number;
    }>
  >(),
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
  mockFetchChannelMessageSecuritySnapshots: vi.fn(),
  mockFetchDMMessageSecuritySnapshots: vi.fn(),
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

vi.mock("./chatApi", async (importOriginal) => ({
  // safeAvatarUrl is real: it's the pure, already-tested same-origin policy
  // (chatApi.test.ts), and these WS tests need the genuine behavior rather
  // than a second hand-rolled copy of it.
  safeAvatarUrl: (await importOriginal<typeof import("./chatApi")>()).safeAvatarUrl,
  fetchChannelMessages: (id: string, cursor?: string, signal?: AbortSignal) =>
    mockFetchChannelMessages(id, cursor, signal),
  fetchChannelMessage: (id: string, msgId: string, signal?: AbortSignal) =>
    mockFetchChannelMessage(id, msgId, signal),
  fetchDMMessages: (id: string, cursor?: string, signal?: AbortSignal) =>
    mockFetchDMMessages(id, cursor, signal),
  fetchDMMessage: (id: string, msgId: string, signal?: AbortSignal) =>
    mockFetchDMMessage(id, msgId, signal),
  fetchChannelMessageSecuritySnapshots: (id: string, messageIds: string[], signal?: AbortSignal) =>
    mockFetchChannelMessageSecuritySnapshots(id, messageIds, signal),
  fetchDMMessageSecuritySnapshots: (id: string, messageIds: string[], signal?: AbortSignal) =>
    mockFetchDMMessageSecuritySnapshots(id, messageIds, signal),
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
  reconcileMessageLinkSafety: (messageId: string, signal?: AbortSignal) =>
    mockReconcileMessageLinkSafety(messageId, signal),
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

/**
 * Fire the issue #135 correction: what is known about a *published* message's
 * links changed. Unlike message.blocked it is addressed to the conversation,
 * because the message was delivered and everyone holding it has to converge.
 */
function fireWsLinkSafetyChanged(
  messageId: string,
  state: string,
  updatedAt = "2099-08-18T12:00:00Z",
): void {
  capturedOnLinkSafetyChanged?.({
    type: "message.link_safety_changed",
    target_type: "channel",
    target_id: "chan-1",
    message_id: messageId,
    link_safety: { message_id: messageId, state, updated_at: updatedAt },
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
        linkSafetyState: "",
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
      linkSafetyState: "",
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
          linkSafetyState: "",
        },
      }),
    );
    await act(async () => Promise.resolve());
    expect(result.current.state.messages[0].reference).toEqual({ available: false });
  });

  it("does not let a stale reference response undo a realtime condemnation", async () => {
    const reference: NonNullable<Message["reference"]> = {
      available: true,
      messageId: "protected-source",
      targetType: "channel",
      targetId: "private-source",
      targetLabel: "Privado",
      authorDisplayName: "Ana",
      bodyText: "https://bad.example",
      bodyFormat: "v3",
      createdAt: "2026-08-18T10:00:00Z",
      updatedAt: "2026-08-18T10:00:00Z",
      linkSafetyState: "inconclusive",
    };
    mockFetchChannelMessages.mockResolvedValue({
      messages: [makeMessage({ id: "destination-message", reference })],
      nextCursor: "",
    });
    let resolveReference!: (value: Record<string, NonNullable<Message["reference"]>>) => void;
    mockResolveChannelMessageReferences.mockReturnValueOnce(
      new Promise((resolve) => (resolveReference = resolve)),
    );
    const { result } = renderHook(() =>
      useMessages({ kind: "channel", targetId: "destination", currentUserId: "user-me" }),
    );
    await waitFor(() => expect(result.current.state.status).toBe("ready"));
    act(() => window.dispatchEvent(new Event("focus")));
    await waitFor(() => expect(mockResolveChannelMessageReferences).toHaveBeenCalledOnce());

    act(() => fireWsLinkSafetyChanged("protected-source", "malicious", "2026-08-18T12:00:00Z"));
    act(() =>
      resolveReference({
        "destination-message": {
          ...reference,
          updatedAt: "2026-08-18T11:00:00Z",
        },
      }),
    );

    await waitFor(() =>
      expect(result.current.state.messages[0].reference).toMatchObject({
        linkSafetyState: "malicious",
        bodyText: "",
        updatedAt: "2026-08-18T12:00:00Z",
      }),
    );
  });

  it("refreshes only the reference and preserves the destination snapshot", async () => {
    const destination = makeMessage({
      id: "destination-message",
      bodyText: "edited body",
      editedAt: "2026-07-21T13:00:00Z",
      reactions: [{ emoji: "👍", count: 4, reactedByMe: true, users: [] }],
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
      reactions: [{ emoji: "👍", count: 4, reactedByMe: true, users: [] }],
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
      { emoji: "👍", count: 2, reactedByMe: true, users: [] },
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
    const originalReactions = [{ emoji: "👍", count: 1, reactedByMe: false, users: [] }];
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
      { emoji: "👍", count: 2, reactedByMe: true, users: [] },
    ]);

    act(() => capturedOnReactionError?.({ type: "error", code: "temporarily_unavailable" }));

    expect(result.current.state.messages[0].reactions).toEqual(originalReactions);
    expect(result.current.state.actionError).toMatch(/temporariamente indisponíveis/i);
  });

  it("reverts an optimistic reaction when confirmation times out", async () => {
    const originalReactions = [{ emoji: "👍", count: 1, reactedByMe: false, users: [] }];
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
      makeMessage({
        id: "msg-remote",
        reactions: [{ emoji: "🔥", count: 4, reactedByMe: true, users: [] }],
      }),
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
        reactions: [{ emoji: "🔥", count: 2, reactedByMe: false, users: [] }],
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
      { emoji: "👍", count: 1, reactedByMe: false, users: [] },
    ]);
    expect(onOwnReactionConfirmed).not.toHaveBeenCalled();
  });

  // A re-delivered event must not inflate the count or list a reactor twice, and
  // the event that follows it must still be able to take a reactor away.
  it("converges on duplicate and superseded reaction events", async () => {
    mockFetchChannelMessages.mockResolvedValue({
      messages: [
        makeMessage({
          id: "msg-dup",
          reactions: [
            {
              emoji: "🎉",
              count: 1,
              reactedByMe: false,
              users: [{ userId: "user-two", displayName: "Bruna" }],
            },
          ],
        }),
      ],
      nextCursor: "",
    });
    const { result } = renderHook(() =>
      useMessages({ kind: "channel", targetId: "ch-1", currentUserId: "user-me" }),
    );
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    const added = {
      type: "reaction.updated" as const,
      target_type: "channel" as const,
      target_id: "ch-1",
      message_id: "msg-dup",
      reaction: {
        message_id: "msg-dup",
        actor_user_id: "user-three",
        emoji: "🎉",
        added: true,
        reactions: [
          {
            emoji: "🎉",
            count: 2,
            users: [
              { user_id: "user-two", display_name: "Bruna" },
              { user_id: "user-three", display_name: "Caio" },
            ],
          },
        ],
      },
    };

    act(() => capturedOnReactionUpdated?.(added));
    act(() => capturedOnReactionUpdated?.(added));

    expect(result.current.state.messages[0].reactions).toEqual([
      {
        emoji: "🎉",
        count: 2,
        reactedByMe: false,
        users: [
          { userId: "user-two", displayName: "Bruna" },
          { userId: "user-three", displayName: "Caio" },
        ],
      },
    ]);

    act(() =>
      capturedOnReactionUpdated?.({
        ...added,
        reaction: {
          ...added.reaction,
          added: false,
          reactions: [
            { emoji: "🎉", count: 1, users: [{ user_id: "user-two", display_name: "Bruna" }] },
          ],
        },
      }),
    );

    expect(result.current.state.messages[0].reactions).toEqual([
      {
        emoji: "🎉",
        count: 1,
        reactedByMe: false,
        users: [{ userId: "user-two", displayName: "Bruna" }],
      },
    ]);
  });

  // The reader's own state is derived, never carried by the event: a duplicate
  // confirmation of their own toggle must not flip it back.
  it("keeps the reader's own state stable across a duplicated own-toggle event", async () => {
    mockFetchChannelMessages.mockResolvedValue({
      messages: [makeMessage({ id: "msg-own", reactions: [] })],
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

    const own = {
      type: "reaction.updated" as const,
      target_type: "channel" as const,
      target_id: "ch-1",
      message_id: "msg-own",
      reaction: {
        message_id: "msg-own",
        actor_user_id: "user-me",
        emoji: "👍",
        added: true,
        reactions: [{ emoji: "👍", count: 1, users: [{ user_id: "user-me", display_name: "Eu" }] }],
      },
    };

    act(() => capturedOnReactionUpdated?.(own));
    act(() => capturedOnReactionUpdated?.(own));

    expect(result.current.state.messages[0].reactions).toEqual([
      {
        emoji: "👍",
        count: 1,
        reactedByMe: true,
        users: [{ userId: "user-me", displayName: "Eu" }],
      },
    ]);
    // Nor may a duplicate reach the emoji history — and this reader never
    // toggled here, so the reaction came from another session and counts for
    // nothing on this client either way.
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
      updatedAt: "2024-01-15T09:00:00Z",
      linkSafetyState: "",
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

  // Issue #495: message.created must carry the sender's avatar so the
  // timeline never has to fetch a profile per message to render one.
  it("renders realtime payload's sender avatar", async () => {
    const payload = makePayload({
      id: "msg-avatar",
      sender_avatar_url: "/avatars/user-sender.png",
    });
    mockFetchChannelMessages.mockResolvedValue(emptyPage);

    const { result } = renderHook(() =>
      useMessages({ kind: "channel", targetId: "ch-avatar", currentUserId: "user-me" }),
    );

    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() => {
      fireWsEventWithPayload("channel", "ch-avatar", payload);
    });

    await waitFor(() => expect(result.current.state.messages).toHaveLength(1));
    expect(result.current.state.messages[0].senderAvatarUrl).toBe("/avatars/user-sender.png");
  });

  // The same same-origin policy every other avatar in the app already
  // applies (chatApi's safeAvatarUrl) must reject a cross-origin URL here too
  // — a message payload is not a second, weaker place to trust one.
  it("drops a realtime sender avatar that is not a safe same-origin URL", async () => {
    const payload = makePayload({
      id: "msg-unsafe-avatar",
      sender_avatar_url: "https://evil.test/tracker.png",
    });
    mockFetchChannelMessages.mockResolvedValue(emptyPage);

    const { result } = renderHook(() =>
      useMessages({ kind: "channel", targetId: "ch-unsafe-avatar", currentUserId: "user-me" }),
    );

    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() => {
      fireWsEventWithPayload("channel", "ch-unsafe-avatar", payload);
    });

    await waitFor(() => expect(result.current.state.messages).toHaveLength(1));
    expect(result.current.state.messages[0].senderAvatarUrl).toBeUndefined();
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

/**
 * Reaction reconciliation (issue #496, CQ round 4).
 *
 * The rule these all check is one sentence: what the reader sees is the state
 * the server has confirmed, with every local intent still awaiting confirmation
 * applied on top of it. An intent is one `(message, emoji)` pair — never a whole
 * message — so confirming, failing or timing out one of them leaves the others
 * exactly where they were.
 */
describe("useMessages — concurrent reaction intents", () => {
  const messageId = "msg-r";

  function ownEvent(
    emoji: string,
    added: boolean,
    reactions: { emoji: string; count: number }[],
    actor = "user-me",
  ): WSReactionUpdatedEvent {
    return {
      type: "reaction.updated",
      target_type: "channel",
      target_id: "ch-1",
      message_id: messageId,
      reaction: { message_id: messageId, actor_user_id: actor, emoji, added, reactions },
    };
  }

  function setup(reactions: Message["reactions"] = [], onOwnReactionConfirmed = vi.fn()) {
    mockFetchChannelMessages.mockResolvedValue({
      messages: [makeMessage({ id: messageId, reactions })],
      nextCursor: "",
    });
    const hook = renderHook(() =>
      useMessages({
        kind: "channel",
        targetId: "ch-1",
        currentUserId: "user-me",
        onOwnReactionConfirmed,
      }),
    );
    return { ...hook, onOwnReactionConfirmed };
  }

  function emojis(result: { current: { state: { messages: Message[] } } }): string[] {
    return result.current.state.messages[0].reactions.map((item) => item.emoji);
  }

  const mine = (emoji: string, count = 1) => ({ emoji, count, reactedByMe: true, users: [] });

  it("keeps a second pending reaction alive when the first is confirmed", async () => {
    const { result, onOwnReactionConfirmed } = setup();
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() => result.current.toggleReaction(messageId, "🎉"));
    act(() => result.current.toggleReaction(messageId, "🚀"));
    expect(emojis(result)).toEqual(["🎉", "🚀"]);

    act(() => capturedOnReactionUpdated?.(ownEvent("🎉", true, [{ emoji: "🎉", count: 1 }])));
    // 🎉 is confirmed; 🚀 is still an intent, so it is still drawn.
    expect(emojis(result)).toEqual(["🎉", "🚀"]);

    act(() =>
      capturedOnReactionUpdated?.(
        ownEvent("🚀", true, [
          { emoji: "🎉", count: 1 },
          { emoji: "🚀", count: 1 },
        ]),
      ),
    );
    expect(result.current.state.messages[0].reactions).toEqual([mine("🎉"), mine("🚀")]);
    expect(onOwnReactionConfirmed.mock.calls).toEqual([["🎉"], ["🚀"]]);
  });

  it("converges when the confirmations arrive in the opposite order", async () => {
    const { result } = setup();
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() => result.current.toggleReaction(messageId, "🎉"));
    act(() => result.current.toggleReaction(messageId, "🚀"));

    act(() => capturedOnReactionUpdated?.(ownEvent("🚀", true, [{ emoji: "🚀", count: 1 }])));
    expect(emojis(result)).toEqual(["🚀", "🎉"]);

    act(() =>
      capturedOnReactionUpdated?.(
        ownEvent("🎉", true, [
          { emoji: "🚀", count: 1 },
          { emoji: "🎉", count: 1 },
        ]),
      ),
    );
    expect(result.current.state.messages[0].reactions).toEqual([mine("🚀"), mine("🎉")]);
  });

  it("reverts only the reaction whose confirmation timed out", async () => {
    const { result, onOwnReactionConfirmed } = setup();
    await waitFor(() => expect(result.current.state.status).toBe("ready"));
    vi.useFakeTimers();
    try {
      act(() => result.current.toggleReaction(messageId, "🎉"));
      act(() => vi.advanceTimersByTime(3_000));
      act(() => result.current.toggleReaction(messageId, "🚀"));
      // Only 🎉's own 8s window elapses; 🚀 was started 3s later.
      act(() => vi.advanceTimersByTime(5_000));

      expect(emojis(result)).toEqual(["🚀"]);
      expect(result.current.state.actionError).toMatch(/confirmar a reação/i);
    } finally {
      vi.useRealTimers();
    }

    act(() => capturedOnReactionUpdated?.(ownEvent("🚀", true, [{ emoji: "🚀", count: 1 }])));
    expect(result.current.state.messages[0].reactions).toEqual([mine("🚀")]);
    expect(onOwnReactionConfirmed.mock.calls).toEqual([["🚀"]]);
  });

  it("keeps the surviving intent when the socket refuses the other toggle", async () => {
    const { result } = setup();
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() => result.current.toggleReaction(messageId, "🎉"));
    mockToggleReaction.mockReturnValueOnce(false);
    act(() => result.current.toggleReaction(messageId, "🚀"));

    // 🚀 never left the client; 🎉 is untouched and still pending.
    expect(emojis(result)).toEqual(["🎉"]);
    expect(result.current.state.actionError).toMatch(/tempo real/i);

    act(() => capturedOnReactionUpdated?.(ownEvent("🎉", true, [{ emoji: "🎉", count: 1 }])));
    expect(result.current.state.messages[0].reactions).toEqual([mine("🎉")]);
  });

  it("reconciles a removal and an addition of different emoji", async () => {
    const { result, onOwnReactionConfirmed } = setup([
      { emoji: "🎉", count: 1, reactedByMe: true, users: [] },
    ]);
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() => result.current.toggleReaction(messageId, "🎉"));
    act(() => result.current.toggleReaction(messageId, "🚀"));
    expect(emojis(result)).toEqual(["🚀"]);

    act(() => capturedOnReactionUpdated?.(ownEvent("🎉", false, [])));
    // The removal is confirmed; the addition is still only an intent.
    expect(emojis(result)).toEqual(["🚀"]);

    act(() => capturedOnReactionUpdated?.(ownEvent("🚀", true, [{ emoji: "🚀", count: 1 }])));
    expect(result.current.state.messages[0].reactions).toEqual([mine("🚀")]);
    // A removal is not a use; only the addition counts.
    expect(onOwnReactionConfirmed.mock.calls).toEqual([["🚀"]]);
  });

  it("lets the newest intent win when the same emoji is toggled twice", async () => {
    const { result, onOwnReactionConfirmed } = setup();
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() => result.current.toggleReaction(messageId, "🎉"));
    act(() => result.current.toggleReaction(messageId, "🎉"));
    expect(emojis(result)).toEqual([]);

    // The add lands first. It must not resurrect 🎉 against the newer intent.
    act(() => capturedOnReactionUpdated?.(ownEvent("🎉", true, [{ emoji: "🎉", count: 1 }])));
    expect(emojis(result)).toEqual([]);

    act(() => capturedOnReactionUpdated?.(ownEvent("🎉", false, [])));
    expect(emojis(result)).toEqual([]);
    // The add was never the reader's settled choice, so it is not a use.
    expect(onOwnReactionConfirmed).not.toHaveBeenCalled();
  });

  it("re-applies pending intents over a refetched snapshot", async () => {
    const { result } = setup();
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() => result.current.toggleReaction(messageId, "🚀"));
    mockFetchChannelMessage.mockResolvedValue(
      makeMessage({
        id: messageId,
        reactions: [{ emoji: "🎉", count: 1, reactedByMe: false, users: [] }],
      }),
    );

    // A reaction event with no payload is the reconnect path: refetch, then
    // reconcile against what is still in flight.
    act(() =>
      capturedOnReactionUpdated?.({
        type: "reaction.updated",
        target_type: "channel",
        target_id: "ch-1",
        message_id: messageId,
      }),
    );

    await waitFor(() => expect(emojis(result)).toEqual(["🎉", "🚀"]));
  });

  it("re-applies a pending removal over a refetched snapshot", async () => {
    const { result } = setup([
      { emoji: "🎉", count: 1, reactedByMe: true, users: [] },
      { emoji: "🚀", count: 1, reactedByMe: false, users: [] },
    ]);
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() => result.current.toggleReaction(messageId, "🎉"));
    mockFetchChannelMessage.mockResolvedValue(
      makeMessage({
        id: messageId,
        reactions: [
          { emoji: "🎉", count: 1, reactedByMe: true, users: [] },
          { emoji: "🚀", count: 1, reactedByMe: false, users: [] },
        ],
      }),
    );

    act(() =>
      capturedOnReactionUpdated?.({
        type: "reaction.updated",
        target_type: "channel",
        target_id: "ch-1",
        message_id: messageId,
      }),
    );

    await waitFor(() => expect(emojis(result)).toEqual(["🚀"]));
  });
});

/**
 * Seeing an event is not the same as having your own toggle confirmed
 * (issue #496, CQ round 5).
 *
 * Every valid event updates what this client believes the server holds. Only an
 * event that says back what *this reader* asked for settles their intent — and
 * only then may the rollback timer stop and the emoji history learn anything.
 * An event from somebody else, or a stale one of the reader's own that the
 * newest local intent has already superseded, does neither.
 */
describe("useMessages — events that do not confirm the local intent", () => {
  const messageId = "msg-c";

  function event(
    emoji: string,
    added: boolean,
    reactions: {
      emoji: string;
      count: number;
      users?: { user_id: string; display_name: string }[];
    }[],
    actor: string,
  ): WSReactionUpdatedEvent {
    return {
      type: "reaction.updated",
      target_type: "channel",
      target_id: "ch-1",
      message_id: messageId,
      reaction: { message_id: messageId, actor_user_id: actor, emoji, added, reactions },
    };
  }

  function setup(reactions: Message["reactions"] = []) {
    mockFetchChannelMessages.mockResolvedValue({
      messages: [makeMessage({ id: messageId, reactions })],
      nextCursor: "",
    });
    const onOwnReactionConfirmed = vi.fn();
    const hook = renderHook(() =>
      useMessages({
        kind: "channel",
        targetId: "ch-1",
        currentUserId: "user-me",
        onOwnReactionConfirmed,
      }),
    );
    return { ...hook, onOwnReactionConfirmed };
  }

  const drawn = (result: { current: { state: { messages: Message[] } } }) =>
    result.current.state.messages[0].reactions;

  const other = [{ user_id: "user-two", display_name: "Bruna" }];

  it("does not let another reader's addition confirm this reader's own", async () => {
    const { result, onOwnReactionConfirmed } = setup();
    await waitFor(() => expect(result.current.state.status).toBe("ready"));
    vi.useFakeTimers();
    try {
      act(() => result.current.toggleReaction(messageId, "🚀"));
      expect(drawn(result)).toEqual([{ emoji: "🚀", count: 1, reactedByMe: true, users: [] }]);

      // Somebody else reacts with the same emoji while this reader waits.
      act(() =>
        capturedOnReactionUpdated?.(
          event("🚀", true, [{ emoji: "🚀", count: 1, users: other }], "user-two"),
        ),
      );

      // Server has one reactor; this reader's own toggle is still on top of it.
      expect(drawn(result)).toEqual([
        {
          emoji: "🚀",
          count: 2,
          reactedByMe: true,
          users: [{ userId: "user-two", displayName: "Bruna" }],
        },
      ]);
      expect(onOwnReactionConfirmed).not.toHaveBeenCalled();

      // The timer was never this event's to cancel, so the rollback still runs.
      act(() => vi.advanceTimersByTime(8_000));
    } finally {
      vi.useRealTimers();
    }

    // Only this reader's contribution goes; the other reactor stays.
    expect(drawn(result)).toEqual([
      {
        emoji: "🚀",
        count: 1,
        reactedByMe: false,
        users: [{ userId: "user-two", displayName: "Bruna" }],
      },
    ]);
    expect(result.current.state.actionError).toMatch(/confirmar a reação/i);
  });

  it("does not cancel a pending remove when a delayed own add arrives", async () => {
    const { result, onOwnReactionConfirmed } = setup();
    await waitFor(() => expect(result.current.state.status).toBe("ready"));
    vi.useFakeTimers();
    try {
      act(() => result.current.toggleReaction(messageId, "🎉"));
      act(() => result.current.toggleReaction(messageId, "🎉"));
      expect(drawn(result)).toEqual([]);

      // The first toggle's confirmation lands late. The reader has since asked
      // for the opposite, so this settles the server's view and nothing else.
      act(() =>
        capturedOnReactionUpdated?.(
          event(
            "🎉",
            true,
            [{ emoji: "🎉", count: 1, users: [{ user_id: "user-me", display_name: "Eu" }] }],
            "user-me",
          ),
        ),
      );

      expect(drawn(result)).toEqual([]);
      expect(onOwnReactionConfirmed).not.toHaveBeenCalled();

      // The removal was never confirmed, so its own window still expires.
      act(() => vi.advanceTimersByTime(8_000));
    } finally {
      vi.useRealTimers();
    }

    // Rolling back the removal leaves what the server actually holds.
    expect(drawn(result)).toEqual([
      {
        emoji: "🎉",
        count: 1,
        reactedByMe: true,
        users: [{ userId: "user-me", displayName: "Eu" }],
      },
    ]);
    expect(result.current.state.actionError).toMatch(/confirmar a reação/i);
  });

  it("does not cancel a pending add when a delayed own remove arrives", async () => {
    const { result, onOwnReactionConfirmed } = setup([
      {
        emoji: "🎉",
        count: 1,
        reactedByMe: true,
        users: [{ userId: "user-me", displayName: "Eu" }],
      },
    ]);
    await waitFor(() => expect(result.current.state.status).toBe("ready"));
    vi.useFakeTimers();
    try {
      act(() => result.current.toggleReaction(messageId, "🎉"));
      act(() => result.current.toggleReaction(messageId, "🎉"));
      expect(drawn(result)).toEqual([
        {
          emoji: "🎉",
          count: 1,
          reactedByMe: true,
          users: [{ userId: "user-me", displayName: "Eu" }],
        },
      ]);

      act(() => capturedOnReactionUpdated?.(event("🎉", false, [], "user-me")));

      // Server says gone; the newest intent says added, and it is still drawn.
      expect(drawn(result)).toEqual([{ emoji: "🎉", count: 1, reactedByMe: true, users: [] }]);
      expect(onOwnReactionConfirmed).not.toHaveBeenCalled();

      act(() => vi.advanceTimersByTime(8_000));
    } finally {
      vi.useRealTimers();
    }

    expect(drawn(result)).toEqual([]);
  });

  it("does not let another reader's removal confirm this reader's own", async () => {
    const { result, onOwnReactionConfirmed } = setup([
      {
        emoji: "🚀",
        count: 2,
        reactedByMe: true,
        users: [
          { userId: "user-me", displayName: "Eu" },
          { userId: "user-two", displayName: "Bruna" },
        ],
      },
    ]);
    await waitFor(() => expect(result.current.state.status).toBe("ready"));
    vi.useFakeTimers();
    try {
      act(() => result.current.toggleReaction(messageId, "🚀"));
      expect(drawn(result)).toEqual([
        {
          emoji: "🚀",
          count: 1,
          reactedByMe: false,
          users: [
            { userId: "user-me", displayName: "Eu" },
            { userId: "user-two", displayName: "Bruna" },
          ],
        },
      ]);

      act(() =>
        capturedOnReactionUpdated?.(
          event(
            "🚀",
            false,
            [{ emoji: "🚀", count: 1, users: [{ user_id: "user-me", display_name: "Eu" }] }],
            "user-two",
          ),
        ),
      );
      expect(onOwnReactionConfirmed).not.toHaveBeenCalled();

      act(() => vi.advanceTimersByTime(8_000));
    } finally {
      vi.useRealTimers();
    }

    // The reader's removal was never confirmed, so it rolls back to the server's
    // list — which no longer has the other reactor.
    expect(drawn(result)).toEqual([
      {
        emoji: "🚀",
        count: 1,
        reactedByMe: true,
        users: [{ userId: "user-me", displayName: "Eu" }],
      },
    ]);
  });

  // The other side of the rule: an event that *does* say back what was asked
  // ends the wait, and the rollback must not fire afterwards.
  it("stops the rollback once the matching confirmation arrives", async () => {
    const { result, onOwnReactionConfirmed } = setup();
    await waitFor(() => expect(result.current.state.status).toBe("ready"));
    vi.useFakeTimers();
    try {
      act(() => result.current.toggleReaction(messageId, "🚀"));
      act(() =>
        capturedOnReactionUpdated?.(
          event(
            "🚀",
            true,
            [{ emoji: "🚀", count: 1, users: [{ user_id: "user-me", display_name: "Eu" }] }],
            "user-me",
          ),
        ),
      );
      act(() => vi.advanceTimersByTime(8_000));
    } finally {
      vi.useRealTimers();
    }

    expect(drawn(result)).toEqual([
      {
        emoji: "🚀",
        count: 1,
        reactedByMe: true,
        users: [{ userId: "user-me", displayName: "Eu" }],
      },
    ]);
    expect(result.current.state.actionError).toBeNull();
    expect(onOwnReactionConfirmed).toHaveBeenCalledTimes(1);
  });

  it("stops the rollback once a matching removal is confirmed, without a use", async () => {
    const { result, onOwnReactionConfirmed } = setup([
      { emoji: "🚀", count: 1, reactedByMe: true, users: [] },
    ]);
    await waitFor(() => expect(result.current.state.status).toBe("ready"));
    vi.useFakeTimers();
    try {
      act(() => result.current.toggleReaction(messageId, "🚀"));
      act(() => capturedOnReactionUpdated?.(event("🚀", false, [], "user-me")));
      act(() => vi.advanceTimersByTime(8_000));
    } finally {
      vi.useRealTimers();
    }

    expect(drawn(result)).toEqual([]);
    expect(result.current.state.actionError).toBeNull();
    expect(onOwnReactionConfirmed).not.toHaveBeenCalled();
  });
});

/**
 * What counts as a use for "Mais usados" (issue #496, CQ round 4).
 *
 * Only a local addition the server confirmed. Not a removal, not a redelivery of
 * an event already acted on, not a failed attempt, and not something this reader
 * did in another tab — the picker's history is this client's own record of what
 * the reader reached for here.
 */
describe("useMessages — own reaction usage", () => {
  const messageId = "msg-u";

  function ownEvent(emoji: string, added: boolean, reactions: { emoji: string; count: number }[]) {
    return {
      type: "reaction.updated" as const,
      target_type: "channel" as const,
      target_id: "ch-1",
      message_id: messageId,
      reaction: {
        message_id: messageId,
        actor_user_id: "user-me",
        emoji,
        added,
        reactions,
      },
    };
  }

  function setup(reactions: Message["reactions"] = []) {
    mockFetchChannelMessages.mockResolvedValue({
      messages: [makeMessage({ id: messageId, reactions })],
      nextCursor: "",
    });
    const onOwnReactionConfirmed = vi.fn();
    const hook = renderHook(() =>
      useMessages({
        kind: "channel",
        targetId: "ch-1",
        currentUserId: "user-me",
        onOwnReactionConfirmed,
      }),
    );
    return { ...hook, onOwnReactionConfirmed };
  }

  it("records a confirmed addition exactly once, however often it is redelivered", async () => {
    const { result, onOwnReactionConfirmed } = setup();
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() => result.current.toggleReaction(messageId, "🚀"));
    const added = ownEvent("🚀", true, [{ emoji: "🚀", count: 1 }]);
    act(() => capturedOnReactionUpdated?.(added));
    act(() => capturedOnReactionUpdated?.(added));
    act(() => capturedOnReactionUpdated?.(added));

    expect(onOwnReactionConfirmed).toHaveBeenCalledTimes(1);
    expect(onOwnReactionConfirmed).toHaveBeenCalledWith("🚀");
    expect(result.current.state.messages[0].reactions).toEqual([
      { emoji: "🚀", count: 1, reactedByMe: true, users: [] },
    ]);
  });

  it("never records a removal, redelivered or not", async () => {
    const { result, onOwnReactionConfirmed } = setup([
      { emoji: "🚀", count: 1, reactedByMe: true, users: [] },
    ]);
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() => result.current.toggleReaction(messageId, "🚀"));
    const removed = ownEvent("🚀", false, []);
    act(() => capturedOnReactionUpdated?.(removed));
    act(() => capturedOnReactionUpdated?.(removed));

    expect(onOwnReactionConfirmed).not.toHaveBeenCalled();
    expect(result.current.state.messages[0].reactions).toEqual([]);
  });

  it("does not record an addition that never got confirmed", async () => {
    const { result, onOwnReactionConfirmed } = setup();
    await waitFor(() => expect(result.current.state.status).toBe("ready"));
    vi.useFakeTimers();
    try {
      act(() => result.current.toggleReaction(messageId, "🚀"));
      act(() => vi.advanceTimersByTime(8_000));
    } finally {
      vi.useRealTimers();
    }

    expect(result.current.state.messages[0].reactions).toEqual([]);
    expect(onOwnReactionConfirmed).not.toHaveBeenCalled();
  });

  // The same person, another tab. The reaction is theirs and the badge must show
  // it, but this client's own history is a record of what was reached for *here*.
  it("does not record a reaction this client never initiated", async () => {
    const { result, onOwnReactionConfirmed } = setup();
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() => capturedOnReactionUpdated?.(ownEvent("👍", true, [{ emoji: "👍", count: 1 }])));

    expect(result.current.state.messages[0].reactions).toEqual([
      { emoji: "👍", count: 1, reactedByMe: true, users: [] },
    ]);
    expect(onOwnReactionConfirmed).not.toHaveBeenCalled();
  });
});

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
      reactions: [{ emoji: "👍", count: 2, reactedByMe: true, users: [] }],
      isFavorited: true,
      linkSafetyState: "safe",
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
        linkSafetyState: "inconclusive",
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
      linkSafetyState: "inconclusive",
    });
  });

  it.each([
    ["", "safe"],
    ["", "inconclusive"],
    ["safe", "inconclusive"],
    ["inconclusive", "safe"],
    ["safe", ""],
  ] as const)("applies message.updated link safety %s -> %s", async (before, after) => {
    mockFetchChannelMessages.mockResolvedValue({
      messages: [makeMessage({ id: "msg-edit", linkSafetyState: before })],
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
          body: after === "" ? "sem URL" : "https://example.test",
          body_format: "v3",
          edited_at: "2026-08-18T12:00:00Z",
          edit_count: 1,
          is_edited: true,
          link_safety_state: after,
        },
      }),
    );

    expect(result.current.state.messages[0].linkSafetyState).toBe(after);
  });

  it("preserves link safety when a rolling-deploy message.updated omits the field", async () => {
    mockFetchChannelMessages.mockResolvedValue({
      messages: [makeMessage({ id: "msg-edit", linkSafetyState: "malicious", bodyText: "" })],
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
          body: "legacy body",
          body_format: "v3",
          edited_at: "2026-08-18T12:00:00Z",
          edit_count: 1,
          is_edited: true,
        },
      }),
    );

    expect(result.current.state.messages[0]).toMatchObject({
      linkSafetyState: "malicious",
      bodyText: "",
    });
  });

  it("applies an optimistic edit and restores the exact message when PATCH fails", async () => {
    const original = makeMessage({
      id: "msg-edit",
      bodyText: "https://safe.example/old",
      bodyFormat: "v2",
      linkSafetyState: "safe",
    });
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
      linkSafetyState: "unknown",
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
      reactions: [{ emoji: "👍", count: 1, reactedByMe: true, users: [] }],
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
        linkSafetyState: "",
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
              linkSafetyState: "",
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

// ── RF-21 link-safety corrections on a published message (issue #135) ─────────
//
// The state a message carries about its links can change *after* it has been
// delivered, in either direction: a verdict finally arrives and the notice goes
// away, or the link turns out to be malicious and its content is withdrawn.
//
// The event that carries that is deliberately not a second message.created. It
// mutates one field of a message the client already holds, which is what makes it
// idempotent under at-least-once delivery and what stops the message being
// duplicated or its mentions being fired twice.
describe("useMessages link-safety corrections", () => {
  beforeEach(() => {
    mockFetchChannelMessages.mockResolvedValue(emptyPage);
    mockFetchDMMessages.mockResolvedValue(emptyPage);
    mockFetchLinkSafetyStatuses.mockResolvedValue([]);
  });

  /** Renders the hook holding one published message with an unverified link. */
  async function hookWithUnverifiedMessage() {
    mockFetchChannelMessages.mockResolvedValue({
      messages: [makeMessage({ id: "msg-unverified", linkSafetyState: "inconclusive" })],
      nextCursor: "",
    });
    const { result } = renderHook(() =>
      useMessages({ kind: "channel", targetId: "chan-1", currentUserId: "user-me" }),
    );
    await waitFor(() => expect(result.current.state.status).toBe("ready"));
    return result;
  }

  it("carries the link-safety state from the initial load", async () => {
    const result = await hookWithUnverifiedMessage();

    expect(result.current.state.messages[0].linkSafetyState).toBe("inconclusive");
  });

  // A message published while its links were unverified arrives over the socket
  // carrying that marker, so the notice appears without a follow-up fetch.
  it("carries the link-safety state from a realtime creation", async () => {
    const { result } = renderHook(() =>
      useMessages({ kind: "channel", targetId: "chan-1", currentUserId: "user-me" }),
    );
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() => {
      fireWsEventWithPayload(
        "channel",
        "chan-1",
        makePayload({
          id: "msg-new",
          link_safety_state: "inconclusive",
        }),
      );
    });

    await waitFor(() => expect(result.current.state.messages).toHaveLength(1));
    expect(result.current.state.messages[0].linkSafetyState).toBe("inconclusive");
  });

  // A later clearance removes the notice, in place: same message, same position,
  // no duplicate.
  it("clears the notice when a verdict finally arrives", async () => {
    const result = await hookWithUnverifiedMessage();

    act(() => fireWsLinkSafetyChanged("msg-unverified", "safe"));

    await waitFor(() => expect(result.current.state.messages[0].linkSafetyState).toBe("safe"));
    expect(result.current.state.messages).toHaveLength(1);
    expect(result.current.state.messages[0].status).toBe("active");
  });

  // The direction that costs a reader something. The message stays where it is —
  // it was delivered — and the marker is what withdraws its links.
  it("withdraws the links when a published message is later condemned", async () => {
    mockFetchChannelMessages.mockResolvedValue({
      messages: [
        makeMessage({
          id: "msg-unverified",
          linkSafetyState: "inconclusive",
          bodyText: "https://bad.example",
          updatedAt: "2026-08-18T10:00:00Z",
        }),
      ],
      nextCursor: "",
    });
    const { result } = renderHook(() =>
      useMessages({ kind: "channel", targetId: "chan-1", currentUserId: "user-me" }),
    );
    await waitFor(() => expect(result.current.state.status).toBe("ready"));
    act(() => result.current.selectReply(result.current.state.messages[0]));

    act(() => fireWsLinkSafetyChanged("msg-unverified", "malicious"));

    await waitFor(() => expect(result.current.state.messages[0].linkSafetyState).toBe("malicious"));
    expect(result.current.state.messages).toHaveLength(1);
    expect(result.current.state.messages[0].bodyText).toBe("");
    expect(result.current.state.replyTo).toMatchObject({
      linkSafetyState: "malicious",
      bodyText: "",
    });
  });

  it("does not let a delayed edit event restore a condemned body", async () => {
    mockFetchChannelMessages.mockResolvedValue({
      messages: [
        makeMessage({
          id: "msg-unverified",
          bodyText: "https://bad.example",
          linkSafetyState: "inconclusive",
          updatedAt: "2026-08-18T10:00:00Z",
        }),
      ],
      nextCursor: "",
    });
    const { result } = renderHook(() =>
      useMessages({ kind: "channel", targetId: "chan-1", currentUserId: "user-me" }),
    );
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() => fireWsLinkSafetyChanged("msg-unverified", "malicious", "2026-08-18T13:00:00Z"));
    act(() =>
      capturedOnMessageUpdated?.({
        type: "message.updated",
        target_type: "channel",
        target_id: "chan-1",
        message_update: {
          message_id: "msg-unverified",
          body: "https://bad.example",
          body_format: "v3",
          link_safety_state: "inconclusive",
          edited_at: "2026-08-18T12:00:00Z",
          updated_at: "2026-08-18T12:00:00Z",
          edit_count: 1,
          is_edited: true,
        },
      }),
    );

    expect(result.current.state.messages[0]).toMatchObject({
      linkSafetyState: "malicious",
      bodyText: "",
      updatedAt: "2026-08-18T13:00:00Z",
    });
  });

  it("does not let a delayed PATCH success restore a condemned body", async () => {
    mockFetchChannelMessages.mockResolvedValue({
      messages: [
        makeMessage({
          id: "msg-unverified",
          bodyText: "https://bad.example",
          linkSafetyState: "inconclusive",
          updatedAt: "2026-08-18T10:00:00Z",
        }),
      ],
      nextCursor: "",
    });
    let resolveEdit!: (message: Message) => void;
    mockEditMessage.mockImplementation(() => new Promise((resolve) => (resolveEdit = resolve)));
    const { result } = renderHook(() =>
      useMessages({ kind: "channel", targetId: "chan-1", currentUserId: "user-me" }),
    );
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    let request!: Promise<Message>;
    act(() => {
      request = result.current.editMessageLocal("msg-unverified", "https://bad.example", "v3");
    });
    act(() => fireWsLinkSafetyChanged("msg-unverified", "malicious", "2026-08-18T13:00:00Z"));
    act(() =>
      resolveEdit(
        makeMessage({
          id: "msg-unverified",
          bodyText: "https://bad.example",
          bodyFormat: "v3",
          linkSafetyState: "inconclusive",
          updatedAt: "2026-08-18T12:00:00Z",
          editedAt: "2026-08-18T12:00:00Z",
          editCount: 1,
          isEdited: true,
        }),
      ),
    );
    await request;

    expect(result.current.state.messages[0]).toMatchObject({
      linkSafetyState: "malicious",
      bodyText: "",
      updatedAt: "2026-08-18T13:00:00Z",
    });
  });

  it("does not let a delayed message snapshot restore a condemned body", async () => {
    mockFetchChannelMessages.mockResolvedValue({
      messages: [
        makeMessage({
          id: "msg-unverified",
          bodyText: "https://bad.example",
          linkSafetyState: "inconclusive",
          updatedAt: "2026-08-18T10:00:00Z",
        }),
      ],
      nextCursor: "",
    });
    let resolveSnapshot!: (message: Message) => void;
    mockFetchChannelMessage.mockImplementation(
      () => new Promise((resolve) => (resolveSnapshot = resolve)),
    );
    const { result } = renderHook(() =>
      useMessages({ kind: "channel", targetId: "chan-1", currentUserId: "user-me" }),
    );
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() =>
      capturedOnMessageUpdated?.({
        type: "message.updated",
        target_type: "channel",
        target_id: "chan-1",
        message_id: "msg-unverified",
      }),
    );
    await waitFor(() => expect(mockFetchChannelMessage).toHaveBeenCalledOnce());
    act(() => fireWsLinkSafetyChanged("msg-unverified", "malicious", "2026-08-18T13:00:00Z"));
    act(() =>
      resolveSnapshot(
        makeMessage({
          id: "msg-unverified",
          bodyText: "https://bad.example",
          linkSafetyState: "inconclusive",
          updatedAt: "2026-08-18T12:00:00Z",
        }),
      ),
    );

    await waitFor(() =>
      expect(result.current.state.messages[0]).toMatchObject({
        linkSafetyState: "malicious",
        bodyText: "",
        updatedAt: "2026-08-18T13:00:00Z",
      }),
    );
  });

  it("ignores an old condemnation after a newer link-free edit", async () => {
    mockFetchChannelMessages.mockResolvedValue({
      messages: [makeMessage({ id: "msg-unverified", updatedAt: "2026-08-18T10:00:00Z" })],
      nextCursor: "",
    });
    const { result } = renderHook(() =>
      useMessages({ kind: "channel", targetId: "chan-1", currentUserId: "user-me" }),
    );
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() =>
      capturedOnMessageUpdated?.({
        type: "message.updated",
        target_type: "channel",
        target_id: "chan-1",
        message_update: {
          message_id: "msg-unverified",
          body: "sem URL",
          body_format: "v3",
          link_safety_state: "",
          edited_at: "2026-08-18T14:00:00Z",
          updated_at: "2026-08-18T14:00:00Z",
          edit_count: 1,
          is_edited: true,
        },
      }),
    );
    act(() => fireWsLinkSafetyChanged("msg-unverified", "malicious", "2026-08-18T13:00:00Z"));

    expect(result.current.state.messages[0]).toMatchObject({
      linkSafetyState: "",
      bodyText: "sem URL",
      updatedAt: "2026-08-18T14:00:00Z",
    });
  });

  it("applies a correction that arrives before message.created", async () => {
    const { result } = renderHook(() =>
      useMessages({ kind: "channel", targetId: "chan-1", currentUserId: "user-me" }),
    );
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() => fireWsLinkSafetyChanged("msg-late", "malicious", "2026-08-18T13:00:00Z"));
    act(() =>
      fireWsEventWithPayload(
        "channel",
        "chan-1",
        makePayload({
          id: "msg-late",
          body_text: "https://bad.example",
          link_safety_state: "inconclusive",
          updated_at: "2026-08-18T12:00:00Z",
        }),
      ),
    );

    await waitFor(() => expect(result.current.state.messages).toHaveLength(1));
    expect(result.current.state.messages[0]).toMatchObject({
      linkSafetyState: "malicious",
      bodyText: "",
      updatedAt: "2026-08-18T13:00:00Z",
    });
  });

  it("applies an early correction to quotes and references in a delayed page", async () => {
    let resolvePage!: (page: MessagePage) => void;
    mockFetchChannelMessages.mockImplementation(
      () => new Promise((resolve) => (resolvePage = resolve)),
    );
    const { result } = renderHook(() =>
      useMessages({ kind: "channel", targetId: "chan-1", currentUserId: "user-me" }),
    );

    act(() => fireWsLinkSafetyChanged("source", "malicious", "2026-08-18T13:00:00Z"));
    act(() =>
      resolvePage({
        messages: [
          makeMessage({
            id: "quote-destination",
            quoted: {
              id: "source",
              authorId: "author",
              bodyText: "https://bad.example",
              bodyFormat: "v2",
              isRemoved: false,
              deletedAt: null,
              createdAt: "2026-08-18T10:00:00Z",
              updatedAt: "2026-08-18T12:00:00Z",
              linkSafetyState: "inconclusive",
            },
          }),
          makeMessage({
            id: "reference-destination",
            reference: {
              available: true,
              messageId: "source",
              targetType: "channel",
              targetId: "other-channel",
              targetLabel: "origem",
              authorDisplayName: "Alice",
              bodyText: "https://bad.example",
              bodyFormat: "v2",
              createdAt: "2026-08-18T10:00:00Z",
              updatedAt: "2026-08-18T12:00:00Z",
              linkSafetyState: "inconclusive",
            },
          }),
        ],
        nextCursor: "",
      }),
    );

    await waitFor(() => expect(result.current.state.status).toBe("ready"));
    expect(result.current.state.messages[0].quoted).toMatchObject({
      linkSafetyState: "malicious",
      bodyText: "",
      updatedAt: "2026-08-18T13:00:00Z",
    });
    expect(result.current.state.messages[1].reference).toMatchObject({
      linkSafetyState: "malicious",
      bodyText: "",
      updatedAt: "2026-08-18T13:00:00Z",
    });
  });

  it("converges a visible source and quote on a realtime condemnation", async () => {
    const source = makeMessage({
      id: "source",
      bodyText: "https://bad.example",
      linkSafetyState: "inconclusive",
    });
    const quote = makeMessage({
      id: "quote-destination",
      quoted: {
        id: "source",
        authorId: "author",
        bodyText: "https://bad.example",
        bodyFormat: "v2",
        isRemoved: false,
        deletedAt: null,
        createdAt: "2026-08-18T10:00:00Z",
        linkSafetyState: "inconclusive",
      },
    });
    mockFetchChannelMessages.mockResolvedValue({
      messages: [source, quote],
      nextCursor: "",
    });
    const { result } = renderHook(() =>
      useMessages({ kind: "channel", targetId: "chan-1", currentUserId: "user-me" }),
    );
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() => fireWsLinkSafetyChanged("source", "malicious"));

    await waitFor(() => {
      expect(result.current.state.messages[0]).toMatchObject({
        linkSafetyState: "malicious",
        bodyText: "",
      });
      expect(result.current.state.messages[1].quoted).toMatchObject({
        linkSafetyState: "malicious",
        bodyText: "",
      });
    });
  });

  it("does not sanitize dependent text when the source becomes safe", async () => {
    mockFetchChannelMessages.mockResolvedValue({
      messages: [
        makeMessage({
          id: "quote-destination",
          quoted: {
            id: "source",
            authorId: "author",
            bodyText: "texto preservado",
            bodyFormat: "v2",
            isRemoved: false,
            deletedAt: null,
            createdAt: "2026-08-18T10:00:00Z",
            linkSafetyState: "inconclusive",
          },
        }),
      ],
      nextCursor: "",
    });
    const { result } = renderHook(() =>
      useMessages({ kind: "channel", targetId: "chan-1", currentUserId: "user-me" }),
    );
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() => fireWsLinkSafetyChanged("source", "safe"));

    expect(result.current.state.messages[0].quoted).toMatchObject({
      linkSafetyState: "safe",
      bodyText: "texto preservado",
    });
  });

  it("ignores a delayed condemnation older than visible quote and reference snapshots", async () => {
    mockFetchChannelMessages.mockResolvedValue({
      messages: [
        makeMessage({
          id: "quote-destination",
          quoted: {
            id: "source",
            authorId: "author",
            bodyText: "texto seguro",
            bodyFormat: "v2",
            isRemoved: false,
            deletedAt: null,
            createdAt: "2026-08-18T10:00:00Z",
            updatedAt: "2026-08-18T14:00:00Z",
            linkSafetyState: "safe",
          },
        }),
        makeMessage({
          id: "reference-destination",
          reference: {
            available: true,
            messageId: "source",
            targetType: "channel",
            targetId: "other-channel",
            targetLabel: "origem",
            authorDisplayName: "Alice",
            bodyText: "texto seguro",
            bodyFormat: "v2",
            createdAt: "2026-08-18T10:00:00Z",
            updatedAt: "2026-08-18T14:00:00Z",
            linkSafetyState: "safe",
          },
        }),
      ],
      nextCursor: "",
    });
    const { result } = renderHook(() =>
      useMessages({ kind: "channel", targetId: "chan-1", currentUserId: "user-me" }),
    );
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() => fireWsLinkSafetyChanged("source", "malicious", "2026-08-18T13:00:00Z"));

    expect(result.current.state.messages[0].quoted).toMatchObject({
      linkSafetyState: "safe",
      bodyText: "texto seguro",
      updatedAt: "2026-08-18T14:00:00Z",
    });
    expect(result.current.state.messages[1].reference).toMatchObject({
      linkSafetyState: "safe",
      bodyText: "texto seguro",
      updatedAt: "2026-08-18T14:00:00Z",
    });
  });

  // Delivery is at-least-once, so the same correction may arrive twice. Applying
  // it again must be a no-op rather than a duplicate message or a second render
  // that scrolls the conversation.
  it("is idempotent under repeated delivery", async () => {
    const result = await hookWithUnverifiedMessage();

    act(() => fireWsLinkSafetyChanged("msg-unverified", "safe"));
    await waitFor(() => expect(result.current.state.messages[0].linkSafetyState).toBe("safe"));
    const afterFirst = result.current.state.messages;

    act(() => fireWsLinkSafetyChanged("msg-unverified", "safe"));

    expect(result.current.state.messages).toBe(afterFirst);
    expect(result.current.state.messages).toHaveLength(1);
  });

  // A correction for something this view never held does not insert a husk; it
  // is retained only to order a later message.created for the same id.
  it("does not insert a correction for a message it does not hold", async () => {
    const result = await hookWithUnverifiedMessage();

    act(() => fireWsLinkSafetyChanged("msg-elsewhere", "malicious"));

    expect(result.current.state.messages).toHaveLength(1);
    expect(result.current.state.messages[0].id).toBe("msg-unverified");
  });

  // An unrecognised state resolves to `unknown`, which authorises nothing —
  // deliberately not `inconclusive`, which would authorise an anchor (CQ-004).
  // "The provider produced no usable verdict" and "this build does not recognise
  // what the server said" are different facts with different safe answers.
  it("treats an unknown state as unknown, not as inconclusive", async () => {
    const result = await hookWithUnverifiedMessage();

    act(() => fireWsLinkSafetyChanged("msg-unverified", "probably_fine"));

    expect(result.current.state.messages[0].linkSafetyState).toBe("unknown");
  });

  // "Verificar novamente" asks the backend and applies the authoritative answer
  // locally, so the button works even when realtime is down — which is exactly
  // when a reader is most likely to press it.
  it("applies the reconcile reply locally", async () => {
    mockReconcileMessageLinkSafety.mockResolvedValue({
      state: "safe",
      updatedAt: "2099-08-18T12:00:00Z",
      retryAfterSeconds: 60,
    });
    const result = await hookWithUnverifiedMessage();

    await act(async () => {
      await result.current.reconcileLinkSafety("msg-unverified");
    });

    expect(mockReconcileMessageLinkSafety).toHaveBeenCalledWith("msg-unverified", undefined);
    expect(result.current.state.messages[0].linkSafetyState).toBe("safe");
    // No duplicate: a correction is not a creation.
    expect(result.current.state.messages).toHaveLength(1);
  });

  // A failed request is not turned into a banner over somebody's message. The
  // outcome the reader sees is "still not verified", which is the truth.
  it("leaves the message alone when the reconcile request fails", async () => {
    mockReconcileMessageLinkSafety.mockRejectedValue(new Error("429"));
    const result = await hookWithUnverifiedMessage();

    await act(async () => {
      await result.current.reconcileLinkSafety("msg-unverified");
    });

    expect(result.current.state.messages[0].linkSafetyState).toBe("inconclusive");
    expect(result.current.state.actionError).toBeNull();
  });
});

describe("useMessages — reconnect authoritative security refresh", () => {
  beforeEach(() => {
    mockFetchChannelMessageSecuritySnapshots.mockResolvedValue([]);
    mockFetchDMMessageSecuritySnapshots.mockResolvedValue([]);
    mockFetchLinkSafetyStatuses.mockResolvedValue([]);
  });

  async function reconnect() {
    await act(async () => {
      capturedOnSubscribed?.({
        type: "subscribed",
        operation: "subscribe",
        target_type: "channel",
        target_id: "chan-1",
      });
      await Promise.resolve();
    });
  }

  it("recovers a missed malicious event for source, quote, and cross-channel reference", async () => {
    const reference = {
      available: true as const,
      messageId: "source",
      targetType: "channel" as const,
      targetId: "other-channel",
      targetLabel: "origem",
      authorDisplayName: "Alice",
      bodyText: "https://bad.example",
      bodyFormat: "v2" as const,
      createdAt: "2026-08-18T10:00:00Z",
      linkSafetyState: "inconclusive" as const,
    };
    mockFetchChannelMessages.mockResolvedValue({
      messages: [
        makeMessage({
          id: "source",
          bodyText: "https://bad.example",
          linkSafetyState: "inconclusive",
        }),
        makeMessage({
          id: "quote-destination",
          quoted: {
            id: "source",
            authorId: "author",
            bodyText: "https://bad.example",
            bodyFormat: "v2",
            isRemoved: false,
            deletedAt: null,
            createdAt: "2026-08-18T10:00:00Z",
            linkSafetyState: "inconclusive",
          },
        }),
        makeMessage({ id: "reference-destination", reference }),
      ],
      nextCursor: "",
    });
    const { result } = renderHook(() =>
      useMessages({ kind: "channel", targetId: "chan-1", currentUserId: "user-me" }),
    );
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    mockFetchChannelMessageSecuritySnapshots.mockResolvedValue([
      {
        messageId: "source",
        available: true,
        status: "active",
        linkSafetyState: "malicious",
        updatedAt: "2099-08-18T12:00:00Z",
      },
      {
        messageId: "quote-destination",
        available: true,
        status: "active",
        linkSafetyState: "",
        updatedAt: "2099-08-18T12:00:00Z",
        quoted: {
          messageId: "source",
          status: "active",
          linkSafetyState: "malicious",
          updatedAt: "2099-08-18T12:00:00Z",
        },
      },
    ]);
    mockResolveChannelMessageReferences.mockResolvedValue({
      "reference-destination": { ...reference, bodyText: "", linkSafetyState: "malicious" },
    });

    await reconnect();

    await waitFor(() => {
      expect(mockFetchChannelMessageSecuritySnapshots).toHaveBeenCalledWith(
        "chan-1",
        ["source", "quote-destination"],
        expect.any(AbortSignal),
      );
      expect(mockResolveChannelMessageReferences).toHaveBeenCalledTimes(1);
      expect(result.current.state.messages[0]).toMatchObject({
        linkSafetyState: "malicious",
        bodyText: "",
      });
      expect(result.current.state.messages[1].quoted).toMatchObject({
        linkSafetyState: "malicious",
        bodyText: "",
      });
      expect(result.current.state.messages[2].reference).toMatchObject({
        linkSafetyState: "malicious",
        bodyText: "",
      });
    });
  });

  it("removes the warning when an inconclusive source became safe offline", async () => {
    mockFetchChannelMessages.mockResolvedValue({
      messages: [makeMessage({ id: "source", linkSafetyState: "inconclusive" })],
      nextCursor: "",
    });
    mockFetchChannelMessageSecuritySnapshots.mockResolvedValue([
      {
        messageId: "source",
        available: true,
        status: "active",
        linkSafetyState: "safe",
        updatedAt: "2099-08-18T12:00:00Z",
      },
    ]);
    const { result } = renderHook(() =>
      useMessages({ kind: "channel", targetId: "chan-1", currentUserId: "user-me" }),
    );
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    await reconnect();

    await waitFor(() => expect(result.current.state.messages[0].linkSafetyState).toBe("safe"));
  });

  it("does not let an older reconnect snapshot overwrite a newer quote", async () => {
    mockFetchChannelMessages.mockResolvedValue({
      messages: [
        makeMessage({
          id: "quote-destination",
          quoted: {
            id: "source",
            authorId: "author",
            bodyText: "https://unverified.example",
            bodyFormat: "v2",
            isRemoved: false,
            deletedAt: null,
            createdAt: "2026-08-18T10:00:00Z",
            updatedAt: "2026-08-18T14:00:00Z",
            linkSafetyState: "inconclusive",
          },
        }),
      ],
      nextCursor: "",
    });
    mockFetchChannelMessageSecuritySnapshots.mockResolvedValue([
      {
        messageId: "quote-destination",
        available: true,
        status: "active",
        linkSafetyState: "",
        updatedAt: "2026-08-18T14:00:00Z",
        quoted: {
          messageId: "source",
          status: "active",
          linkSafetyState: "malicious",
          updatedAt: "2026-08-18T13:00:00Z",
        },
      },
    ]);
    const { result } = renderHook(() =>
      useMessages({ kind: "channel", targetId: "chan-1", currentUserId: "user-me" }),
    );
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    await reconnect();

    await waitFor(() => expect(mockFetchChannelMessageSecuritySnapshots).toHaveBeenCalledOnce());
    expect(result.current.state.messages[0].quoted).toMatchObject({
      linkSafetyState: "inconclusive",
      bodyText: "https://unverified.example",
      updatedAt: "2026-08-18T14:00:00Z",
    });
  });

  it("removes stale content when a reconnect snapshot is no longer available", async () => {
    mockFetchChannelMessages.mockResolvedValue({
      messages: [
        makeMessage({
          id: "source",
          bodyText: "https://private.example",
          linkSafetyState: "inconclusive",
          reference: {
            available: true,
            messageId: "cross-channel-source",
            targetType: "channel",
            targetId: "other-channel",
            targetLabel: "Other channel",
            authorDisplayName: "Other",
            bodyText: "https://private.example",
            bodyFormat: "v2",
            createdAt: "2026-08-18T10:00:00Z",
            linkSafetyState: "inconclusive",
          },
        }),
      ],
      nextCursor: "",
    });
    mockFetchChannelMessageSecuritySnapshots.mockResolvedValue([
      { messageId: "source", available: false },
    ]);
    const { result } = renderHook(() =>
      useMessages({ kind: "channel", targetId: "chan-1", currentUserId: "user-me" }),
    );
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    await reconnect();

    await waitFor(() =>
      expect(result.current.state.messages[0]).toMatchObject({
        status: "deleted",
        isRemoved: true,
        bodyText: "",
        reference: undefined,
      }),
    );
  });
});

// CQ-004: an unrecognised link-safety state, over the websocket, must withdraw
// the anchor rather than granting one.
describe("useMessages unknown link-safety states", () => {
  beforeEach(() => {
    mockFetchChannelMessages.mockResolvedValue(emptyPage);
    mockFetchDMMessages.mockResolvedValue(emptyPage);
    mockFetchLinkSafetyStatuses.mockResolvedValue([]);
  });

  it("decodes an unknown realtime state as unknown, not as inconclusive", async () => {
    mockFetchChannelMessages.mockResolvedValue({
      messages: [makeMessage({ id: "msg-unverified", linkSafetyState: "inconclusive" })],
      nextCursor: "",
    });
    const { result } = renderHook(() =>
      useMessages({ kind: "channel", targetId: "chan-1", currentUserId: "user-me" }),
    );
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() => fireWsLinkSafetyChanged("msg-unverified", "future_state_v2"));

    await waitFor(() => expect(result.current.state.messages[0].linkSafetyState).toBe("unknown"));
    // The message itself is untouched — it was published and stays published.
    expect(result.current.state.messages).toHaveLength(1);
    expect(result.current.state.messages[0].status).toBe("active");
  });

  it("decodes an unknown state on a realtime creation as unknown", async () => {
    const { result } = renderHook(() =>
      useMessages({ kind: "channel", targetId: "chan-1", currentUserId: "user-me" }),
    );
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() => {
      fireWsEventWithPayload(
        "channel",
        "chan-1",
        makePayload({
          id: "msg-new",
          link_safety_state: "future_state_v2",
        }),
      );
    });

    await waitFor(() => expect(result.current.state.messages).toHaveLength(1));
    expect(result.current.state.messages[0].linkSafetyState).toBe("unknown");
  });
});
