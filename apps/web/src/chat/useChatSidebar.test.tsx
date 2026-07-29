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
  },
}));

vi.mock("./chatApi", () => ({ fetchSidebarData: mockFetchSidebarData }));
vi.mock("./useChatWebSocket", () => ({
  useChatWebSocket: vi.fn(
    ({ onMessageCreated }: { onMessageCreated: (event: WSMessageCreatedEvent) => void }) => {
      websocket.onMessageCreated = onMessageCreated;
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
