import { act, renderHook, waitFor } from "@testing-library/react";
import type { PropsWithChildren } from "react";
import { MemoryRouter, useLocation, useNavigate } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type {
  ShowBrowserMessageNotificationInput,
  ShowBrowserMessageNotificationResult,
} from "./browserNotification";
import { parseInstant } from "./sidebarOrder";
import { savePersistedUnread } from "./sidebarUnreadPersistence";
import type { WSMessageCreatedEvent } from "./useChatWebSocket";
import { useChatSidebar, type SidebarState } from "./useChatSidebar";

const {
  mockFetchSidebarData,
  mockMarkConversationRead,
  mockSetSidebarConversationPinned,
  mockRenameChannel,
  mockSetConversationMuted,
  mockLeaveConversation,
  mockPlayMessageSound,
  mockGetSoundNotificationMode,
  mockShowBrowserMessageNotification,
  websocket,
} = vi.hoisted(() => ({
  mockFetchSidebarData: vi.fn(),
  mockMarkConversationRead: vi.fn(),
  mockSetSidebarConversationPinned: vi.fn(),
  mockRenameChannel: vi.fn(),
  mockSetConversationMuted: vi.fn(),
  mockLeaveConversation: vi.fn(),
  mockPlayMessageSound: vi.fn(),
  mockGetSoundNotificationMode: vi.fn(
    () => "all" as "off" | "all" | "mentions" | "mentions_and_dms",
  ),
  // Defaults to {shown:false} so every existing test below — written before
  // the browser Notification integration existed — keeps falling through to
  // the chime exactly as before, with no changes required to those tests.
  mockShowBrowserMessageNotification: vi.fn<
    (input: ShowBrowserMessageNotificationInput) => ShowBrowserMessageNotificationResult
  >(() => ({ shown: false })),
  websocket: {
    onMessageCreated: null as ((event: WSMessageCreatedEvent) => void) | null,
    onConversationAvailable: null as (() => void) | null,
    onConversationUpdated: null as (() => void) | null,
    onConversationEvent: null as (() => void) | null,
  },
}));

vi.mock("./chatApi", () => ({
  fetchSidebarData: mockFetchSidebarData,
  markConversationRead: mockMarkConversationRead,
  setSidebarConversationPinned: mockSetSidebarConversationPinned,
  renameChannel: mockRenameChannel,
  setConversationMuted: mockSetConversationMuted,
  leaveConversation: mockLeaveConversation,
}));
vi.mock("./messageSound", () => ({
  playMessageSound: mockPlayMessageSound,
}));
vi.mock("./soundPreference", () => ({
  getSoundNotificationMode: mockGetSoundNotificationMode,
}));
vi.mock("./browserNotification", () => ({
  showBrowserMessageNotification: mockShowBrowserMessageNotification,
}));
vi.mock("./useChatWebSocket", () => ({
  useChatWebSocket: vi.fn(
    ({
      onMessageCreated,
      onConversationAvailable,
      onConversationUpdated,
      onConversationEvent,
    }: {
      onMessageCreated: (event: WSMessageCreatedEvent) => void;
      onConversationAvailable?: () => void;
      onConversationUpdated?: () => void;
      onConversationEvent?: () => void;
    }) => {
      websocket.onMessageCreated = onMessageCreated;
      websocket.onConversationAvailable = onConversationAvailable ?? null;
      websocket.onConversationUpdated = onConversationUpdated ?? null;
      websocket.onConversationEvent = onConversationEvent ?? null;
      return { toggleReaction: vi.fn() };
    },
  ),
}));

const channelA = "11111111-1111-4111-8111-111111111111";
const channelB = "22222222-2222-4222-8222-222222222222";
const dmC = "33333333-3333-4333-8333-333333333333";
const groupD = "44444444-4444-4444-8444-444444444444";
const currentUserId = "00000000-0000-4000-8000-0000000000f1";
const otherUserId = "00000000-0000-4000-8000-0000000000f2";

function deferredValue<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason: unknown) => void;
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return { promise, resolve, reject };
}

function wrapper(path: string) {
  return function SidebarWrapper({ children }: PropsWithChildren) {
    return <MemoryRouter initialEntries={[path]}>{children}</MemoryRouter>;
  };
}

/**
 * Like `wrapper`, but exposes a `navigate` function that changes the route
 * without unmounting the router (or the hook under test) — so a test can
 * simulate "opening a conversation" mid-test and assert the badge clears in
 * the same hook instance, the same way the real app's route change does.
 */
function navigableWrapper(path: string) {
  const navigateRef: { current: (to: string) => void } = { current: () => {} };
  function NavigateCapture() {
    navigateRef.current = useNavigate();
    return null;
  }
  function Wrapper({ children }: PropsWithChildren) {
    return (
      <MemoryRouter initialEntries={[path]}>
        <NavigateCapture />
        {children}
      </MemoryRouter>
    );
  }
  return { Wrapper, navigateRef };
}

/**
 * Like `wrapper`, but reports the route the hook is on, so a test can assert
 * where a navigation landed instead of only that one was requested.
 */
function routedWrapper(path: string) {
  const pathnameRef = { current: path };
  function LocationCapture() {
    pathnameRef.current = useLocation().pathname;
    return null;
  }
  function Wrapper({ children }: PropsWithChildren) {
    return (
      <MemoryRouter initialEntries={[path]}>
        <LocationCapture />
        {children}
      </MemoryRouter>
    );
  }
  return { Wrapper, pathnameRef };
}

function messageCreated(
  messageId: string,
  targetId: string,
  senderId = "other-1",
  targetType: "channel" | "dm" = "channel",
  /** The message's persisted creation instant — the sidebar's ordering key. */
  messageCreatedAt = "2026-07-28T12:00:00Z",
  bodyText = "Nova mensagem",
  kind = "user",
): WSMessageCreatedEvent {
  return {
    type: "message.created",
    workspace_id: "workspace-1",
    target_type: targetType,
    target_id: targetId,
    message_id: messageId,
    event_id: `event-${messageId}`,
    created_at: "2026-07-28T12:00:00Z",
    payload: {
      id: messageId,
      workspace_id: "workspace-1",
      channel_id: targetType === "channel" ? targetId : undefined,
      dm_conversation_id: targetType === "dm" ? targetId : undefined,
      sender_id: senderId,
      sender_display_name: "Other",
      kind,
      body_text: bodyText,
      status: "active",
      is_removed: false,
      created_at: messageCreatedAt,
      updated_at: messageCreatedAt,
    },
  };
}

/** The same official mention token format RichTextRenderer parses (see richTextMarkers.ts). */
function mentionToken(userId: string, label = "Você") {
  return `@[${label}](mention:user:${userId})`;
}

/**
 * A message.created event that carries no message DTO.
 *
 * The real protocol produces these: a message with an RF-09 reference is
 * delivered route-only, and an event relayed from another chat-service instance
 * has its payload stripped at the bus boundary. Such an event says that
 * something happened without saying when.
 */
function routeOnlyMessageCreated(messageId: string, targetId: string): WSMessageCreatedEvent {
  const event = messageCreated(messageId, targetId);
  delete event.payload;
  return event;
}

function unreadCounts(state: ReturnType<typeof useChatSidebar>["state"]) {
  if (state.status !== "ready") throw new Error("sidebar not ready");
  return {
    channelA: state.channels.find(({ id }) => id === channelA)?.unreadCount ?? 0,
    channelB: state.channels.find(({ id }) => id === channelB)?.unreadCount ?? 0,
    dmC: state.dms.find(({ id }) => id === dmC)?.unreadCount ?? 0,
  };
}

describe("useChatSidebar identity retry", () => {
  beforeEach(() => {
    mockFetchSidebarData.mockReset();
    websocket.onMessageCreated = null;
    websocket.onConversationAvailable = null;
  });

  it("keeps attachment limits from the canonical sidebar response", async () => {
    mockFetchSidebarData.mockResolvedValueOnce({
      currentUserId,
      workspaceId: "workspace-1",
      maxUploadBytes: 8 * 1024 * 1024,
      maxFiles: 10,
      maxBytes: 512 * 1024 * 1024,
      channels: [],
      dms: [],
      categories: [],
    });

    const { result } = renderHook(() => useChatSidebar(), { wrapper: wrapper("/chat") });
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    if (result.current.state.status !== "ready") throw new Error("sidebar not ready");
    expect(result.current.state.attachmentLimits).toEqual({
      maxUploadBytes: 8 * 1024 * 1024,
      maxFiles: 10,
      maxBytes: 512 * 1024 * 1024,
    });
    expect(mockFetchSidebarData).toHaveBeenCalledOnce();
  });

  it("shares one retry request and allows another attempt after failure", async () => {
    mockFetchSidebarData.mockRejectedValueOnce(new Error("offline"));
    const { result } = renderHook(() => useChatSidebar(), { wrapper: wrapper("/chat") });
    await waitFor(() => expect(result.current.state.status).toBe("error"));

    const failedRetry = deferredValue<never>();
    mockFetchSidebarData.mockReturnValue(failedRetry.promise);
    let firstRetry!: ReturnType<typeof result.current.retry>;
    let duplicateRetry!: ReturnType<typeof result.current.retry>;
    act(() => {
      firstRetry = result.current.retry();
      duplicateRetry = result.current.retry();
    });

    expect(firstRetry).toBeInstanceOf(Promise);
    expect(duplicateRetry).toBe(firstRetry);
    expect(mockFetchSidebarData).toHaveBeenCalledTimes(2);
    expect(result.current.state.status).toBe("loading");

    failedRetry.reject(new Error("still offline"));
    await act(async () => firstRetry);
    expect(result.current.state.status).toBe("error");

    mockFetchSidebarData.mockResolvedValueOnce({
      currentUserId,
      channels: [],
      dms: [],
    });
    await act(async () => result.current.retry());

    expect(mockFetchSidebarData).toHaveBeenCalledTimes(3);
    expect(result.current.state.status).toBe("ready");
  });

  it("does not update after unmount while identity retry is pending", async () => {
    mockFetchSidebarData.mockRejectedValueOnce(new Error("offline"));
    const { result, unmount } = renderHook(() => useChatSidebar(), {
      wrapper: wrapper("/chat"),
    });
    await waitFor(() => expect(result.current.state.status).toBe("error"));

    const retry = deferredValue<{
      currentUserId: string;
      channels: [];
      dms: [];
    }>();
    mockFetchSidebarData.mockReturnValueOnce(retry.promise);
    const operation = result.current.retry();
    unmount();
    retry.resolve({ currentUserId, channels: [], dms: [] });
    await operation;

    expect(mockFetchSidebarData).toHaveBeenCalledTimes(2);
  });
});

describe("useChatSidebar sidebar pins", () => {
  beforeEach(() => {
    mockFetchSidebarData.mockReset();
    mockSetSidebarConversationPinned.mockReset();
    mockFetchSidebarData.mockResolvedValue({
      currentUserId,
      channels: [{ id: channelA, name: "A", type: "public", canWrite: true, unreadCount: 2 }],
      dms: [],
    });
  });

  it("optimistically marks a conversation pinned and rolls back on persistence failure", async () => {
    mockSetSidebarConversationPinned.mockRejectedValue(new Error("offline"));
    const { result } = renderHook(() => useChatSidebar(), { wrapper: wrapper("/chat") });
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    let operation!: Promise<void>;
    act(() => {
      operation = result.current.setPinned({ kind: "channel", targetId: channelA }, true);
    });
    expect(result.current.state.status).toBe("ready");
    if (result.current.state.status === "ready") {
      expect(result.current.state.channels[0]).toMatchObject({
        pinnedAt: "0001-01-01T00:00:00Z",
        unreadCount: 2,
      });
    }
    await act(async () => {
      await expect(operation).rejects.toThrow("offline");
    });
    if (result.current.state.status === "ready") {
      expect(result.current.state.channels[0]).toMatchObject({ pinnedAt: null, unreadCount: 2 });
    }
    expect(mockSetSidebarConversationPinned).toHaveBeenCalledWith("channel", channelA, true);
  });
});

describe("useChatSidebar realtime unread", () => {
  beforeEach(() => {
    websocket.onMessageCreated = null;
    mockFetchSidebarData.mockResolvedValue({
      currentUserId,
      channels: [
        { id: channelA, name: "A", type: "public", canWrite: true },
        { id: channelB, name: "B", type: "private", canWrite: true },
      ],
      dms: [{ id: dmC, type: "1:1", name: "C", participants: [] }],
    });
  });

  it("deduplicates repeated background messages by message id", async () => {
    const { result } = renderHook(() => useChatSidebar(), {
      wrapper: wrapper(`/chat/channel/${channelB}`),
    });
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    const event = messageCreated("message-1", channelA);
    act(() => {
      websocket.onMessageCreated?.(event);
      websocket.onMessageCreated?.(event);
    });

    expect(unreadCounts(result.current.state).channelA).toBe(1);
  });

  it("does not increment unread for the current user's own background message", async () => {
    const { result } = renderHook(() => useChatSidebar(), {
      wrapper: wrapper(`/chat/channel/${channelB}`),
    });
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() => websocket.onMessageCreated?.(messageCreated("message-own", channelA, currentUserId)));

    expect(unreadCounts(result.current.state).channelA).toBe(0);
  });

  it("does not increment unread for the active conversation", async () => {
    const { result } = renderHook(() => useChatSidebar(), {
      wrapper: wrapper(`/chat/channel/${channelA}`),
    });
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() => websocket.onMessageCreated?.(messageCreated("message-active", channelA)));

    expect(unreadCounts(result.current.state).channelA).toBe(0);
  });

  it("updates only the target conversation across channel and DM events", async () => {
    const { result } = renderHook(() => useChatSidebar(), {
      wrapper: wrapper(`/chat/channel/${channelB}`),
    });
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() => websocket.onMessageCreated?.(messageCreated("message-a", channelA)));
    expect(unreadCounts(result.current.state)).toEqual({ channelA: 1, channelB: 0, dmC: 0 });

    act(() => websocket.onMessageCreated?.(messageCreated("message-c", dmC, "other-2", "dm")));
    expect(unreadCounts(result.current.state)).toEqual({ channelA: 1, channelB: 0, dmC: 1 });
  });

  // #492: opening a conversation's route is navigation, not a read receipt —
  // the badge (and its mention flag) survive until something explicitly
  // marks it read. The route still suppresses *further* increments for the
  // conversation currently open, which is a separate, still-active rule
  // (countsAsUnread's activeTarget check, independent of target_opened).
  it("does not clear a conversation's badge on opening it, but still suppresses new unread while it stays open", async () => {
    const { Wrapper, navigateRef } = navigableWrapper("/chat");
    const { result } = renderHook(() => useChatSidebar(), { wrapper: Wrapper });
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() =>
      websocket.onMessageCreated?.(
        messageCreated(
          "open-a",
          channelA,
          "other-1",
          "channel",
          undefined,
          `oi ${mentionToken(currentUserId)}`,
        ),
      ),
    );
    act(() => websocket.onMessageCreated?.(messageCreated("open-b", channelB)));
    expect(unreadCounts(result.current.state)).toEqual({ channelA: 1, channelB: 1, dmC: 0 });
    if (result.current.state.status === "ready") {
      expect(result.current.state.channels.find((c) => c.id === channelA)?.hasMentionUnread).toBe(
        true,
      );
    }

    act(() => navigateRef.current(`/chat/channel/${channelA}`));
    expect(unreadCounts(result.current.state).channelA).toBe(1);
    if (result.current.state.status === "ready") {
      expect(result.current.state.channels.find((c) => c.id === channelA)?.hasMentionUnread).toBe(
        true,
      );
    }

    act(() => websocket.onMessageCreated?.(messageCreated("while-open", channelA)));
    expect(unreadCounts(result.current.state).channelA).toBe(1);
    expect(unreadCounts(result.current.state).channelB).toBe(1);
  });
});

describe("useChatSidebar notification sound", () => {
  beforeEach(() => {
    websocket.onMessageCreated = null;
    mockPlayMessageSound.mockReset();
    mockFetchSidebarData.mockResolvedValue({
      currentUserId,
      channels: [
        { id: channelA, name: "A", type: "public", canWrite: true },
        { id: channelB, name: "B", type: "private", canWrite: true },
      ],
      dms: [
        { id: dmC, type: "1:1", name: "C", participants: [] },
        { id: groupD, type: "group", name: "D", participants: [] },
      ],
    });
  });

  it("plays a sound for another person's message in a background conversation", async () => {
    const { result } = renderHook(() => useChatSidebar(), {
      wrapper: wrapper(`/chat/channel/${channelB}`),
    });
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() => websocket.onMessageCreated?.(messageCreated("message-1", channelA)));

    expect(mockPlayMessageSound).toHaveBeenCalledTimes(1);
  });

  it("does not play a sound for the current user's own message", async () => {
    const { result } = renderHook(() => useChatSidebar(), {
      wrapper: wrapper(`/chat/channel/${channelB}`),
    });
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() => websocket.onMessageCreated?.(messageCreated("message-own", channelA, currentUserId)));

    expect(mockPlayMessageSound).not.toHaveBeenCalled();
  });

  it("does not play a sound for a message in the currently active conversation", async () => {
    const { result } = renderHook(() => useChatSidebar(), {
      wrapper: wrapper(`/chat/channel/${channelA}`),
    });
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() => websocket.onMessageCreated?.(messageCreated("message-active", channelA)));

    expect(mockPlayMessageSound).not.toHaveBeenCalled();
  });

  it("plays a sound only once for a duplicate delivery of the same message id", async () => {
    const { result } = renderHook(() => useChatSidebar(), {
      wrapper: wrapper(`/chat/channel/${channelB}`),
    });
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    const event = messageCreated("message-dup", channelA);
    act(() => {
      websocket.onMessageCreated?.(event);
      websocket.onMessageCreated?.(event);
    });

    expect(mockPlayMessageSound).toHaveBeenCalledTimes(1);
  });

  it("does not play a sound for a route-only event with no message payload", async () => {
    const { result } = renderHook(() => useChatSidebar(), {
      wrapper: wrapper(`/chat/channel/${channelB}`),
    });
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() =>
      websocket.onMessageCreated?.(routeOnlyMessageCreated("message-route-only", channelA)),
    );

    expect(mockPlayMessageSound).not.toHaveBeenCalled();
  });

  it("still updates the unread badge when playMessageSound throws", async () => {
    mockPlayMessageSound.mockImplementation(() => {
      throw new Error("audio backend unavailable");
    });
    const { result } = renderHook(() => useChatSidebar(), {
      wrapper: wrapper(`/chat/channel/${channelB}`),
    });
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() => websocket.onMessageCreated?.(messageCreated("message-1", channelA)));

    expect(unreadCounts(result.current.state).channelA).toBe(1);
  });

  // ── DM coverage ──────────────────────────────────────────────────────────
  // Investigated a report that the sound "works for channels but not DMs".
  // Direct code tracing plus a live probe against the real backend and a real
  // browser found the DM and channel paths are byte-for-byte symmetric (same
  // onMessageCreated callback, same gating expression, only target_type/
  // target_id differ) — no production code changed as a result. These tests
  // close the real gap that investigation surfaced: the DM path itself had no
  // dedicated sound test before, even though the channel path did.

  it("plays a sound for another person's DM message in a background conversation", async () => {
    const { result } = renderHook(() => useChatSidebar(), {
      wrapper: wrapper(`/chat/channel/${channelB}`),
    });
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() => websocket.onMessageCreated?.(messageCreated("dm-message-1", dmC, "other-1", "dm")));

    expect(mockPlayMessageSound).toHaveBeenCalledTimes(1);
  });

  it("does not play a sound for a DM message in the currently active DM", async () => {
    vi.spyOn(document, "hasFocus").mockReturnValue(true);
    const { result } = renderHook(() => useChatSidebar(), {
      wrapper: wrapper(`/chat/dm/${dmC}`),
    });
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() =>
      websocket.onMessageCreated?.(messageCreated("dm-message-active", dmC, "other-1", "dm")),
    );

    expect(mockPlayMessageSound).not.toHaveBeenCalled();
  });

  it("does not play a sound for the current user's own DM message", async () => {
    const { result } = renderHook(() => useChatSidebar(), {
      wrapper: wrapper(`/chat/channel/${channelB}`),
    });
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() =>
      websocket.onMessageCreated?.(messageCreated("dm-message-own", dmC, currentUserId, "dm")),
    );

    expect(mockPlayMessageSound).not.toHaveBeenCalled();
  });

  it("plays a sound only once for a duplicate delivery of the same DM message id", async () => {
    const { result } = renderHook(() => useChatSidebar(), {
      wrapper: wrapper(`/chat/channel/${channelB}`),
    });
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    const event = messageCreated("dm-message-dup", dmC, "other-1", "dm");
    act(() => {
      websocket.onMessageCreated?.(event);
      websocket.onMessageCreated?.(event);
    });

    expect(mockPlayMessageSound).toHaveBeenCalledTimes(1);
  });

  it("counts a duplicate @mention delivery only once for sound, badge and hasMentionUnread", async () => {
    const { result } = renderHook(() => useChatSidebar(), {
      wrapper: wrapper(`/chat/channel/${channelB}`),
    });
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    const event = messageCreated(
      "mention-dup",
      channelA,
      "other-1",
      "channel",
      undefined,
      `oi ${mentionToken(currentUserId)}`,
    );
    act(() => {
      websocket.onMessageCreated?.(event);
      websocket.onMessageCreated?.(event);
    });

    expect(mockPlayMessageSound).toHaveBeenCalledTimes(1);
    if (result.current.state.status === "ready") {
      const channel = result.current.state.channels.find((c) => c.id === channelA);
      expect(channel?.unreadCount).toBe(1);
      expect(channel?.hasMentionUnread).toBe(true);
    }
  });

  it("counts a duplicate DM+mention delivery only once for sound, badge and hasMentionUnread", async () => {
    const { result } = renderHook(() => useChatSidebar(), {
      wrapper: wrapper(`/chat/channel/${channelB}`),
    });
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    const event = messageCreated(
      "dm-mention-dup",
      dmC,
      "other-1",
      "dm",
      undefined,
      `oi ${mentionToken(currentUserId)}`,
    );
    act(() => {
      websocket.onMessageCreated?.(event);
      websocket.onMessageCreated?.(event);
    });

    expect(mockPlayMessageSound).toHaveBeenCalledTimes(1);
    if (result.current.state.status === "ready") {
      const dm = result.current.state.dms.find((d) => d.id === dmC);
      expect(dm?.unreadCount).toBe(1);
      expect(dm?.hasMentionUnread).toBe(true);
    }
  });

  it("recognizes the right conversation when a channel and a DM share no identifier overlap", async () => {
    // channelA and dmC are different UUIDs on purpose (channel/DM ids are
    // never the same value in the real schema either) — this asserts the
    // sound/badge machinery keys off (kind, id) together, not id alone, and
    // that a DM event never gets attributed to a channel row or vice versa.
    const { result } = renderHook(() => useChatSidebar(), {
      wrapper: wrapper(`/chat/channel/${channelB}`),
    });
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() => websocket.onMessageCreated?.(messageCreated("chan-message", channelA)));
    expect(mockPlayMessageSound).toHaveBeenCalledTimes(1);
    expect(unreadCounts(result.current.state)).toEqual({ channelA: 1, channelB: 0, dmC: 0 });

    mockPlayMessageSound.mockClear();
    act(() => websocket.onMessageCreated?.(messageCreated("dm-message", dmC, "other-1", "dm")));
    expect(mockPlayMessageSound).toHaveBeenCalledTimes(1);
    expect(unreadCounts(result.current.state)).toEqual({ channelA: 1, channelB: 0, dmC: 1 });
  });

  it("plays a sound for a background group DM the same way as a 1:1 DM", async () => {
    const { result } = renderHook(() => useChatSidebar(), {
      wrapper: wrapper(`/chat/channel/${channelB}`),
    });
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() =>
      websocket.onMessageCreated?.(messageCreated("group-message-1", groupD, "other-1", "dm")),
    );

    expect(mockPlayMessageSound).toHaveBeenCalledTimes(1);
    if (result.current.state.status === "ready") {
      expect(result.current.state.dms.find((d) => d.id === groupD)?.unreadCount).toBe(1);
    }
  });
});

describe("useChatSidebar native browser notification", () => {
  beforeEach(() => {
    websocket.onMessageCreated = null;
    mockPlayMessageSound.mockReset();
    mockShowBrowserMessageNotification.mockReset();
    mockShowBrowserMessageNotification.mockReturnValue({ shown: false });
    mockFetchSidebarData.mockResolvedValue({
      currentUserId,
      channels: [
        { id: channelA, name: "A", type: "public", canWrite: true },
        { id: channelB, name: "B", type: "private", canWrite: true },
      ],
      dms: [{ id: dmC, type: "1:1", name: "C", participants: [] }],
    });
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("shows a native notification and skips the chime when the tab is backgrounded", async () => {
    mockShowBrowserMessageNotification.mockReturnValue({ shown: true });
    const visibility = vi.spyOn(document, "visibilityState", "get");
    visibility.mockReturnValue("hidden");
    const { result } = renderHook(() => useChatSidebar(), {
      wrapper: wrapper(`/chat/channel/${channelB}`),
    });
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() => websocket.onMessageCreated?.(messageCreated("native-1", channelA)));

    expect(mockShowBrowserMessageNotification).toHaveBeenCalledTimes(1);
    expect(mockShowBrowserMessageNotification).toHaveBeenCalledWith(
      expect.objectContaining({
        targetKind: "channel",
        targetId: channelA,
        senderDisplayName: "Other",
        bodyText: "Nova mensagem",
      }),
    );
    expect(mockPlayMessageSound).not.toHaveBeenCalled();
  });

  it("falls back to the chime when the native notification is not shown", async () => {
    mockShowBrowserMessageNotification.mockReturnValue({ shown: false });
    const visibility = vi.spyOn(document, "visibilityState", "get");
    visibility.mockReturnValue("hidden");
    const { result } = renderHook(() => useChatSidebar(), {
      wrapper: wrapper(`/chat/channel/${channelB}`),
    });
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() => websocket.onMessageCreated?.(messageCreated("native-2", channelA)));

    expect(mockShowBrowserMessageNotification).toHaveBeenCalledTimes(1);
    expect(mockPlayMessageSound).toHaveBeenCalledTimes(1);
  });

  it("uses a native notification when the page is visible but another application has focus", async () => {
    mockShowBrowserMessageNotification.mockReturnValue({ shown: true });
    const visibility = vi.spyOn(document, "visibilityState", "get");
    visibility.mockReturnValue("visible");
    vi.spyOn(document, "hasFocus").mockReturnValue(false);
    const { result } = renderHook(() => useChatSidebar(), {
      wrapper: wrapper(`/chat/channel/${channelB}`),
    });
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() => websocket.onMessageCreated?.(messageCreated("native-unfocused", channelA)));

    expect(mockShowBrowserMessageNotification).toHaveBeenCalledTimes(1);
    expect(mockPlayMessageSound).not.toHaveBeenCalled();
  });

  it("never attempts a native notification while the page and window are focused", async () => {
    const visibility = vi.spyOn(document, "visibilityState", "get");
    visibility.mockReturnValue("visible");
    vi.spyOn(document, "hasFocus").mockReturnValue(true);
    const { result } = renderHook(() => useChatSidebar(), {
      wrapper: wrapper(`/chat/channel/${channelB}`),
    });
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() => websocket.onMessageCreated?.(messageCreated("native-visible", channelA)));

    expect(mockShowBrowserMessageNotification).not.toHaveBeenCalled();
    expect(mockPlayMessageSound).toHaveBeenCalledTimes(1);
  });

  it("keeps a muted conversation unread without a native notification or chime", async () => {
    const visibility = vi.spyOn(document, "visibilityState", "get");
    visibility.mockReturnValue("hidden");
    mockFetchSidebarData.mockResolvedValue({
      currentUserId,
      channels: [
        { id: channelA, name: "A", type: "public", canWrite: true, muted: true },
        { id: channelB, name: "B", type: "private", canWrite: true, muted: false },
      ],
      dms: [{ id: dmC, type: "1:1", name: "C", participants: [], muted: false }],
    });
    const { result } = renderHook(() => useChatSidebar(), {
      wrapper: wrapper(`/chat/channel/${channelB}`),
    });
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() => websocket.onMessageCreated?.(messageCreated("native-muted", channelA)));

    expect(mockShowBrowserMessageNotification).not.toHaveBeenCalled();
    expect(mockPlayMessageSound).not.toHaveBeenCalled();
    expect(unreadCounts(result.current.state).channelA).toBe(1);
  });

  it("attempts a native notification at most once for a duplicate delivery", async () => {
    const visibility = vi.spyOn(document, "visibilityState", "get");
    visibility.mockReturnValue("hidden");
    const { result } = renderHook(() => useChatSidebar(), {
      wrapper: wrapper(`/chat/channel/${channelB}`),
    });
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    const event = messageCreated("native-dup", channelA);
    act(() => {
      websocket.onMessageCreated?.(event);
      websocket.onMessageCreated?.(event);
    });

    expect(mockShowBrowserMessageNotification).toHaveBeenCalledTimes(1);
  });

  it("does not attempt a native notification for the current user's own message", async () => {
    const visibility = vi.spyOn(document, "visibilityState", "get");
    visibility.mockReturnValue("hidden");
    const { result } = renderHook(() => useChatSidebar(), {
      wrapper: wrapper(`/chat/channel/${channelB}`),
    });
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() => websocket.onMessageCreated?.(messageCreated("native-own", channelA, currentUserId)));

    expect(mockShowBrowserMessageNotification).not.toHaveBeenCalled();
  });

  it("attempts a native notification for a backgrounded, currently-open DM mention (same rule as the chime)", async () => {
    const visibility = vi.spyOn(document, "visibilityState", "get");
    visibility.mockReturnValue("hidden");
    const { result } = renderHook(() => useChatSidebar(), {
      wrapper: wrapper(`/chat/dm/${dmC}`),
    });
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() =>
      websocket.onMessageCreated?.(messageCreated("native-active-dm", dmC, "other-1", "dm")),
    );

    expect(mockShowBrowserMessageNotification).toHaveBeenCalledTimes(1);
  });

  it("does not crash and still falls back to the chime when showBrowserMessageNotification throws", async () => {
    mockShowBrowserMessageNotification.mockImplementation(() => {
      throw new Error("native notification backend unavailable");
    });
    const visibility = vi.spyOn(document, "visibilityState", "get");
    visibility.mockReturnValue("hidden");
    const { result } = renderHook(() => useChatSidebar(), {
      wrapper: wrapper(`/chat/channel/${channelB}`),
    });
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    expect(() =>
      act(() => websocket.onMessageCreated?.(messageCreated("native-throws", channelA))),
    ).not.toThrow();

    expect(mockPlayMessageSound).toHaveBeenCalledTimes(1);
    expect(unreadCounts(result.current.state).channelA).toBe(1);
  });

  // The user may click the native notification long after it was shown, with
  // the sidebar's loaded list now stale (e.g. a brand-new DM). onNavigate
  // must both navigate and reconcile the sidebar against the server, exactly
  // like the sidebar's own "conversation just created" path already does.
  it("refreshes the sidebar when the notification's onNavigate is invoked, simulating a late click", async () => {
    mockShowBrowserMessageNotification.mockReturnValue({ shown: true });
    const visibility = vi.spyOn(document, "visibilityState", "get");
    visibility.mockReturnValue("hidden");
    const { result } = renderHook(() => useChatSidebar(), {
      wrapper: wrapper("/chat"),
    });
    await waitFor(() => expect(result.current.state.status).toBe("ready"));
    const fetchesBeforeClick = mockFetchSidebarData.mock.calls.length;

    act(() => websocket.onMessageCreated?.(messageCreated("native-late-click", channelA)));
    expect(unreadCounts(result.current.state).channelA).toBe(1);

    const { onNavigate } = mockShowBrowserMessageNotification.mock.calls[0][0];
    act(() => onNavigate(`/chat/channel/${channelA}`));

    // Real navigation happened, but that alone is not a read receipt (#492) —
    // the badge survives. What this proves is that refreshSidebar() fired: a
    // fresh fetch beyond the one triggered by the message event's own state
    // update.
    await waitFor(() =>
      expect(mockFetchSidebarData.mock.calls.length).toBeGreaterThan(fetchesBeforeClick),
    );
    expect(unreadCounts(result.current.state).channelA).toBe(1);

    // vi.restoreAllMocks() (afterEach, below) does not clear a plain
    // mockReturnValue on a vi.fn(), so leaving `shown: true` here would leak
    // into later describe blocks that assume the module-level default.
    mockShowBrowserMessageNotification.mockReturnValue({ shown: false });
  });

  // Repeated/rapid clicks (e.g. double-click, or a stale tag re-triggering)
  // must coalesce through the same refreshInFlight/refreshQueued mechanism
  // as onConversationAvailable, never firing one refetch per click.
  it("coalesces rapid repeated onNavigate invocations into at most two refetches", async () => {
    mockShowBrowserMessageNotification.mockReturnValue({ shown: true });
    const visibility = vi.spyOn(document, "visibilityState", "get");
    visibility.mockReturnValue("hidden");
    const { result } = renderHook(() => useChatSidebar(), {
      wrapper: wrapper("/chat"),
    });
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() => websocket.onMessageCreated?.(messageCreated("native-burst", channelA)));
    const { onNavigate } = mockShowBrowserMessageNotification.mock.calls[0][0];
    const fetchesBeforeClicks = mockFetchSidebarData.mock.calls.length;

    await act(async () => {
      for (let i = 0; i < 6; i++) onNavigate(`/chat/channel/${channelA}`);
    });

    await waitFor(() => expect(result.current.state.status).toBe("ready"));
    const triggered = mockFetchSidebarData.mock.calls.length - fetchesBeforeClicks;
    expect(triggered).toBeGreaterThan(0);
    expect(triggered).toBeLessThanOrEqual(2);

    // See the comment on the previous test: avoid leaking `shown: true` past
    // this describe block via vi.restoreAllMocks()'s no-op on mockReturnValue.
    mockShowBrowserMessageNotification.mockReturnValue({ shown: false });
  });
});

describe("useChatSidebar sound preference and DM/mention rules", () => {
  beforeEach(() => {
    websocket.onMessageCreated = null;
    mockPlayMessageSound.mockReset();
    mockGetSoundNotificationMode.mockReset();
    mockGetSoundNotificationMode.mockReturnValue("all");
    mockFetchSidebarData.mockResolvedValue({
      currentUserId,
      channels: [
        { id: channelA, name: "A", type: "public", canWrite: true },
        { id: channelB, name: "B", type: "private", canWrite: true },
      ],
      dms: [
        { id: dmC, type: "1:1", name: "C", participants: [] },
        { id: groupD, type: "group", name: "D", participants: [] },
      ],
    });
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("does not play any sound when the sound mode is 'off'", async () => {
    mockGetSoundNotificationMode.mockReturnValue("off");
    const { result } = renderHook(() => useChatSidebar(), {
      wrapper: wrapper(`/chat/channel/${channelB}`),
    });
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() => websocket.onMessageCreated?.(messageCreated("message-1", channelA)));

    expect(mockPlayMessageSound).not.toHaveBeenCalled();
  });

  it("still updates the unread badge when the sound mode is 'off'", async () => {
    mockGetSoundNotificationMode.mockReturnValue("off");
    const { result } = renderHook(() => useChatSidebar(), {
      wrapper: wrapper(`/chat/channel/${channelB}`),
    });
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() => websocket.onMessageCreated?.(messageCreated("message-1", channelA)));

    expect(unreadCounts(result.current.state).channelA).toBe(1);
  });

  it("plays a sound for a real @mention of the current user in a background channel", async () => {
    const { result } = renderHook(() => useChatSidebar(), {
      wrapper: wrapper(`/chat/channel/${channelB}`),
    });
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() =>
      websocket.onMessageCreated?.(
        messageCreated(
          "mention-1",
          channelA,
          "other-1",
          "channel",
          undefined,
          `oi ${mentionToken(currentUserId)} bora?`,
        ),
      ),
    );

    expect(mockPlayMessageSound).toHaveBeenCalledTimes(1);
  });

  it("does not give mention priority to a mention of a different user", async () => {
    const visibility = vi.spyOn(document, "visibilityState", "get");
    visibility.mockReturnValue("hidden");
    const { result } = renderHook(() => useChatSidebar(), {
      wrapper: wrapper(`/chat/channel/${channelA}`),
    });
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    // Active conversation, window unfocused: a real MENTION/DM would still
    // play here, but a mention of someone else is just STANDARD, which never
    // plays while its conversation is open (focused or not).
    act(() =>
      websocket.onMessageCreated?.(
        messageCreated(
          "mention-other",
          channelA,
          "other-1",
          "channel",
          undefined,
          `oi ${mentionToken(otherUserId)} bora?`,
        ),
      ),
    );

    expect(mockPlayMessageSound).not.toHaveBeenCalled();
  });

  it("does not play a sound for a mention inside the current user's own message", async () => {
    // Also stubbed hidden here: every other gate (focus, category) would say
    // "notify" for this event — only the own-message check should block both
    // the chime and a native notification attempt.
    const visibility = vi.spyOn(document, "visibilityState", "get");
    visibility.mockReturnValue("hidden");
    const { result } = renderHook(() => useChatSidebar(), {
      wrapper: wrapper(`/chat/channel/${channelB}`),
    });
    await waitFor(() => expect(result.current.state.status).toBe("ready"));
    // This block never otherwise asserts on the native-notification mock, so
    // its call history isn't cleared by any earlier beforeEach here — clear
    // it locally rather than asserting against whatever an unrelated,
    // untouched-until-now mock happens to carry over from block 5.
    mockShowBrowserMessageNotification.mockClear();

    act(() =>
      websocket.onMessageCreated?.(
        messageCreated(
          "mention-own",
          channelA,
          currentUserId,
          "channel",
          undefined,
          `oi ${mentionToken(currentUserId)}`,
        ),
      ),
    );

    expect(mockPlayMessageSound).not.toHaveBeenCalled();
    expect(mockShowBrowserMessageNotification).not.toHaveBeenCalled();
    expect(unreadCounts(result.current.state).channelA).toBe(0);
    if (result.current.state.status === "ready") {
      expect(
        result.current.state.channels.find((c) => c.id === channelA)?.hasMentionUnread,
      ).toBeFalsy();
    }
  });

  it("does not classify a system message as a mention even when it names the current user", async () => {
    const { result } = renderHook(() => useChatSidebar(), {
      wrapper: wrapper(`/chat/channel/${channelB}`),
    });
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() =>
      websocket.onMessageCreated?.(
        messageCreated(
          "system-1",
          channelA,
          "other-1",
          "channel",
          undefined,
          `${mentionToken(currentUserId)} entrou no canal`,
          "system",
        ),
      ),
    );

    expect(mockPlayMessageSound).not.toHaveBeenCalled();
  });

  it("plays a sound for a DM in the active conversation once the window loses focus", async () => {
    const visibility = vi.spyOn(document, "visibilityState", "get");
    visibility.mockReturnValue("visible");
    vi.spyOn(document, "hasFocus").mockReturnValue(true);
    const { result } = renderHook(() => useChatSidebar(), {
      wrapper: wrapper(`/chat/dm/${dmC}`),
    });
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    // Focused + active: no sound (unchanged from the existing DM-active test).
    act(() => websocket.onMessageCreated?.(messageCreated("dm-focused", dmC, "other-1", "dm")));
    expect(mockPlayMessageSound).not.toHaveBeenCalled();

    // Same active DM, window now in the background: DM priority still plays.
    visibility.mockReturnValue("hidden");
    act(() => websocket.onMessageCreated?.(messageCreated("dm-unfocused", dmC, "other-1", "dm")));
    expect(mockPlayMessageSound).toHaveBeenCalledTimes(1);
  });

  it("does not play a standard channel message in the active conversation even when the window is unfocused", async () => {
    const visibility = vi.spyOn(document, "visibilityState", "get");
    visibility.mockReturnValue("hidden");
    const { result } = renderHook(() => useChatSidebar(), {
      wrapper: wrapper(`/chat/channel/${channelA}`),
    });
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() => websocket.onMessageCreated?.(messageCreated("standard-unfocused", channelA)));

    expect(mockPlayMessageSound).not.toHaveBeenCalled();
  });

  // Combines the two rules directly above into one session: badge
  // suppression for the active conversation holds identically whether or not
  // sound actually fired — it isn't merely a byproduct of "sound stayed
  // silent". No existing test carries a channel-target mention through the
  // active+unfocused path (the DM variant is covered above at "plays a sound
  // for a DM in the active conversation...").
  it("keeps the active conversation's badge suppressed across a STANDARD-then-MENTION sequence, independent of the sound outcome", async () => {
    const visibility = vi.spyOn(document, "visibilityState", "get");
    visibility.mockReturnValue("hidden");
    const { result } = renderHook(() => useChatSidebar(), {
      wrapper: wrapper(`/chat/channel/${channelA}`),
    });
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() =>
      websocket.onMessageCreated?.(messageCreated("active-seq-standard", channelA, "other-1")),
    );
    expect(mockPlayMessageSound).not.toHaveBeenCalled();
    expect(unreadCounts(result.current.state).channelA).toBe(0);

    act(() =>
      websocket.onMessageCreated?.(
        messageCreated(
          "active-seq-mention",
          channelA,
          "other-1",
          "channel",
          undefined,
          `oi ${mentionToken(currentUserId)}`,
        ),
      ),
    );
    expect(mockPlayMessageSound).toHaveBeenCalledTimes(1);
    // Badge stays 0 even on the message that DID play.
    expect(unreadCounts(result.current.state).channelA).toBe(0);
    if (result.current.state.status === "ready") {
      expect(
        result.current.state.channels.find((c) => c.id === channelA)?.hasMentionUnread,
      ).toBeFalsy();
    }
  });

  it("does not crash when document.visibilityState is unavailable", async () => {
    const visibility = vi.spyOn(document, "visibilityState", "get");
    visibility.mockReturnValue(undefined as unknown as DocumentVisibilityState);
    const { result } = renderHook(() => useChatSidebar(), {
      wrapper: wrapper(`/chat/dm/${dmC}`),
    });
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    // Active DM, visibility unreadable: treated as "not focused" (safe default),
    // so a DM/MENTION still plays — the same branch exercised by the "hidden"
    // case above, just reached through an undefined read instead of a known one.
    expect(() =>
      act(() =>
        websocket.onMessageCreated?.(messageCreated("dm-no-visibility", dmC, "other-1", "dm")),
      ),
    ).not.toThrow();
    expect(mockPlayMessageSound).toHaveBeenCalledTimes(1);
  });

  it("plays only once for a DM message that also contains a mention of the current user", async () => {
    const { result } = renderHook(() => useChatSidebar(), {
      wrapper: wrapper(`/chat/channel/${channelB}`),
    });
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() =>
      websocket.onMessageCreated?.(
        messageCreated(
          "dm-mention-1",
          dmC,
          "other-1",
          "dm",
          undefined,
          `oi ${mentionToken(currentUserId)}`,
        ),
      ),
    );

    expect(mockPlayMessageSound).toHaveBeenCalledTimes(1);
  });

  // ── 'mentions' mode ──────────────────────────────────────────────────────

  it("in 'mentions' mode, does not play for a background standard channel message", async () => {
    mockGetSoundNotificationMode.mockReturnValue("mentions");
    const { result } = renderHook(() => useChatSidebar(), {
      wrapper: wrapper(`/chat/channel/${channelB}`),
    });
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() => websocket.onMessageCreated?.(messageCreated("standard-1", channelA)));

    expect(mockPlayMessageSound).not.toHaveBeenCalled();
  });

  it("in 'mentions' mode, still updates the unread badge for a plain message even without sound", async () => {
    mockGetSoundNotificationMode.mockReturnValue("mentions");
    const { result } = renderHook(() => useChatSidebar(), {
      wrapper: wrapper(`/chat/channel/${channelB}`),
    });
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() => websocket.onMessageCreated?.(messageCreated("standard-badge-mentions", channelA)));

    expect(mockPlayMessageSound).not.toHaveBeenCalled();
    if (result.current.state.status === "ready") {
      expect(result.current.state.channels.find((c) => c.id === channelA)?.unreadCount).toBe(1);
    }
  });

  it("in 'mentions' mode, does not play for a background DM without a mention", async () => {
    mockGetSoundNotificationMode.mockReturnValue("mentions");
    const { result } = renderHook(() => useChatSidebar(), {
      wrapper: wrapper(`/chat/channel/${channelB}`),
    });
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() => websocket.onMessageCreated?.(messageCreated("dm-plain", dmC, "other-1", "dm")));

    expect(mockPlayMessageSound).not.toHaveBeenCalled();
  });

  it("in 'mentions_and_dms' mode, plays for a background DM without a mention", async () => {
    mockGetSoundNotificationMode.mockReturnValue("mentions_and_dms");
    const { result } = renderHook(() => useChatSidebar(), {
      wrapper: wrapper(`/chat/channel/${channelB}`),
    });
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() => websocket.onMessageCreated?.(messageCreated("dm-mode4", dmC, "other-1", "dm")));

    expect(mockPlayMessageSound).toHaveBeenCalledTimes(1);
  });

  it("in 'mentions_and_dms' mode, does not play for a plain channel message", async () => {
    mockGetSoundNotificationMode.mockReturnValue("mentions_and_dms");
    const { result } = renderHook(() => useChatSidebar(), {
      wrapper: wrapper(`/chat/channel/${channelB}`),
    });
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() => websocket.onMessageCreated?.(messageCreated("standard-mode4", channelA)));

    expect(mockPlayMessageSound).not.toHaveBeenCalled();
  });

  it("in 'mentions_and_dms' mode, plays for a real @mention in a channel", async () => {
    mockGetSoundNotificationMode.mockReturnValue("mentions_and_dms");
    const { result } = renderHook(() => useChatSidebar(), {
      wrapper: wrapper(`/chat/channel/${channelB}`),
    });
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() =>
      websocket.onMessageCreated?.(
        messageCreated(
          "mention-mode4",
          channelA,
          "other-1",
          "channel",
          undefined,
          `oi ${mentionToken(currentUserId)}`,
        ),
      ),
    );

    expect(mockPlayMessageSound).toHaveBeenCalledTimes(1);
  });

  it("in 'mentions' mode, plays once for a DM that also contains a mention of the current user", async () => {
    mockGetSoundNotificationMode.mockReturnValue("mentions");
    const { result } = renderHook(() => useChatSidebar(), {
      wrapper: wrapper(`/chat/channel/${channelB}`),
    });
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() =>
      websocket.onMessageCreated?.(
        messageCreated(
          "dm-mention-2",
          dmC,
          "other-1",
          "dm",
          undefined,
          `oi ${mentionToken(currentUserId)}`,
        ),
      ),
    );

    expect(mockPlayMessageSound).toHaveBeenCalledTimes(1);
  });

  it("in 'mentions' mode, plays for a real @mention of the current user in a background channel", async () => {
    mockGetSoundNotificationMode.mockReturnValue("mentions");
    const { result } = renderHook(() => useChatSidebar(), {
      wrapper: wrapper(`/chat/channel/${channelB}`),
    });
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() =>
      websocket.onMessageCreated?.(
        messageCreated(
          "mention-in-mentions-mode",
          channelA,
          "other-1",
          "channel",
          undefined,
          `oi ${mentionToken(currentUserId)}`,
        ),
      ),
    );

    expect(mockPlayMessageSound).toHaveBeenCalledTimes(1);
  });

  it("in 'mentions' mode, plays for an @all broadcast in a background channel", async () => {
    mockGetSoundNotificationMode.mockReturnValue("mentions");
    const { result } = renderHook(() => useChatSidebar(), {
      wrapper: wrapper(`/chat/channel/${channelB}`),
    });
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() =>
      websocket.onMessageCreated?.(
        messageCreated("all-1", channelA, "other-1", "channel", undefined, "heads up @all"),
      ),
    );

    expect(mockPlayMessageSound).toHaveBeenCalledTimes(1);
  });

  it("in 'all' mode, an @all broadcast also plays (background channel)", async () => {
    const { result } = renderHook(() => useChatSidebar(), {
      wrapper: wrapper(`/chat/channel/${channelB}`),
    });
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() =>
      websocket.onMessageCreated?.(
        messageCreated("all-2", channelA, "other-1", "channel", undefined, "heads up @all"),
      ),
    );

    expect(mockPlayMessageSound).toHaveBeenCalledTimes(1);
  });

  // ── Mention badge (visual indicator, independent of sound mode) ─────────

  it("sets hasMentionUnread on the mentioned channel", async () => {
    const { result } = renderHook(() => useChatSidebar(), {
      wrapper: wrapper(`/chat/channel/${channelB}`),
    });
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() =>
      websocket.onMessageCreated?.(
        messageCreated(
          "badge-mention-1",
          channelA,
          "other-1",
          "channel",
          undefined,
          `oi ${mentionToken(currentUserId)}`,
        ),
      ),
    );

    if (result.current.state.status === "ready") {
      const channel = result.current.state.channels.find((c) => c.id === channelA);
      expect(channel?.hasMentionUnread).toBe(true);
      expect(channel?.unreadCount).toBe(1);
    }
  });

  it("does not set hasMentionUnread for a plain unread message", async () => {
    const { result } = renderHook(() => useChatSidebar(), {
      wrapper: wrapper(`/chat/channel/${channelB}`),
    });
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() => websocket.onMessageCreated?.(messageCreated("badge-plain-1", channelA)));

    if (result.current.state.status === "ready") {
      const channel = result.current.state.channels.find((c) => c.id === channelA);
      expect(channel?.hasMentionUnread).toBeFalsy();
      expect(channel?.unreadCount).toBe(1);
    }
  });

  it("keeps updating hasMentionUnread even when the sound mode is 'off'", async () => {
    mockGetSoundNotificationMode.mockReturnValue("off");
    const { result } = renderHook(() => useChatSidebar(), {
      wrapper: wrapper(`/chat/channel/${channelB}`),
    });
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() =>
      websocket.onMessageCreated?.(
        messageCreated(
          "badge-mention-off",
          channelA,
          "other-1",
          "channel",
          undefined,
          `oi ${mentionToken(currentUserId)}`,
        ),
      ),
    );

    expect(mockPlayMessageSound).not.toHaveBeenCalled();
    if (result.current.state.status === "ready") {
      expect(result.current.state.channels.find((c) => c.id === channelA)?.hasMentionUnread).toBe(
        true,
      );
    }
  });
});

describe("useChatSidebar sound mode changes at runtime", () => {
  beforeEach(() => {
    websocket.onMessageCreated = null;
    mockPlayMessageSound.mockReset();
    mockGetSoundNotificationMode.mockReset();
    mockGetSoundNotificationMode.mockReturnValue("all");
    mockFetchSidebarData.mockResolvedValue({
      currentUserId,
      channels: [{ id: channelA, name: "A", type: "public", canWrite: true }],
      dms: [],
    });
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("applies a preference change to the very next event, without remounting the hook", async () => {
    const { result } = renderHook(() => useChatSidebar(), {
      wrapper: wrapper(`/chat/channel/${channelB}`),
    });
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    // Starts in 'all': a plain background message plays.
    act(() => websocket.onMessageCreated?.(messageCreated("runtime-1", channelA)));
    expect(mockPlayMessageSound).toHaveBeenCalledTimes(1);

    // Switched to 'off' mid-session: the very next event must not play, with no
    // remount and no reset of the hook's dedup set in between — but the badge
    // keeps counting regardless of mute (muting is a sound-only concern).
    mockGetSoundNotificationMode.mockReturnValue("off");
    act(() => websocket.onMessageCreated?.(messageCreated("runtime-2", channelA)));
    expect(mockPlayMessageSound).toHaveBeenCalledTimes(1);
    expect(unreadCounts(result.current.state).channelA).toBe(2);

    // Switched back to 'all' mid-session: the very next eligible event plays
    // immediately, and the badge keeps counting correctly across the flip.
    mockGetSoundNotificationMode.mockReturnValue("all");
    act(() => websocket.onMessageCreated?.(messageCreated("runtime-2b", channelA)));
    expect(mockPlayMessageSound).toHaveBeenCalledTimes(2);
    expect(unreadCounts(result.current.state).channelA).toBe(3);

    // Switched to 'mentions': a plain message still doesn't play, but a real
    // @mention does — both take effect immediately, same hook instance.
    mockGetSoundNotificationMode.mockReturnValue("mentions");
    act(() => websocket.onMessageCreated?.(messageCreated("runtime-3", channelA)));
    expect(mockPlayMessageSound).toHaveBeenCalledTimes(2);

    act(() =>
      websocket.onMessageCreated?.(
        messageCreated(
          "runtime-4",
          channelA,
          "other-1",
          "channel",
          undefined,
          `oi ${mentionToken(currentUserId)}`,
        ),
      ),
    );
    expect(mockPlayMessageSound).toHaveBeenCalledTimes(3);

    // Replaying the very first (already-seen) message id after the mode
    // changes must stay deduplicated — a later preference change does not
    // reopen dedup for an event already processed.
    act(() => websocket.onMessageCreated?.(messageCreated("runtime-1", channelA)));
    expect(mockPlayMessageSound).toHaveBeenCalledTimes(3);
  });
});

// ── Activity reconciliation (issue #414) ─────────────────────────────────────
// The hook owns *when* a conversation was last written in. Where that puts it
// on screen is ChatSidebar's job and is asserted there.

describe("useChatSidebar — atividade da conversa", () => {
  const initial = {
    currentUserId,
    channels: [
      {
        id: channelA,
        name: "A",
        type: "public" as const,
        canWrite: true,
        createdAt: "2026-01-01T00:00:00Z",
        lastMessageAt: "2026-07-28T10:00:00Z",
      },
      {
        id: channelB,
        name: "B",
        type: "private" as const,
        canWrite: true,
        createdAt: "2026-02-01T00:00:00Z",
        lastMessageAt: null,
      },
    ],
    dms: [
      {
        id: dmC,
        type: "1:1" as const,
        name: "C",
        participants: [],
        createdAt: "2026-03-01T00:00:00Z",
        lastMessageAt: "2026-07-28T09:00:00Z",
      },
    ],
  };

  /** The same sidebar, with channelA's activity set to a given instant. */
  const withChannelActivity = (lastMessageAt: string) => ({
    ...initial,
    channels: [{ ...initial.channels[0]!, lastMessageAt }, initial.channels[1]!],
  });

  function activity(state: ReturnType<typeof useChatSidebar>["state"]) {
    if (state.status !== "ready") throw new Error("sidebar not ready");
    return {
      channelA: state.channels.find(({ id }) => id === channelA)?.lastMessageAt,
      channelB: state.channels.find(({ id }) => id === channelB)?.lastMessageAt,
      dmC: state.dms.find(({ id }) => id === dmC)?.lastMessageAt,
    };
  }

  beforeEach(() => {
    mockFetchSidebarData.mockReset();
    websocket.onMessageCreated = null;
    websocket.onConversationAvailable = null;
    mockFetchSidebarData.mockResolvedValue(initial);
  });

  it("adopts the server timestamp of a message the current user sent", async () => {
    const { result } = renderHook(() => useChatSidebar(), {
      wrapper: wrapper(`/chat/channel/${channelA}`),
    });
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() =>
      websocket.onMessageCreated?.(
        messageCreated("own-1", channelA, currentUserId, "channel", "2026-07-30T18:00:00Z"),
      ),
    );

    // The conversation moves even though the sender is the current user and the
    // conversation is the one on screen — neither is a reason to ignore a write.
    expect(activity(result.current.state).channelA).toBe("2026-07-30T18:00:00Z");
    // And still no unread badge for it, which is a separate question.
    expect(unreadCounts(result.current.state).channelA).toBe(0);
  });

  it("adopts the server timestamp of a message received from someone else", async () => {
    const { result } = renderHook(() => useChatSidebar(), { wrapper: wrapper("/chat") });
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() =>
      websocket.onMessageCreated?.(
        messageCreated("other-1", channelB, "other-user", "channel", "2026-07-30T18:00:00Z"),
      ),
    );

    // A conversation that had never been written in now has activity.
    expect(activity(result.current.state).channelB).toBe("2026-07-30T18:00:00Z");
  });

  it("does not let an out-of-order event move a conversation backwards", async () => {
    const { result } = renderHook(() => useChatSidebar(), { wrapper: wrapper("/chat") });
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() =>
      websocket.onMessageCreated?.(
        messageCreated("newer", channelA, "other-user", "channel", "2026-07-30T18:00:00Z"),
      ),
    );
    act(() =>
      websocket.onMessageCreated?.(
        messageCreated("older", channelA, "other-user", "channel", "2026-07-29T08:00:00Z"),
      ),
    );

    expect(activity(result.current.state).channelA).toBe("2026-07-30T18:00:00Z");
  });

  it("is idempotent for a repeated event and never duplicates the row", async () => {
    const { result } = renderHook(() => useChatSidebar(), { wrapper: wrapper("/chat") });
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    const event = messageCreated(
      "echo-1",
      channelA,
      currentUserId,
      "channel",
      "2026-07-30T18:00:00Z",
    );
    act(() => {
      websocket.onMessageCreated?.(event);
      websocket.onMessageCreated?.(event);
      websocket.onMessageCreated?.(event);
    });

    if (result.current.state.status !== "ready") throw new Error("not ready");
    expect(activity(result.current.state).channelA).toBe("2026-07-30T18:00:00Z");
    expect(result.current.state.channels.filter(({ id }) => id === channelA)).toHaveLength(1);
  });

  it("touches only the conversation the event names", async () => {
    const { result } = renderHook(() => useChatSidebar(), { wrapper: wrapper("/chat") });
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() =>
      websocket.onMessageCreated?.(
        messageCreated("ch", channelA, "other-user", "channel", "2026-07-30T18:00:00Z"),
      ),
    );
    // A channel event leaves every DM exactly where it was.
    expect(activity(result.current.state).dmC).toBe("2026-07-28T09:00:00Z");
    expect(activity(result.current.state).channelB).toBeNull();

    act(() =>
      websocket.onMessageCreated?.(
        messageCreated("dm", dmC, "other-user", "dm", "2026-07-31T18:00:00Z"),
      ),
    );
    // And a DM event leaves every channel where it was.
    expect(activity(result.current.state)).toEqual({
      channelA: "2026-07-30T18:00:00Z",
      channelB: null,
      dmC: "2026-07-31T18:00:00Z",
    });
  });

  it("builds no row from an event for a conversation it does not have", async () => {
    const { result } = renderHook(() => useChatSidebar(), { wrapper: wrapper("/chat") });
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() =>
      websocket.onMessageCreated?.(
        messageCreated("ghost", "44444444-4444-4444-8444-444444444444", "other-user"),
      ),
    );

    if (result.current.state.status !== "ready") throw new Error("not ready");
    expect(result.current.state.channels.map(({ id }) => id)).toEqual([channelA, channelB]);
    expect(result.current.state.dms.map(({ id }) => id)).toEqual([dmC]);
  });

  it("asks the server instead of guessing when the event carries no timestamp", async () => {
    const { result } = renderHook(() => useChatSidebar(), { wrapper: wrapper("/chat") });
    await waitFor(() => expect(result.current.state.status).toBe("ready"));
    const afterMount = mockFetchSidebarData.mock.calls.length;

    await act(async () => {
      websocket.onMessageCreated?.(routeOnlyMessageCreated("route-only", channelA));
    });

    await waitFor(() => expect(mockFetchSidebarData.mock.calls.length).toBeGreaterThan(afterMount));
    // Nothing was invented for the conversation in the meantime.
    expect(activity(result.current.state).channelA).toBe("2026-07-28T10:00:00Z");
  });

  it("does not let a stale refetch undo activity that arrived while it was in flight", async () => {
    const { result } = renderHook(() => useChatSidebar(), { wrapper: wrapper("/chat") });
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    // A refetch starts, then a newer event arrives, then the older response lands.
    const inFlight = deferredValue<typeof initial>();
    mockFetchSidebarData.mockReturnValueOnce(inFlight.promise);
    act(() => {
      websocket.onConversationAvailable?.();
    });
    act(() =>
      websocket.onMessageCreated?.(
        messageCreated("during", channelA, "other-user", "channel", "2026-07-31T20:00:00Z"),
      ),
    );
    await act(async () => {
      inFlight.resolve(initial);
      await inFlight.promise;
    });

    expect(activity(result.current.state).channelA).toBe("2026-07-31T20:00:00Z");
  });

  it("takes membership from the refetch even while keeping the newer activity", async () => {
    const { result } = renderHook(() => useChatSidebar(), { wrapper: wrapper("/chat") });
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() =>
      websocket.onMessageCreated?.(
        messageCreated("before", channelA, "other-user", "channel", "2026-07-31T20:00:00Z"),
      ),
    );

    // The server no longer lists channelB (access revoked) and now lists a new
    // conversation. Local state must not preserve the one nor miss the other.
    const channelD = "55555555-5555-4555-8555-555555555555";
    mockFetchSidebarData.mockResolvedValue({
      ...initial,
      channels: [
        initial.channels[0]!,
        {
          id: channelD,
          name: "D",
          type: "public" as const,
          canWrite: true,
          createdAt: "2026-07-20T00:00:00Z",
          lastMessageAt: null,
        },
      ],
    });
    await act(async () => {
      websocket.onConversationAvailable?.();
    });

    await waitFor(() => {
      if (result.current.state.status !== "ready") throw new Error("not ready");
      expect(result.current.state.channels.map(({ id }) => id)).toEqual([channelA, channelD]);
    });
    expect(activity(result.current.state).channelA).toBe("2026-07-31T20:00:00Z");
  });

  it("takes the server's word again after a full reload", async () => {
    const { result } = renderHook(() => useChatSidebar(), { wrapper: wrapper("/chat") });
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() =>
      websocket.onMessageCreated?.(
        messageCreated("local", channelA, "other-user", "channel", "2026-07-31T20:00:00Z"),
      ),
    );
    expect(activity(result.current.state).channelA).toBe("2026-07-31T20:00:00Z");

    // retry() is the reload path: it clears the list first, so the persisted
    // state is what comes back — the same order a browser reload would show.
    await act(async () => result.current.retry());

    expect(activity(result.current.state).channelA).toBe("2026-07-28T10:00:00Z");
  });

  // ── Sub-millisecond precision ───────────────────────────────────────────────
  // chat.messages.created_at holds microseconds, and both the sidebar payload
  // and the WebSocket event publish them. The merge decides "is this newer?",
  // so it has to see the whole value and not the millisecond it rounds to.

  it("promotes a conversation when the event is newer only in microseconds", async () => {
    mockFetchSidebarData.mockResolvedValue(withChannelActivity("2026-08-04T12:00:00.900045Z"));
    const { result } = renderHook(() => useChatSidebar(), { wrapper: wrapper("/chat") });
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() =>
      websocket.onMessageCreated?.(
        messageCreated("micro", channelA, "other-user", "channel", "2026-08-04T12:00:00.900123Z"),
      ),
    );

    expect(activity(result.current.state).channelA).toBe("2026-08-04T12:00:00.900123Z");
  });

  it("does not regress on an event older within the same millisecond", async () => {
    mockFetchSidebarData.mockResolvedValue(withChannelActivity("2026-08-04T12:00:00.900123Z"));
    const { result } = renderHook(() => useChatSidebar(), { wrapper: wrapper("/chat") });
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() =>
      websocket.onMessageCreated?.(
        messageCreated("stale", channelA, "other-user", "channel", "2026-08-04T12:00:00.900045Z"),
      ),
    );

    expect(activity(result.current.state).channelA).toBe("2026-08-04T12:00:00.900123Z");
  });

  it("does not let a less precise stale refetch undo a newer event", async () => {
    // The response was computed before the event and carries the previous
    // activity, truncated by a server that had not yet been corrected.
    mockFetchSidebarData.mockResolvedValue(withChannelActivity("2026-08-04T12:00:00.900045Z"));
    const { result } = renderHook(() => useChatSidebar(), { wrapper: wrapper("/chat") });
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    const inFlight = deferredValue<ReturnType<typeof withChannelActivity>>();
    mockFetchSidebarData.mockReturnValueOnce(inFlight.promise);
    act(() => {
      websocket.onConversationAvailable?.();
    });
    act(() =>
      websocket.onMessageCreated?.(
        messageCreated("during", channelA, "other-user", "channel", "2026-08-04T12:00:00.900123Z"),
      ),
    );
    await act(async () => {
      inFlight.resolve(withChannelActivity("2026-08-04T12:00:00Z"));
      await inFlight.promise;
    });

    expect(activity(result.current.state).channelA).toBe("2026-08-04T12:00:00.900123Z");
  });

  it("reports no change when a refetch restates the same instant differently", async () => {
    mockFetchSidebarData.mockResolvedValue(withChannelActivity("2026-08-04T12:00:00.1Z"));
    const { result } = renderHook(() => useChatSidebar(), { wrapper: wrapper("/chat") });
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    // .1, .100000000 and the same moment at -03:00 are one instant. The
    // notation the server sends is adopted; the instant it denotes does not
    // move, so nothing about the ordering changes.
    const before = parseInstant(activity(result.current.state).channelA);
    for (const equivalent of ["2026-08-04T12:00:00.100000000Z", "2026-08-04T09:00:00.100-03:00"]) {
      mockFetchSidebarData.mockResolvedValue(withChannelActivity(equivalent));
      await act(async () => {
        websocket.onConversationAvailable?.();
      });
      await waitFor(() => expect(result.current.state.status).toBe("ready"));
      expect(parseInstant(activity(result.current.state).channelA)).toEqual(before);
    }

    // And an event at that same instant is still not newer than what is held.
    act(() =>
      websocket.onMessageCreated?.(
        messageCreated("same", channelA, "other-user", "channel", "2026-08-04T12:00:00.1Z"),
      ),
    );
    expect(parseInstant(activity(result.current.state).channelA)).toEqual(before);
  });

  it("reproduces the realtime order after a reload of the persisted state", async () => {
    mockFetchSidebarData.mockResolvedValue(withChannelActivity("2026-08-04T12:00:00.900045Z"));
    const { result } = renderHook(() => useChatSidebar(), { wrapper: wrapper("/chat") });
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() =>
      websocket.onMessageCreated?.(
        messageCreated("micro", channelA, "other-user", "channel", "2026-08-04T12:00:00.900123Z"),
      ),
    );
    const afterRealtime = activity(result.current.state).channelA;

    // What the server now persists is exactly what the event reported, so the
    // reload has to land on the same value — and therefore the same order.
    mockFetchSidebarData.mockResolvedValue(withChannelActivity("2026-08-04T12:00:00.900123Z"));
    await act(async () => result.current.retry());

    expect(activity(result.current.state).channelA).toBe(afterRealtime);
  });
});

// ── conversation.available (issue #398) ─────────────────────────────────────

describe("useChatSidebar — conversa recém-disponível", () => {
  it("refetches the sidebar and shows the new conversation", async () => {
    const withoutB = {
      currentUserId,
      channels: [{ id: channelA, name: "A", type: "public" as const, canWrite: true }],
      dms: [],
    };
    const withB = {
      currentUserId,
      channels: [
        { id: channelA, name: "A", type: "public" as const, canWrite: true },
        { id: channelB, name: "B", type: "private" as const, canWrite: true },
      ],
      dms: [],
    };
    mockFetchSidebarData.mockResolvedValueOnce(withoutB).mockResolvedValue(withB);

    const { result } = renderHook(() => useChatSidebar(), { wrapper: wrapper("/chat") });
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    await act(async () => {
      websocket.onConversationAvailable?.();
    });

    await waitFor(() => {
      if (result.current.state.status !== "ready") throw new Error("not ready");
      expect(result.current.state.channels.map((c) => c.id)).toEqual([channelA, channelB]);
    });
  });

  // The refetch replaces the list wholesale, so repeated events cannot duplicate.
  it("does not duplicate conversations across repeated events", async () => {
    const data = {
      currentUserId,
      channels: [{ id: channelA, name: "A", type: "public" as const, canWrite: true }],
      dms: [{ id: dmC, type: "1:1" as const, name: "C", participants: [] }],
    };
    mockFetchSidebarData.mockResolvedValue(data);

    const { result } = renderHook(() => useChatSidebar(), { wrapper: wrapper("/chat") });
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    await act(async () => {
      websocket.onConversationAvailable?.();
      websocket.onConversationAvailable?.();
      websocket.onConversationAvailable?.();
    });

    await waitFor(() => {
      if (result.current.state.status !== "ready") throw new Error("not ready");
      expect(result.current.state.channels).toHaveLength(1);
      expect(result.current.state.dms).toHaveLength(1);
    });
  });

  // A burst must not start one refetch per event: an in-flight request absorbs
  // the others and runs at most once more.
  it("coalesces a burst of events into at most two refetches", async () => {
    mockFetchSidebarData.mockResolvedValue({
      currentUserId,
      channels: [{ id: channelA, name: "A", type: "public" as const, canWrite: true }],
      dms: [],
    });

    const { result } = renderHook(() => useChatSidebar(), { wrapper: wrapper("/chat") });
    await waitFor(() => expect(result.current.state.status).toBe("ready"));
    const afterMount = mockFetchSidebarData.mock.calls.length;

    await act(async () => {
      for (let i = 0; i < 6; i++) websocket.onConversationAvailable?.();
    });

    await waitFor(() => expect(result.current.state.status).toBe("ready"));
    const triggered = mockFetchSidebarData.mock.calls.length - afterMount;
    expect(triggered).toBeGreaterThan(0);
    expect(triggered).toBeLessThanOrEqual(2);
  });

  // A failed hint must not blank a working sidebar or start a retry loop.
  it("keeps the current sidebar when the refetch fails", async () => {
    const data = {
      currentUserId,
      channels: [{ id: channelA, name: "A", type: "public" as const, canWrite: true }],
      dms: [],
    };
    mockFetchSidebarData.mockResolvedValueOnce(data).mockRejectedValue(new Error("offline"));

    const { result } = renderHook(() => useChatSidebar(), { wrapper: wrapper("/chat") });
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    await act(async () => {
      websocket.onConversationAvailable?.();
    });

    await waitFor(() => {
      if (result.current.state.status !== "ready") throw new Error("state was blanked");
      expect(result.current.state.channels).toHaveLength(1);
    });
  });

  // The refetch must not flip the sidebar back through "loading", which would
  // unmount the rendered list for a frame.
  it("never returns to the loading state while refreshing", async () => {
    mockFetchSidebarData.mockResolvedValue({
      currentUserId,
      channels: [{ id: channelA, name: "A", type: "public" as const, canWrite: true }],
      dms: [],
    });

    const { result } = renderHook(() => useChatSidebar(), { wrapper: wrapper("/chat") });
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    const seen: string[] = [];
    await act(async () => {
      websocket.onConversationAvailable?.();
      seen.push(result.current.state.status);
    });

    expect(seen).not.toContain("loading");
  });
});

// ── Badge persistence ─────────────────────────────────────────────────────────
// The gates covered above (own message, active conversation, dedup, sound
// preference) are unaffected by persistence — mergeUnread only changes what a
// "loaded" dispatch does with unread/mention fields, never message_created or
// target_opened. These tests cover the persistence contract itself.
describe("useChatSidebar — badge persistence", () => {
  const workspaceId = "workspace-1";
  const otherWorkspaceId = "workspace-2";

  beforeEach(() => {
    websocket.onMessageCreated = null;
    websocket.onConversationAvailable = null;
    mockFetchSidebarData.mockReset();
    mockMarkConversationRead.mockReset();
    mockPlayMessageSound.mockReset();
    mockShowBrowserMessageNotification.mockReset();
    mockShowBrowserMessageNotification.mockReturnValue({ shown: false });
  });

  it("uses the server unread count over a different persisted value", async () => {
    savePersistedUnread(currentUserId, workspaceId, [
      { id: channelA, type: "channel", unreadCount: 9, hasMentionUnread: true },
    ]);
    mockFetchSidebarData.mockResolvedValue({
      currentUserId,
      workspaceId,
      channels: [{ id: channelA, name: "A", type: "public", canWrite: true, unreadCount: 2 }],
      dms: [],
    });

    const { result } = renderHook(() => useChatSidebar(), { wrapper: wrapper("/chat") });
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    expect(unreadCounts(result.current.state).channelA).toBe(2);
    expect(
      result.current.state.status === "ready" && result.current.state.channels[0].hasMentionUnread,
    ).toBe(true);
  });

  it("lets a lower server count replace the current tab count on refresh", async () => {
    mockFetchSidebarData
      .mockResolvedValueOnce({
        currentUserId,
        workspaceId,
        channels: [{ id: channelA, name: "A", type: "public", canWrite: true, unreadCount: 4 }],
        dms: [],
      })
      .mockResolvedValueOnce({
        currentUserId,
        workspaceId,
        channels: [{ id: channelA, name: "A", type: "public", canWrite: true, unreadCount: 1 }],
        dms: [],
      });
    const { result } = renderHook(() => useChatSidebar(), { wrapper: wrapper("/chat") });
    await waitFor(() => expect(unreadCounts(result.current.state).channelA).toBe(4));

    await act(async () => websocket.onConversationAvailable?.());
    await waitFor(() => expect(unreadCounts(result.current.state).channelA).toBe(1));
  });

  // #492: opening a conversation's route is a navigation event, not a read
  // receipt. Marking read now happens only once ChatMessageArea has evidence
  // the user reached the real bottom, via the same markRead() below — never
  // automatically from the route alone.
  it("does not mark a conversation read just because its route opened", async () => {
    mockFetchSidebarData.mockResolvedValue({
      currentUserId,
      workspaceId,
      channels: [{ id: channelA, name: "A", type: "public", canWrite: true, unreadCount: 3 }],
      dms: [],
    });

    const { result } = renderHook(() => useChatSidebar(), {
      wrapper: wrapper(`/chat/channel/${channelA}`),
    });
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    expect(mockMarkConversationRead).not.toHaveBeenCalled();
    expect(unreadCounts(result.current.state).channelA).toBe(3);
  });

  afterEach(() => {
    localStorage.clear();
    vi.restoreAllMocks();
  });

  it("hydrates unread and mention state from localStorage on a fresh mount — the same mechanism behind refresh, browser reopen, remount and re-login by the same user, all of which start with no in-memory previous state", async () => {
    savePersistedUnread(currentUserId, workspaceId, [
      { id: channelA, type: "channel", unreadCount: 2, hasMentionUnread: true },
    ]);
    mockFetchSidebarData.mockResolvedValue({
      currentUserId,
      workspaceId,
      channels: [{ id: channelA, name: "A", type: "public", canWrite: true }],
      dms: [],
    });

    const { result } = renderHook(() => useChatSidebar(), { wrapper: wrapper("/chat") });
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    if (result.current.state.status === "ready") {
      const channel = result.current.state.channels.find((c) => c.id === channelA);
      expect(channel?.unreadCount).toBe(2);
      expect(channel?.hasMentionUnread).toBe(true);
    }
  });

  it("keeps a live unread count when an older backend omits unread_count during refresh", async () => {
    mockFetchSidebarData.mockResolvedValue({
      currentUserId,
      workspaceId,
      channels: [{ id: channelA, name: "A", type: "public", canWrite: true }],
      dms: [],
    });

    const { result } = renderHook(() => useChatSidebar(), {
      wrapper: wrapper(`/chat/channel/${channelB}`),
    });
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() => websocket.onMessageCreated?.(messageCreated("live-1", channelA)));
    expect(unreadCounts(result.current.state).channelA).toBe(1);

    await act(async () => {
      websocket.onConversationAvailable?.();
    });
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    expect(unreadCounts(result.current.state).channelA).toBe(1);
  });

  it("does not resurrect a conversation's badge after it was marked read, even on a fresh remount", async () => {
    mockFetchSidebarData.mockResolvedValue({
      currentUserId,
      workspaceId,
      channels: [{ id: channelA, name: "A", type: "public", canWrite: true }],
      dms: [],
    });

    const { Wrapper, navigateRef } = navigableWrapper("/chat");
    const { result, unmount } = renderHook(() => useChatSidebar(), { wrapper: Wrapper });
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() => websocket.onMessageCreated?.(messageCreated("to-be-read", channelA)));
    expect(unreadCounts(result.current.state).channelA).toBe(1);

    // #492: opening the route no longer marks it read — mark it explicitly,
    // the same way ChatMessageArea will once it confirms the real bottom.
    act(() => navigateRef.current(`/chat/channel/${channelA}`));
    act(() => result.current.markRead({ kind: "channel", targetId: channelA }));
    await waitFor(() => expect(unreadCounts(result.current.state).channelA).toBe(0));
    unmount();

    const { result: remounted } = renderHook(() => useChatSidebar(), { wrapper: wrapper("/chat") });
    await waitFor(() => expect(remounted.current.state.status).toBe("ready"));
    expect(unreadCounts(remounted.current.state).channelA).toBe(0);
  });

  it("keeps two different users' badges isolated within the same workspace", async () => {
    savePersistedUnread(currentUserId, workspaceId, [
      { id: channelA, type: "channel", unreadCount: 3, hasMentionUnread: false },
    ]);

    mockFetchSidebarData.mockResolvedValue({
      currentUserId: otherUserId,
      workspaceId,
      channels: [{ id: channelA, name: "A", type: "public", canWrite: true }],
      dms: [],
    });
    const { result: asOtherUser } = renderHook(() => useChatSidebar(), {
      wrapper: wrapper("/chat"),
    });
    await waitFor(() => expect(asOtherUser.current.state.status).toBe("ready"));
    expect(unreadCounts(asOtherUser.current.state).channelA).toBe(0);

    mockFetchSidebarData.mockResolvedValue({
      currentUserId,
      workspaceId,
      channels: [{ id: channelA, name: "A", type: "public", canWrite: true }],
      dms: [],
    });
    const { result: asOriginalUser } = renderHook(() => useChatSidebar(), {
      wrapper: wrapper("/chat"),
    });
    await waitFor(() => expect(asOriginalUser.current.state.status).toBe("ready"));
    expect(unreadCounts(asOriginalUser.current.state).channelA).toBe(3);
  });

  it("keeps the same user's badges isolated between two different workspaces", async () => {
    savePersistedUnread(currentUserId, workspaceId, [
      { id: channelA, type: "channel", unreadCount: 4, hasMentionUnread: false },
    ]);

    mockFetchSidebarData.mockResolvedValue({
      currentUserId,
      workspaceId: otherWorkspaceId,
      channels: [{ id: channelA, name: "A", type: "public", canWrite: true }],
      dms: [],
    });
    const { result: inOtherWorkspace } = renderHook(() => useChatSidebar(), {
      wrapper: wrapper("/chat"),
    });
    await waitFor(() => expect(inOtherWorkspace.current.state.status).toBe("ready"));
    expect(unreadCounts(inOtherWorkspace.current.state).channelA).toBe(0);

    mockFetchSidebarData.mockResolvedValue({
      currentUserId,
      workspaceId,
      channels: [{ id: channelA, name: "A", type: "public", canWrite: true }],
      dms: [],
    });
    const { result: inOriginalWorkspace } = renderHook(() => useChatSidebar(), {
      wrapper: wrapper("/chat"),
    });
    await waitFor(() => expect(inOriginalWorkspace.current.state.status).toBe("ready"));
    expect(unreadCounts(inOriginalWorkspace.current.state).channelA).toBe(4);
  });

  it("never plays a sound or attempts a native notification while hydrating persisted badges", async () => {
    savePersistedUnread(currentUserId, workspaceId, [
      { id: channelA, type: "channel", unreadCount: 5, hasMentionUnread: true },
    ]);
    mockFetchSidebarData.mockResolvedValue({
      currentUserId,
      workspaceId,
      channels: [{ id: channelA, name: "A", type: "public", canWrite: true }],
      dms: [],
    });

    const { result } = renderHook(() => useChatSidebar(), { wrapper: wrapper("/chat") });
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    expect(unreadCounts(result.current.state).channelA).toBe(5);
    expect(mockPlayMessageSound).not.toHaveBeenCalled();
    expect(mockShowBrowserMessageNotification).not.toHaveBeenCalled();
  });

  it("does not crash on a corrupted localStorage payload and falls back to the server's counts", async () => {
    // Mirrors sidebarUnreadPersistence.ts's own key format — that module
    // already proves loadPersistedUnread() never throws on this; this test
    // proves the hook mounts correctly end-to-end when it doesn't.
    localStorage.setItem(`nchat.sidebar.unread.v1:${workspaceId}:${currentUserId}`, "{not json");
    mockFetchSidebarData.mockResolvedValue({
      currentUserId,
      workspaceId,
      channels: [{ id: channelA, name: "A", type: "public", canWrite: true }],
      dms: [],
    });

    const { result } = renderHook(() => useChatSidebar(), { wrapper: wrapper("/chat") });
    await waitFor(() => expect(result.current.state.status).toBe("ready"));
    expect(unreadCounts(result.current.state).channelA).toBe(0);
  });
});

// ── Menu actions (ISSUE #527) ────────────────────────────────────────────────
//
// Marking read and renaming are the two actions the row menu adds. Both must
// converge on the same canonical state every other source uses, and neither may
// invent a rule of its own.

describe("useChatSidebar — ações do menu de conversa", () => {
  const channel = (overrides: Record<string, unknown> = {}) => ({
    id: channelA,
    name: "infra",
    type: "public",
    canWrite: true,
    canRename: true,
    ...overrides,
  });

  beforeEach(() => {
    mockFetchSidebarData.mockReset();
    mockMarkConversationRead.mockReset();
    mockRenameChannel.mockReset();
    websocket.onConversationUpdated = null;
    mockMarkConversationRead.mockResolvedValue(undefined);
    mockFetchSidebarData.mockResolvedValue({
      currentUserId,
      workspaceId: "workspace-1",
      channels: [channel({ unreadCount: 5 })],
      dms: [{ id: dmC, type: "1:1", name: "Juliane", participants: [], unreadCount: 3 }],
    });
  });

  it("clears the badge and sends the receipt without navigating", async () => {
    const { result } = renderHook(() => useChatSidebar(), { wrapper: wrapper("/chat") });
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() => result.current.markRead({ kind: "channel", targetId: channelA }));

    expect(unreadCounts(result.current.state).channelA).toBe(0);
    // The conversation that was not named keeps its badge.
    expect(unreadCounts(result.current.state).dmC).toBe(3);
    expect(mockMarkConversationRead).toHaveBeenCalledWith("channel", channelA);
  });

  // A failed receipt is not a UI failure: the badge stays cleared locally and
  // the next refetch reconciles, exactly as on navigation.
  it("survives a failed read receipt", async () => {
    mockMarkConversationRead.mockRejectedValue(new Error("offline"));
    const { result } = renderHook(() => useChatSidebar(), { wrapper: wrapper("/chat") });
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    await act(async () => {
      result.current.markRead({ kind: "channel", targetId: channelA });
    });

    expect(result.current.state.status).toBe("ready");
    expect(unreadCounts(result.current.state).channelA).toBe(0);
  });

  // No optimistic rename: a name the server never accepted must not appear, so
  // the new one arrives only through the canonical refetch.
  it("converges on the persisted name through a refetch, keeping the same row", async () => {
    mockRenameChannel.mockResolvedValue({ id: channelA, name: "Plataforma" });
    const { result } = renderHook(() => useChatSidebar(), { wrapper: wrapper("/chat") });
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    mockFetchSidebarData.mockResolvedValue({
      currentUserId,
      workspaceId: "workspace-1",
      channels: [channel({ name: "Plataforma", unreadCount: 5, pinnedAt: "2026-08-12T10:00:00Z" })],
      dms: [{ id: dmC, type: "1:1", name: "Juliane", participants: [], unreadCount: 3 }],
    });

    await act(async () => {
      await result.current.renameChannel(channelA, "Plataforma");
    });

    expect(mockRenameChannel).toHaveBeenCalledWith(channelA, "Plataforma");
    await waitFor(() => {
      if (result.current.state.status !== "ready") throw new Error("not ready");
      expect(result.current.state.channels).toHaveLength(1);
      expect(result.current.state.channels[0]).toMatchObject({
        id: channelA,
        name: "Plataforma",
        pinnedAt: "2026-08-12T10:00:00Z",
      });
    });
  });

  // The dialog needs the failure to stay open and recoverable.
  it("propagates a refused rename and leaves the list untouched", async () => {
    mockRenameChannel.mockRejectedValue(new Error("forbidden"));
    const { result } = renderHook(() => useChatSidebar(), { wrapper: wrapper("/chat") });
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    await act(async () => {
      await expect(result.current.renameChannel(channelA, "Plataforma")).rejects.toThrow(
        "forbidden",
      );
    });

    if (result.current.state.status !== "ready") throw new Error("not ready");
    expect(result.current.state.channels[0]).toMatchObject({ id: channelA, name: "infra" });
  });

  // Someone else renamed the channel. The event names it and nothing else, so
  // the only correct response is the canonical refetch.
  it("refetches when a conversation.updated event arrives, without duplicating the row", async () => {
    const { result } = renderHook(() => useChatSidebar(), { wrapper: wrapper("/chat") });
    await waitFor(() => expect(result.current.state.status).toBe("ready"));
    expect(websocket.onConversationUpdated).toBeTypeOf("function");

    mockFetchSidebarData.mockResolvedValue({
      currentUserId,
      workspaceId: "workspace-1",
      channels: [channel({ name: "Plataforma", unreadCount: 5 })],
      dms: [{ id: dmC, type: "1:1", name: "Juliane", participants: [], unreadCount: 3 }],
    });

    // Twice: a repeated event must be indistinguishable from one.
    await act(async () => {
      websocket.onConversationUpdated?.();
      websocket.onConversationUpdated?.();
      await Promise.resolve();
    });

    await waitFor(() => {
      if (result.current.state.status !== "ready") throw new Error("not ready");
      expect(result.current.state.channels).toHaveLength(1);
      expect(result.current.state.channels[0]).toMatchObject({ id: channelA, name: "Plataforma" });
      expect(unreadCounts(result.current.state).channelA).toBe(5);
    });
  });
});

// ── System messages must not become mentions or chimes (issue #527) ──────────
//
// A rename or a departure arrives as `conversation.event`, never as
// `message.created`, so it structurally cannot reach the mention classifier or
// the sound/notification path — both of which live in the message.created
// handler. These pin that down.

describe("useChatSidebar — system messages", () => {
  beforeEach(() => {
    mockFetchSidebarData.mockReset();
    mockPlayMessageSound.mockReset();
    mockShowBrowserMessageNotification.mockReset();
    mockShowBrowserMessageNotification.mockReturnValue({ shown: false });
    websocket.onConversationEvent = null;
    mockFetchSidebarData.mockResolvedValue({
      currentUserId,
      workspaceId: "workspace-1",
      channels: [
        {
          id: channelA,
          name: "infra",
          type: "public",
          canWrite: true,
          unreadCount: 2,
          hasMentionUnread: false,
        },
      ],
      dms: [],
    });
  });

  it("never raises a mention, a chime or a notification for a conversation event", async () => {
    const { result } = renderHook(() => useChatSidebar(), { wrapper: wrapper("/chat") });
    await waitFor(() => expect(result.current.state.status).toBe("ready"));
    expect(websocket.onConversationEvent).toBeTypeOf("function");

    await act(async () => {
      websocket.onConversationEvent?.();
      await Promise.resolve();
    });

    if (result.current.state.status !== "ready") throw new Error("not ready");
    const channel = result.current.state.channels[0];
    // The mention flag is untouched: only message.created can set it, and a
    // system event is not one.
    expect(channel?.hasMentionUnread).toBe(false);
    expect(mockPlayMessageSound).not.toHaveBeenCalled();
    expect(mockShowBrowserMessageNotification).not.toHaveBeenCalled();
  });
  // ── Leaving the conversation on screen (issue #527, code review) ───────────
  //
  // Leaving is the one action that can remove the conversation the reader is
  // looking at. The row disappearing is not enough: the route still names a
  // conversation this user is no longer a member of, and everything downstream
  // of it — the message list, the details panel — would keep asking for it.
  describe("leaving the open conversation", () => {
    beforeEach(() => {
      mockLeaveConversation.mockResolvedValue(undefined);
      mockFetchSidebarData.mockResolvedValue({
        currentUserId,
        channels: [{ id: channelA, name: "A", type: "public", canWrite: true }],
        dms: [{ id: groupD, name: "Squad", type: "group", isGroup: true }],
      });
    });

    it("returns to the neutral route after leaving the channel being read", async () => {
      const { Wrapper, pathnameRef } = routedWrapper(`/chat/channel/${channelA}`);
      const { result } = renderHook(() => useChatSidebar(), { wrapper: Wrapper });
      await waitFor(() => expect(result.current.state.status).toBe("ready"));

      await act(async () => {
        await result.current.leaveConversation({ kind: "channel", targetId: channelA });
      });

      expect(mockLeaveConversation).toHaveBeenCalledWith("channel", channelA);
      expect(pathnameRef.current).toBe("/chat");
    });

    it("returns to the neutral route after leaving the group being read", async () => {
      const { Wrapper, pathnameRef } = routedWrapper(`/chat/dm/${groupD}`);
      const { result } = renderHook(() => useChatSidebar(), { wrapper: Wrapper });
      await waitFor(() => expect(result.current.state.status).toBe("ready"));

      await act(async () => {
        await result.current.leaveConversation({ kind: "dm", targetId: groupD });
      });

      expect(pathnameRef.current).toBe("/chat");
    });

    // Leaving from the sidebar while reading something else must not move the
    // reader: the action is about the row whose menu was opened, never about
    // the selection.
    it("stays where it is when the conversation left is not the one on screen", async () => {
      const { Wrapper, pathnameRef } = routedWrapper(`/chat/channel/${channelA}`);
      const { result } = renderHook(() => useChatSidebar(), { wrapper: Wrapper });
      await waitFor(() => expect(result.current.state.status).toBe("ready"));

      await act(async () => {
        await result.current.leaveConversation({ kind: "dm", targetId: groupD });
      });

      expect(pathnameRef.current).toBe(`/chat/channel/${channelA}`);
    });

    // The navigation is a consequence of the departure, so it must not precede
    // it: a request that fails leaves the reader exactly where they were, still
    // in a conversation they are still a member of.
    it("does not leave the route when the request fails", async () => {
      mockLeaveConversation.mockRejectedValueOnce(new Error("offline"));
      const { Wrapper, pathnameRef } = routedWrapper(`/chat/channel/${channelA}`);
      const { result } = renderHook(() => useChatSidebar(), { wrapper: Wrapper });
      await waitFor(() => expect(result.current.state.status).toBe("ready"));

      await act(async () => {
        await expect(
          result.current.leaveConversation({ kind: "channel", targetId: channelA }),
        ).rejects.toThrow("offline");
      });

      expect(pathnameRef.current).toBe(`/chat/channel/${channelA}`);
    });
  });
});

// ── Per-conversation preferences on every kind of row (issue #527) ──────────
//
// Pin and mute are the same shape — optimistic write, rollback on refusal — and
// both have to work on a direct conversation and a group, not only on a channel.
// The list a preference lands in is chosen from the target's kind, so a DM
// target reaching the channel list would silently update nothing.
describe("useChatSidebar conversation preferences", () => {
  beforeEach(() => {
    mockFetchSidebarData.mockReset();
    mockSetSidebarConversationPinned.mockReset();
    mockSetConversationMuted.mockReset();
    mockSetSidebarConversationPinned.mockResolvedValue(undefined);
    mockSetConversationMuted.mockResolvedValue(undefined);
    mockFetchSidebarData.mockResolvedValue({
      currentUserId,
      channels: [{ id: channelA, name: "A", type: "public", canWrite: true }],
      dms: [
        { id: dmC, name: "Juliane", type: "1:1" },
        { id: groupD, name: "Squad", type: "group", isGroup: true },
      ],
    });
  });

  const readyHook = async () => {
    const { result } = renderHook(() => useChatSidebar(), { wrapper: wrapper("/chat") });
    await waitFor(() => expect(result.current.state.status).toBe("ready"));
    return result;
  };

  const dmRow = (result: { current: { state: SidebarState } }, id: string) => {
    if (result.current.state.status !== "ready") throw new Error("not ready");
    return result.current.state.dms.find((dm) => dm.id === id);
  };

  it("mutes a direct conversation optimistically and keeps the other rows alone", async () => {
    const result = await readyHook();

    await act(async () => {
      await result.current.setMuted({ kind: "dm", targetId: dmC }, true);
    });

    expect(mockSetConversationMuted).toHaveBeenCalledWith("dm", dmC, true);
    expect(dmRow(result, dmC)?.muted).toBe(true);
    // A preference is per conversation: nothing else moved.
    expect(dmRow(result, groupD)?.muted).toBeFalsy();
    if (result.current.state.status === "ready") {
      expect(result.current.state.channels[0]?.muted).toBeFalsy();
    }
  });

  it("rolls a mute back to what it was when the server refuses", async () => {
    mockSetConversationMuted.mockRejectedValueOnce(new Error("offline"));
    const result = await readyHook();

    await act(async () => {
      await expect(
        result.current.setMuted({ kind: "channel", targetId: channelA }, true),
      ).rejects.toThrow("offline");
    });

    if (result.current.state.status !== "ready") throw new Error("not ready");
    // Un-muted is what it was, so un-muted is what it must be again: a refusal
    // must never leave a conversation looking silenced.
    expect(result.current.state.channels[0]?.muted).toBeFalsy();
  });

  it("unmutes a group back to false", async () => {
    mockFetchSidebarData.mockResolvedValue({
      currentUserId,
      channels: [],
      dms: [{ id: groupD, name: "Squad", type: "group", isGroup: true, muted: true }],
    });
    const result = await readyHook();
    expect(dmRow(result, groupD)?.muted).toBe(true);

    await act(async () => {
      await result.current.setMuted({ kind: "dm", targetId: groupD }, false);
    });

    expect(mockSetConversationMuted).toHaveBeenCalledWith("dm", groupD, false);
    expect(dmRow(result, groupD)?.muted).toBe(false);
  });

  it("pins a direct conversation optimistically, then reconciles with the server", async () => {
    const persisted = deferredValue<void>();
    mockSetSidebarConversationPinned.mockReturnValueOnce(persisted.promise);
    const result = await readyHook();
    const fetchesBefore = mockFetchSidebarData.mock.calls.length;

    let operation!: Promise<void>;
    act(() => {
      operation = result.current.setPinned({ kind: "dm", targetId: dmC }, true);
    });

    expect(mockSetSidebarConversationPinned).toHaveBeenCalledWith("dm", dmC, true);
    // The row shows the pin while the write is in flight, and the row next to it
    // does not.
    expect(dmRow(result, dmC)?.pinnedAt).toBeTruthy();
    expect(dmRow(result, groupD)?.pinnedAt).toBeFalsy();

    persisted.resolve();
    await act(async () => operation);

    // Only after it is persisted does the canonical list get refetched — what is
    // finally on screen is the server's answer, not the optimistic guess.
    await waitFor(() =>
      expect(mockFetchSidebarData.mock.calls.length).toBeGreaterThan(fetchesBefore),
    );
  });

  it("restores a direct conversation's previous pin when the write fails", async () => {
    mockFetchSidebarData.mockResolvedValue({
      currentUserId,
      channels: [],
      dms: [{ id: dmC, name: "Juliane", type: "1:1", pinnedAt: "2026-08-01T10:00:00Z" }],
    });
    mockSetSidebarConversationPinned.mockRejectedValueOnce(new Error("offline"));
    const result = await readyHook();

    await act(async () => {
      await expect(result.current.setPinned({ kind: "dm", targetId: dmC }, false)).rejects.toThrow(
        "offline",
      );
    });

    expect(dmRow(result, dmC)?.pinnedAt).toBe("2026-08-01T10:00:00Z");
  });

  // A preference for a conversation the sidebar does not have changes nothing —
  // membership comes from the server's list and never from an action's target.
  it("changes nothing when the target is not in the sidebar", async () => {
    const result = await readyHook();
    const before = result.current.state;

    await act(async () => {
      await result.current.setMuted({ kind: "dm", targetId: "missing-id" }, true);
      await result.current.setPinned({ kind: "channel", targetId: "missing-id" }, true);
    });

    if (result.current.state.status !== "ready" || before.status !== "ready") {
      throw new Error("not ready");
    }
    // The rows the sidebar does have are untouched, and no row was invented for
    // the id that is not in it.
    expect(result.current.state.channels.map((channel) => channel.id)).toEqual([channelA]);
    expect(result.current.state.dms.map((dm) => dm.id)).toEqual([dmC, groupD]);
    expect(result.current.state.channels.every((channel) => !channel.muted)).toBe(true);
    expect(result.current.state.dms.every((dm) => !dm.muted && !dm.pinnedAt)).toBe(true);
  });
});
