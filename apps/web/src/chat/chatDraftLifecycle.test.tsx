/**
 * The draft lifecycle across real navigation (issue #929).
 *
 * NAVIGATION PRESERVES DRAFT. CONFIRMED SEND CONSUMES ITS SNAPSHOT.
 *
 * Driven through the real <App /> — real router, real ChatMessageArea, real
 * useMessages, real draft store in AppShell — so every switch here is the
 * same in-place route update and keyed ChatComposer remount production does,
 * and "the reply came back" is the preview on screen, not a getDraft() call.
 *
 * All chat HTTP calls are mocked at the chatApi/filesApi module level; the
 * WebSocket is an inert fake.
 */

import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";

import App from "../App";
import { clearTokens, setTokens } from "../lib/authSession";
import { _resetChatSocket } from "./chatSocket";
import { loadDraftPersistence } from "./chatDraftPersistence";
import type { ChannelAttachment, Message, MessagePage } from "./chatTypes";

// ── Module mocks ──────────────────────────────────────────────────────────────

const { api, files } = vi.hoisted(() => ({
  api: {
    fetchSidebarData: vi.fn(),
    fetchChannelMessages: vi.fn(),
    fetchDMMessages: vi.fn(),
    fetchPins: vi.fn(),
    fetchAllowedReactionEmojis: vi.fn(),
    postChannelMessage: vi.fn(),
    postDMMessage: vi.fn(),
    fetchMentionCandidates: vi.fn(),
    fetchChannelDetails: vi.fn(),
  },
  files: {
    uploadAttachment: vi.fn(),
    deleteAttachmentDraft: vi.fn(),
    fetchConversationAttachments: vi.fn(),
  },
}));

vi.mock("./chatApi", async (importOriginal) => ({
  ...(await importOriginal<typeof import("./chatApi")>()),
  ...api,
}));

vi.mock("./filesApi", async (importOriginal) => ({
  ...(await importOriginal<typeof import("./filesApi")>()),
  ...files,
}));

class FakeWebSocket {
  static OPEN = 1;
  static CLOSED = 3;
  readyState = FakeWebSocket.OPEN;
  onopen: (() => void) | null = null;
  onmessage: ((event: MessageEvent) => void) | null = null;
  onerror: (() => void) | null = null;
  onclose: (() => void) | null = null;
  constructor() {
    queueMicrotask(() => this.onopen?.());
  }
  send() {}
  close() {
    this.readyState = FakeWebSocket.CLOSED;
  }
}
const OriginalWebSocket = global.WebSocket;

beforeAll(async () => {
  await import("./ChatMessageArea");
});
afterAll(() => {
  global.WebSocket = OriginalWebSocket;
});

// ── Fixtures ──────────────────────────────────────────────────────────────────

/**
 * A different reader per test. The store mirrors text to sessionStorage
 * behind a 400ms debounce and these tests run on the real clock, so a timer
 * armed by one test could otherwise land after the next has cleared storage
 * and be hydrated back by it.
 */
let userId = "me-0";
let testCount = 0;
const channelA = "11111111-1111-4111-8111-111111111111";
const channelB = "33333333-3333-4333-8333-333333333333";
const dmId = "22222222-2222-4222-8222-222222222222";
const keyA = `channel:${channelA}`;
const keyB = `channel:${channelB}`;

function message(id: string, bodyText: string, senderDisplayName = "Alice"): Message {
  return {
    id,
    senderId: `sender-${senderDisplayName}`,
    senderDisplayName,
    senderEmail: `${senderDisplayName.toLowerCase()}@example.com`,
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

const r1 = message("m-r1", "pergunta da Ana", "Ana");
const r2 = message("m-r2", "pergunta do Bruno", "Bruno");
const pageA: MessagePage = { messages: [r1, r2], nextCursor: "" };
const pageB: MessagePage = { messages: [message("m-b", "mensagem do canal B")], nextCursor: "" };

function uploaded(file: File): ChannelAttachment {
  return {
    id: `att-${file.name}`,
    filename: file.name,
    contentType: file.type,
    size: file.size,
    status: "pending_scan",
    previewStatus: "pending",
    createdAt: "",
  };
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

const pdf = (name: string) => new File(["x"], name, { type: "application/pdf" });

/**
 * The layout jsdom never gives the timeline (same shape as ChatMessageArea's
 * own tests): the hover toolbar only opens against a bubble that sits inside
 * the band the list occupies, so the list fills the window and every bubble
 * sits inside it.
 */
function layoutRect(this: Element): DOMRect {
  const rect = (r: Partial<DOMRect>) =>
    ({
      x: 0,
      y: 0,
      top: 0,
      left: 0,
      right: 0,
      bottom: 0,
      width: 0,
      height: 0,
      toJSON: () => ({}),
      ...r,
    }) as DOMRect;
  if (this.classList.contains("chat-msg-area__list")) {
    return rect({ right: 1024, bottom: 768, width: 1024, height: 768 });
  }
  if (this.classList.contains("chat-msg-area__msg-bubble")) {
    return rect({ top: 300, bottom: 340, left: 100, right: 400, width: 300, height: 40 });
  }
  return rect({});
}

let user: ReturnType<typeof userEvent.setup>;

beforeEach(() => {
  // Drafts live in sessionStorage next to the auth session: wipe first, then sign in.
  userId = `me-${++testCount}`;
  sessionStorage.clear();
  clearTokens();
  setTokens("test-token");
  vi.clearAllMocks();
  vi.spyOn(Element.prototype, "getBoundingClientRect").mockImplementation(layoutRect);
  window.matchMedia = undefined as unknown as typeof window.matchMedia;
  user = userEvent.setup();
  global.WebSocket = FakeWebSocket as unknown as typeof WebSocket;
  _resetChatSocket();

  api.fetchSidebarData.mockResolvedValue({
    currentUserId: userId,
    workspaceId: "w1",
    maxFiles: 10,
    channels: [
      { id: channelA, name: "alfa", type: "public" },
      { id: channelB, name: "beta", type: "public" },
    ],
    dms: [{ id: dmId, type: "1:1", name: "Juliane", participants: [] }],
    categories: [],
  });
  api.fetchChannelMessages.mockImplementation(async (id: string) =>
    id === channelA ? pageA : pageB,
  );
  api.fetchDMMessages.mockResolvedValue({ messages: [], nextCursor: "" });
  api.fetchPins.mockResolvedValue([]);
  api.fetchAllowedReactionEmojis.mockResolvedValue(["👍"]);
  api.postChannelMessage.mockResolvedValue(message("m-new", "enviada"));
  api.postDMMessage.mockResolvedValue(message("m-new", "enviada"));
  api.fetchMentionCandidates.mockResolvedValue([]);
  api.fetchChannelDetails.mockResolvedValue({
    id: channelA,
    slug: "alfa",
    name: "alfa",
    type: "public",
    createdAt: "2026-01-01T10:00:00Z",
    memberCount: 1,
    onlineCount: 0,
    onlineMembers: [],
    canManageMembers: false,
  });
  files.fetchConversationAttachments.mockResolvedValue([]);
  files.uploadAttachment.mockImplementation(async (_target: unknown, file: File) => uploaded(file));
  files.deleteAttachmentDraft.mockResolvedValue(undefined);
});

afterEach(() => {
  vi.restoreAllMocks();
  _resetChatSocket();
  global.WebSocket = OriginalWebSocket;
  clearTokens();
});

// ── Helpers ───────────────────────────────────────────────────────────────────

function renderAt(path: string) {
  window.history.pushState({}, "", path);
  return render(<App />);
}

const rowA = () => screen.getByRole("option", { name: /canal alfa/i });
const rowB = () => screen.getByRole("option", { name: /canal beta/i });

async function openA() {
  await user.click(rowA());
  await waitFor(() => expect(window.location.pathname).toBe(`/chat/channel/${channelA}`));
}
async function openB() {
  await user.click(rowB());
  await waitFor(() => expect(window.location.pathname).toBe(`/chat/channel/${channelB}`));
  await screen.findByText("mensagem do canal B");
}

const composer = () => screen.getByTestId("chat-composer-input");
const quote = () => screen.queryByTestId("chat-composer-quote");
const pendingAttachments = () => screen.queryAllByTestId("chat-composer-pending-attachment");

/** ProseMirror ignores synthetic key events in JSDOM; a paste is the public path in. */
async function type(text: string) {
  const input = await screen.findByTestId("chat-composer-input");
  fireEvent.paste(input, {
    clipboardData: {
      files: [],
      types: ["text/plain"],
      getData: (kind: string) => (kind === "text/plain" ? text : ""),
    },
  });
  await waitFor(() => expect(input).toHaveTextContent(text));
}

/**
 * Hovers one bubble and answers it; the toolbar floats over the hovered
 * message only.
 */
async function replyTo(target: Message) {
  const bubble = await screen.findByText(target.bodyText);
  const shell = bubble.closest("[data-message-id]") as HTMLElement;
  fireEvent.mouseEnter(shell);
  await user.click(await within(shell).findByRole("button", { name: "Responder" }));
  await waitFor(() => expect(quote()).toHaveTextContent(target.senderDisplayName));
}

async function attach(file: File) {
  await user.upload(screen.getByTestId("chat-composer-file-input"), file);
}

async function expectReadyAttachment(name: string) {
  await waitFor(() => {
    const items = pendingAttachments();
    expect(items.some((item) => item.textContent?.includes(name))).toBe(true);
  });
}

function draftBadgeOn(row: HTMLElement) {
  return within(row).queryByTestId("chat-sidebar-draft-badge");
}

async function send() {
  await user.click(screen.getByTestId("chat-send-btn"));
}

// ── Tests ─────────────────────────────────────────────────────────────────────

describe("navigation preserves the whole draft (issue #929)", () => {
  it("brings back the reply together with the text after A → B → A (the reported bug)", async () => {
    renderAt(`/chat/channel/${channelA}`);
    await replyTo(r1);
    await type("Vou verificar");

    await openB();
    expect(quote()).toBeNull();
    expect(composer()).toHaveTextContent("");
    expect(draftBadgeOn(rowA())).toBeVisible();

    await openA();

    await waitFor(() => expect(quote()).toHaveTextContent("Ana"));
    expect(quote()).toHaveTextContent("pergunta da Ana");
    await waitFor(() => expect(composer()).toHaveTextContent("Vou verificar"));
  });

  it("drops only the reply when its message is no longer in the page A reloads with, and keeps the text", async () => {
    renderAt(`/chat/channel/${channelA}`);
    await replyTo(r1);
    await type("Vou verificar");

    await openB();
    // R1 was deleted (or paged out) while the reader was away.
    api.fetchChannelMessages.mockImplementation(async (id: string) =>
      id === channelA ? { messages: [r2], nextCursor: "" } : pageB,
    );
    await openA();

    await waitFor(() => expect(composer()).toHaveTextContent("Vou verificar"));
    await screen.findByText(r2.bodyText);
    expect(quote()).toBeNull();
    // Gone from the draft too, so a later send does not post a parent the
    // server would reject.
    await waitFor(() => expect(loadDraftPersistence(userId, keyA)?.replyToMessageId).toBeNull());
  });

  it("keeps an attachment, and an upload that finishes while B is open lands in A and nowhere else", async () => {
    const upload = deferred<ChannelAttachment>();
    files.uploadAttachment.mockReturnValueOnce(upload.promise);
    renderAt(`/chat/channel/${channelA}`);
    await screen.findByText(r1.bodyText);
    await attach(pdf("X.pdf"));
    await waitFor(() => expect(files.uploadAttachment).toHaveBeenCalledOnce());
    expect(screen.getByTestId("chat-composer-upload-status")).toHaveTextContent("X.pdf");

    await openB();
    expect(screen.queryByTestId("chat-composer-upload-status")).toBeNull();

    // The upload the reader walked away from completes in the background.
    await act(async () => upload.resolve(uploaded(pdf("X.pdf"))));

    // B never hears of it...
    expect(screen.queryByTestId("chat-composer-upload-status")).toBeNull();
    expect(pendingAttachments()).toHaveLength(0);
    expect(files.deleteAttachmentDraft).not.toHaveBeenCalled();

    // ...and A has it ready to send.
    await openA();
    await expectReadyAttachment("X.pdf");
    expect(screen.getByTestId("chat-send-btn")).toBeEnabled();
  });

  it("does not resurrect an attachment removed while its upload was still running", async () => {
    const upload = deferred<ChannelAttachment>();
    files.uploadAttachment.mockReturnValueOnce(upload.promise);
    renderAt(`/chat/channel/${channelA}`);
    await screen.findByText(r1.bodyText);
    await attach(pdf("X.pdf"));
    await waitFor(() => expect(files.uploadAttachment).toHaveBeenCalledOnce());

    await user.click(screen.getByTestId("chat-composer-remove-attachment"));
    await waitFor(() => expect(screen.queryByTestId("chat-composer-upload-status")).toBeNull());

    // Late completion of a request the reader already aborted by removing
    // the file: the upload hook aborted it, but a server answer that got
    // through anyway must not bring the file back.
    await act(async () => upload.resolve(uploaded(pdf("X.pdf"))));

    await openB();
    await openA();
    expect(screen.queryByTestId("chat-composer-upload-status")).toBeNull();
    expect(pendingAttachments()).toHaveLength(0);
  });

  it("restores a composite draft — reply, text and attachment — whole, and a confirmed send consumes all of it", async () => {
    renderAt(`/chat/channel/${channelA}`);
    await replyTo(r1);
    await type("T1");
    await attach(pdf("X.pdf"));
    await expectReadyAttachment("X.pdf");

    await openB();
    await openA();

    await waitFor(() => expect(quote()).toHaveTextContent("Ana"));
    await waitFor(() => expect(composer()).toHaveTextContent("T1"));
    await expectReadyAttachment("X.pdf");

    await send();
    await waitFor(() => expect(api.postChannelMessage).toHaveBeenCalledOnce());
    expect(api.postChannelMessage).toHaveBeenCalledWith(
      channelA,
      "T1",
      expect.objectContaining({ parentMessageId: r1.id, attachmentIds: ["att-X.pdf"] }),
    );

    // Composer: nothing of the three left.
    await waitFor(() => expect(composer()).toHaveTextContent(""));
    await waitFor(() => expect(quote()).toBeNull());
    expect(pendingAttachments()).toHaveLength(0);
    expect(screen.getByTestId("chat-send-btn")).toBeDisabled();
    expect(files.deleteAttachmentDraft).not.toHaveBeenCalled();

    // Store, badge and sessionStorage: no draft at all.
    await openB();
    expect(draftBadgeOn(rowA())).toBeNull();
    expect(loadDraftPersistence(userId, keyA)).toBeNull();

    // Neither coming back nor a fresh store (an F5) brings it back.
    await openA();
    await screen.findByText(r1.bodyText);
    expect(composer()).toHaveTextContent("");
    expect(quote()).toBeNull();
    expect(pendingAttachments()).toHaveLength(0);
  });
});

describe("a confirmed send consumes its snapshot and nothing more (issue #929)", () => {
  it("delayed ACK: a reply and an attachment composed while the send is in flight survive its acknowledgement", async () => {
    const post = deferred<Message>();
    api.postChannelMessage.mockReturnValueOnce(post.promise);
    renderAt(`/chat/channel/${channelA}`);
    await replyTo(r1);
    await type("T1");
    await attach(pdf("X.pdf"));
    await expectReadyAttachment("X.pdf");

    await send();
    await waitFor(() => expect(api.postChannelMessage).toHaveBeenCalledOnce());

    // While S1 is still open the reader lines up the next message: answers
    // Bruno instead, and drops Z.
    await replyTo(r2);
    await attach(pdf("Z.pdf"));
    await expectReadyAttachment("Z.pdf");

    await act(async () => post.resolve(message("m-sent", "T1")));

    // T1, R1 and X went out with S1; R2 and Z are the next message's.
    await waitFor(() => expect(composer()).toHaveTextContent(""));
    await waitFor(() => expect(quote()).toHaveTextContent("Bruno"));
    await waitFor(() =>
      expect(pendingAttachments().map((item) => item.textContent)).toEqual([
        expect.stringContaining("Z.pdf"),
      ]),
    );
    expect(files.deleteAttachmentDraft).not.toHaveBeenCalled();

    // And they are what the next send carries.
    await type("T2");
    await send();
    await waitFor(() => expect(api.postChannelMessage).toHaveBeenCalledTimes(2));
    expect(api.postChannelMessage).toHaveBeenLastCalledWith(
      channelA,
      "T2",
      expect.objectContaining({ parentMessageId: r2.id, attachmentIds: ["att-Z.pdf"] }),
    );
  });

  it("navigation during the send: A's acknowledgement reconciles A only, leaves B's draft alone, and does not pull the reader back", async () => {
    const post = deferred<Message>();
    api.postChannelMessage.mockReturnValueOnce(post.promise);
    renderAt(`/chat/channel/${channelA}`);
    await replyTo(r1);
    await type("T1");
    await send();
    await waitFor(() => expect(api.postChannelMessage).toHaveBeenCalledOnce());

    await openB();
    await replyTo(pageB.messages[0]);
    await type("rascunho B");
    expect(draftBadgeOn(rowA())).toBeVisible();

    await act(async () => post.resolve(message("m-sent", "T1")));

    // Still on B, with B's draft exactly as it was.
    expect(window.location.pathname).toBe(`/chat/channel/${channelB}`);
    expect(composer()).toHaveTextContent("rascunho B");
    expect(quote()).toHaveTextContent("Alice");
    // A's draft is consumed: no badge, no persistence...
    await waitFor(() => expect(draftBadgeOn(rowA())).toBeNull());
    expect(loadDraftPersistence(userId, keyA)).toBeNull();
    expect(loadDraftPersistence(userId, keyB)).toBeNull();

    // ...and nothing of it comes back on A.
    await openA();
    await screen.findByText(r1.bodyText);
    expect(composer()).toHaveTextContent("");
    expect(quote()).toBeNull();
  });

  it("a failed send preserves reply, text and attachment for the retry", async () => {
    api.postChannelMessage.mockRejectedValueOnce(new Error("falhou"));
    renderAt(`/chat/channel/${channelA}`);
    await replyTo(r1);
    await type("T1");
    await attach(pdf("X.pdf"));
    await expectReadyAttachment("X.pdf");

    await send();
    await waitFor(() => expect(api.postChannelMessage).toHaveBeenCalledOnce());
    await screen.findByTestId("chat-send-error");

    expect(composer()).toHaveTextContent("T1");
    expect(quote()).toHaveTextContent("Ana");
    expect(pendingAttachments()).toHaveLength(1);

    // Still all there after leaving and coming back.
    await openB();
    await openA();
    await waitFor(() => expect(quote()).toHaveTextContent("Ana"));
    await waitFor(() => expect(composer()).toHaveTextContent("T1"));
    await expectReadyAttachment("X.pdf");
  });
});

/**
 * Code Quality Review of #929, finding 1: the reader comes back to A while
 * S1 is still in flight. The composer mounted then is a *new* instance,
 * seeded from the draft as it was at submit — it has no `sending` of its
 * own, and nothing told it when the old instance's acknowledgement
 * consumed the draft. Both halves are covered: the send stays
 * un-duplicable while pending, and the mounted composer converges with the
 * store once the send settles.
 */
describe("a send in flight survives the composer's own remount (#929 review)", () => {
  /** Submits from A with the POST held open, then does A → B → A. */
  async function submitAndComeBack() {
    const post = deferred<Message>();
    api.postChannelMessage.mockReturnValueOnce(post.promise);
    await send();
    await waitFor(() => expect(api.postChannelMessage).toHaveBeenCalledOnce());
    await openB();
    await openA();
    return post;
  }

  it("blocks a second send of the same draft while S1 is pending, then reconciles reply, text and attachment on sent", async () => {
    renderAt(`/chat/channel/${channelA}`);
    await replyTo(r1);
    await type("T1");
    await attach(pdf("X.pdf"));
    await expectReadyAttachment("X.pdf");
    const post = await submitAndComeBack();

    // The remounted composer knows a send of this draft is still open: it
    // cannot post the same snapshot again, by button or by Enter.
    await waitFor(() => expect(screen.getByTestId("chat-send-btn")).toBeDisabled());
    const input = composer();
    input.focus();
    fireEvent.keyDown(input, { key: "Enter", code: "Enter" });
    await send();
    expect(api.postChannelMessage).toHaveBeenCalledOnce();

    await act(async () => post.resolve(message("m-sent", "T1")));

    // Everything S1 carried is gone from the composer this reader is looking at.
    await waitFor(() => expect(composer()).toHaveTextContent(""));
    await waitFor(() => expect(pendingAttachments()).toHaveLength(0));
    expect(quote()).toBeNull();
    expect(screen.getByTestId("chat-send-btn")).toBeDisabled();
    expect(files.deleteAttachmentDraft).not.toHaveBeenCalled();
    expect(api.postChannelMessage).toHaveBeenCalledOnce();
    expect(loadDraftPersistence(userId, keyA)).toBeNull();

    // Typing again must be possible, and must post only the new message.
    await type("T2");
    await send();
    await waitFor(() => expect(api.postChannelMessage).toHaveBeenCalledTimes(2));
    expect(api.postChannelMessage).toHaveBeenLastCalledWith(
      channelA,
      "T2",
      expect.objectContaining({ parentMessageId: undefined, attachmentIds: undefined }),
    );

    await openB();
    expect(draftBadgeOn(rowA())).toBeNull();
    await openA();
    await screen.findByText(r1.bodyText);
    expect(composer()).toHaveTextContent("");
    expect(quote()).toBeNull();
    expect(pendingAttachments()).toHaveLength(0);
  });

  /**
   * The disabled button is the UX; the guard on the send path is the
   * invariant (#929 review). Two clicks landing in the same tick read the
   * same, still-enabled render — the lifecycle is what refuses the second.
   */
  it("refuses a second send of the same draft even when two clicks land before the button re-renders", async () => {
    renderAt(`/chat/channel/${channelA}`);
    await screen.findByText(r1.bodyText);
    await type("T1");
    const post = deferred<Message>();
    api.postChannelMessage.mockReturnValueOnce(post.promise);

    const button = screen.getByTestId("chat-send-btn");
    await act(async () => {
      fireEvent.click(button);
      fireEvent.click(button);
    });

    expect(api.postChannelMessage).toHaveBeenCalledOnce();
    await act(async () => post.resolve(message("m-sent", "T1")));
    await waitFor(() => expect(composer()).toHaveTextContent(""));
    expect(api.postChannelMessage).toHaveBeenCalledOnce();
  });

  it("text only: the remounted editor empties once S1 is acknowledged", async () => {
    renderAt(`/chat/channel/${channelA}`);
    await screen.findByText(r1.bodyText);
    await type("T1");
    const post = await submitAndComeBack();
    await waitFor(() => expect(composer()).toHaveTextContent("T1"));

    await act(async () => post.resolve(message("m-sent", "T1")));

    await waitFor(() => expect(composer()).toHaveTextContent(""));
    expect(screen.getByTestId("chat-send-btn")).toBeDisabled();
    expect(api.postChannelMessage).toHaveBeenCalledOnce();
  });

  it("attachment: X sent with S1 leaves, Z dropped while S1 was open stays", async () => {
    renderAt(`/chat/channel/${channelA}`);
    await screen.findByText(r1.bodyText);
    await type("T1");
    await attach(pdf("X.pdf"));
    await expectReadyAttachment("X.pdf");
    const post = deferred<Message>();
    api.postChannelMessage.mockReturnValueOnce(post.promise);
    await send();
    await waitFor(() => expect(api.postChannelMessage).toHaveBeenCalledOnce());
    // Dropping a file is still allowed while the send is open (#875).
    await attach(pdf("Z.pdf"));
    await expectReadyAttachment("Z.pdf");
    await openB();
    await openA();
    await waitFor(() => expect(pendingAttachments()).toHaveLength(2));

    await act(async () => post.resolve(message("m-sent", "T1")));

    await waitFor(() =>
      expect(pendingAttachments().map((item) => item.textContent)).toEqual([
        expect.stringContaining("Z.pdf"),
      ]),
    );
    await waitFor(() => expect(composer()).toHaveTextContent(""));
    // Z is sendable on its own; X is not re-sent with it.
    expect(screen.getByTestId("chat-send-btn")).toBeEnabled();
    expect(files.deleteAttachmentDraft).not.toHaveBeenCalled();
    await send();
    await waitFor(() => expect(api.postChannelMessage).toHaveBeenCalledTimes(2));
    expect(api.postChannelMessage).toHaveBeenLastCalledWith(
      channelA,
      "",
      expect.objectContaining({ attachmentIds: ["att-Z.pdf"] }),
    );
  });

  /**
   * The upload belongs to the draft, not to the component (issue #929):
   * one started before the reader left finishes while a *new* composer is
   * on screen, and that composer has to show the result — no third
   * navigation, no reload.
   */
  it("shows the result of an upload that finished after the composer was remounted", async () => {
    const upload = deferred<ChannelAttachment>();
    files.uploadAttachment.mockReturnValueOnce(upload.promise);
    renderAt(`/chat/channel/${channelA}`);
    await screen.findByText(r1.bodyText);
    await type("T1");
    await attach(pdf("X.pdf"));
    await waitFor(() => expect(files.uploadAttachment).toHaveBeenCalledOnce());

    await openB();
    await openA();
    // The queue this instance was seeded with still shows X going up.
    await waitFor(() =>
      expect(screen.getByTestId("chat-composer-upload-status")).toHaveTextContent("Enviando"),
    );

    await act(async () => upload.resolve(uploaded(pdf("X.pdf"))));

    await expectReadyAttachment("X.pdf");
    // And it can be sent from here, with the id the upload produced.
    await send();
    await waitFor(() => expect(api.postChannelMessage).toHaveBeenCalledOnce());
    expect(api.postChannelMessage).toHaveBeenLastCalledWith(
      channelA,
      "T1",
      expect.objectContaining({ attachmentIds: ["att-X.pdf"] }),
    );
  });

  // The counterweight to the session boundary (#929, third review):
  // leaving a conversation is not leaving the session, so an upload that
  // outlives the composer still belongs to the draft it was started for.
  it("keeps an upload valid across a conversation switch — only a cleared session invalidates one", async () => {
    const upload = deferred<ChannelAttachment>();
    files.uploadAttachment.mockReturnValueOnce(upload.promise);
    renderAt(`/chat/channel/${channelA}`);
    await screen.findByText(r1.bodyText);
    await attach(pdf("X.pdf"));
    await waitFor(() => expect(files.uploadAttachment).toHaveBeenCalledOnce());

    await openB();
    await openA();
    await openB();
    await openA();

    await act(async () => upload.resolve(uploaded(pdf("X.pdf"))));

    await expectReadyAttachment("X.pdf");
    expect(files.deleteAttachmentDraft).not.toHaveBeenCalled();
  });

  it("shows the failure of an upload that failed after the composer was remounted", async () => {
    const upload = deferred<ChannelAttachment>();
    files.uploadAttachment.mockReturnValueOnce(upload.promise);
    renderAt(`/chat/channel/${channelA}`);
    await screen.findByText(r1.bodyText);
    await attach(pdf("X.pdf"));
    await waitFor(() => expect(files.uploadAttachment).toHaveBeenCalledOnce());

    await openB();
    await openA();

    await act(async () => {
      upload.reject(new Error("rede"));
      await upload.promise.catch(() => undefined);
    });

    const status = await screen.findByTestId("chat-composer-upload-status");
    await waitFor(() => expect(status).toHaveTextContent("Não foi possível enviar o arquivo."));
    expect(within(status).getByRole("button", { name: "Tentar novamente" })).toBeVisible();
  });

  it("failed after the remount: the draft stays whole and the composer is unblocked for the retry", async () => {
    renderAt(`/chat/channel/${channelA}`);
    await replyTo(r1);
    await type("T1");
    await attach(pdf("X.pdf"));
    await expectReadyAttachment("X.pdf");
    const post = await submitAndComeBack();
    await waitFor(() => expect(screen.getByTestId("chat-send-btn")).toBeDisabled());

    await act(async () => {
      post.reject(new Error("falhou"));
      await post.promise.catch(() => undefined);
    });

    await waitFor(() => expect(screen.getByTestId("chat-send-btn")).toBeEnabled());
    expect(composer()).toHaveTextContent("T1");
    expect(quote()).toHaveTextContent("Ana");
    expect(pendingAttachments()).toHaveLength(1);
    expect(files.deleteAttachmentDraft).not.toHaveBeenCalled();

    await send();
    await waitFor(() => expect(api.postChannelMessage).toHaveBeenCalledTimes(2));
    expect(api.postChannelMessage).toHaveBeenLastCalledWith(
      channelA,
      "T1",
      expect.objectContaining({ parentMessageId: r1.id, attachmentIds: ["att-X.pdf"] }),
    );
  });

  it("stale (failed while the reader was on B): the draft stays whole and A is unblocked on return", async () => {
    renderAt(`/chat/channel/${channelA}`);
    await screen.findByText(r1.bodyText);
    await type("T1");
    await attach(pdf("X.pdf"));
    await expectReadyAttachment("X.pdf");
    const post = deferred<Message>();
    api.postChannelMessage.mockReturnValueOnce(post.promise);
    await send();
    await waitFor(() => expect(api.postChannelMessage).toHaveBeenCalledOnce());
    await openB();

    await act(async () => {
      post.reject(new Error("falhou"));
      await post.promise.catch(() => undefined);
    });
    // B shows nothing of it — no error banner either.
    expect(screen.queryByTestId("chat-send-error")).toBeNull();

    await openA();
    await waitFor(() => expect(composer()).toHaveTextContent("T1"));
    await expectReadyAttachment("X.pdf");
    await waitFor(() => expect(screen.getByTestId("chat-send-btn")).toBeEnabled());
    await send();
    await waitFor(() => expect(api.postChannelMessage).toHaveBeenCalledTimes(2));
  });
});
