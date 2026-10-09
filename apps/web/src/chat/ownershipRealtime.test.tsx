import { act, renderHook, waitFor } from "@testing-library/react";
import type { PropsWithChildren } from "react";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { clearTokens, setTokens } from "../lib/authSession";
import { fetchChannelDetails, fetchGroupDetails, fetchSidebarData } from "./chatApi";
import { _resetChatSocket, RECONNECT_BASE_DELAY_MS } from "./chatSocket";
import type { ChannelDetails, GroupDetails } from "./chatTypes";
import type { ConversationRole, OwnershipDetails } from "./ownershipApi";
import { useChatSidebar } from "./useChatSidebar";
import { useConversationDetails } from "./useConversationDetails";

vi.mock("./chatApi", async () => {
  const actual = await vi.importActual<typeof import("./chatApi")>("./chatApi");
  return {
    ...actual,
    fetchSidebarData: vi.fn(),
    fetchChannelDetails: vi.fn(),
    fetchGroupDetails: vi.fn(),
  };
});
vi.mock("./filesApi", () => ({
  fetchConversationAttachmentPage: vi.fn().mockResolvedValue({ attachments: [], nextCursor: null }),
}));

// Same controlled transport as #947. Socket routing, subscription recovery,
// sidebar invalidation and the details hook all run their production code.
class FakeWebSocket {
  static readonly OPEN = 1;
  static readonly CLOSED = 3;
  static instances: FakeWebSocket[] = [];
  readyState = 0;
  sent: string[] = [];
  onopen: (() => void) | null = null;
  onmessage: ((event: MessageEvent) => void) | null = null;
  onerror: (() => void) | null = null;
  onclose: ((event: CloseEvent) => void) | null = null;
  constructor() {
    FakeWebSocket.instances.push(this);
  }
  send(data: string) {
    this.sent.push(data);
  }
  open() {
    this.readyState = FakeWebSocket.OPEN;
    this.onopen?.();
  }
  message(data: object) {
    this.onmessage?.(new MessageEvent("message", { data: JSON.stringify(data) }));
  }
  close() {
    this.readyState = FakeWebSocket.CLOSED;
    this.onclose?.(new CloseEvent("close", { code: 1006 }));
  }
  subscriptions() {
    return this.sent
      .map((frame) => JSON.parse(frame) as { type: string; target_type: string; target_id: string })
      .filter((frame) => frame.type === "subscribe");
  }
  acknowledge() {
    for (const frame of this.subscriptions())
      this.message({ ...frame, type: "subscribed", operation: "subscribe" });
  }
}

const targetID = "private-target";
const OriginalWebSocket = global.WebSocket;
function wrapper({ children }: PropsWithChildren) {
  return <MemoryRouter>{children}</MemoryRouter>;
}

function ownership(role: ConversationRole): OwnershipDetails {
  return {
    enabled: true,
    members: [
      {
        userId: "member",
        displayName: "Participante",
        role,
        actions: { remove: false, assignRole: false, transfer: false },
      },
    ],
    capabilities: { addMembers: false, manageRoles: false, editMetadata: false, leave: true },
    leavePreview: { lastOwner: false, blocked: false },
  };
}
function channel(role: ConversationRole): ChannelDetails {
  return {
    id: targetID,
    slug: "private",
    name: "Private",
    type: "private",
    description: "",
    createdAt: "2026-01-01T00:00:00Z",
    memberCount: 1,
    onlineCount: 0,
    onlineMembers: [],
    canManageMembers: false,
    canRemoveMembers: false,
    ownership: ownership(role),
  };
}
function group(role: ConversationRole): GroupDetails {
  return {
    id: targetID,
    name: "Group",
    description: "",
    createdAt: "2026-01-01T00:00:00Z",
    participantCount: 1,
    participants: [],
    canManageMembers: false,
    canRemoveMembers: false,
    ownership: ownership(role),
  };
}
function serveRole(role: ConversationRole) {
  vi.mocked(fetchChannelDetails).mockResolvedValue(channel(role));
  vi.mocked(fetchGroupDetails).mockResolvedValue(group(role));
}

beforeEach(() => {
  FakeWebSocket.instances = [];
  global.WebSocket = FakeWebSocket as unknown as typeof WebSocket;
  _resetChatSocket(() => 0);
  setTokens("test-token");
  serveRole("member");
});
afterEach(() => {
  _resetChatSocket();
  vi.useRealTimers();
  global.WebSocket = OriginalWebSocket;
  clearTokens();
  vi.clearAllMocks();
});

function mountOwnership(kind: "channel" | "group") {
  vi.mocked(fetchSidebarData).mockResolvedValue({
    currentUserId: "reader",
    workspaceId: "workspace",
    categories: [],
    channels: [
      { id: "primary", name: "Primary", type: "public", canWrite: true },
      ...(kind === "channel"
        ? [{ id: targetID, name: "Private", type: "private" as const, canWrite: true }]
        : []),
    ],
    dms: kind === "group" ? [{ id: targetID, name: "Group", type: "group", participants: [] }] : [],
  });
  return renderHook(
    () => {
      useChatSidebar();
      return useConversationDetails({ kind, id: targetID });
    },
    { wrapper },
  );
}

function updated(kind: "channel" | "group") {
  return {
    type: "conversation.updated",
    target_type: kind === "channel" ? "channel" : "dm",
    target_id: targetID,
  };
}

function currentRole(details: ReturnType<typeof useConversationDetails>["details"]) {
  if (details.status !== "ready" || details.data.kind === "direct") return undefined;
  return details.data.ownership?.members[0]?.role;
}

describe.each(["channel", "group"] as const)("ownership realtime: %s", (kind) => {
  it("refetches open details for updates, duplicates and missed events after subscription recovery", async () => {
    const { result } = mountOwnership(kind);
    await waitFor(() => expect(currentRole(result.current.details)).toBe("member"));
    const socket = FakeWebSocket.instances[0];
    act(() => {
      socket.open();
      socket.acknowledge();
    });
    await waitFor(() => expect(currentRole(result.current.details)).toBe("member"));
    expect(FakeWebSocket.instances).toHaveLength(1);

    serveRole("owner");
    act(() => {
      socket.message(updated(kind));
      socket.message(updated(kind));
    });
    await waitFor(() => expect(currentRole(result.current.details)).toBe("owner"));
    expect(
      result.current.details.status === "ready" &&
        result.current.details.data.kind !== "direct" &&
        result.current.details.data.ownership?.members,
    ).toHaveLength(1);

    vi.useFakeTimers();
    act(() => socket.close());
    serveRole("admin"); // Lost while disconnected: no event is delivered.
    act(() => vi.advanceTimersByTime(RECONNECT_BASE_DELAY_MS / 2));
    vi.useRealTimers();
    expect(FakeWebSocket.instances).toHaveLength(2);
    const recovered = FakeWebSocket.instances[1];
    act(() => recovered.open());
    expect(currentRole(result.current.details)).toBe("owner");
    const [primary] = recovered.subscriptions();
    act(() => recovered.message({ ...primary, type: "subscribed", operation: "subscribe" }));
    expect(currentRole(result.current.details)).toBe("owner");
    act(() => recovered.acknowledge());
    await waitFor(() => expect(currentRole(result.current.details)).toBe("admin"));
  });

  it("clears privileged details after a denied subscription and authorized refetch failure", async () => {
    const { result } = mountOwnership(kind);
    await waitFor(() => expect(currentRole(result.current.details)).toBe("member"));
    const socket = FakeWebSocket.instances[0];
    act(() => {
      socket.open();
      socket.acknowledge();
    });
    await waitFor(() => expect(currentRole(result.current.details)).toBe("member"));
    vi.mocked(fetchChannelDetails).mockRejectedValue(new Error("not found"));
    vi.mocked(fetchGroupDetails).mockRejectedValue(new Error("not found"));
    act(() =>
      socket.message({
        ...updated(kind),
        type: "error",
        operation: "subscribe",
        code: "room_access_denied",
      }),
    );
    await waitFor(() => expect(result.current.details).toEqual({ status: "error" }));
    expect(currentRole(result.current.details)).toBeUndefined();
  });
});
