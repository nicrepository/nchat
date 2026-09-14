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
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";

import App from "../App";
import { clearTokens, setTokens } from "../lib/authSession";
import { _resetChatSocket } from "./chatSocket";
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

beforeAll(async () => {
  // This suite verifies target navigation, not lazy chunk loading. Resolve the
  // real route module before assertions start so Suspense compilation time is
  // never charged against Testing Library's query timeout.
  await import("./ChatMessageArea");
});

afterAll(() => {
  global.WebSocket = OriginalWebSocket;
});

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

/**
 * The one user-event session for the current test, created in beforeEach.
 *
 * userEvent's direct API (`userEvent.click(...)`) runs a full setup() per call —
 * re-patching the document, the clipboard and the pointer state every time. With
 * this app mounted that is measurable, and it was part of what pushed these
 * tests near the timeout under coverage instrumentation. One session per test
 * keeps the interactions identical and pays the setup once.
 */
let user: ReturnType<typeof userEvent.setup>;

async function clickTarget(name: RegExp) {
  await user.click(await screen.findByRole("option", { name }));
}

const clickChannel = () => clickTarget(/Canal geral/i);
const clickSecretChannel = () => clickTarget(/Canal privado confidencial/i);
const clickDM = () => clickTarget(/Mensagem direta com Juliane/i);
const clickOtherDM = () => clickTarget(/Mensagem direta com Marcos/i);

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
  vi.unstubAllGlobals();
  // Defensive, regardless of what another test file left behind (some stub
  // window.matchMedia/IntersectionObserver directly rather than through
  // vi.stubGlobal, which vi.unstubAllGlobals() above cannot undo): this
  // file's own fixtures assume jsdom's real absence of both, exactly like a
  // browser that has never had either touched. window.IntersectionObserver
  // is deliberately left as whatever jsdom/setupTests already provides —
  // only matchMedia is forced, since it is the one ChatComposer's own
  // useMediaQuery reads directly.
  window.matchMedia = undefined as unknown as typeof window.matchMedia;
  user = userEvent.setup();
  FakeWebSocket.instances = [];
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  global.WebSocket = FakeWebSocket as any;
  _resetChatSocket();

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
  _resetChatSocket();
  global.WebSocket = OriginalWebSocket;

  // FakeWebSocket.instances is static, so a socket a previous test opened would
  // otherwise still be reachable from the next one's assertions.
  FakeWebSocket.instances = [];
  clearTokens();
});

/** The tab's single shared chat connection. */
function socket(): FakeWebSocket {
  const current = FakeWebSocket.instances.at(-1);
  if (!current) throw new Error("no chat socket was opened");
  return current;
}

function renderAt(path: string) {
  window.history.pushState({}, "", path);
  return render(<App />);
}

// ── Tests ─────────────────────────────────────────────────────────────────────

describe("navigating between DM and channel targets", () => {
  it("renders the channel after leaving a DM", async () => {
    const initialDMPage = deferred<MessagePage>();
    api.fetchDMMessages.mockImplementation((conversationId: string) => {
      if (conversationId !== dmId) throw new Error(`Unexpected DM target: ${conversationId}`);
      return initialDMPage.promise;
    });

    renderAt(`/chat/dm/${dmId}`);

    await waitFor(() => expect(window.location.pathname).toBe(`/chat/dm/${dmId}`));

    const dmHeader = await screen.findByTestId("chat-msg-header");
    expect(await within(dmHeader).findByText("Juliane")).toBeInTheDocument();

    await waitFor(() =>
      expect(api.fetchDMMessages).toHaveBeenCalledWith(dmId, undefined, expect.any(AbortSignal)),
    );
    expect(screen.getByLabelText("Carregando mensagens")).toBeInTheDocument();

    await act(async () => {
      initialDMPage.resolve(page([message("m-dm", dmText)]));
      await initialDMPage.promise;
    });

    expect(await screen.findByText(dmText)).toBeInTheDocument();

    await clickChannel();

    // The route, the header and the timeline settle on three separate updates —
    // the click, the router's, and the channel fetch resolving. Each is awaited
    // on its own observable outcome, and only then is the DM's absence checked:
    // asserting it right after the last findBy would be asserting on whichever
    // render happened to have landed first.
    await waitFor(() => expect(window.location.pathname).toBe(`/chat/channel/${channelId}`));

    const header = await screen.findByTestId("chat-msg-header");
    expect(await within(header).findByText("geral")).toBeInTheDocument();

    expect(await screen.findByText(channelText)).toBeInTheDocument();

    await waitFor(() => expect(screen.queryByText(dmText)).not.toBeInTheDocument());

    expect(screen.getByTestId("chat-message-area")).toBeInTheDocument();
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

  it("moves the single shared connection from the DM to the channel", async () => {
    renderAt(`/chat/dm/${dmId}`);
    expect(await screen.findByText(dmText)).toBeInTheDocument();

    // Issue #449: the sidebar, the message list and call signalling all share
    // one connection. Opening one socket each is what exhausted the server's
    // per-user connection budget, so the count is the assertion.
    await waitFor(() =>
      expect(socket().subscriptions()).toContainEqual({
        type: "subscribe",
        target_type: "dm",
        target_id: dmId,
      }),
    );
    expect(FakeWebSocket.instances).toHaveLength(1);

    await clickChannel();
    await waitFor(() =>
      expect(socket().subscriptions()).toContainEqual({
        type: "subscribe",
        target_type: "channel",
        target_id: channelId,
      }),
    );

    // Switching target resubscribes on the live socket instead of replacing it.
    expect(FakeWebSocket.instances).toHaveLength(1);
    expect(socket().closed).toBe(false);
    // And the conversation it left stays subscribed (issue #444): the sidebar is
    // still watching that DM for new messages and presence, and the connection —
    // not the view that happened to close — owns the subscription. Sending
    // unsubscribe here is what used to make the sidebar go quiet for a
    // conversation the user had merely navigated away from.
    expect(socket().subscriptions()).not.toContainEqual({
      type: "unsubscribe",
      target_type: "dm",
      target_id: dmId,
    });
    // One subscribe per target, however many views want it.
    expect(
      socket()
        .subscriptions()
        .filter((frame) => frame.type === "subscribe" && frame.target_id === channelId),
    ).toHaveLength(1);
  });

  it("leaves no subscription or timeline state after a fast A → B → C switch", async () => {
    renderAt(`/chat/channel/${secretChannelId}`);
    const initialHeader = await screen.findByTestId("chat-msg-header");
    expect(within(initialHeader).getByText("confidencial")).toBeInTheDocument();
    await waitFor(() =>
      expect(socket().subscriptions()).toContainEqual({
        type: "subscribe",
        target_type: "channel",
        target_id: secretChannelId,
      }),
    );

    await clickChannel();
    await waitFor(() => expect(window.location.pathname).toBe(`/chat/channel/${channelId}`));

    await clickDM();
    await waitFor(() => expect(window.location.pathname).toBe(`/chat/dm/${dmId}`));
    expect(await screen.findByText(dmText)).toBeInTheDocument();

    await waitFor(() =>
      expect(socket().subscriptions()).toContainEqual({
        type: "subscribe",
        target_type: "dm",
        target_id: dmId,
      }),
    );

    // No switch leaked a second socket, and none of the abandoned targets was
    // unsubscribed: the sidebar still watches every one of them, and a view
    // closing is not the connection losing interest (issue #444).
    expect(FakeWebSocket.instances).toHaveLength(1);
    expect(socket().closed).toBe(false);
    for (const abandoned of [secretChannelId, channelId]) {
      expect(socket().subscriptions()).not.toContainEqual({
        type: "unsubscribe",
        target_type: "channel",
        target_id: abandoned,
      });
      // Still exactly one subscribe each: revisiting a target nobody released
      // costs no frame at all.
      expect(
        socket()
          .subscriptions()
          .filter((frame) => frame.type === "subscribe" && frame.target_id === abandoned),
      ).toHaveLength(1);
    }
    // What does go is the timeline state the view owned.
    expect(screen.queryByText(channelText)).not.toBeInTheDocument();
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
        { kind: "channel", id: secretChannelId },
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

  // Issue #769 deliberately inverts this case. Before #769, ChatComposer
  // discarded its content on every remount — including a round trip back to
  // the *same* conversation — because a draft had nowhere to live but the
  // TipTap instance itself. #769 gives it somewhere to live (AppShell's
  // useConversationDrafts, keyed by kind:targetId, rendered here through the
  // real app tree): the draft typed in the secret channel now survives
  // leaving it and belongs there, exactly like every other per-target piece
  // of state this describe block still requires stays isolated between
  // *different* targets (see the tests above and below this one, all still
  // green and unmodified). This is the restoration RF-769 exists to add,
  // not a relaxation of the isolation those other tests guard.
  it("restores a draft when navigation returns to the same conversation", async () => {
    renderAt(`/chat/channel/${secretChannelId}`);
    await typeDraft(secretDraft);

    await clickChannel();
    await expectEmptyComposer(secretDraft);

    // Returning to the secret channel by the same click-navigation every
    // other test in this file already uses to prove isolation between
    // *different* targets — deliberately not window.history.back(): that
    // path drives jsdom's popstate handling, which has been observed to
    // race with React Router's own async transition under this file's
    // fixtures independently of anything this issue changed, and is not
    // what RF-769 is actually about. The guarantee under test is "revisit
    // the same conversation, get the same draft back" — this exercises
    // exactly that, through the identical, already-reliable mechanism the
    // isolation tests above depend on.
    await clickSecretChannel();
    await waitFor(() => expect(window.location.pathname).toBe(`/chat/channel/${secretChannelId}`));

    const input = await screen.findByTestId("chat-composer-input");
    await waitFor(() => expect(input).toHaveTextContent(secretDraft));
    expect(screen.getByTestId("chat-send-btn")).toBeEnabled();
  });

  it("sends only the newly typed body, to the target that is open", async () => {
    renderAt(`/chat/channel/${secretChannelId}`);
    await typeDraft(secretDraft);

    await clickChannel();
    await expectEmptyComposer(secretDraft);
    await typeDraft("mensagem nova para o canal geral");

    await user.click(screen.getByTestId("chat-send-btn"));

    await waitFor(() => expect(api.postChannelMessage).toHaveBeenCalledOnce());
    expect(api.postChannelMessage).toHaveBeenCalledWith(
      channelId,
      "mensagem nova para o canal geral",
      {
        parentMessageId: undefined,
        referencedMessageId: undefined,
        attachmentIds: undefined,
        idempotencyKey: expect.any(String),
        // Issue #824: carried on every send and omitted from the wire when
        // false, which chatApiAcknowledgement.test.ts asserts separately.
        acknowledgementRequired: false,
      },
    );
    expect(api.postChannelMessage).not.toHaveBeenCalledWith(
      expect.anything(),
      expect.stringContaining(secretDraft),
      expect.anything(),
    );
  });

  // Regression (issue #769 follow-up): ChatMessageArea's handleSend used to
  // call drafts.setReply(key, null) unconditionally after every successful
  // send, even one with no reply — bumping the draft's revision for no real
  // reason. ChatComposer's ACK-race guard then always saw that bump as "the
  // reader changed something since submitting" and never cleared the
  // editor, so a second message typed right after the first got appended
  // to it instead of replacing it (surfaced by an E2E flow; this is the
  // unit-level guard against a regression).
  it("clears the composer after a plain send with no reply, so the next message is not appended to it", async () => {
    renderAt(`/chat/channel/${channelId}`);

    const input = await typeDraft("primeira");
    await user.click(screen.getByTestId("chat-send-btn"));
    await waitFor(() => expect(api.postChannelMessage).toHaveBeenCalledOnce());
    await waitFor(() => expect(input).not.toHaveTextContent("primeira"));

    await typeDraft("segunda");
    await user.click(screen.getByTestId("chat-send-btn"));
    await waitFor(() => expect(api.postChannelMessage).toHaveBeenCalledTimes(2));

    expect(api.postChannelMessage).toHaveBeenNthCalledWith(
      2,
      channelId,
      "segunda",
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
