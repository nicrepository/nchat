import { act, renderHook, waitFor } from "@testing-library/react";
import type { PropsWithChildren } from "react";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

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
      created_at: "2026-07-28T12:00:00Z",
      updated_at: "2026-07-28T12:00:00Z",
    },
  };
}

function unreadCounts(state: ReturnType<typeof useChatSidebar>["state"]) {
  if (state.status !== "ready") throw new Error("sidebar not ready");
  return {
    channelA: state.channels.find(({ id }) => id === channelA)?.unreadCount ?? 0,
    channelB: state.channels.find(({ id }) => id === channelB)?.unreadCount ?? 0,
    dmC: state.dms.find(({ id }) => id === dmC)?.unreadCount ?? 0,
  };
}

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
