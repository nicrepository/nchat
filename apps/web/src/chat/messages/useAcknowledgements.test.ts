/**
 * The acknowledgement cache and its one write (issue #824).
 *
 * These are the rules that are not visible in the markup: which reads happen
 * and when, what a double click costs, what a reconnect re-reads, and — the
 * rule #820 exists to protect — that nothing except the explicit action ever
 * posts a confirmation.
 */

import { act, renderHook, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { useAcknowledgements } from "./useAcknowledgements";
import { useRequestRegistry } from "./useRequestRegistry";
import type { ConversationScope } from "./useConversationScope";
import type { Message, MessageAcknowledgement } from "../chatTypes";

const { mockFetch, mockFetchBatch, mockAcknowledge } = vi.hoisted(() => ({
  mockFetch: vi.fn<(id: string, signal?: AbortSignal) => Promise<MessageAcknowledgement>>(),
  mockFetchBatch:
    vi.fn<
      (ids: string[], signal?: AbortSignal) => Promise<Record<string, MessageAcknowledgement>>
    >(),
  mockAcknowledge: vi.fn<(id: string, signal?: AbortSignal) => Promise<MessageAcknowledgement>>(),
}));

vi.mock("../chatApi", () => ({
  fetchMessageAcknowledgement: (id: string, signal?: AbortSignal) => mockFetch(id, signal),
  fetchMessageAcknowledgements: (ids: string[], signal?: AbortSignal) =>
    mockFetchBatch(ids, signal),
  acknowledgeMessage: (id: string, signal?: AbortSignal) => mockAcknowledge(id, signal),
}));

function summaryFor(messageId: string, overrides: Partial<MessageAcknowledgement> = {}) {
  return {
    messageId,
    required: true,
    total: 3,
    pending: 2,
    acknowledged: 1,
    responded: 0,
    expired: 0,
    cancelled: 0,
    viewerState: "pending" as const,
    ...overrides,
  };
}

function messageFor(id: string, acknowledgementRequired: boolean): Message {
  return {
    id,
    senderId: "user-1",
    senderDisplayName: "Alex",
    senderEmail: "alex@example.test",
    kind: "user",
    bodyText: "olá",
    bodyFormat: "v2",
    isRemoved: false,
    status: "active",
    deletedAt: null,
    createdAt: "2026-09-11T12:00:00Z",
    updatedAt: "2026-09-11T12:00:00Z",
    isEdited: false,
    editCount: 0,
    reactions: [],
    isFavorited: false,
    isForwarded: false,
    acknowledgementRequired,
  };
}

/** A scope pinned to one conversation, holding the messages a test names. */
function scopeFor(messages: Message[], key = "channel:ch-1"): ConversationScope {
  return {
    kind: "channel",
    targetId: "ch-1",
    key,
    isCurrent: (candidate: string) => candidate === key,
    isRendered: (id: string) => messages.some((message) => message.id === id),
    messages: () => messages,
    replyTo: () => null,
    sanitize: (message: Message) => message,
    rememberDeleted: () => undefined,
    retarget: () => undefined,
  };
}

function renderAcknowledgements(messages: Message[], scope?: ConversationScope) {
  return renderHook(
    // A bare rerender() passes no props, so the initial page is the default —
    // which is exactly the "same page rendered again" case these tests check.
    (props?: { messages: Message[] }) => {
      const current = props?.messages ?? messages;
      const requests = useRequestRegistry();
      return useAcknowledgements({
        scope: scope ?? scopeFor(current),
        messages: current,
        requests,
      });
    },
    { initialProps: { messages } },
  );
}

/** The batch answers for every id it was given, as the server does. */
function batchOf(ids: string[], overrides: Partial<MessageAcknowledgement> = {}) {
  return Object.fromEntries(ids.map((id) => [id, { ...summaryFor(id), ...overrides }]));
}

beforeEach(() => {
  mockFetch.mockReset();
  mockFetchBatch.mockReset();
  mockAcknowledge.mockReset();
  mockFetch.mockImplementation((id: string) => Promise.resolve(summaryFor(id)));
  mockFetchBatch.mockImplementation((ids: string[]) => Promise.resolve(batchOf(ids)));
  mockAcknowledge.mockImplementation((id: string) =>
    Promise.resolve(summaryFor(id, { viewerState: "acknowledged", acknowledged: 2, pending: 1 })),
  );
});

describe("useAcknowledgements", () => {
  // The correction: a page is one request, however many of its messages asked
  // for confirmation.
  it("reads the whole page in one request", async () => {
    const { result } = renderAcknowledgements([
      messageFor("msg-1", true),
      messageFor("msg-2", true),
      messageFor("msg-3", true),
    ]);
    await waitFor(() => expect(Object.keys(result.current.summaries)).toHaveLength(3));
    expect(mockFetchBatch).toHaveBeenCalledTimes(1);
    expect(mockFetchBatch).toHaveBeenCalledWith(["msg-1", "msg-2", "msg-3"], expect.anything());
    // Never one call per message, which is what this replaces.
    expect(mockFetch).not.toHaveBeenCalled();
  });

  // Ordinary messages are not asked about at all, so a normal conversation
  // still costs nothing.
  it("asks only about the messages that requested confirmation", async () => {
    const { result } = renderAcknowledgements([
      messageFor("msg-1", true),
      messageFor("msg-plain", false),
    ]);
    await waitFor(() => expect(result.current.summaries["msg-1"]).toBeDefined());
    expect(mockFetchBatch).toHaveBeenCalledWith(["msg-1"], expect.anything());
  });

  // An answer the server withheld leaves no entry, so a client can tell
  // "not readable" from "asked nobody".
  it("keeps only the entries the server returned", async () => {
    mockFetchBatch.mockImplementation((ids: string[]) => Promise.resolve(batchOf(ids.slice(0, 1))));
    const { result } = renderAcknowledgements([
      messageFor("msg-1", true),
      messageFor("msg-2", true),
    ]);
    await waitFor(() => expect(result.current.summaries["msg-1"]).toBeDefined());
    expect(result.current.summaries["msg-2"]).toBeUndefined();
  });

  // The common case costs nothing: a conversation of ordinary messages issues
  // no request at all, which is what keeps this feature free for everybody who
  // is not using it.
  it("reads nothing for a conversation of ordinary messages", async () => {
    renderAcknowledgements([messageFor("msg-1", false), messageFor("msg-2", false)]);
    await Promise.resolve();
    expect(mockFetch).not.toHaveBeenCalled();
  });

  // Reading a message is not confirming it (#820: DELIVERED != READ !=
  // ACKNOWLEDGED). Loading the summaries must never post one.
  it("never confirms anything while loading summaries", async () => {
    const { result } = renderAcknowledgements([messageFor("msg-1", true)]);
    await waitFor(() => expect(result.current.summaries["msg-1"]).toBeDefined());
    expect(mockAcknowledge).not.toHaveBeenCalled();
  });

  it("stores the authoritative summary the confirmation returned", async () => {
    const { result } = renderAcknowledgements([messageFor("msg-1", true)]);
    await waitFor(() => expect(result.current.summaries["msg-1"]).toBeDefined());

    act(() => result.current.acknowledge("msg-1"));
    await waitFor(() =>
      expect(result.current.summaries["msg-1"]?.viewerState).toBe("acknowledged"),
    );
    expect(result.current.summaries["msg-1"]?.acknowledged).toBe(2);
    expect(result.current.pendingId).toBeNull();
  });

  it("turns a double click into one request", async () => {
    let resolve: ((summary: MessageAcknowledgement) => void) | undefined;
    mockAcknowledge.mockImplementation(
      () =>
        new Promise<MessageAcknowledgement>((done) => {
          resolve = done;
        }),
    );
    const { result } = renderAcknowledgements([messageFor("msg-1", true)]);
    await waitFor(() => expect(result.current.summaries["msg-1"]).toBeDefined());

    act(() => result.current.acknowledge("msg-1"));
    act(() => result.current.acknowledge("msg-1"));
    expect(mockAcknowledge).toHaveBeenCalledTimes(1);
    expect(result.current.pendingId).toBe("msg-1");

    await act(async () => {
      resolve?.(summaryFor("msg-1", { viewerState: "acknowledged" }));
    });
    expect(result.current.pendingId).toBeNull();
  });

  it("reports a failure and leaves the stored state alone", async () => {
    mockAcknowledge.mockRejectedValue(new Error("network"));
    const { result } = renderAcknowledgements([messageFor("msg-1", true)]);
    await waitFor(() => expect(result.current.summaries["msg-1"]).toBeDefined());

    act(() => result.current.acknowledge("msg-1"));
    await waitFor(() => expect(result.current.error).not.toBeNull());
    // A failed write says nothing about the acknowledgement, so the last answer
    // the server gave still stands.
    expect(result.current.summaries["msg-1"]?.viewerState).toBe("pending");
    expect(result.current.pendingId).toBeNull();
  });

  // A reconnect is this client's only signal that it may have missed
  // something, so it re-asks rather than trusting what it cached off-socket.
  // A message the server withheld must not be asked about again on every
  // render: "already asked" is remembered separately from "has an answer", or a
  // page containing one unreadable id would loop forever.
  it("does not re-ask about a message the server withheld", async () => {
    mockFetchBatch.mockImplementation((ids: string[]) =>
      Promise.resolve(batchOf(ids.filter((id) => id !== "msg-2"))),
    );
    const { result, rerender } = renderAcknowledgements([
      messageFor("msg-1", true),
      messageFor("msg-2", true),
    ]);
    await waitFor(() => expect(result.current.summaries["msg-1"]).toBeDefined());
    expect(result.current.summaries["msg-2"]).toBeUndefined();

    rerender();
    rerender();
    expect(mockFetchBatch).toHaveBeenCalledTimes(1);
  });

  // Paging in older messages asks only about what arrived, never about the page
  // already answered.
  it("asks only about newly loaded messages when the page grows", async () => {
    const { result, rerender } = renderAcknowledgements([messageFor("msg-1", true)]);
    await waitFor(() => expect(result.current.summaries["msg-1"]).toBeDefined());
    mockFetchBatch.mockClear();

    rerender({ messages: [messageFor("msg-0", true), messageFor("msg-1", true)] });
    await waitFor(() => expect(mockFetchBatch).toHaveBeenCalledTimes(1));
    expect(mockFetchBatch).toHaveBeenCalledWith(["msg-0"], expect.anything());
  });

  it("re-reads the page in one request when the subscription comes back", async () => {
    const { result } = renderAcknowledgements([
      messageFor("msg-1", true),
      messageFor("msg-2", true),
    ]);
    await waitFor(() => expect(Object.keys(result.current.summaries)).toHaveLength(2));
    mockFetchBatch.mockClear();
    mockFetchBatch.mockImplementation((ids: string[]) =>
      Promise.resolve(batchOf(ids, { viewerState: "responded" })),
    );

    act(() => result.current.reconcile());
    await waitFor(() => expect(result.current.summaries["msg-1"]?.viewerState).toBe("responded"));
    // A reconnect asks about everything on screen, and still only once.
    expect(mockFetchBatch).toHaveBeenCalledTimes(1);
    expect(mockFetchBatch).toHaveBeenCalledWith(["msg-1", "msg-2"], expect.anything());
  });

  // The reload case: a fresh mount holds nothing and rebuilds the whole state
  // from the server, because none of it ever lived only in React.
  it("rebuilds the state from the server on a fresh mount", async () => {
    mockFetchBatch.mockImplementation((ids: string[]) =>
      Promise.resolve(batchOf(ids, { viewerState: "acknowledged" })),
    );
    const { result } = renderAcknowledgements([messageFor("msg-1", true)]);
    expect(result.current.summaries["msg-1"]).toBeUndefined();
    await waitFor(() =>
      expect(result.current.summaries["msg-1"]?.viewerState).toBe("acknowledged"),
    );
  });

  it("does not re-read a summary it already holds", async () => {
    const { result, rerender } = renderAcknowledgements([messageFor("msg-1", true)]);
    await waitFor(() => expect(result.current.summaries["msg-1"]).toBeDefined());
    mockFetch.mockClear();
    rerender();
    await Promise.resolve();
    expect(mockFetch).not.toHaveBeenCalled();
  });

  // An answer that arrives after the reader has moved on describes a
  // conversation that is no longer on screen, and is discarded rather than
  // shown against the one that is.
  it("discards an answer for a conversation the reader has left", async () => {
    let currentKey = "channel:ch-1";
    const messages = [messageFor("msg-1", true)];
    const movingScope: ConversationScope = {
      ...scopeFor(messages),
      get key() {
        return currentKey;
      },
      isCurrent: (candidate: string) => candidate === currentKey,
    };
    let resolve: ((page: Record<string, MessageAcknowledgement>) => void) | undefined;
    mockFetchBatch.mockImplementation(
      () =>
        new Promise<Record<string, MessageAcknowledgement>>((done) => {
          resolve = done;
        }),
    );

    const { result } = renderAcknowledgements(messages, movingScope);
    await waitFor(() => expect(mockFetchBatch).toHaveBeenCalled());
    currentKey = "channel:ch-2";
    await act(async () => {
      resolve?.(batchOf(["msg-1"]));
    });
    expect(result.current.summaries["msg-1"]).toBeUndefined();
  });

  // A read that fails leaves the cache exactly as it was: absence of an answer
  // is not an answer.
  it("keeps the last known summary when a re-read fails", async () => {
    const { result } = renderAcknowledgements([messageFor("msg-1", true)]);
    await waitFor(() => expect(result.current.summaries["msg-1"]?.viewerState).toBe("pending"));

    mockFetchBatch.mockRejectedValue(new Error("network"));
    act(() => result.current.reconcile());
    await waitFor(() => expect(mockFetchBatch).toHaveBeenCalledTimes(2));
    expect(result.current.summaries["msg-1"]?.viewerState).toBe("pending");
  });

  // A second render while the first read is still running does not start a
  // second one: the registry keys them by message.
  it("does not start a second page read while the first is still running", async () => {
    mockFetchBatch.mockImplementation(
      () => new Promise<Record<string, MessageAcknowledgement>>(() => undefined),
    );
    const { rerender } = renderAcknowledgements([messageFor("msg-1", true)]);
    await waitFor(() => expect(mockFetchBatch).toHaveBeenCalledTimes(1));
    rerender();
    rerender();
    expect(mockFetchBatch).toHaveBeenCalledTimes(1);
  });

  // A confirmation that fails after the reader left reports nothing: the banner
  // belongs to the conversation the action was taken in.
  it("reports no error for a conversation the reader has left", async () => {
    let currentKey = "channel:ch-1";
    const messages = [messageFor("msg-1", true)];
    const movingScope: ConversationScope = {
      ...scopeFor(messages),
      get key() {
        return currentKey;
      },
      isCurrent: (candidate: string) => candidate === currentKey,
    };
    let reject: ((reason: Error) => void) | undefined;
    mockAcknowledge.mockImplementation(
      () =>
        new Promise<MessageAcknowledgement>((_, fail) => {
          reject = fail;
        }),
    );

    const { result } = renderAcknowledgements(messages, movingScope);
    await waitFor(() => expect(result.current.summaries["msg-1"]).toBeDefined());
    act(() => result.current.acknowledge("msg-1"));
    currentKey = "channel:ch-2";
    await act(async () => {
      reject?.(new Error("network"));
    });
    expect(result.current.error).toBeNull();
  });

  // ── targeted reconciliation (issue #824 realtime) ──────────────────────────
  //
  // A realtime event names one message, so exactly one message is re-read. The
  // alternative — reconciling the conversation — would turn one person's click
  // into a request per asking message on every open session.

  it("re-reads only the message the event named", async () => {
    const { result } = renderAcknowledgements([
      messageFor("msg-1", true),
      messageFor("msg-2", true),
    ]);
    await waitFor(() => expect(Object.keys(result.current.summaries)).toHaveLength(2));
    mockFetch.mockClear();
    mockFetch.mockImplementation((id: string) =>
      Promise.resolve(summaryFor(id, { viewerState: "acknowledged" })),
    );

    act(() => result.current.reconcileOne("msg-1"));
    await waitFor(() =>
      expect(result.current.summaries["msg-1"]?.viewerState).toBe("acknowledged"),
    );
    expect(mockFetch).toHaveBeenCalledTimes(1);
    expect(mockFetch).toHaveBeenCalledWith("msg-1", expect.anything());
    // The other message was never asked about, and still reads as it did.
    expect(result.current.summaries["msg-2"]?.viewerState).toBe("pending");
  });

  // A redelivered event costs one read, not one per delivery: the registry keys
  // reads by message, and the answer is authoritative whenever it lands.
  it("collapses a burst of events for one message into a single read", async () => {
    const { result } = renderAcknowledgements([messageFor("msg-1", true)]);
    await waitFor(() => expect(result.current.summaries["msg-1"]).toBeDefined());
    mockFetch.mockImplementation(() => new Promise<MessageAcknowledgement>(() => undefined));

    act(() => {
      result.current.reconcileOne("msg-1");
      result.current.reconcileOne("msg-1");
      result.current.reconcileOne("msg-1");
    });
    // Targeted and deduplicated: the registry keys reads by message.
    expect(mockFetch).toHaveBeenCalledTimes(1);
  });

  // An event for a message this view does not hold has nothing to update, so it
  // costs no request at all.
  it("ignores an event for a message that is not on screen", async () => {
    const { result } = renderAcknowledgements([messageFor("msg-1", true)]);
    await waitFor(() => expect(result.current.summaries["msg-1"]).toBeDefined());
    mockFetch.mockClear();

    act(() => result.current.reconcileOne("msg-not-loaded"));
    expect(mockFetch).not.toHaveBeenCalled();
  });

  // The event is a hint, never a confirmation: receiving one must not record an
  // acknowledgement on this reader's behalf.
  it("never confirms anything because an event arrived", async () => {
    const { result } = renderAcknowledgements([messageFor("msg-1", true)]);
    await waitFor(() => expect(result.current.summaries["msg-1"]).toBeDefined());

    act(() => result.current.reconcileOne("msg-1"));
    await waitFor(() => expect(mockFetch).toHaveBeenCalled());
    expect(mockAcknowledge).not.toHaveBeenCalled();
  });
});
