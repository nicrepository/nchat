import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

/**
 * Issue #947: the timeline converges on the database after a resubscription,
 * without a reload.
 *
 * A resubscription — a reconnect, or the server's conversation.sync_required
 * after it could not prove access to deliver something — is this client's only
 * signal that events may have been lost. Persistence is the source of truth, so
 * what was missed is read back through the same authorized listing the first
 * page came from, and merged through the reducer's existing idempotent
 * transitions.
 *
 * The socket is replaced by a stub that hands the test the resync callback, so
 * every assertion runs the production catch-up, gateway routing and reducer.
 */

import type { Message, MessagePage } from "./chatTypes";
import type {
  WSMessageBlockedEvent,
  WSMessageCreatedEvent,
  WSMessagePayload,
  WSMessageUpdatedEvent,
  WSReactionUpdatedEvent,
} from "./useChatWebSocket";

let capturedOnSubscribed: (() => void) | null = null;
let capturedOnMessageCreated: ((event: WSMessageCreatedEvent) => void) | null = null;
let capturedOnMessageUpdated: ((event: WSMessageUpdatedEvent) => void) | null = null;
let capturedOnReactionUpdated: ((event: WSReactionUpdatedEvent) => void) | null = null;
let capturedOnMessageBlocked: ((event: WSMessageBlockedEvent) => void) | null = null;
/**
 * When set, the stub acknowledges each new target the way a fast server does:
 * from the effect of the render that moved the route — the one render whose
 * state still belongs to the previous conversation.
 */
let acknowledgeOnTargetChange = false;

vi.mock("./useChatWebSocket", async () => {
  const { useEffect } = await import("react");
  return {
    useChatWebSocket: (options: {
      targetId: string;
      onSubscribed?: () => void;
      onMessageCreated: (event: WSMessageCreatedEvent) => void;
      onMessageUpdated?: (event: WSMessageUpdatedEvent) => void;
      onReactionUpdated?: (event: WSReactionUpdatedEvent) => void;
      onMessageBlocked?: (event: WSMessageBlockedEvent) => void;
    }) => {
      capturedOnSubscribed = options.onSubscribed ?? null;
      capturedOnMessageCreated = options.onMessageCreated;
      capturedOnMessageUpdated = options.onMessageUpdated ?? null;
      capturedOnReactionUpdated = options.onReactionUpdated ?? null;
      capturedOnMessageBlocked = options.onMessageBlocked ?? null;
      const { targetId, onSubscribed } = options;
      useEffect(() => {
        if (acknowledgeOnTargetChange) queueMicrotask(() => onSubscribed?.());
      }, [targetId, onSubscribed]);
      return { toggleReaction: vi.fn(() => true), sendTyping: vi.fn() };
    },
  };
});

const {
  mockFetchChannelMessages,
  mockFetchChannelMessage,
  mockFetchDMMessages,
  mockEditMessage,
  mockPostChannelMessage,
} = vi.hoisted(() => ({
  mockEditMessage: vi.fn<(id: string, body: string, format: string) => Promise<Message>>(),
  mockPostChannelMessage: vi.fn<(id: string, body: string, options: unknown) => Promise<Message>>(),
  mockFetchChannelMessages:
    vi.fn<(id: string, cursor?: string, signal?: AbortSignal) => Promise<MessagePage>>(),
  mockFetchChannelMessage:
    vi.fn<(id: string, messageId: string, signal?: AbortSignal) => Promise<Message>>(),
  mockFetchDMMessages:
    vi.fn<(id: string, cursor?: string, signal?: AbortSignal) => Promise<MessagePage>>(),
}));

vi.mock("./chatApi", async (importOriginal) => ({
  ...(await importOriginal<typeof import("./chatApi")>()),
  fetchChannelMessages: mockFetchChannelMessages,
  fetchDMMessages: mockFetchDMMessages,
  fetchChannelMessage: mockFetchChannelMessage,
  fetchDMMessage: vi.fn(),
  fetchChannelMessageSecuritySnapshots: vi.fn().mockResolvedValue([]),
  fetchDMMessageSecuritySnapshots: vi.fn().mockResolvedValue([]),
  resolveChannelMessageReferences: vi.fn().mockResolvedValue({}),
  resolveDMMessageReferences: vi.fn().mockResolvedValue({}),
  fetchMessageAcknowledgements: vi.fn().mockResolvedValue([]),
  editMessage: mockEditMessage,
  postChannelMessage: mockPostChannelMessage,
}));

import { catchUpMaxPages, catchUpRetryDelaysMs } from "./messages/useTimelineCatchUp";
import { useMessages } from "./useMessages";

function message(n: number, overrides: Partial<Message> = {}): Message {
  const at = new Date(Date.UTC(2026, 9, 1, 12, 0, n)).toISOString();
  return {
    id: `m-${String(n).padStart(4, "0")}`,
    senderId: "user-2",
    senderDisplayName: "Bea",
    senderEmail: "",
    kind: "user",
    bodyText: `mensagem ${n}`,
    bodyFormat: "v3",
    isRemoved: false,
    status: "active",
    createdAt: at,
    updatedAt: at,
    isEdited: false,
    editCount: 0,
    reactions: [],
    isFavorited: false,
    isForwarded: false,
    ...overrides,
  } as Message;
}

/** Messages a..b inclusive, oldest first — the order a page carries them in. */
function range(a: number, b: number): Message[] {
  return Array.from({ length: b - a + 1 }, (_, index) => message(a + index));
}

function page(messages: Message[], nextCursor = ""): MessagePage {
  return { messages, nextCursor };
}

function ids(messages: Message[]): string[] {
  return messages.map(({ id }) => id);
}

function payloadOf(m: Message): WSMessagePayload {
  return {
    id: m.id,
    workspace_id: "ws-1",
    channel_id: "ch-1",
    sender_id: m.senderId,
    sender_display_name: m.senderDisplayName,
    kind: "user",
    body_text: m.bodyText,
    body_format: "v3",
    status: "active",
    is_removed: false,
    created_at: m.createdAt,
    updated_at: m.updatedAt,
  } as unknown as WSMessagePayload;
}

function renderChannel(targetId = "ch-1") {
  return renderHook(
    ({ target }) => useMessages({ kind: "channel", targetId: target, currentUserId: "user-1" }),
    { initialProps: { target: targetId } },
  );
}

async function loadedWith(messages: Message[], nextCursor = "") {
  mockFetchChannelMessages.mockResolvedValueOnce(page(messages, nextCursor));
  const view = renderChannel();
  await waitFor(() => expect(view.result.current.state.status).toBe("ready"));
  return view;
}

/** The resync point a reconnect, or a server-requested resync, produces. */
function resubscribe() {
  act(() => capturedOnSubscribed?.());
}

beforeEach(() => {
  acknowledgeOnTargetChange = false;
  capturedOnSubscribed = null;
  capturedOnMessageCreated = null;
  mockFetchChannelMessages.mockReset();
  mockFetchDMMessages.mockReset().mockResolvedValue(page([]));
});

afterEach(() => {
  vi.useRealTimers();
});

describe("useMessages — timeline catch-up on resubscription (issue #947)", () => {
  // F03, the central case: M101..M103 were persisted while this client's socket
  // was down. Nothing on the wire will ever carry them again.
  it("shows the messages persisted during the gap without a reload", async () => {
    const { result } = await loadedWith(range(1, 100));
    mockFetchChannelMessages.mockImplementation(serveListing(range(1, 103)));

    resubscribe();

    await waitFor(() => expect(ids(result.current.state.messages)).toEqual(ids(range(1, 103))));
    // Appended like any other arrival, so the viewport keeps its own rules:
    // follow the bottom only when the reader is already there.
    expect(result.current.state.lastMutation).toBe("ws_append");
  });

  // A gap longer than one page is paged back until it meets what is held —
  // never a single page that silently leaves a hole in the middle — and on
  // until the oldest held message is covered (P1), then no further.
  it("pages back until the read covers the timeline it already holds", async () => {
    const { result } = await loadedWith(range(1, 3));
    mockFetchChannelMessages.mockImplementation(serveListing(range(1, 102)));

    resubscribe();

    await waitFor(() => expect(ids(result.current.state.messages)).toEqual(ids(range(1, 102))));
    const cursors = mockFetchChannelMessages.mock.calls.map(([, cursor]) => cursor);
    expect(cursors).toEqual([undefined, undefined, "c52", "c2"]);
  });

  // Edits, deletions and reactions on messages already drawn are carried by the
  // same listing, so the catch-up converges them too.
  it("converges edits, deletions and reactions on held messages", async () => {
    const { result } = await loadedWith(range(1, 3));
    const edited = message(1, { bodyText: "editada", isEdited: true, editCount: 1 });
    const removed = message(2, { bodyText: "", isRemoved: true, status: "deleted" });
    const reacted = message(3, {
      reactions: [{ emoji: "👍", count: 1, reactedByMe: false, users: [] }],
    });
    mockFetchChannelMessages.mockResolvedValueOnce(page([edited, removed, reacted, message(4)]));

    resubscribe();

    await waitFor(() => expect(ids(result.current.state.messages)).toEqual(ids(range(1, 4))));
    const [first, second, third] = result.current.state.messages;
    expect(first.bodyText).toBe("editada");
    expect(second.isRemoved).toBe(true);
    expect(second.bodyText).toBe("");
    expect(third.reactions).toHaveLength(1);
  });

  // F04: identity is the message id. The listing and the socket may both carry
  // the same message, in either order, any number of times.
  it("keeps one row per message when the socket redelivers what the catch-up read", async () => {
    const { result } = await loadedWith(range(1, 3));
    mockFetchChannelMessages.mockResolvedValue(page(range(1, 5)));

    resubscribe();
    await waitFor(() => expect(ids(result.current.state.messages)).toEqual(ids(range(1, 5))));
    act(() =>
      capturedOnMessageCreated?.({
        type: "message.created",
        target_type: "channel",
        target_id: "ch-1",
        message_id: message(4).id,
        payload: payloadOf(message(4)),
      } as WSMessageCreatedEvent),
    );
    resubscribe();
    await waitFor(() => expect(mockFetchChannelMessages).toHaveBeenCalledTimes(3));

    await waitFor(() => expect(ids(result.current.state.messages)).toEqual(ids(range(1, 5))));
  });

  // Distinct messages that say the same thing are distinct rows.
  it("never merges two messages because their text is equal", async () => {
    const { result } = await loadedWith([message(1, { bodyText: "ok" })]);
    mockFetchChannelMessages.mockResolvedValueOnce(
      page([message(1, { bodyText: "ok" }), message(2, { bodyText: "ok" })]),
    );

    resubscribe();

    await waitFor(() => expect(result.current.state.messages).toHaveLength(2));
  });

  // F05/F06: a catch-up belongs to the conversation that started it. A target
  // change — another conversation, or another workspace's — must not receive
  // its late answer.
  it("does not apply a late catch-up to the conversation that replaced it", async () => {
    const view = await loadedWith(range(1, 3));
    let answerOld!: (value: MessagePage) => void;
    mockFetchChannelMessages.mockImplementationOnce(
      () => new Promise<MessagePage>((resolve) => (answerOld = resolve)),
    );
    resubscribe();
    await waitFor(() => expect(mockFetchChannelMessages).toHaveBeenCalledTimes(2));

    mockFetchChannelMessages.mockResolvedValueOnce(page([message(500)]));
    view.rerender({ target: "ch-2" });
    await waitFor(() => expect(ids(view.result.current.state.messages)).toEqual([message(500).id]));

    await act(async () => answerOld(page(range(1, 9))));

    expect(ids(view.result.current.state.messages)).toEqual([message(500).id]);
  });

  // F07: reconnects in quick succession cost one catch-up in flight, and the
  // newest one decides.
  it("coalesces successive resubscriptions into one catch-up in flight", async () => {
    const { result } = await loadedWith(range(1, 3));
    const signals: AbortSignal[] = [];
    const answers: Array<(value: MessagePage) => void> = [];
    mockFetchChannelMessages.mockImplementation(
      (_id, _cursor, signal) =>
        new Promise<MessagePage>((resolve) => {
          signals.push(signal!);
          answers.push(resolve);
        }),
    );

    resubscribe();
    resubscribe();
    resubscribe();
    await waitFor(() => expect(signals).toHaveLength(3));

    expect(signals.slice(0, 2).every((signal) => signal.aborted)).toBe(true);
    expect(signals[2].aborted).toBe(false);
    await act(async () => answers[2](page(range(1, 6))));
    await waitFor(() => expect(ids(result.current.state.messages)).toEqual(ids(range(1, 6))));
  });

  // F11: a failed read is not reported as synchronised, and recovery is bounded
  // and automatic — the reader does not have to reload.
  it("reports a failed catch-up and converges on a bounded retry", async () => {
    const { result } = await loadedWith(range(1, 3));
    mockFetchChannelMessages
      .mockRejectedValueOnce(new Error("503"))
      .mockResolvedValueOnce(page(range(1, 5)));

    resubscribe();

    await waitFor(() => expect(syncNotice(result.current.state)).toBeTruthy());
    await waitFor(() => expect(ids(result.current.state.messages)).toEqual(ids(range(1, 5))), {
      timeout: 5_000,
    });
    expect(syncNotice(result.current.state)).toBeFalsy();
  });

  // A gap longer than the read bound is not stitched with a hole in it: the
  // timeline becomes what was read, and nothing else the reader is doing moves.
  it("replaces the timeline with what was read when the gap is too long to stitch", async () => {
    const { result } = await loadedWith(range(1, 3));
    act(() => result.current.selectReply(message(2)));
    for (let p = 0; p < catchUpMaxPages; p += 1) {
      const newest = 1000 - p * 50;
      mockFetchChannelMessages.mockResolvedValueOnce(page(range(newest - 49, newest), `p${p + 1}`));
    }

    resubscribe();

    await waitFor(() => expect(result.current.state.messages).toHaveLength(50 * catchUpMaxPages));
    expect(result.current.state.messages[0].id).toBe(message(1000 - 50 * catchUpMaxPages + 1).id);
    expect(result.current.state.messages.at(-1)!.id).toBe(message(1000).id);
    expect(result.current.state.nextCursor).toBe(`p${catchUpMaxPages}`);
    expect(result.current.state.replyTo?.id).toBe(message(2).id);
    expect(mockFetchChannelMessages).toHaveBeenCalledTimes(1 + catchUpMaxPages);
  });

  // §11.6: the listing was requested before something newer was drawn; its
  // older copy must not undo it.
  it("never lets an older listing copy overwrite a newer drawn one", async () => {
    const newer = message(1, { bodyText: "editada depois", updatedAt: "2026-10-01T13:00:00.000Z" });
    const { result } = await loadedWith([newer]);
    mockFetchChannelMessages.mockResolvedValueOnce(page([message(1), message(2)]));

    resubscribe();

    await waitFor(() => expect(ids(result.current.state.messages)).toEqual(ids(range(1, 2))));
    expect(result.current.state.messages[0].bodyText).toBe("editada depois");
  });

  // CQ947-02: the obligation is never dropped, and pursuing it never becomes a
  // request loop — the backoff settles at the last delay and stays there.
  it("keeps pursuing a failing catch-up at a capped pace and keeps it reported", async () => {
    const { result } = await loadedWith(range(1, 3));
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    mockFetchChannelMessages.mockRejectedValue(new Error("503"));
    const ceiling = catchUpRetryDelaysMs.at(-1)!;

    resubscribe();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5 * 60_000);
    });
    const before = mockFetchChannelMessages.mock.calls.length;
    await act(async () => {
      await vi.advanceTimersByTimeAsync(10 * ceiling);
    });

    expect(mockFetchChannelMessages.mock.calls.length - before).toBe(10);
    expect(vi.getTimerCount()).toBe(1);
    expect(syncNotice(result.current.state)).toBeTruthy();
    expect(ids(result.current.state.messages)).toEqual(ids(range(1, 3)));
  });

  // Before the first page has landed there is nothing to catch up: the load in
  // flight is itself the authoritative read.
  it("does not catch up a timeline that has not loaded yet", async () => {
    mockFetchChannelMessages.mockImplementationOnce(() => new Promise<MessagePage>(() => {}));
    renderChannel();
    await waitFor(() => expect(capturedOnSubscribed).not.toBeNull());

    resubscribe();

    expect(mockFetchChannelMessages).toHaveBeenCalledTimes(1);
  });

  // The race the draft-lifecycle E2E exposed: the acknowledgement for the new
  // conversation can run while the state is still the previous one's "ready".
  // Catching up then would read the wrong timeline and disturb the first page.
  it("does not catch up a conversation whose first page is still loading", async () => {
    const view = await loadedWith(range(1, 3));
    acknowledgeOnTargetChange = true;
    mockFetchChannelMessages.mockImplementationOnce(() => new Promise<MessagePage>(() => {}));

    view.rerender({ target: "ch-2" });
    await act(async () => {
      await Promise.resolve();
    });

    expect(mockFetchChannelMessages).toHaveBeenCalledTimes(2);
  });

  // F12: a view that is gone takes its catch-up with it.
  it("abandons a pending catch-up when the view unmounts", async () => {
    const view = await loadedWith(range(1, 3));
    let signal: AbortSignal | undefined;
    mockFetchChannelMessages.mockImplementationOnce((_id, _cursor, received) => {
      signal = received;
      return new Promise<MessagePage>(() => {});
    });
    resubscribe();
    await waitFor(() => expect(signal).toBeDefined());

    view.unmount();

    expect(signal!.aborted).toBe(true);
  });
});

// ── Issue #947 follow-up ─────────────────────────────────────────────────────

/**
 * A listing that pages the way chat-service does: newest first, `size` per page,
 * an opaque cursor naming where the previous page stopped, and a 400 for a
 * cursor it never issued. `all` is read on every request, so a test mutates the
 * "database" simply by replacing entries in it.
 */
function serveListing(all: Message[], size = 50) {
  return async (_id: string, cursor?: string): Promise<MessagePage> => {
    const end = cursor === undefined ? all.length : Number(cursor.replace(/^c/, ""));
    if (cursor !== undefined && (!/^c\d+$/.test(cursor) || end > all.length)) {
      throw new Error("400 invalid cursor");
    }
    const start = Math.max(0, end - size);
    return page(all.slice(start, end), start > 0 ? `c${start}` : "");
  };
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (error: Error) => void;
  const promise = new Promise<T>((done, fail) => {
    resolve = done;
    reject = fail;
  });
  return { promise, resolve, reject };
}

function reactionEvent(
  messageId: string,
  reactions: Array<{ emoji: string; users: string[] }>,
): WSReactionUpdatedEvent {
  return {
    type: "reaction.updated",
    target_type: "channel",
    target_id: "ch-1",
    message_id: messageId,
    reaction: {
      message_id: messageId,
      actor_user_id: "user-3",
      emoji: reactions[0]?.emoji ?? "👍",
      added: reactions.length > 0,
      reactions: reactions.map(({ emoji, users }) => ({
        emoji,
        count: users.length,
        users: users.map((user_id) => ({ user_id, display_name: user_id })),
      })),
    },
  };
}

function reacted(n: number, emoji: string, users: string[], overrides: Partial<Message> = {}) {
  return message(n, {
    reactions: [
      {
        emoji,
        count: users.length,
        reactedByMe: false,
        users: users.map((userId) => ({ userId, displayName: userId })),
      },
    ],
    ...overrides,
  });
}

function byId(messages: Message[], n: number): Message {
  return messages.find(({ id }) => id === message(n).id)!;
}

describe("useMessages — catch-up covers every held message, not a recent window (#947 P1)", () => {
  // The scenario the follow-up names: the changed messages sit far outside the
  // five newest pages, and 150 new messages arrived on top of them.
  it("converges old edits, reactions and deletions beyond the five newest pages", async () => {
    const { result } = await loadedWith(range(1, 200));
    const server = range(1, 350);
    server[14] = message(15, {
      bodyText: "M015 editada",
      isEdited: true,
      editCount: 1,
      updatedAt: "2026-10-02T00:00:00.000Z",
    });
    server[19] = reacted(20, "🎉", ["user-2", "user-4"]);
    server[24] = message(25, {
      bodyText: "",
      isRemoved: true,
      status: "deleted",
      updatedAt: "2026-10-02T00:00:00.000Z",
    });
    mockFetchChannelMessages.mockImplementation(serveListing(server));

    resubscribe();

    await waitFor(() => expect(result.current.state.messages).toHaveLength(350));
    const messages = result.current.state.messages;
    expect(new Set(ids(messages)).size).toBe(350);
    expect(byId(messages, 15).bodyText).toBe("M015 editada");
    expect(byId(messages, 20).reactions[0]).toMatchObject({ emoji: "🎉", count: 2 });
    expect(byId(messages, 25).isRemoved).toBe(true);
    expect(byId(messages, 26).bodyText).toBe("mensagem 26");
    // Appended at the bottom like any arrival: the viewport keeps its anchor.
    expect(result.current.state.lastMutation).toBe("ws_append");
  });

  it("covers the held timeline with short pages too", async () => {
    const { result } = await loadedWith(range(1, 30));
    const server = range(1, 30);
    server[1] = message(2, { bodyText: "M002 editada", updatedAt: "2026-10-02T00:00:00.000Z" });
    mockFetchChannelMessages.mockImplementation(serveListing(server, 7));

    resubscribe();

    await waitFor(() =>
      expect(byId(result.current.state.messages, 2).bodyText).toBe("M002 editada"),
    );
    expect(result.current.state.messages).toHaveLength(30);
  });

  it("re-reads the held pages without changing anything when nothing changed", async () => {
    const { result } = await loadedWith(range(1, 120));
    mockFetchChannelMessages.mockImplementation(serveListing(range(1, 120)));
    const before = result.current.state.messages;

    resubscribe();

    await waitFor(() => expect(mockFetchChannelMessages).toHaveBeenCalledTimes(1 + 3));
    expect(ids(result.current.state.messages)).toEqual(ids(before));
    expect(result.current.state.lastMutation).not.toBe("initial");
  });

  // A gap too long to stitch replaces the timeline; what the reader pages back
  // into afterwards comes from the server, so nothing drawn is ever stale.
  it("serves older history from the server after an unstitchable gap", async () => {
    const { result } = await loadedWith(range(1, 3));
    const server = range(1, 400);
    server[0] = message(1, { bodyText: "M001 editada", updatedAt: "2026-10-02T00:00:00.000Z" });
    mockFetchChannelMessages.mockImplementation(serveListing(server));

    resubscribe();
    await waitFor(() => expect(result.current.state.messages.at(-1)!.id).toBe(message(400).id));
    expect(result.current.state.nextCursor).not.toBe("");
    while (result.current.state.nextCursor) {
      act(() => result.current.loadMore());
      await waitFor(() => expect(result.current.state.loadingMore).toBe(false));
    }

    expect(result.current.state.messages).toHaveLength(400);
    expect(byId(result.current.state.messages, 1).bodyText).toBe("M001 editada");
  });
});

describe("useMessages — catch-up paging edge cases (#947 P1)", () => {
  // A cursor the server no longer accepts fails the walk; the retry starts
  // again from the newest page instead of resuming from a dead cursor.
  it("restarts from the newest page when the server refuses a cursor", async () => {
    const { result } = await loadedWith(range(1, 60));
    const server = range(1, 61);
    const listing = serveListing(server);
    let refused = false;
    mockFetchChannelMessages.mockImplementation(async (id, cursor) => {
      if (cursor && !refused) {
        refused = true;
        throw new Error("400 invalid cursor");
      }
      return listing(id, cursor);
    });

    resubscribe();

    await waitFor(() => expect(ids(result.current.state.messages)).toEqual(ids(range(1, 61))), {
      timeout: 5_000,
    });
    expect(result.current.state.realtimeError).toBeNull();
  });
});

describe("useMessages — realtime during the catch-up read (#947 P2)", () => {
  async function inFlight() {
    const view = await loadedWith(range(1, 3));
    const read = deferred<MessagePage>();
    mockFetchChannelMessages.mockImplementationOnce(() => read.promise);
    resubscribe();
    await waitFor(() => expect(mockFetchChannelMessages).toHaveBeenCalledTimes(2));
    return { view, read };
  }

  // Reactions do not move updatedAt, so no timestamp can say the read is older.
  it("keeps a reaction added while the read was in flight", async () => {
    const { view, read } = await inFlight();
    act(() =>
      capturedOnReactionUpdated?.(
        reactionEvent(message(2).id, [{ emoji: "👍", users: ["user-3", "user-4"] }]),
      ),
    );

    await act(async () => read.resolve(page(range(1, 3))));

    expect(byId(view.result.current.state.messages, 2).reactions[0]).toMatchObject({
      emoji: "👍",
      count: 2,
    });
  });

  it("keeps a reaction removed while the read was in flight", async () => {
    const view = await loadedWith([message(1), reacted(2, "👍", ["user-3"]), message(3)]);
    const read = deferred<MessagePage>();
    mockFetchChannelMessages.mockImplementationOnce(() => read.promise);
    resubscribe();
    await waitFor(() => expect(mockFetchChannelMessages).toHaveBeenCalledTimes(2));
    act(() => capturedOnReactionUpdated?.(reactionEvent(message(2).id, [])));

    await act(async () =>
      read.resolve(page([message(1), reacted(2, "👍", ["user-3"]), message(3)])),
    );

    expect(byId(view.result.current.state.messages, 2).reactions).toEqual([]);
  });

  // An edit missed during the outage and a reaction delivered during the read,
  // on the same message: both must survive. Skipping the read for a message the
  // socket touched would lose the edit; applying it blindly loses the reaction.
  it("keeps both a missed edit and a reaction delivered during the read", async () => {
    const { view, read } = await inFlight();
    act(() =>
      capturedOnReactionUpdated?.(
        reactionEvent(message(2).id, [{ emoji: "👍", users: ["user-3"] }]),
      ),
    );

    await act(async () =>
      read.resolve(
        page([
          message(1),
          message(2, { bodyText: "editada offline", updatedAt: "2026-10-02T00:00:00.000Z" }),
          message(3),
        ]),
      ),
    );

    const m2 = byId(view.result.current.state.messages, 2);
    expect(m2.bodyText).toBe("editada offline");
    expect(m2.reactions[0]).toMatchObject({ emoji: "👍", count: 1 });
  });

  it("keeps an edit and a deletion delivered during the read", async () => {
    const { view, read } = await inFlight();
    act(() =>
      capturedOnMessageUpdated?.({
        type: "message.updated",
        target_type: "channel",
        target_id: "ch-1",
        message_update: {
          message_id: message(1).id,
          body: "editada agora",
          body_format: "v3",
          edited_at: "2026-10-03T00:00:00.000Z",
          edit_count: 1,
          is_edited: true,
          updated_at: "2026-10-03T00:00:00.000Z",
        },
      }),
    );
    act(() =>
      capturedOnMessageUpdated?.({
        type: "message.updated",
        target_type: "channel",
        target_id: "ch-1",
        message_update: {
          message_id: message(3).id,
          body: "",
          body_format: "v3",
          edited_at: "2026-10-03T00:00:00.000Z",
          edit_count: 0,
          is_edited: false,
          status: "deleted",
          is_removed: true,
          deleted_at: "2026-10-03T00:00:00.000Z",
          updated_at: "2026-10-03T00:00:00.000Z",
        },
      }),
    );

    await act(async () => read.resolve(page(range(1, 4))));

    const messages = view.result.current.state.messages;
    expect(byId(messages, 1).bodyText).toBe("editada agora");
    expect(byId(messages, 3).isRemoved).toBe(true);
    expect(ids(messages)).toEqual(ids(range(1, 4)));
  });

  it("applies a reaction delivered after the read landed", async () => {
    const { view, read } = await inFlight();
    await act(async () => read.resolve(page(range(1, 3))));

    act(() =>
      capturedOnReactionUpdated?.(
        reactionEvent(message(2).id, [{ emoji: "👍", users: ["user-3"] }]),
      ),
    );

    expect(byId(view.result.current.state.messages, 2).reactions[0]).toMatchObject({ count: 1 });
  });
});

describe("useMessages — the first page and the subscription (#947 P3)", () => {
  // The subscription is acknowledged while the first page is still in flight.
  // Something persisted after the database answered that page and before the
  // subscription existed is in neither; no later event will ever carry it.
  it("shows a message persisted between the first page and the subscription", async () => {
    const first = deferred<MessagePage>();
    mockFetchChannelMessages.mockImplementationOnce(() => first.promise);
    const { result } = renderChannel();
    await waitFor(() => expect(capturedOnSubscribed).not.toBeNull());
    resubscribe();
    mockFetchChannelMessages.mockImplementation(serveListing(range(1, 4)));

    await act(async () => first.resolve(page(range(1, 3))));

    await waitFor(() => expect(ids(result.current.state.messages)).toEqual(ids(range(1, 4))));
  });

  it("catches up once the first page lands after a failed first attempt", async () => {
    mockFetchChannelMessages.mockRejectedValueOnce(new Error("503"));
    const { result } = renderChannel();
    await waitFor(() => expect(result.current.state.status).toBe("error"));
    resubscribe();
    mockFetchChannelMessages
      .mockResolvedValueOnce(page(range(1, 3)))
      .mockImplementation(serveListing(range(1, 4)));

    act(() => result.current.retry());

    await waitFor(() => expect(ids(result.current.state.messages)).toEqual(ids(range(1, 4))));
  });

  it("does not carry an owed catch-up into the conversation that replaced it", async () => {
    const first = deferred<MessagePage>();
    mockFetchChannelMessages.mockImplementationOnce(() => first.promise);
    const view = renderChannel();
    await waitFor(() => expect(capturedOnSubscribed).not.toBeNull());
    resubscribe();
    mockFetchChannelMessages.mockResolvedValueOnce(page([message(500)]));

    view.rerender({ target: "ch-2" });
    await waitFor(() => expect(ids(view.result.current.state.messages)).toEqual([message(500).id]));
    await act(async () => first.resolve(page(range(1, 3))));

    expect(mockFetchChannelMessages).toHaveBeenCalledTimes(2);
  });

  it("keeps the reply being composed when the owed catch-up lands", async () => {
    const first = deferred<MessagePage>();
    mockFetchChannelMessages.mockImplementationOnce(() => first.promise);
    const { result } = renderChannel();
    await waitFor(() => expect(capturedOnSubscribed).not.toBeNull());
    resubscribe();
    const read = deferred<MessagePage>();
    mockFetchChannelMessages.mockImplementationOnce(() => read.promise);
    await act(async () => first.resolve(page(range(1, 3))));
    act(() => result.current.selectReply(message(2)));

    await act(async () => read.resolve(page(range(1, 4))));

    expect(ids(result.current.state.messages)).toEqual(ids(range(1, 4)));
    expect(result.current.state.replyTo?.id).toBe(message(2).id);
  });
});

// ── Code Quality Review findings (CQ947-01..05, 07) ─────────────────────────

const syncNotice = (state: { realtimeError: string | null; syncError?: string | null }) =>
  state.realtimeError ?? state.syncError;

describe("useMessages — the reconciliation obligation outlives a failed attempt (CQ947-01/02)", () => {
  // CQ947-01: an attempt whose journal overflowed must be retried, not dropped.
  // M4 was persisted while the read was in flight; nothing else will ever
  // announce it — no broadcast, no reconnect.
  it("retries after the journal overflowed and shows the message the first read had", async () => {
    const { result } = await loadedWith(range(1, 3));
    const first = deferred<MessagePage>();
    // What the server holds once the burst is over: M4, and M2 with the last
    // reaction list the burst delivered.
    const server = [message(1), reacted(2, "👍", ["u500"]), message(3), message(4)];
    mockFetchChannelMessages
      .mockImplementationOnce(() => first.promise)
      .mockImplementation(serveListing(server));
    resubscribe();
    await waitFor(() => expect(mockFetchChannelMessages).toHaveBeenCalledTimes(2));
    act(() => {
      for (let i = 0; i <= 500; i += 1) {
        capturedOnReactionUpdated?.(
          reactionEvent(message(2).id, [{ emoji: "👍", users: [`u${i}`] }]),
        );
      }
    });

    await act(async () => first.resolve(page(range(1, 4))));

    await waitFor(() => expect(ids(result.current.state.messages)).toEqual(ids(range(1, 4))), {
      timeout: 5_000,
    });
    expect(byId(result.current.state.messages, 2).reactions[0]).toMatchObject({ count: 1 });
  }, 10_000);

  it("applies a read whose journal holds exactly the limit", async () => {
    const { result } = await loadedWith(range(1, 3));
    const first = deferred<MessagePage>();
    mockFetchChannelMessages.mockImplementationOnce(() => first.promise);
    resubscribe();
    await waitFor(() => expect(mockFetchChannelMessages).toHaveBeenCalledTimes(2));
    act(() => {
      for (let i = 0; i < 500; i += 1) {
        capturedOnReactionUpdated?.(
          reactionEvent(message(2).id, [{ emoji: "👍", users: [`u${i}`] }]),
        );
      }
    });

    await act(async () => first.resolve(page(range(1, 4))));

    expect(ids(result.current.state.messages)).toEqual(ids(range(1, 4)));
    expect(mockFetchChannelMessages).toHaveBeenCalledTimes(2);
  });

  // CQ947-02: the immediate retries can be spent, the obligation cannot.
  it("keeps the obligation after the immediate retries and converges once REST recovers", async () => {
    const { result } = await loadedWith(range(1, 3));
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    let healthy = false;
    const listing = serveListing(range(1, 4));
    mockFetchChannelMessages.mockImplementation(async (id, cursor) => {
      if (!healthy) throw new Error("503");
      return listing(id, cursor);
    });

    resubscribe();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(10_000);
    });
    expect(syncNotice(result.current.state)).toBeTruthy();
    healthy = true;
    await act(async () => {
      await vi.advanceTimersByTimeAsync(120_000);
    });

    expect(ids(result.current.state.messages)).toEqual(ids(range(1, 4)));
    expect(syncNotice(result.current.state)).toBeFalsy();
  });

  // A healthy event is not a reconciliation: it must not make the reader
  // believe the conversation is in sync while the obligation is pending.
  it("keeps reporting the pending reconciliation when an unrelated event arrives", async () => {
    const { result } = await loadedWith(range(1, 3));
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    mockFetchChannelMessages.mockRejectedValue(new Error("503"));
    resubscribe();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(100);
    });
    expect(syncNotice(result.current.state)).toBeTruthy();

    act(() =>
      capturedOnReactionUpdated?.(reactionEvent(message(2).id, [{ emoji: "👍", users: ["u1"] }])),
    );

    expect(syncNotice(result.current.state)).toBeTruthy();
  });
});

describe("useMessages — lifecycle of an owed reconciliation (CQ947-01/02)", () => {
  it("survives an overflow followed by a failed read, and converges after both", async () => {
    const { result } = await loadedWith(range(1, 3));
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    const first = deferred<MessagePage>();
    mockFetchChannelMessages
      .mockImplementationOnce(() => first.promise)
      .mockRejectedValueOnce(new Error("503"))
      .mockImplementation(serveListing(range(1, 4)));
    resubscribe();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    act(() => {
      for (let i = 0; i <= 500; i += 1) {
        capturedOnReactionUpdated?.(
          reactionEvent(message(2).id, [{ emoji: "👍", users: [`u${i}`] }]),
        );
      }
    });
    await act(async () => first.resolve(page(range(1, 4))));

    await act(async () => {
      await vi.advanceTimersByTimeAsync(10_000);
    });

    expect(ids(result.current.state.messages)).toEqual(ids(range(1, 4)));
    expect(syncNotice(result.current.state)).toBeFalsy();
  });

  it("drops the obligation, and its timer, when the reader leaves the conversation", async () => {
    const view = await loadedWith(range(1, 3));
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    mockFetchChannelMessages.mockRejectedValueOnce(new Error("503"));
    resubscribe();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(100);
    });
    mockFetchChannelMessages.mockResolvedValue(page([message(500)]));

    view.rerender({ target: "ch-2" });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    const afterSwitch = mockFetchChannelMessages.mock.calls.length;
    await act(async () => {
      await vi.advanceTimersByTimeAsync(10 * 60_000);
    });

    expect(mockFetchChannelMessages.mock.calls.length).toBe(afterSwitch);
    expect(vi.getTimerCount()).toBe(0);
  });

  it("leaves no timer behind when the view unmounts while the obligation waits", async () => {
    const view = await loadedWith(range(1, 3));
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    mockFetchChannelMessages.mockRejectedValue(new Error("503"));
    resubscribe();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(100);
    });
    expect(vi.getTimerCount()).toBe(1);

    view.unmount();

    expect(vi.getTimerCount()).toBe(0);
  });
});

describe("useMessages — the rebase never replays a local intent (CQ947-03/04/05)", () => {
  async function readInFlight(messages: Message[]) {
    const view = await loadedWith(messages);
    const read = deferred<MessagePage>();
    mockFetchChannelMessages.mockImplementationOnce(() => read.promise);
    resubscribe();
    await waitFor(() => expect(mockFetchChannelMessages).toHaveBeenCalledTimes(2));
    return { view, read };
  }

  // CQ947-03: the read already carries the persisted edit; the optimistic one
  // must not be counted again, and its confirmation must be accepted.
  it("does not count an optimistic edit twice when the read already has it", async () => {
    const { view, read } = await readInFlight(range(1, 3));
    const confirmed = message(2, {
      bodyText: "editada",
      isEdited: true,
      editCount: 1,
      editedAt: "2026-10-05T00:00:00.000Z",
      updatedAt: "2026-10-05T00:00:00.000Z",
      linkSafetyState: "safe",
    });
    const request = deferred<Message>();
    mockEditMessage.mockImplementationOnce(() => request.promise);
    let edit!: Promise<unknown>;
    act(() => {
      edit = view.result.current.editMessageLocal(message(2).id, "editada", "v3");
    });

    await act(async () => read.resolve(page([message(1), confirmed, message(3)])));
    expect(byId(view.result.current.state.messages, 2).editCount).toBe(1);
    await act(async () => {
      request.resolve(confirmed);
      await edit;
    });

    const m2 = byId(view.result.current.state.messages, 2);
    expect(m2).toMatchObject({ bodyText: "editada", editCount: 1, editedAt: confirmed.editedAt });
    expect(m2.linkSafetyState).toBe("safe");
  });

  // An edit the read does not have yet stays drawn until it is confirmed.
  it("keeps an optimistic edit the read does not have yet", async () => {
    const { view, read } = await readInFlight(range(1, 3));
    mockEditMessage.mockImplementationOnce(() => new Promise<Message>(() => {}));
    act(() => {
      void view.result.current.editMessageLocal(message(2).id, "ainda não salva", "v3");
    });

    await act(async () => read.resolve(page(range(1, 3))));

    expect(byId(view.result.current.state.messages, 2)).toMatchObject({
      bodyText: "ainda não salva",
      editCount: 1,
    });
  });

  // CQ947-04: a confirmed toggle must not be toggled again over a read that
  // already carries it.
  it("does not resurrect a confirmed reaction intent as a phantom removal", async () => {
    const { view, read } = await readInFlight(range(1, 3));
    act(() => view.result.current.toggleReaction(message(2).id, "👍"));
    act(() =>
      capturedOnReactionUpdated?.({
        ...reactionEvent(message(2).id, [{ emoji: "👍", users: ["user-1"] }]),
        reaction: {
          ...reactionEvent(message(2).id, [{ emoji: "👍", users: ["user-1"] }]).reaction!,
          actor_user_id: "user-1",
        },
      }),
    );

    const withMine = reacted(2, "👍", ["user-1"]);
    withMine.reactions[0].reactedByMe = true;
    await act(async () => read.resolve(page([message(1), withMine, message(3)])));

    const m2 = byId(view.result.current.state.messages, 2);
    expect(m2.reactions).toHaveLength(1);
    expect(m2.reactions[0]).toMatchObject({ emoji: "👍", reactedByMe: true, count: 1 });
    expect(view.result.current.state.pendingReactions.size).toBe(0);
  });

  it("leaves no reaction deadline behind once the toggle is confirmed and the read lands", async () => {
    const { view, read } = await readInFlight(range(1, 3));
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    act(() => view.result.current.toggleReaction(message(2).id, "👍"));
    act(() =>
      capturedOnReactionUpdated?.({
        ...reactionEvent(message(2).id, [{ emoji: "👍", users: ["user-1"] }]),
        reaction: {
          ...reactionEvent(message(2).id, [{ emoji: "👍", users: ["user-1"] }]).reaction!,
          actor_user_id: "user-1",
        },
      }),
    );
    const withMine = reacted(2, "👍", ["user-1"]);
    withMine.reactions[0].reactedByMe = true;
    await act(async () => read.resolve(page([message(1), withMine, message(3)])));

    expect(vi.getTimerCount()).toBe(0);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(60_000);
    });
    expect(view.result.current.state.actionError).toBeNull();
    expect(byId(view.result.current.state.messages, 2).reactions[0]).toMatchObject({
      reactedByMe: true,
    });
  });

  it("keeps a pending reaction intent drawn over a read that does not have it yet", async () => {
    const { view, read } = await readInFlight(range(1, 3));
    act(() => view.result.current.toggleReaction(message(2).id, "👍"));

    await act(async () => read.resolve(page(range(1, 3))));

    const m2 = byId(view.result.current.state.messages, 2);
    expect(m2.reactions[0]).toMatchObject({ emoji: "👍", reactedByMe: true });
    expect(view.result.current.state.pendingReactions.get(message(2).id)?.intents.get("👍")).toBe(
      "added",
    );
  });

  // CQ947-05: the rebase is about the timeline; it must not consume the next
  // reply or end the next send.
  it("does not touch the next reply or the send in flight when the read lands", async () => {
    const { view, read } = await readInFlight(range(1, 3));
    const sentA = message(10, { senderId: "user-1", bodyText: "A" });
    mockPostChannelMessage.mockResolvedValueOnce(sentA);
    act(() => view.result.current.selectReply(message(1)));
    await act(async () => {
      await view.result.current.sendMessage("A");
    });
    act(() => view.result.current.selectReply(message(1)));
    mockPostChannelMessage.mockImplementationOnce(() => new Promise<Message>(() => {}));
    act(() => {
      void view.result.current.sendMessage("B");
    });
    expect(view.result.current.state.sending).toBe(true);

    await act(async () => read.resolve(page(range(1, 3))));

    expect(view.result.current.state.sending).toBe(true);
    expect(view.result.current.state.replyTo?.id).toBe(message(1).id);
    expect(ids(view.result.current.state.messages)).toContain(sentA.id);
  });
});

describe("useMessages — an older page from before a replacement (CQ947-07)", () => {
  it("ignores an older page requested before the timeline was replaced", async () => {
    const { result } = await loadedWith(range(101, 150), "c100");
    const server = range(1, 600);
    const listing = serveListing(server);
    const older = deferred<MessagePage>();
    mockFetchChannelMessages.mockImplementation((id, cursor) =>
      cursor === "c100" ? older.promise : listing(id, cursor),
    );
    act(() => result.current.loadMore());
    await waitFor(() => expect(result.current.state.loadingMore).toBe(true));

    resubscribe();
    await waitFor(() => expect(result.current.state.messages.at(-1)!.id).toBe(message(600).id));
    const replaced = ids(result.current.state.messages);
    const cursor = result.current.state.nextCursor;
    await act(async () => older.resolve(page(range(51, 100), "c50")));

    expect(ids(result.current.state.messages)).toEqual(replaced);
    expect(result.current.state.nextCursor).toBe(cursor);
    expect(result.current.state.loadingMore).toBe(false);
  });

  async function replacedWhileOlderPageInFlight() {
    const view = await loadedWith(range(101, 150), "c100");
    const listing = serveListing(range(1, 600));
    const older = deferred<MessagePage>();
    mockFetchChannelMessages.mockImplementation((id, cursor) =>
      cursor === "c100" ? older.promise : listing(id, cursor),
    );
    act(() => view.result.current.loadMore());
    await waitFor(() => expect(view.result.current.state.loadingMore).toBe(true));
    resubscribe();
    await waitFor(() =>
      expect(view.result.current.state.messages.at(-1)!.id).toBe(message(600).id),
    );
    return { view, older, listing };
  }

  it("ignores the failure of an older page requested before the replacement", async () => {
    const { view, older } = await replacedWhileOlderPageInFlight();
    const settled = view.result.current.state.olderPagesSettled;

    await act(async () => older.reject(new Error("503")));

    expect(view.result.current.state.olderPagesSettled).toBe(settled);
    expect(view.result.current.state.loadingMore).toBe(false);
  });

  it("pages back from the new cursor through the whole history after a replacement", async () => {
    const { view, older } = await replacedWhileOlderPageInFlight();
    await act(async () => older.resolve(page(range(51, 100), "c50")));

    while (view.result.current.state.nextCursor) {
      act(() => view.result.current.loadMore());
      await waitFor(() => expect(view.result.current.state.loadingMore).toBe(false));
    }

    expect(ids(view.result.current.state.messages)).toEqual(ids(range(1, 600)));
  });

  // A page asked for after the replacement is the one that owns the guard; the
  // older answer arriving in the middle must neither end it nor splice in.
  it("lets a page requested after the replacement own the request guard", async () => {
    const { view, older, listing } = await replacedWhileOlderPageInFlight();
    const fresh = deferred<MessagePage>();
    mockFetchChannelMessages.mockImplementation((id, cursor) =>
      cursor === "c350" ? fresh.promise : listing(id, cursor),
    );
    act(() => view.result.current.loadMore());
    await waitFor(() => expect(view.result.current.state.loadingMore).toBe(true));

    await act(async () => older.resolve(page(range(51, 100), "c50")));
    expect(view.result.current.state.loadingMore).toBe(true);
    act(() => view.result.current.loadMore());
    expect(mockFetchChannelMessages.mock.calls.filter(([, c]) => c === "c350")).toHaveLength(1);

    await act(async () => fresh.resolve(await listing("ch-1", "c350")));
    expect(view.result.current.state.messages[0].id).toBe(message(301).id);
    expect(view.result.current.state.nextCursor).toBe("c300");
  });

  it("drops an older page aborted by a change of conversation", async () => {
    const view = await loadedWith(range(101, 150), "c100");
    mockFetchChannelMessages.mockImplementation(
      (_id, cursor, signal) =>
        new Promise<MessagePage>((resolve, reject) => {
          if (cursor !== "c100") {
            resolve(page([message(900)]));
            return;
          }
          signal?.addEventListener("abort", () =>
            reject(Object.assign(new Error("aborted"), { name: "AbortError" })),
          );
        }),
    );
    act(() => view.result.current.loadMore());
    await waitFor(() => expect(view.result.current.state.loadingMore).toBe(true));

    view.rerender({ target: "ch-2" });

    await waitFor(() => expect(ids(view.result.current.state.messages)).toEqual([message(900).id]));
    expect(view.result.current.state.olderPagesSettled).toBe(0);
    expect(view.result.current.state.loadingMore).toBe(false);
  });
});

// ── Second Code Quality Review findings (CQ947-R2-01..03) ───────────────────

const blocked = (n: number): WSMessageBlockedEvent => ({
  type: "message.blocked",
  message_id: message(n).id,
  reason: "malicious_url",
});

const deletedCopy = (n: number) =>
  message(n, {
    bodyText: "",
    isRemoved: true,
    status: "deleted",
    deletedAt: "2026-10-03T00:00:00.000Z",
    updatedAt: "2026-10-03T00:00:00.000Z",
  });

describe("useMessages — a refusal delivered during the read is never undone by it (CQ947-R2-01)", () => {
  // M4 is the reader's own message, still being scanned.
  const pending = message(4, { senderId: "user-1", status: "pending_link_scan" });

  async function scanning(held: Message[] = [...range(1, 3), pending]) {
    const view = await loadedWith(held);
    const read = deferred<MessagePage>();
    mockFetchChannelMessages.mockImplementationOnce(() => read.promise);
    resubscribe();
    await waitFor(() => expect(mockFetchChannelMessages).toHaveBeenCalledTimes(2));
    return { view, read };
  }

  it("keeps a message blocked during the read out of a listing that still has it", async () => {
    const { view, read } = await scanning();
    act(() => capturedOnMessageBlocked?.(blocked(4)));
    expect(ids(view.result.current.state.messages)).toEqual(ids(range(1, 3)));

    await act(async () => read.resolve(page([...range(1, 3), pending])));

    expect(ids(view.result.current.state.messages)).toEqual(ids(range(1, 3)));
    expect(syncNotice(view.result.current.state)).toBeFalsy();
    expect(mockFetchChannelMessages).toHaveBeenCalledTimes(2);
  });

  it("does not repeat the refusal's composer effects over the next send", async () => {
    const { view, read } = await scanning();
    act(() => capturedOnMessageBlocked?.(blocked(4)));
    expect(view.result.current.state.sendError).toBeTruthy();
    mockPostChannelMessage.mockImplementationOnce(() => new Promise<Message>(() => {}));
    act(() => {
      void view.result.current.sendMessage("de novo");
    });

    await act(async () => read.resolve(page([...range(1, 3), pending])));

    expect(view.result.current.state.sending).toBe(true);
    expect(view.result.current.state.sendError).toBeNull();
  });

  it("withdraws only the message the refusal names", async () => {
    const other = message(5, { senderId: "user-1", status: "pending_link_scan" });
    const { view, read } = await scanning([...range(1, 3), pending, other]);
    act(() => capturedOnMessageBlocked?.(blocked(5)));

    await act(async () => read.resolve(page([...range(1, 3), pending, other])));

    expect(ids(view.result.current.state.messages)).toEqual(ids([...range(1, 3), pending]));
  });

  it("keeps several refusals, and a listing that no longer has them changes nothing", async () => {
    const other = message(5, { senderId: "user-1", status: "pending_link_scan" });
    const { view, read } = await scanning([...range(1, 3), pending, other]);
    act(() => capturedOnMessageBlocked?.(blocked(4)));
    act(() => capturedOnMessageBlocked?.(blocked(5)));

    await act(async () => read.resolve(page(range(1, 3))));

    expect(ids(view.result.current.state.messages)).toEqual(ids(range(1, 3)));
  });

  // The identity is the id: a new message with the same text is not the refused one.
  it("lets a new message with the same text through", async () => {
    const { view, read } = await scanning();
    act(() => capturedOnMessageBlocked?.(blocked(4)));
    const again = message(6, { senderId: "user-1", bodyText: pending.bodyText });

    await act(async () => read.resolve(page([...range(1, 3), pending, again])));

    expect(ids(view.result.current.state.messages)).toEqual(ids([...range(1, 3), again]));
  });

  it("keeps the refusal through a repeated resync signal", async () => {
    const { view, read } = await scanning();
    act(() => capturedOnMessageBlocked?.(blocked(4)));
    const second = deferred<MessagePage>();
    mockFetchChannelMessages.mockImplementationOnce(() => second.promise);
    resubscribe();
    await waitFor(() => expect(mockFetchChannelMessages).toHaveBeenCalledTimes(3));

    await act(async () => read.resolve(page([...range(1, 3), pending])));
    await act(async () => second.resolve(page(range(1, 3))));

    expect(ids(view.result.current.state.messages)).toEqual(ids(range(1, 3)));
  });

  it("keeps the refusal when the read overflowed and the retry converges", async () => {
    const { view, read } = await scanning();
    mockFetchChannelMessages.mockImplementation(serveListing(range(1, 3)));
    act(() => {
      capturedOnMessageBlocked?.(blocked(4));
      for (let i = 0; i <= 500; i += 1) {
        capturedOnReactionUpdated?.(
          reactionEvent(message(2).id, [{ emoji: "👍", users: [`u${i}`] }]),
        );
      }
    });

    await act(async () => read.resolve(page([...range(1, 3), pending])));

    await waitFor(() => expect(syncNotice(view.result.current.state)).toBeFalsy(), {
      timeout: 5_000,
    });
    expect(ids(view.result.current.state.messages)).toEqual(ids(range(1, 3)));
  }, 10_000);

  it("withdraws a message blocked after the read landed", async () => {
    const { view, read } = await scanning();
    await act(async () => read.resolve(page([...range(1, 3), pending])));

    act(() => capturedOnMessageBlocked?.(blocked(4)));

    expect(ids(view.result.current.state.messages)).toEqual(ids(range(1, 3)));
  });
});

describe("useMessages — a reply whose parent the read finds deleted (CQ947-R2-02)", () => {
  async function replyingTo(n: number) {
    const view = await loadedWith(range(1, 3));
    act(() => view.result.current.selectReply(message(n)));
    const read = deferred<MessagePage>();
    mockFetchChannelMessages.mockImplementationOnce(() => read.promise);
    resubscribe();
    await waitFor(() => expect(mockFetchChannelMessages).toHaveBeenCalledTimes(2));
    return { view, read };
  }

  it("drops the reply to a message the read says was deleted, and nothing else", async () => {
    const { view, read } = await replyingTo(2);
    mockPostChannelMessage.mockImplementationOnce(() => new Promise<Message>(() => {}));
    act(() => {
      void view.result.current.sendMessage("em voo");
    });

    await act(async () => read.resolve(page([message(1), deletedCopy(2), message(3)])));

    const state = view.result.current.state;
    expect(byId(state.messages, 2)).toMatchObject({ isRemoved: true, bodyText: "" });
    expect(state.replyTo).toBeNull();
    expect(state.sending).toBe(true);
  });

  it("keeps a reply to a message the read still has", async () => {
    const { view, read } = await replyingTo(2);

    await act(async () => read.resolve(page([message(1), message(2), deletedCopy(3)])));

    expect(view.result.current.state.replyTo?.id).toBe(message(2).id);
  });

  // The identity is read when the read is applied, not when it was requested.
  it("keeps a reply chosen while the read was in flight", async () => {
    const { view, read } = await replyingTo(1);
    act(() => view.result.current.selectReply(message(2)));

    await act(async () => read.resolve(page([deletedCopy(1), message(2), message(3)])));

    expect(view.result.current.state.replyTo?.id).toBe(message(2).id);
  });
});

describe("useMessages — the backoff leaves nothing on the signal (CQ947-R2-03)", () => {
  /** The abort listeners currently registered on the catch-up's own signal. */
  function failingAndWatched() {
    const live = new Set<EventListenerOrEventListenerObject>();
    const watched = new WeakSet<AbortSignal>();
    mockFetchChannelMessages.mockImplementation(async (_id, _cursor, signal) => {
      if (signal && !watched.has(signal)) {
        watched.add(signal);
        const add = signal.addEventListener.bind(signal);
        const remove = signal.removeEventListener.bind(signal);
        vi.spyOn(signal, "addEventListener").mockImplementation((type, listener, options) => {
          if (type === "abort" && listener) live.add(listener);
          add(type, listener, options);
        });
        vi.spyOn(signal, "removeEventListener").mockImplementation((type, listener, options) => {
          if (type === "abort" && listener) live.delete(listener);
          remove(type, listener, options);
        });
        // An abort ends every wait at once; `once` drops what fired.
        signal.addEventListener("abort", () => live.clear());
        live.clear();
      }
      throw new Error("503");
    });
    return live;
  }

  it.each([1, 50, 100])("holds at most the current wait's listener after %i retries", async (n) => {
    const { result } = await loadedWith(range(1, 3));
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    const live = failingAndWatched();
    resubscribe();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });

    for (let tries = 0; tries < n; tries += 1) {
      const wait = catchUpRetryDelaysMs[Math.min(tries, catchUpRetryDelaysMs.length - 1)];
      await act(async () => {
        await vi.advanceTimersByTimeAsync(wait);
      });
      expect(live.size).toBe(1);
    }

    expect(mockFetchChannelMessages).toHaveBeenCalledTimes(n + 2);
    expect(vi.getTimerCount()).toBe(1);
    expect(syncNotice(result.current.state)).toBeTruthy();
  });

  it("releases the wait's listener when REST recovers", async () => {
    const { result } = await loadedWith(range(1, 3));
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    const live = failingAndWatched();
    resubscribe();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    mockFetchChannelMessages.mockImplementation(serveListing(range(1, 4)));

    await act(async () => {
      await vi.advanceTimersByTimeAsync(catchUpRetryDelaysMs[0]);
    });

    expect(ids(result.current.state.messages)).toEqual(ids(range(1, 4)));
    expect(live.size).toBe(0);
    expect(vi.getTimerCount()).toBe(0);
  });

  it("starts no attempt, and keeps no listener, once the wait is cancelled", async () => {
    const view = await loadedWith(range(1, 3));
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    failingAndWatched();
    resubscribe();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    const calls = mockFetchChannelMessages.mock.calls.length;

    view.unmount();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(10 * 60_000);
    });

    expect(mockFetchChannelMessages.mock.calls.length).toBe(calls);
    expect(vi.getTimerCount()).toBe(0);
  });
});

describe("useMessages — a message read back during the catch-up keeps its reactions (CQ947-R3-01)", () => {
  /** A message.updated that names the message and nothing else: it is read back. */
  const routeOnlyUpdate = (n: number): WSMessageUpdatedEvent =>
    ({
      type: "message.updated",
      target_type: "channel",
      target_id: "ch-1",
      message_id: message(n).id,
    }) as WSMessageUpdatedEvent;

  /** Announces message n and lets its single-message read land. */
  async function readBack(copy: Message) {
    const n = Number(copy.id.slice(2));
    mockFetchChannelMessage.mockResolvedValueOnce(copy);
    act(() => capturedOnMessageUpdated?.(routeOnlyUpdate(n)));
    await waitFor(() => expect(mockFetchChannelMessage).toHaveBeenCalled());
    await act(async () => {});
  }

  beforeEach(() => mockFetchChannelMessage.mockReset());

  // The reaction moved no updated_at: the read and the listing carry the same
  // version of message 2, and only the read has the reaction.
  it("keeps the reaction when the stale listing lands after the read", async () => {
    const view = await loadedWith(range(1, 3));
    const listing = deferred<MessagePage>();
    mockFetchChannelMessages.mockImplementationOnce(() => listing.promise);
    resubscribe();
    await waitFor(() => expect(mockFetchChannelMessages).toHaveBeenCalledTimes(2));

    await readBack(reacted(2, "👍", ["user-3"]));
    expect(byId(view.result.current.state.messages, 2).reactions).toHaveLength(1);
    await act(async () => listing.resolve(page(range(1, 3))));

    const state = view.result.current.state;
    expect(byId(state.messages, 2).reactions[0]).toMatchObject({ emoji: "👍", count: 1 });
    expect(syncNotice(state)).toBeFalsy();
    expect(ids(state.messages)).toEqual(ids(range(1, 3)));
  });

  it("keeps the reaction when the read replaces the timeline", async () => {
    const view = await loadedWith(range(1, 3));
    const first = deferred<MessagePage>();
    mockFetchChannelMessages.mockImplementationOnce(() => first.promise);
    for (let p = 1; p < catchUpMaxPages; p += 1) {
      const newest = 1000 - p * 50;
      mockFetchChannelMessages.mockResolvedValueOnce(page(range(newest - 49, newest), `p${p + 1}`));
    }
    resubscribe();
    await waitFor(() => expect(mockFetchChannelMessages).toHaveBeenCalledTimes(2));
    act(() =>
      capturedOnMessageCreated?.({
        type: "message.created",
        target_type: "channel",
        target_id: "ch-1",
        message_id: message(1000).id,
        payload: payloadOf(message(1000)),
      } as WSMessageCreatedEvent),
    );

    await readBack(reacted(1000, "👍", ["user-3"]));
    expect(byId(view.result.current.state.messages, 1000).reactions).toHaveLength(1);
    await act(async () => first.resolve(page(range(951, 1000), "p1")));

    const state = view.result.current.state;
    expect(state.messages).toHaveLength(50 * catchUpMaxPages);
    expect(byId(state.messages, 1000).reactions[0]).toMatchObject({ emoji: "👍", count: 1 });
    expect(syncNotice(state)).toBeFalsy();
  });
});
