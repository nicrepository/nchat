import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

/**
 * RF-32 in the timeline hook: sending a reference, receiving one over the
 * socket, and reconciling a verdict that lands afterwards.
 */

let capturedOnMessageCreated: ((evt: unknown) => void) | null = null;
let capturedOnAttachmentStatus: ((evt: unknown) => void) | null = null;

vi.mock("./useChatWebSocket", () => ({
  useChatWebSocket: ({
    onMessageCreated,
    onAttachmentStatus,
  }: {
    onMessageCreated: (evt: unknown) => void;
    onAttachmentStatus?: (evt: unknown) => void;
  }) => {
    capturedOnMessageCreated = onMessageCreated;
    capturedOnAttachmentStatus = onAttachmentStatus ?? null;
    return { toggleReaction: vi.fn() };
  },
}));

const { mockFetchChannelMessages, mockPostChannelMessage, mockPostDMMessage } = vi.hoisted(() => ({
  mockFetchChannelMessages: vi.fn(),
  mockPostChannelMessage: vi.fn(),
  mockPostDMMessage: vi.fn(),
}));

vi.mock("./chatApi", () => ({
  fetchChannelMessages: (...args: unknown[]) => mockFetchChannelMessages(...args),
  fetchChannelMessage: vi.fn(),
  fetchDMMessages: vi.fn().mockResolvedValue({ messages: [], nextCursor: "" }),
  fetchDMMessage: vi.fn(),
  resolveChannelMessageReferences: vi.fn().mockResolvedValue({}),
  resolveDMMessageReferences: vi.fn().mockResolvedValue({}),
  postChannelMessage: (...args: unknown[]) => mockPostChannelMessage(...args),
  postDMMessage: (...args: unknown[]) => mockPostDMMessage(...args),
  favoriteMessage: vi.fn(),
  unfavoriteMessage: vi.fn(),
  editMessage: vi.fn(),
  deleteMessage: vi.fn(),
}));

import { useMessages } from "./useMessages";
import type { ChannelAttachment, Message } from "./chatTypes";

const attachment: ChannelAttachment = {
  id: "att-1",
  filename: "relatorio.pdf",
  contentType: "application/pdf",
  size: 2048,
  status: "pending_scan",
  previewStatus: "pending",
  createdAt: "",
};

function message(overrides: Partial<Message> = {}): Message {
  return {
    id: "msg-1",
    senderId: "user-1",
    senderDisplayName: "Alice",
    senderEmail: "alice@example.test",
    kind: "user",
    bodyText: "",
    bodyFormat: "v3",
    isRemoved: false,
    status: "active",
    createdAt: "2026-08-03T12:00:00.000Z",
    updatedAt: "2026-08-03T12:00:00.000Z",
    isEdited: false,
    editCount: 0,
    reactions: [],
    isFavorited: false,
    isForwarded: false,
    ...overrides,
  };
}

function renderMessages() {
  return renderHook(() =>
    useMessages({ kind: "channel", targetId: "ch-1", currentUserId: "user-1" }),
  );
}

beforeEach(() => {
  capturedOnMessageCreated = null;
  capturedOnAttachmentStatus = null;
  mockFetchChannelMessages.mockReset().mockResolvedValue({ messages: [], nextCursor: "" });
  mockPostChannelMessage.mockReset().mockResolvedValue(message({ attachments: [attachment] }));
  mockPostDMMessage.mockReset();
});

afterEach(() => {
  vi.clearAllMocks();
});

describe("useMessages — attachments", () => {
  it("posts an attachment-only message and keeps its attachments", async () => {
    const { result } = renderMessages();
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    await act(async () => {
      const sent = await result.current.sendMessage("", undefined, ["att-1"]);
      expect(sent).toEqual({ status: "sent" });
    });

    expect(mockPostChannelMessage).toHaveBeenCalledWith("ch-1", "", {
      parentMessageId: undefined,
      referencedMessageId: undefined,
      attachmentIds: ["att-1"],
      idempotencyKey: expect.any(String),
    });
    expect(result.current.state.messages[0]?.attachments).toEqual([attachment]);
  });

  // The empty send that has always been refused stays refused.
  it("refuses an empty message with no attachment", async () => {
    const { result } = renderMessages();
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    await act(async () => {
      const sent = await result.current.sendMessage("   ");
      expect(sent).toEqual({ status: "stale" });
    });
    expect(mockPostChannelMessage).not.toHaveBeenCalled();
  });

  it("keeps the idempotency key for an unchanged retry and renews it after content changes", async () => {
    mockPostChannelMessage
      .mockRejectedValueOnce(new Error("network"))
      .mockResolvedValue(message({ attachments: [attachment] }));
    const { result } = renderMessages();
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    await act(async () => {
      await expect(result.current.sendMessage("legenda", undefined, ["att-1"])).rejects.toThrow();
    });
    const firstKey = mockPostChannelMessage.mock.calls[0][2].idempotencyKey;
    await act(async () => {
      await result.current.sendMessage("legenda", undefined, ["att-1"]);
    });
    expect(mockPostChannelMessage.mock.calls[1][2].idempotencyKey).toBe(firstKey);

    await act(async () => {
      await result.current.sendMessage("legenda alterada", undefined, ["att-1"]);
    });
    expect(mockPostChannelMessage.mock.calls[2][2].idempotencyKey).not.toBe(firstKey);
  });

  it("renders an attachment carried by a realtime message without refetching", async () => {
    const { result } = renderMessages();
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() => {
      capturedOnMessageCreated?.({
        type: "message.created",
        event_id: "evt-1",
        created_at: "2026-08-03T12:01:00.000Z",
        workspace_id: "ws-1",
        target_type: "channel",
        target_id: "ch-1",
        message_id: "msg-ws",
        payload: {
          id: "msg-ws",
          workspace_id: "ws-1",
          sender_id: "user-2",
          sender_display_name: "Bruno",
          kind: "user",
          body_text: "veja",
          body_format: "v3",
          status: "active",
          is_removed: false,
          created_at: "2026-08-03T12:01:00.000Z",
          updated_at: "2026-08-03T12:01:00.000Z",
          attachments: [
            {
              id: "att-ws",
              filename: "foto.png",
              content_type: "image/png",
              size: 64,
              status: "pending_scan",
              preview_status: "pending",
            },
          ],
        },
      });
    });

    const received = result.current.state.messages.find((item) => item.id === "msg-ws");
    expect(received?.attachments).toEqual([
      {
        id: "att-ws",
        filename: "foto.png",
        contentType: "image/png",
        size: 64,
        status: "pending_scan",
        previewStatus: "pending",
        createdAt: "",
      },
    ]);
  });

  it("reconciles a scan verdict into the message already on screen", async () => {
    mockFetchChannelMessages.mockResolvedValue({
      messages: [message({ attachments: [attachment] })],
      nextCursor: "",
    });
    const { result } = renderMessages();
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    act(() => {
      capturedOnAttachmentStatus?.({
        type: "attachment.status",
        target_type: "channel",
        target_id: "ch-1",
        attachment: {
          attachment_id: "att-1",
          status: "clean",
          updated_at: "2026-08-03T12:05:00.000Z",
        },
      });
    });

    expect(result.current.state.messages[0]?.attachments?.[0]?.status).toBe("clean");
    // Reconciled from the event itself: no second read of the page.
    expect(mockFetchChannelMessages).toHaveBeenCalledTimes(1);
  });

  it("ignores a verdict for an attachment this timeline does not show", async () => {
    mockFetchChannelMessages.mockResolvedValue({
      messages: [message({ attachments: [attachment] })],
      nextCursor: "",
    });
    const { result } = renderMessages();
    await waitFor(() => expect(result.current.state.status).toBe("ready"));
    const before = result.current.state.messages;

    act(() => {
      capturedOnAttachmentStatus?.({
        type: "attachment.status",
        target_type: "channel",
        target_id: "ch-1",
        attachment: {
          attachment_id: "someone-elses",
          status: "rejected",
          updated_at: "2026-08-03T12:05:00.000Z",
        },
      });
    });

    expect(result.current.state.messages).toBe(before);
  });
});
