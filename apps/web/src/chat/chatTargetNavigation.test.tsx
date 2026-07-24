/**
 * Target-navigation regression tests (CHAT-378).
 *
 * React Router keeps the same <ChatMessageArea /> instance mounted when the
 * route changes between /chat/dm/:id and /chat/channel/:id — only the `kind`
 * prop and the :id param change. These tests drive the real <App /> through a
 * real router so that every switch exercises the in-place prop update instead
 * of a fresh mount, and assert the message area survives it.
 *
 * All chat HTTP calls are mocked at the chatApi module level; the WebSocket is
 * a controllable fake so subscription cleanup can be asserted.
 */

import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import App from "../App";
import { clearTokens, setTokens } from "../lib/authSession";
import type { Message, MessagePage } from "./chatTypes";

// ── chatApi mock ──────────────────────────────────────────────────────────────

const { api } = vi.hoisted(() => ({
  api: {
    fetchSidebarData: vi.fn(),
    fetchChannelMessages: vi.fn(),
    fetchDMMessages: vi.fn(),
    fetchPins: vi.fn(),
    fetchAllowedReactionEmojis: vi.fn(),
    postChannelMessage: vi.fn(),
    postDMMessage: vi.fn(),
    fetchMentionCandidates: vi.fn(),
  },
}));

vi.mock("./chatApi", async (importOriginal) => ({
  ...(await importOriginal<typeof import("./chatApi")>()),
  fetchSidebarData: api.fetchSidebarData,
  fetchChannelMessages: api.fetchChannelMessages,
  fetchDMMessages: api.fetchDMMessages,
  fetchPins: api.fetchPins,
  fetchAllowedReactionEmojis: api.fetchAllowedReactionEmojis,
  postChannelMessage: api.postChannelMessage,
  postDMMessage: api.postDMMessage,
  fetchMentionCandidates: api.fetchMentionCandidates,
}));

// ── WebSocket fake ────────────────────────────────────────────────────────────

class FakeWebSocket {
  static OPEN = 1;
  static CLOSED = 3;
  static instances: FakeWebSocket[] = [];

  readyState = FakeWebSocket.OPEN;
  sent: string[] = [];
  closed = false;

  onopen: (() => void) | null = null;
  onmessage: ((event: MessageEvent) => void) | null = null;
  onerror: (() => void) | null = null;
  onclose: (() => void) | null = null;

  constructor() {
    FakeWebSocket.instances.push(this);
    queueMicrotask(() => this.onopen?.());
  }

  send(data: string) {
    this.sent.push(data);
  }

  close() {
    this.closed = true;
    this.readyState = FakeWebSocket.CLOSED;
  }

  subscriptions(): Array<{ type: string; target_type: string; target_id: string }> {
    return this.sent.map((raw) => JSON.parse(raw) as never);
  }
}

const OriginalWebSocket = global.WebSocket;

// ── Fixtures ──────────────────────────────────────────────────────────────────

const channelId = "11111111-1111-4111-8111-111111111111";
const dmId = "22222222-2222-4222-8222-222222222222";
/** Second pair of targets, used by the same-kind draft-isolation tests. */
const secretChannelId = "33333333-3333-4333-8333-333333333333";
const otherDmId = "44444444-4444-4444-8444-444444444444";

function message(id: string, bodyText: string): Message {
  return {
    id,
    senderId: "sender-1",
    senderDisplayName: "Alice",
    senderEmail: "alice@example.com",
    kind: "user",
    bodyText,
    bodyFormat: "v2",
    isRemoved: false,
    status: "active",
    deletedAt: null,
    createdAt: "2026-01-01T10:00:00Z",
    updatedAt: "2026-01-01T10:00:00Z",
    isEdited: false,
    editCount: 0,
    reactions: [],
    isFavorited: false,
    isForwarded: false,
  };
}

const channelText = "mensagem publicada no canal";
const dmText = "mensagem enviada na conversa";

function page(messages: Message[]): MessagePage {
  return { messages, nextCursor: "" };
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

async function clickChannel() {
  await userEvent.click(await screen.findByRole("option", { name: /Canal geral/i }));
}

async function clickSecretChannel() {
  await userEvent.click(await screen.findByRole("option", { name: /Canal privado confidencial/i }));
}

async function clickDM() {
  await userEvent.click(
    await screen.findByRole("option", { name: /Mensagem direta com Juliane/i }),
  );
}

async function clickOtherDM() {
  await userEvent.click(await screen.findByRole("option", { name: /Mensagem direta com Marcos/i }));
}

function header() {
  return screen.getByTestId("chat-msg-header");
}

// ── Composer helpers ──────────────────────────────────────────────────────────

/**
 * Types into the production TipTap editor through a paste event — the same
 * public path ChatComposer.test.tsx uses, since ProseMirror ignores synthetic
 * per-character key events in JSDOM.
 */
async function typeDraft(text: string) {
  const input = await screen.findByTestId("chat-composer-input");
  fireEvent.paste(input, {
    clipboardData: {
      files: [],
      types: ["text/plain"],
      getData: (type: string) => (type === "text/plain" ? text : ""),
    },
  });
  await waitFor(() => expect(input).toHaveTextContent(text));
  await waitFor(() => expect(screen.getByTestId("chat-send-btn")).toBeEnabled());
  return input;
}

/** The composer must be empty and unable to send. */
async function expectEmptyComposer(leakedText: string) {
  const input = await screen.findByTestId("chat-composer-input");
  await waitFor(() => expect(input).not.toHaveTextContent(leakedText));
  expect(input.textContent ?? "").toBe("");
  expect(screen.getByTestId("chat-send-btn")).toBeDisabled();
}

beforeEach(() => {
  clearTokens();
  setTokens("test-token");
  vi.clearAllMocks();
  FakeWebSocket.instances = [];
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  global.WebSocket = FakeWebSocket as any;

  api.fetchSidebarData.mockResolvedValue({
    currentUserId: "me-1",
    channels: [
      { id: channelId, name: "geral", type: "public" },
      { id: secretChannelId, name: "confidencial", type: "private" },
    ],
    dms: [
      { id: dmId, type: "1:1", name: "Juliane", participants: [] },
      { id: otherDmId, type: "1:1", name: "Marcos", participants: [] },
    ],
  });
  api.fetchChannelMessages.mockResolvedValue(page([message("m-chan", channelText)]));
  api.fetchDMMessages.mockResolvedValue(page([message("m-dm", dmText)]));
  api.fetchPins.mockResolvedValue([]);
  api.fetchAllowedReactionEmojis.mockResolvedValue(["👍"]);
  api.postChannelMessage.mockResolvedValue(message("m-new", "enviada"));
  api.postDMMessage.mockResolvedValue(message("m-new", "enviada"));
  api.fetchMentionCandidates.mockResolvedValue([]);
});

afterEach(() => {
  global.WebSocket = OriginalWebSocket;
  clearTokens();
});

function renderAt(path: string) {
  window.history.pushState({}, "", path);
  return render(<App />);
}

// ── Tests ─────────────────────────────────────────────────────────────────────

describe("navigating between DM and channel targets", () => {
  it("renders the channel after leaving a DM", async () => {
    renderAt(`/chat/dm/${dmId}`);
    expect(await screen.findByText(dmText)).toBeInTheDocument();

    await clickChannel();

    await waitFor(() => expect(window.location.pathname).toBe(`/chat/channel/${channelId}`));
    expect(await screen.findByTestId("chat-message-area")).toBeInTheDocument();
    expect(
      await within(await screen.findByTestId("chat-msg-header")).findByText("geral"),
    ).toBeInTheDocument();
    expect(await screen.findByText(channelText)).toBeInTheDocument();
    expect(screen.queryByText(dmText)).not.toBeInTheDocument();
  });

  it("renders the DM after leaving a channel", async () => {
    renderAt(`/chat/channel/${channelId}`);
    expect(await screen.findByText(channelText)).toBeInTheDocument();

    await clickDM();

    await waitFor(() => expect(window.location.pathname).toBe(`/chat/dm/${dmId}`));
    expect(await screen.findByTestId("chat-message-area")).toBeInTheDocument();
    expect(within(header()).getByText("Juliane")).toBeInTheDocument();
    expect(await screen.findByText(dmText)).toBeInTheDocument();
    expect(screen.queryByText(channelText)).not.toBeInTheDocument();
  });

  it("shows loading and ignores the stale DM page when it resolves after the switch", async () => {
    const pending = deferred<MessagePage>();
    api.fetchDMMessages.mockReturnValue(pending.promise);

    renderAt(`/chat/dm/${dmId}`);
    expect(await screen.findByLabelText("Carregando mensagens")).toBeInTheDocument();

    await clickChannel();
    await waitFor(() => expect(window.location.pathname).toBe(`/chat/channel/${channelId}`));

    // Old target's response lands only now — it must never reach the timeline.
    await act(async () => {
      pending.resolve(page([message("m-dm", dmText)]));
      await pending.promise;
    });

    expect(await screen.findByText(channelText)).toBeInTheDocument();
    expect(screen.queryByText(dmText)).not.toBeInTheDocument();
    expect(screen.queryByLabelText("Carregando mensagens")).not.toBeInTheDocument();
  });

  it("keeps the channel readable when the aborted DM request rejects with AbortError", async () => {
    const pending = deferred<MessagePage>();
    api.fetchDMMessages.mockReturnValue(pending.promise);

    renderAt(`/chat/dm/${dmId}`);
    expect(await screen.findByLabelText("Carregando mensagens")).toBeInTheDocument();

    await clickChannel();
    await waitFor(() => expect(window.location.pathname).toBe(`/chat/channel/${channelId}`));

    const abortError = new Error("aborted");
    abortError.name = "AbortError";
    await act(async () => {
      pending.reject(abortError);
      await pending.promise.catch(() => undefined);
    });

    expect(await screen.findByText(channelText)).toBeInTheDocument();
    expect(screen.queryByTestId("chat-msg-error")).not.toBeInTheDocument();
    expect(screen.queryByLabelText("Carregando mensagens")).not.toBeInTheDocument();
  });

  it("closes the DM subscription and subscribes to the channel", async () => {
    renderAt(`/chat/dm/${dmId}`);
    expect(await screen.findByText(dmText)).toBeInTheDocument();
    await waitFor(() => expect(FakeWebSocket.instances).toHaveLength(1));
    const dmSocket = FakeWebSocket.instances[0];
    await waitFor(() =>
      expect(dmSocket.subscriptions()).toContainEqual({
        type: "subscribe",
        target_type: "dm",
        target_id: dmId,
      }),
    );

    await clickChannel();
    await waitFor(() => expect(FakeWebSocket.instances).toHaveLength(2));

    expect(dmSocket.closed).toBe(true);
    expect(dmSocket.subscriptions()).toContainEqual({
      type: "unsubscribe",
      target_type: "dm",
      target_id: dmId,
    });

    const channelSocket = FakeWebSocket.instances[1];
    await waitFor(() =>
      expect(channelSocket.subscriptions()).toContainEqual({
        type: "subscribe",
        target_type: "channel",
        target_id: channelId,
      }),
    );
    expect(channelSocket.subscriptions()).not.toContainEqual(
      expect.objectContaining({ target_id: dmId }),
    );
  });

  it("does not carry the DM's pinned messages into the channel", async () => {
    api.fetchPins.mockImplementation(async (target: { kind: string }) =>
      target.kind === "dm"
        ? [{ message: message("m-dm", dmText), pinnedAt: "2026-01-01T10:00:00Z" }]
        : [],
    );

    renderAt(`/chat/dm/${dmId}`);
    expect(await screen.findByTestId("chat-pins")).toBeInTheDocument();

    await clickChannel();

    expect(await screen.findByText(channelText)).toBeInTheDocument();
    await waitFor(() => expect(screen.queryByTestId("chat-pins")).not.toBeInTheDocument());
  });

  it("restores each target through browser back and forward", async () => {
    renderAt(`/chat/dm/${dmId}`);
    expect(await screen.findByText(dmText)).toBeInTheDocument();

    await clickChannel();
    expect(await screen.findByText(channelText)).toBeInTheDocument();

    await act(async () => {
      window.history.back();
    });
    await waitFor(() => expect(window.location.pathname).toBe(`/chat/dm/${dmId}`));
    expect(await screen.findByText(dmText)).toBeInTheDocument();
    expect(screen.queryByText(channelText)).not.toBeInTheDocument();

    await act(async () => {
      window.history.forward();
    });
    await waitFor(() => expect(window.location.pathname).toBe(`/chat/channel/${channelId}`));
    expect(await screen.findByText(channelText)).toBeInTheDocument();
    expect(screen.queryByText(dmText)).not.toBeInTheDocument();
  });
});

// ── Composer draft isolation (security review follow-up) ─────────────────────

const secretDraft = "credenciais internas do projeto";

describe("composer drafts never cross conversation targets", () => {
  it("does not carry a private channel draft into another channel", async () => {
    renderAt(`/chat/channel/${secretChannelId}`);
    await typeDraft(secretDraft);

    await clickChannel();
    await waitFor(() => expect(window.location.pathname).toBe(`/chat/channel/${channelId}`));

    await expectEmptyComposer(secretDraft);

    // Enter on the fresh composer must not flush the previous target's draft.
    fireEvent.keyDown(await screen.findByTestId("chat-composer-input"), {
      key: "Enter",
      code: "Enter",
    });
    await waitFor(() => expect(screen.getByTestId("chat-send-btn")).toBeDisabled());
    expect(api.postChannelMessage).not.toHaveBeenCalled();
    expect(api.postDMMessage).not.toHaveBeenCalled();
  });

  it("does not carry a DM draft into another DM", async () => {
    renderAt(`/chat/dm/${dmId}`);
    await typeDraft(secretDraft);

    await clickOtherDM();
    await waitFor(() => expect(window.location.pathname).toBe(`/chat/dm/${otherDmId}`));

    await expectEmptyComposer(secretDraft);
    expect(api.postDMMessage).not.toHaveBeenCalled();
  });

  it("does not carry a channel draft into a DM, and drops the channel mention context", async () => {
    renderAt(`/chat/channel/${secretChannelId}`);
    await typeDraft(secretDraft);

    await clickDM();
    await waitFor(() => expect(window.location.pathname).toBe(`/chat/dm/${dmId}`));

    await expectEmptyComposer(secretDraft);

    // v2 composers are built without the mention extension: "@" is inert.
    const input = await screen.findByTestId("chat-composer-input");
    fireEvent.paste(input, {
      clipboardData: {
        files: [],
        types: ["text/plain"],
        getData: (type: string) => (type === "text/plain" ? "@jul" : ""),
      },
    });
    await waitFor(() => expect(input).toHaveTextContent("@jul"));
    expect(api.fetchMentionCandidates).not.toHaveBeenCalled();
  });

  it("does not carry a DM draft into a channel, and mentions resolve against that channel", async () => {
    renderAt(`/chat/dm/${dmId}`);
    await typeDraft(secretDraft);

    await clickSecretChannel();
    await waitFor(() => expect(window.location.pathname).toBe(`/chat/channel/${secretChannelId}`));

    await expectEmptyComposer(secretDraft);
    // The white-screen regression stays fixed: the root is still mounted.
    expect(screen.getByTestId("chat-sidebar")).toBeInTheDocument();
    expect(screen.getByTestId("chat-message-area")).toBeInTheDocument();

    const input = await screen.findByTestId("chat-composer-input");
    fireEvent.paste(input, {
      clipboardData: {
        files: [],
        types: ["text/plain"],
        getData: (type: string) => (type === "text/plain" ? "@jul" : ""),
      },
    });
    await waitFor(() =>
      expect(api.fetchMentionCandidates).toHaveBeenCalledWith(
        secretChannelId,
        expect.anything(),
        expect.anything(),
      ),
    );
  });

  it("keeps only the last target's composer across a fast A → B → C hop", async () => {
    renderAt(`/chat/channel/${secretChannelId}`);
    await typeDraft(secretDraft);

    await clickChannel();
    await typeDraft("rascunho intermediario");
    await clickDM();

    await waitFor(() => expect(window.location.pathname).toBe(`/chat/dm/${dmId}`));
    await expectEmptyComposer(secretDraft);
    expect(await screen.findByTestId("chat-composer-input")).not.toHaveTextContent(
      "rascunho intermediario",
    );
    expect(api.postChannelMessage).not.toHaveBeenCalled();
    expect(api.postDMMessage).not.toHaveBeenCalled();
  });

  it("does not restore a draft through browser back", async () => {
    renderAt(`/chat/channel/${secretChannelId}`);
    await typeDraft(secretDraft);

    await clickChannel();
    await expectEmptyComposer(secretDraft);

    await act(async () => {
      window.history.back();
    });
    await waitFor(() => expect(window.location.pathname).toBe(`/chat/channel/${secretChannelId}`));

    await expectEmptyComposer(secretDraft);
  });

  it("sends only the newly typed body, to the target that is open", async () => {
    renderAt(`/chat/channel/${secretChannelId}`);
    await typeDraft(secretDraft);

    await clickChannel();
    await expectEmptyComposer(secretDraft);
    await typeDraft("mensagem nova para o canal geral");

    await userEvent.click(screen.getByTestId("chat-send-btn"));

    await waitFor(() => expect(api.postChannelMessage).toHaveBeenCalledOnce());
    expect(api.postChannelMessage).toHaveBeenCalledWith(
      channelId,
      "mensagem nova para o canal geral",
      undefined,
      undefined,
    );
    expect(api.postChannelMessage).not.toHaveBeenCalledWith(
      expect.anything(),
      expect.stringContaining(secretDraft),
      expect.anything(),
      expect.anything(),
    );
  });

  it("destroys the previous editor instance and leaves no stray mention popup", async () => {
    const warn = vi.spyOn(console, "error").mockImplementation(() => undefined);
    renderAt(`/chat/channel/${secretChannelId}`);
    const firstInput = await typeDraft(secretDraft);

    await clickDM();
    await expectEmptyComposer(secretDraft);

    // The previous ProseMirror DOM node is gone from the document entirely.
    expect(firstInput.isConnected).toBe(false);
    expect(screen.getAllByTestId("chat-composer-input")).toHaveLength(1);
    expect(document.querySelectorAll(".chat-mention-popup")).toHaveLength(0);
    expect(warn).not.toHaveBeenCalled();
    warn.mockRestore();
  });
});
