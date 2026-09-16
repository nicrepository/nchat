import { act, renderHook, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

/**
 * The send boundary's priority invariant (issues #821, #822, #825).
 *
 * Code review finding: `MessagePriorityIntent` is a plain record, so nothing in
 * the type stops a caller handing `persistentNotifications: true` to a priority
 * that is not urgent — and chat-service refuses exactly that pairing
 * (domain.ValidatePersistentNotifications). This asserts that the boundary
 * normalises once, and that the one normalised value is what reaches both the
 * request and the retry signature.
 *
 * Tested through the hook rather than against normalizePriorityIntent directly
 * (messagePriority.test.ts already covers the rule itself): what was broken was
 * not the rule, it was that the boundary never applied it.
 */

vi.mock("./useChatWebSocket", () => ({
  useChatWebSocket: () => ({ toggleReaction: vi.fn() }),
}));

const { mockFetchChannelMessages, mockPostChannelMessage } = vi.hoisted(() => ({
  mockFetchChannelMessages: vi.fn(),
  mockPostChannelMessage: vi.fn(),
}));

vi.mock("./chatApi", async (importOriginal) => ({
  safeAvatarUrl: (await importOriginal<typeof import("./chatApi")>()).safeAvatarUrl,
  fetchChannelMessages: (...args: unknown[]) => mockFetchChannelMessages(...args),
  fetchChannelMessage: vi.fn(),
  fetchDMMessages: vi.fn().mockResolvedValue({ messages: [], nextCursor: "" }),
  fetchDMMessage: vi.fn(),
  resolveChannelMessageReferences: vi.fn().mockResolvedValue({}),
  resolveDMMessageReferences: vi.fn().mockResolvedValue({}),
  postChannelMessage: (...args: unknown[]) => mockPostChannelMessage(...args),
  postDMMessage: vi.fn(),
  favoriteMessage: vi.fn(),
  unfavoriteMessage: vi.fn(),
  editMessage: vi.fn(),
  deleteMessage: vi.fn(),
}));

import { useMessages } from "./useMessages";
import type { MessagePriorityIntent } from "./messagePriority";
import type { Message } from "./chatTypes";

const sentMessage = {
  id: "msg-1",
  senderId: "user-1",
  senderDisplayName: "Alice",
  senderEmail: "alice@example.test",
  kind: "user",
  bodyText: "olá",
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
} as Message;

function renderMessages() {
  return renderHook(() =>
    useMessages({ kind: "channel", targetId: "ch-1", currentUserId: "user-1" }),
  );
}

/** The options object the boundary actually handed to chatApi. */
function postedOptions(call = 0) {
  return mockPostChannelMessage.mock.calls[call][2] as {
    priority: string;
    acknowledgementRequired: boolean;
    persistentNotifications: boolean;
    idempotencyKey: string;
  };
}

async function send(intent: MessagePriorityIntent) {
  const { result } = renderMessages();
  await waitFor(() => expect(result.current.state.status).toBe("ready"));
  await act(async () => {
    await result.current.sendMessage("olá", undefined, undefined, intent);
  });
}

beforeEach(() => {
  mockFetchChannelMessages.mockReset().mockResolvedValue({ messages: [], nextCursor: "" });
  mockPostChannelMessage.mockReset().mockResolvedValue(sentMessage);
});

describe("send boundary — persistentNotifications implies urgent", () => {
  // The two combinations the server refuses outright. The UI cannot produce
  // them, but the type can express them, and a boundary that forwards what it
  // is handed would post a message that is guaranteed to 400.
  it.each(["standard", "important"] as const)(
    "drops the urgent-only options stated alongside %s",
    async (priority) => {
      await send({ priority, acknowledgementRequired: true, persistentNotifications: true });

      expect(postedOptions()).toMatchObject({
        priority,
        acknowledgementRequired: false,
        persistentNotifications: false,
      });
    },
  );

  it("preserves both options where the contract allows them", async () => {
    await send({
      priority: "urgent",
      acknowledgementRequired: true,
      persistentNotifications: true,
    });

    expect(postedOptions()).toMatchObject({
      priority: "urgent",
      acknowledgementRequired: true,
      persistentNotifications: true,
    });
  });

  // No regression for the three ordinary sends.
  it.each(["standard", "important", "urgent"] as const)(
    "leaves a plain %s send exactly as stated",
    async (priority) => {
      await send({
        priority,
        acknowledgementRequired: false,
        persistentNotifications: false,
      });

      expect(postedOptions()).toMatchObject({
        priority,
        acknowledgementRequired: false,
        persistentNotifications: false,
      });
    },
  );
});

describe("send boundary — the signature and the payload state one intention", () => {
  /**
   * The failure mode this guards: an idempotency key computed from the raw
   * intent while the payload carries the normalised one. A retry of the same
   * message would then be fingerprinted as a different message and post twice.
   *
   * Driven through a failed send, because that is the only state in which the
   * retry key is retained — a successful send clears it deliberately.
   */
  it("gives two spellings of the same effective intent one retry key", async () => {
    mockPostChannelMessage.mockRejectedValueOnce(new Error("rede"));
    const { result } = renderMessages();
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    // Stated with a flag that cannot survive `standard`…
    await act(async () => {
      await expect(
        result.current.sendMessage("olá", undefined, undefined, {
          priority: "standard",
          acknowledgementRequired: false,
          persistentNotifications: true,
        }),
      ).rejects.toThrow();
    });

    // …and retried without it. Normalised, these are the same message.
    await act(async () => {
      await result.current.sendMessage("olá", undefined, undefined, {
        priority: "standard",
        acknowledgementRequired: false,
        persistentNotifications: false,
      });
    });

    expect(mockPostChannelMessage).toHaveBeenCalledTimes(2);
    expect(postedOptions(1).idempotencyKey).toBe(postedOptions(0).idempotencyKey);
  });

  // The converse, so the assertion above cannot pass by the signature simply
  // ignoring priority: a genuinely different intent is a different message.
  it("gives a genuinely different intent a different retry key", async () => {
    mockPostChannelMessage.mockRejectedValueOnce(new Error("rede"));
    const { result } = renderMessages();
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    await act(async () => {
      await expect(
        result.current.sendMessage("olá", undefined, undefined, {
          priority: "standard",
          acknowledgementRequired: false,
          persistentNotifications: false,
        }),
      ).rejects.toThrow();
    });

    await act(async () => {
      await result.current.sendMessage("olá", undefined, undefined, {
        priority: "urgent",
        acknowledgementRequired: false,
        persistentNotifications: true,
      });
    });

    expect(postedOptions(1).idempotencyKey).not.toBe(postedOptions(0).idempotencyKey);
  });
});
