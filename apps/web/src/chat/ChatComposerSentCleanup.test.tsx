import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

/**
 * Issue #875: what a confirmed send consumes, and what it must leave alone.
 *
 * The invariant these tests exist for is SENT != DRAFT — a message that the
 * server acknowledged cannot still be sitting in the composer, in the draft,
 * or in sessionStorage. The regression that motivated them: the composer
 * decided whether to clear its text by comparing the *conversation draft's*
 * revision before and after the send, and that revision is bumped by every
 * mutation — including the two a confirmed send performs itself, consuming
 * the reply the message answered and the attachments it published. So the
 * guard meant for "the reader typed something new while the ACK was in
 * flight" fired on the send's own cleanup and left the just-sent text on
 * screen.
 *
 * The counterweight is here too: the protection itself must survive. Content
 * genuinely created after the submit — an attachment dropped while the
 * request is still open — is not consumed by that request's acknowledgement.
 */

const { mockFetchMentionCandidates, mockUploadAttachment, mockDeleteAttachment } = vi.hoisted(
  () => ({
    mockFetchMentionCandidates: vi.fn(),
    mockUploadAttachment: vi.fn(),
    mockDeleteAttachment: vi.fn(),
  }),
);

vi.mock("./chatApi", () => ({
  fetchMentionCandidates: (...args: unknown[]) => mockFetchMentionCandidates(...args),
}));

vi.mock("./filesApi", async () => {
  const actual = await vi.importActual<typeof import("./filesApi")>("./filesApi");
  return {
    ...actual,
    uploadAttachment: (...args: unknown[]) => mockUploadAttachment(...args),
    deleteAttachmentDraft: (...args: unknown[]) => mockDeleteAttachment(...args),
  };
});

import ChatComposer from "./ChatComposer";
import ChatSidebar from "./ChatSidebar";
import type { Channel, DMConversation } from "./chatTypes";
import { loadDraftPersistence } from "./chatDraftPersistence";
import { useConversationDrafts } from "./useConversationDrafts";
import type { ConversationDraftsApi } from "./useConversationDrafts";
import type { SendResult } from "./useMessages";

const draftKey = "dm:caio";
const replyPreview = {
  authorLabel: "Caio",
  bodyText: "pergunta",
  bodyFormat: "v2" as const,
  isRemoved: false,
};

type SendFn = (body: string, attachmentIds?: string[]) => Promise<SendResult>;

function DraftedComposer({
  onSend,
  onDrafts,
  withReplyPreview = false,
}: {
  onSend: SendFn;
  onDrafts: (drafts: ConversationDraftsApi) => void;
  withReplyPreview?: boolean;
}) {
  const drafts = useConversationDrafts("u1");
  onDrafts(drafts);
  return (
    <ChatComposer
      bodyFormat="v2"
      placeholder="Mensagem..."
      onSend={onSend}
      uploadTarget={{ kind: "dm", id: "caio" }}
      attachmentLimits={{
        maxUploadBytes: 8 * 1024 * 1024,
        maxFiles: 10,
        maxBytes: 512 * 1024 * 1024,
      }}
      replyPreview={withReplyPreview ? replyPreview : undefined}
      drafts={drafts}
    />
  );
}

function clipboardData(plain: string): DataTransfer {
  return {
    files: [] as unknown as FileList,
    getData: vi.fn((type: string) => (type === "text/plain" ? plain : "")),
    setData: vi.fn(),
    clearData: vi.fn(),
    items: [] as unknown as DataTransferItemList,
    types: ["text/plain"],
    dropEffect: "none",
    effectAllowed: "all",
    setDragImage: vi.fn(),
  } as unknown as DataTransfer;
}

function dropData(files: File[]): DataTransfer {
  return {
    files,
    items: files as unknown as DataTransferItemList,
    types: ["Files"],
    getData: vi.fn(() => ""),
    setData: vi.fn(),
    clearData: vi.fn(),
    dropEffect: "none",
    effectAllowed: "all",
    setDragImage: vi.fn(),
  } as unknown as DataTransfer;
}

/** The editor is a real TipTap instance: a synthetic input event never reaches its document. */
async function type(text: string): Promise<HTMLElement> {
  const input = await screen.findByTestId("chat-composer-input");
  fireEvent.paste(input, { clipboardData: clipboardData(text) });
  await waitFor(() => expect(input).toHaveTextContent(text));
  return input;
}

const pdf = (name: string) => new File(["x"], name, { type: "application/pdf" });

function uploadsResolveToAttachmentIds() {
  mockUploadAttachment.mockImplementation(async (_target: unknown, file: File) => ({
    id: `att-${file.name}`,
    filename: file.name,
    contentType: "application/pdf",
    size: 1,
    status: "pending_scan",
    previewStatus: "pending",
    createdAt: "",
  }));
}

/**
 * The debounce useConversationDrafts' schedulePersist puts in front of every
 * sessionStorage write. Inline there (`setTimeout(..., 400)`), not an exported
 * constant, and not worth widening production's API to share with one test.
 */
const persistDebounceMs = 400;

beforeEach(() => {
  sessionStorage.clear();
  mockUploadAttachment.mockReset();
  mockDeleteAttachment.mockReset();
});

// Only one test below takes the clock. This is the guard that it can never
// leave it taken — including when an assertion throws mid-window. A no-op for
// every other test, which never leaves real timers.
afterEach(() => {
  vi.useRealTimers();
});

describe("a confirmed send consumes exactly what it sent (issue #875)", () => {
  /**
   * The reported bug. `onSend` here does precisely what ChatMessageArea's own
   * handleSend does on a confirmed send: it consumes the reply the message
   * answered. That consumption is a draft mutation, and it used to be
   * indistinguishable from the reader having typed something new.
   */
  it("clears the text it sent even though the send consumed a reply", async () => {
    let drafts!: ConversationDraftsApi;
    const onSend = vi.fn<SendFn>().mockImplementation(async () => {
      if (drafts.getDraft(draftKey)?.replyToMessageId) drafts.setReply(draftKey, null);
      return { status: "sent" };
    });
    render(<DraftedComposer onSend={onSend} onDrafts={(d) => (drafts = d)} withReplyPreview />);
    await screen.findByTestId("chat-composer-input");
    act(() => drafts.setReply(draftKey, "msg-1"));

    const input = await type("Vou validar agora");
    input.focus();
    fireEvent.keyDown(input, { key: "Enter", code: "Enter" });

    await waitFor(() => expect(onSend).toHaveBeenCalledOnce());
    expect(onSend.mock.calls[0][0]).toBe("Vou validar agora");
    await waitFor(() => expect(input.textContent?.trim()).toBe(""));
    await waitFor(() => expect(drafts.getDraft(draftKey)).toBeUndefined());
    expect(loadDraftPersistence("u1", draftKey)).toBeNull();
  });

  // The two entry points must not disagree about what a send consumes.
  it("clears the same way when the send is started from the Enviar button", async () => {
    let drafts!: ConversationDraftsApi;
    const onSend = vi.fn<SendFn>().mockImplementation(async () => {
      if (drafts.getDraft(draftKey)?.replyToMessageId) drafts.setReply(draftKey, null);
      return { status: "sent" };
    });
    render(<DraftedComposer onSend={onSend} onDrafts={(d) => (drafts = d)} withReplyPreview />);
    await screen.findByTestId("chat-composer-input");
    act(() => drafts.setReply(draftKey, "msg-1"));

    const input = await type("resposta pelo botão");
    fireEvent.click(screen.getByTestId("chat-send-btn"));

    await waitFor(() => expect(onSend).toHaveBeenCalledOnce());
    await waitFor(() => expect(input.textContent?.trim()).toBe(""));
    await waitFor(() => expect(drafts.getDraft(draftKey)).toBeUndefined());
  });

  /**
   * Issue #875 scenario 4, both halves at once: the attachment the message
   * carried is consumed with it, and the one dropped while the request was
   * still open belongs to the *next* message, so the acknowledgement of this
   * one must not take it — nor take the text it did send.
   */
  it("consumes the attachment it published and keeps one dropped while the send was in flight", async () => {
    uploadsResolveToAttachmentIds();
    const user = userEvent.setup();
    let resolveSend!: (result: SendResult) => void;
    const onSend = vi
      .fn<SendFn>()
      .mockReturnValue(new Promise<SendResult>((resolve) => (resolveSend = resolve)));
    let drafts!: ConversationDraftsApi;
    render(<DraftedComposer onSend={onSend} onDrafts={(d) => (drafts = d)} />);

    const input = await type("com anexo");
    await user.upload(screen.getByTestId("chat-composer-file-input"), pdf("X.pdf"));
    await screen.findByTestId("chat-composer-pending-attachment");
    await waitFor(() => expect(drafts.getDraft(draftKey)?.attachments[0]?.attachment).toBeTruthy());

    input.focus();
    fireEvent.keyDown(input, { key: "Enter", code: "Enter" });
    await waitFor(() => expect(onSend).toHaveBeenCalledOnce());
    expect(onSend.mock.calls[0][1]).toEqual(["att-X.pdf"]);

    // The reader lines up the next message's file before this one is confirmed.
    await act(async () => {
      fireEvent.drop(screen.getByTestId("chat-composer-box"), {
        dataTransfer: dropData([pdf("Y.pdf")]),
      });
    });
    await waitFor(() =>
      expect(drafts.getDraft(draftKey)?.attachments.map((item) => item.file.name)).toEqual([
        "X.pdf",
        "Y.pdf",
      ]),
    );

    await act(async () => resolveSend({ status: "sent" }));

    await waitFor(() => expect(input).toHaveAttribute("aria-disabled", "false"));
    // The text went out with the message, so it goes out of the composer.
    expect(input.textContent?.trim()).toBe("");
    // X was published by this send; Y was not, and survives it untouched.
    await waitFor(() =>
      expect(drafts.getDraft(draftKey)?.attachments.map((item) => item.file.name)).toEqual([
        "Y.pdf",
      ]),
    );
    expect(mockDeleteAttachment).not.toHaveBeenCalled();
  });

  /**
   * Nothing pending means no draft at all — not an emptied one.
   *
   * Two distinct resurrections are ruled out here. The store outlives the
   * composer in production (it is mounted in AppShell), so its own getDraft
   * going undefined is what leaving the conversation and coming back would
   * read. Remounting the hook builds a *fresh* store, which can only rehydrate
   * from sessionStorage — that is the F5 path.
   */
  it("does not bring the sent message back on a conversation switch or an F5", async () => {
    let drafts!: ConversationDraftsApi;
    const onSend = vi.fn<SendFn>().mockImplementation(async () => {
      if (drafts.getDraft(draftKey)?.replyToMessageId) drafts.setReply(draftKey, null);
      return { status: "sent" };
    });
    const view = render(
      <DraftedComposer onSend={onSend} onDrafts={(d) => (drafts = d)} withReplyPreview />,
    );
    const input = await screen.findByTestId("chat-composer-input");
    act(() => drafts.setReply(draftKey, "msg-1"));

    // The clock is taken from here to the assertion below, because the write
    // being ruled out is scheduled *by the typing*: a debounce armed under the
    // real clock would be invisible to the fake one. Everything inside this
    // window is driven by events and microtasks, never by waitFor — RTL only
    // recognises fake timers when `jest` is a global, which it is not here, so
    // a waitFor in this window would poll on a frozen clock and hang.
    vi.useFakeTimers();

    act(() => {
      fireEvent.paste(input, { clipboardData: clipboardData("nao pode voltar") });
    });
    expect(input).toHaveTextContent("nao pode voltar");
    input.focus();
    act(() => {
      fireEvent.keyDown(input, { key: "Enter", code: "Enter" });
    });

    // Advances exactly the debounce — not runAllTimers, which would also fire
    // the composer's own unrelated timers. This both settles the send (the
    // awaits in between flush its microtasks) and takes the clock past the
    // moment a surviving debounce would write the pre-send text back.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(persistDebounceMs);
    });

    expect(onSend).toHaveBeenCalledOnce();
    expect(drafts.getDraft(draftKey)).toBeUndefined();
    expect(loadDraftPersistence("u1", draftKey)).toBeNull();

    vi.useRealTimers();

    // A fresh store: whatever comes back now came from sessionStorage.
    view.unmount();
    render(<DraftedComposer onSend={onSend} onDrafts={(d) => (drafts = d)} />);
    const reopened = await screen.findByTestId("chat-composer-input");
    expect(reopened.textContent?.trim()).toBe("");
    expect(drafts.getDraft(draftKey)).toBeUndefined();
  });
});

/**
 * Issue #875, I14: an acknowledgement is scoped to one conversationKey.
 *
 * Every other conversation's draft — and the "Rascunho" badge standing for
 * it — has to come through a confirmed send elsewhere completely untouched.
 * The badge is asserted through the real ChatSidebar fed by the real store's
 * own `summaries`, which is the value AppShell passes it in production, so
 * this covers the store→sidebar step rather than re-deriving the rule here.
 */
describe("an acknowledgement is scoped to its own conversation (issue #875)", () => {
  const keyB = "channel:geral";
  const textB = {
    type: "doc",
    content: [{ type: "paragraph", content: [{ type: "text", text: "Draft B" }] }],
  };

  const sidebarState = {
    status: "ready" as const,
    currentUserId: "u1",
    workspaceId: "w1",
    channels: [{ id: "geral", name: "geral", type: "public", canWrite: true }] as Channel[],
    dms: [{ id: "caio", type: "1:1", name: "Caio", participants: [] }] as DMConversation[],
    categories: [],
  };

  /** The composer for A and the sidebar, both reading the one real store. */
  function ComposerBesideSidebar({
    onSend,
    onDrafts,
    path,
  }: {
    onSend: SendFn;
    onDrafts: (drafts: ConversationDraftsApi) => void;
    path: string;
  }) {
    const drafts = useConversationDrafts("u1");
    onDrafts(drafts);
    return (
      <MemoryRouter initialEntries={[path]}>
        <ChatComposer
          bodyFormat="v2"
          placeholder="Mensagem..."
          onSend={onSend}
          uploadTarget={{ kind: "dm", id: "caio" }}
          drafts={drafts}
        />
        <ChatSidebar state={sidebarState} retry={() => {}} draftSummaries={drafts.summaries} />
      </MemoryRouter>
    );
  }

  const badges = () => screen.queryAllByTestId("chat-sidebar-draft-badge");
  // Regexes, not exact names: a row's accessible name also carries presence
  // and pinned suffixes that have nothing to do with this test.
  const rowFor = (name: RegExp) => screen.getByRole("option", { name });
  const channelB = /canal geral/i;
  const dmA = /mensagem direta com caio/i;

  it("leaves another conversation's draft, summary and badge exactly as they were", async () => {
    let drafts!: ConversationDraftsApi;
    const onSend = vi.fn<SendFn>().mockResolvedValue({ status: "sent" });
    render(
      // A is the conversation being composed, so it is also the active row —
      // the state the app is actually in while this send happens.
      <ComposerBesideSidebar onSend={onSend} onDrafts={(d) => (drafts = d)} path="/chat/dm/caio" />,
    );
    await screen.findByTestId("chat-composer-input");

    // B's draft is what an earlier visit to the channel left behind.
    act(() => drafts.setText(keyB, textB));
    const input = await type("Mensagem A");

    expect(drafts.getDraft(draftKey)).toBeDefined();
    expect(drafts.summaries.has(draftKey)).toBe(true);
    expect(drafts.summaries.has(keyB)).toBe(true);
    // A is active, so only B wears a badge (issue #845, "CONVERSA ATIVA").
    await waitFor(() => expect(badges()).toHaveLength(1));
    expect(within(rowFor(channelB)).getByTestId("chat-sidebar-draft-badge")).toBeVisible();
    const draftBBefore = drafts.getDraft(keyB);

    input.focus();
    fireEvent.keyDown(input, { key: "Enter", code: "Enter" });
    await waitFor(() => expect(onSend).toHaveBeenCalledOnce());

    // A: consumed, and gone from the value the sidebar renders from.
    await waitFor(() => expect(drafts.getDraft(draftKey)).toBeUndefined());
    expect(drafts.summaries.has(draftKey)).toBe(false);
    expect(loadDraftPersistence("u1", draftKey)).toBeNull();

    // B: not one field of it moved. Comparing the whole object, revision
    // included, is what rules out a mutation that happened to be a no-op.
    expect(drafts.getDraft(keyB)).toEqual(draftBBefore);
    expect(drafts.getDraft(keyB)?.text).toEqual(textB);
    expect(drafts.summaries.has(keyB)).toBe(true);
    expect(badges()).toHaveLength(1);
    expect(within(rowFor(channelB)).getByTestId("chat-sidebar-draft-badge")).toBeVisible();
  });

  /**
   * A's badge is hidden while A is the open conversation whether or not a
   * draft exists, so the test above cannot show it going away. This is the
   * same store, after the same send, rendered where A is just another row.
   */
  it("stops claiming a draft for the conversation it emptied once that row is not the open one", async () => {
    let drafts!: ConversationDraftsApi;
    const onSend = vi.fn<SendFn>().mockResolvedValue({ status: "sent" });
    const view = render(
      <ComposerBesideSidebar onSend={onSend} onDrafts={(d) => (drafts = d)} path="/chat/dm/caio" />,
    );
    await screen.findByTestId("chat-composer-input");
    act(() => drafts.setText(keyB, textB));
    const input = await type("Mensagem A");
    input.focus();
    fireEvent.keyDown(input, { key: "Enter", code: "Enter" });
    await waitFor(() => expect(drafts.getDraft(draftKey)).toBeUndefined());

    // The reader leaves the conversation: the sidebar now draws A as an
    // ordinary row, from the store's own post-send summaries.
    view.unmount();
    render(
      <MemoryRouter initialEntries={["/chat"]}>
        <ChatSidebar state={sidebarState} retry={() => {}} draftSummaries={drafts.summaries} />
      </MemoryRouter>,
    );

    expect(badges()).toHaveLength(1);
    expect(within(rowFor(channelB)).getByTestId("chat-sidebar-draft-badge")).toBeVisible();
    expect(within(rowFor(dmA)).queryByTestId("chat-sidebar-draft-badge")).toBeNull();
  });
});
