import { act, renderHook, waitFor } from "@testing-library/react";
import type { PropsWithChildren } from "react";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { parseInstant } from "./sidebarOrder";
import type { WSMessageCreatedEvent } from "./useChatWebSocket";
import { useChatSidebar } from "./useChatSidebar";

const { mockFetchSidebarData, websocket } = vi.hoisted(() => ({
  mockFetchSidebarData: vi.fn(),
  websocket: {
    onMessageCreated: null as ((event: WSMessageCreatedEvent) => void) | null,
    onConversationAvailable: null as (() => void) | null,
  },
}));

vi.mock("./chatApi", () => ({ fetchSidebarData: mockFetchSidebarData }));
vi.mock("./useChatWebSocket", () => ({
  useChatWebSocket: vi.fn(
    ({
      onMessageCreated,
      onConversationAvailable,
    }: {
      onMessageCreated: (event: WSMessageCreatedEvent) => void;
      onConversationAvailable?: () => void;
    }) => {
      websocket.onMessageCreated = onMessageCreated;
      websocket.onConversationAvailable = onConversationAvailable ?? null;
      return { toggleReaction: vi.fn() };
    },
  ),
}));

const channelA = "11111111-1111-4111-8111-111111111111";
const channelB = "22222222-2222-4222-8222-222222222222";
const dmC = "33333333-3333-4333-8333-333333333333";
const currentUserId = "me-1";

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

function messageCreated(
  messageId: string,
  targetId: string,
  senderId = "other-1",
  targetType: "channel" | "dm" = "channel",
  /** The message's persisted creation instant — the sidebar's ordering key. */
  messageCreatedAt = "2026-07-28T12:00:00Z",
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
      kind: "user",
      body_text: "Nova mensagem",
      status: "active",
      is_removed: false,
      created_at: messageCreatedAt,
      updated_at: messageCreatedAt,
    },
  };
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
