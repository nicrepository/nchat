import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { useLayoutEffect } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

/**
 * Issue #929, at the composer's own boundary: ChatMessageArea keys the
 * composer by conversation, so a switch is an unmount of A's instance and a
 * mount of B's, then A's again — against the one draft store that outlives
 * them all. What these tests drive is exactly that remount, with the real
 * recorder, editor and upload queue, and what they assert is what the
 * reader gets back on screen.
 *
 * The reply lives here too, but its preview is ChatMessageArea's to
 * re-derive from the store (chatDraftLifecycle.test.tsx); at this level it is
 * the draft's id that must survive, and that is what is asserted.
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
import type { SendResult } from "./useMessages";
import { useConversationDrafts, type ConversationDraftsApi } from "./useConversationDrafts";
import { loadDraftPersistence } from "./chatDraftPersistence";

const keyA = "dm:caio";
const keyB = "dm:juliane";
const targets = {
  [keyA]: { kind: "dm" as const, id: "caio" },
  [keyB]: { kind: "dm" as const, id: "juliane" },
};

type SendFn = (body: string, attachmentIds?: string[]) => Promise<SendResult>;

/**
 * A different reader per test. The store mirrors text to sessionStorage
 * behind a 400ms debounce, and these tests run on the real clock: a timer
 * armed by one test can land after the next has cleared storage, and be
 * hydrated back by it. Scoping by user makes that impossible rather than
 * unlikely.
 */
let userId = "u0";

/**
 * A send attempted from inside the commit an acknowledgement lands in —
 * after React has written the DOM, before any passive effect of that commit
 * has run. That is the window the second review named: nothing is pending
 * any more, while this composer's mirrors still hold what the send carried.
 *
 * It takes `drafts` so the store's own update re-renders it in that very
 * commit, and it is rendered *ahead* of the composer, so its layout effect
 * runs before the composer's own reconciliation. A MutationObserver cannot
 * serve here: its callback is a microtask, and by the time one runs React
 * has already flushed the reconciliation.
 *
 * The send is attempted through Enter in the editor, and the button is only
 * read. Two different things are then provable: that the button was not
 * offered (the UX), and that a send which really did reach the composer's
 * own handler was refused (the invariant). Clicking the button would prove
 * neither — React suppresses handlers on an element whose props say
 * disabled, whatever the DOM property is set to.
 */
const probe = { armed: false, frames: 0, sawSendable: false };

function SendWindowProbe({ drafts }: { drafts: ConversationDraftsApi }) {
  void drafts;
  useLayoutEffect(() => {
    if (!probe.armed) return;
    const input = document.querySelector<HTMLElement>('[data-testid="chat-composer-input"]');
    const button = document.querySelector<HTMLButtonElement>('[data-testid="chat-send-btn"]');
    if (!input || !button) return;
    probe.armed = false;
    probe.frames += 1;
    probe.sawSendable = !button.disabled;
    input.dispatchEvent(
      new KeyboardEvent("keydown", {
        key: "Enter",
        code: "Enter",
        bubbles: true,
        cancelable: true,
      }),
    );
  });
  return null;
}

/** The store as AppShell holds it, above a composer keyed by conversation as ChatMessageArea keys it. */
function Shell({
  conversation,
  onSend,
  onDrafts,
}: {
  conversation: keyof typeof targets;
  onSend: SendFn;
  onDrafts: (drafts: ConversationDraftsApi) => void;
}) {
  const drafts = useConversationDrafts(userId);
  onDrafts(drafts);
  return (
    <>
      <SendWindowProbe drafts={drafts} />
      <ChatComposer
        key={conversation}
        bodyFormat="v2"
        placeholder="Mensagem..."
        onSend={onSend}
        uploadTarget={targets[conversation]}
        attachmentLimits={{ maxUploadBytes: null, maxFiles: 10, maxBytes: Number.MAX_SAFE_INTEGER }}
        drafts={drafts}
      />
    </>
  );
}

function mount(onSend: SendFn = async () => ({ status: "sent" })) {
  let drafts!: ConversationDraftsApi;
  const shell = (conversation: keyof typeof targets) => (
    <Shell conversation={conversation} onSend={onSend} onDrafts={(d) => (drafts = d)} />
  );
  const view = render(shell(keyA));
  return {
    getDrafts: () => drafts,
    /** A → B → A, each step a real unmount of the previous composer instance. */
    switchTo: async (conversation: keyof typeof targets) => {
      view.rerender(shell(conversation));
      await screen.findByTestId("chat-composer-box");
    },
  };
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

async function type(text: string): Promise<HTMLElement> {
  const input = await screen.findByTestId("chat-composer-input");
  fireEvent.paste(input, { clipboardData: clipboardData(text) });
  await waitFor(() => expect(input).toHaveTextContent(text));
  return input;
}

/** Records and stops: the recording is finished, reviewable and unsent. */
async function recordVoice() {
  fireEvent.click(screen.getByTestId("chat-composer-record-btn"));
  await screen.findByTestId("chat-voice-stop");
  fireEvent.click(screen.getByTestId("chat-voice-stop"));
  await screen.findByTestId("chat-voice-send");
}

/**
 * `stop()` finalizes asynchronously in a real browser, and the composer may
 * well be gone by the time it does. `deferStop` models that: the
 * finalization waits in `pendingFinalizations` until a test runs it.
 */
const pendingFinalizations: Array<() => void> = [];
/** Every recorder this test file built, so a test can assert on the real calls. */
const mediaRecorders: FakeMediaRecorder[] = [];

class FakeMediaRecorder {
  static isTypeSupported(type: string): boolean {
    return type === "audio/webm;codecs=opus";
  }
  static deferStop = false;
  state: "inactive" | "recording" | "paused" = "inactive";
  stopCalls = 0;
  ondataavailable: ((event: { data: Blob }) => void) | null = null;
  onstop: (() => void) | null = null;
  onerror: (() => void) | null = null;
  constructor() {
    mediaRecorders.push(this);
  }
  start(): void {
    this.state = "recording";
  }
  stop(): void {
    this.stopCalls += 1;
    this.state = "inactive";
    const finalize = () => {
      this.ondataavailable?.({ data: new Blob(["chunk"]) });
      this.onstop?.();
    };
    if (FakeMediaRecorder.deferStop) pendingFinalizations.push(finalize);
    else finalize();
  }
}

const originalMediaDevices = navigator.mediaDevices;
let previewUrls = 0;

let testCount = 0;

beforeEach(() => {
  userId = `u${++testCount}`;
  probe.armed = false;
  probe.frames = 0;
  probe.sawSendable = false;
  FakeMediaRecorder.deferStop = false;
  pendingFinalizations.length = 0;
  mediaRecorders.length = 0;
  sessionStorage.clear();
  mockFetchMentionCandidates.mockReset().mockResolvedValue([]);
  mockUploadAttachment.mockReset();
  mockDeleteAttachment.mockReset().mockResolvedValue(undefined);
  Object.defineProperty(navigator, "mediaDevices", {
    value: {
      getUserMedia: vi.fn(async () => ({ getTracks: () => [{ stop: vi.fn() }] })),
    },
    configurable: true,
  });
  vi.stubGlobal("MediaRecorder", FakeMediaRecorder);
  previewUrls = 0;
  vi.stubGlobal("URL", {
    ...URL,
    createObjectURL: vi.fn(() => `blob:voice-${++previewUrls}`),
    revokeObjectURL: vi.fn(),
  });
});

afterEach(() => {
  Object.defineProperty(navigator, "mediaDevices", {
    value: originalMediaDevices,
    configurable: true,
  });
  vi.unstubAllGlobals();
});

describe("a finished voice message is draft state (issue #929)", () => {
  it("survives A → B → A with its preview intact, and B never sees it", async () => {
    const { getDrafts, switchTo } = mount();
    await screen.findByTestId("chat-composer-record-btn");
    await recordVoice();
    expect(getDrafts().getDraft(keyA)?.voiceMessage?.previewUrl).toBe("blob:voice-1");

    await switchTo(keyB);
    expect(screen.queryByTestId("chat-voice-recorder")).toBeNull();
    expect(screen.getByTestId("chat-composer-record-btn")).toBeEnabled();
    expect(URL.revokeObjectURL).not.toHaveBeenCalled();

    await switchTo(keyA);
    expect(await screen.findByTestId("chat-voice-send")).toBeVisible();
    expect(screen.getByTestId("chat-voice-recorder")).toBeVisible();
    expect(getDrafts().getDraft(keyA)?.voiceMessage?.previewUrl).toBe("blob:voice-1");
    expect(URL.revokeObjectURL).not.toHaveBeenCalled();
  });

  it("keeps a composite draft — reply id, text and voice — together across the round trip", async () => {
    const { getDrafts, switchTo } = mount();
    await screen.findByTestId("chat-composer-record-btn");
    act(() => getDrafts().setReply(keyA, "R1"));
    await type("T1");
    await recordVoice();

    await switchTo(keyB);
    await type("draft B");
    await switchTo(keyA);

    expect(await screen.findByTestId("chat-voice-send")).toBeVisible();
    // The editor is hidden behind the voice panel while a recording is
    // under review, but its text is still the draft's.
    expect(getDrafts().getDraft(keyA)).toMatchObject({
      replyToMessageId: "R1",
      voiceMessage: { previewUrl: "blob:voice-1" },
    });
    expect(getDrafts().getDraft(keyA)?.text).toMatchObject({
      content: [{ content: [{ text: "T1" }] }],
    });
    // Discarding the recording brings the editor back with T1 in it.
    fireEvent.click(screen.getByTestId("chat-voice-discard"));
    await waitFor(() => expect(screen.getByTestId("chat-composer-input")).toHaveTextContent("T1"));
    expect(getDrafts().getDraft(keyA)?.voiceMessage).toBeNull();
    expect(URL.revokeObjectURL).toHaveBeenCalledWith("blob:voice-1");
    expect(getDrafts().getDraft(keyB)?.text).toMatchObject({
      content: [{ content: [{ text: "draft B" }] }],
    });
  });

  it("a confirmed voice send consumes the recording and the reply it answered, and leaves the text", async () => {
    mockUploadAttachment.mockResolvedValue({ id: "att-voice" });
    const onSend = vi.fn<SendFn>().mockResolvedValue({ status: "sent" });
    const { getDrafts } = mount(onSend);
    await screen.findByTestId("chat-composer-record-btn");
    act(() => getDrafts().setReply(keyA, "R1"));
    await type("T1");
    await recordVoice();

    fireEvent.click(screen.getByTestId("chat-voice-send"));

    await waitFor(() => expect(onSend).toHaveBeenCalledWith("", ["att-voice"], expect.anything()));
    await waitFor(() => expect(screen.queryByTestId("chat-voice-recorder")).toBeNull());
    const after = getDrafts().getDraft(keyA);
    expect(after?.voiceMessage).toBeNull();
    expect(after?.replyToMessageId).toBeNull();
    expect(after?.text).toMatchObject({ content: [{ content: [{ text: "T1" }] }] });
    expect(screen.getByTestId("chat-composer-input")).toHaveTextContent("T1");
    expect(URL.revokeObjectURL).toHaveBeenCalledTimes(1);
    expect(URL.revokeObjectURL).toHaveBeenCalledWith("blob:voice-1");
  });

  it("a voice send that is not confirmed keeps the recording, the reply and the text", async () => {
    mockUploadAttachment.mockResolvedValue({ id: "att-voice" });
    const onSend = vi.fn<SendFn>().mockResolvedValue({ status: "stale" });
    const { getDrafts } = mount(onSend);
    await screen.findByTestId("chat-composer-record-btn");
    act(() => getDrafts().setReply(keyA, "R1"));
    await type("T1");
    await recordVoice();

    fireEvent.click(screen.getByTestId("chat-voice-send"));

    await waitFor(() => expect(onSend).toHaveBeenCalledOnce());
    expect(await screen.findByTestId("chat-voice-send")).toBeVisible();
    expect(getDrafts().getDraft(keyA)).toMatchObject({
      replyToMessageId: "R1",
      voiceMessage: { previewUrl: "blob:voice-1" },
    });
    expect(URL.revokeObjectURL).not.toHaveBeenCalled();
  });

  /**
   * Code Quality Review of #929, finding 1, voice half: the reader comes
   * back while the recording's send is still open. The recorder mounted
   * then is seeded with V1 from the draft; it must show the send as going
   * out, never offer to send V1 again, and step aside — without touching
   * the URL the store owns — once the old instance's acknowledgement
   * consumes V1.
   */
  it("a send in flight survives the recorder's own remount: pending on return, idle once acknowledged, one revoke", async () => {
    mockUploadAttachment.mockResolvedValue({ id: "att-voice" });
    let confirm!: (result: SendResult) => void;
    const onSend = vi
      .fn<SendFn>()
      .mockReturnValue(new Promise<SendResult>((resolve) => (confirm = resolve)));
    const { getDrafts, switchTo } = mount(onSend);
    await screen.findByTestId("chat-composer-record-btn");
    await recordVoice();
    fireEvent.click(screen.getByTestId("chat-voice-send"));
    await waitFor(() => expect(onSend).toHaveBeenCalledOnce());

    await switchTo(keyB);
    await switchTo(keyA);

    // V1 is still the draft's, shown as going out: nothing to send or discard.
    expect(getDrafts().getDraft(keyA)?.voiceMessage?.previewUrl).toBe("blob:voice-1");
    expect(await screen.findByTestId("chat-voice-recorder")).toHaveTextContent(
      "Enviando mensagem de voz…",
    );
    expect(screen.queryByTestId("chat-voice-send")).toBeNull();
    expect(URL.revokeObjectURL).not.toHaveBeenCalled();

    await act(async () => confirm({ status: "sent" }));

    await waitFor(() => expect(screen.queryByTestId("chat-voice-recorder")).toBeNull());
    expect(screen.getByTestId("chat-composer-record-btn")).toBeEnabled();
    expect(getDrafts().getDraft(keyA)).toBeUndefined();
    expect(URL.revokeObjectURL).toHaveBeenCalledTimes(1);
    expect(URL.revokeObjectURL).toHaveBeenCalledWith("blob:voice-1");
    expect(onSend).toHaveBeenCalledOnce();
  });

  it("a voice send that fails after the remount hands the recording back for the retry", async () => {
    mockUploadAttachment.mockResolvedValue({ id: "att-voice" });
    let fail!: (error: Error) => void;
    const onSend = vi
      .fn<SendFn>()
      .mockReturnValue(new Promise<SendResult>((_resolve, reject) => (fail = reject)));
    const { getDrafts, switchTo } = mount(onSend);
    await screen.findByTestId("chat-composer-record-btn");
    await recordVoice();
    fireEvent.click(screen.getByTestId("chat-voice-send"));
    await waitFor(() => expect(onSend).toHaveBeenCalledOnce());

    await switchTo(keyB);
    await switchTo(keyA);
    expect(await screen.findByTestId("chat-voice-recorder")).toHaveTextContent(
      "Enviando mensagem de voz…",
    );

    await act(async () => fail(new Error("falhou")));

    expect(await screen.findByTestId("chat-voice-send")).toBeVisible();
    expect(getDrafts().getDraft(keyA)?.voiceMessage?.previewUrl).toBe("blob:voice-1");
    expect(URL.revokeObjectURL).not.toHaveBeenCalled();
  });
});

/**
 * Second Code Quality Review, finding A: between the acknowledgement and
 * the mirrors converging there must be no instant in which this draft can
 * be sent again — the button is UX, the draft's own guard is the invariant.
 */
describe("the window between an acknowledgement and the mirrors converging (#929 review 2)", () => {
  async function attachX() {
    mockUploadAttachment.mockResolvedValue({ id: "att-X" });
    const file = new File(["x"], "X.pdf", { type: "application/pdf" });
    fireEvent.change(screen.getByTestId("chat-composer-file-input"), { target: { files: [file] } });
    await screen.findByTestId("chat-composer-pending-attachment");
  }

  it("refuses a send issued in the commit the acknowledgement lands in", async () => {
    let confirm!: (result: SendResult) => void;
    const onSend = vi
      .fn<SendFn>()
      .mockReturnValue(new Promise<SendResult>((resolve) => (confirm = resolve)));
    const { getDrafts, switchTo } = mount(onSend);
    await type("T1");
    await attachX();
    fireEvent.click(screen.getByTestId("chat-send-btn"));
    await waitFor(() => expect(onSend).toHaveBeenCalledOnce());

    await switchTo(keyB);
    await switchTo(keyA);
    // The remounted composer holds what the send carried, and refuses to send it.
    await waitFor(() => expect(screen.getByTestId("chat-composer-input")).toHaveTextContent("T1"));
    expect(screen.getByTestId("chat-send-btn")).toBeDisabled();

    probe.armed = true;
    await act(async () => confirm({ status: "sent" }));

    // The window really was exercised — otherwise this proves nothing.
    expect(probe.frames).toBe(1);
    // The button was not offered as sendable (the UX)…
    expect(probe.sawSendable).toBe(false);
    // …and the Enter the probe pressed reached the composer's own send
    // path and was refused there by the draft's guard (the invariant).
    expect(onSend).toHaveBeenCalledOnce();
    await waitFor(() => expect(screen.getByTestId("chat-composer-input")).toHaveTextContent(""));
    expect(screen.queryAllByTestId("chat-composer-pending-attachment")).toHaveLength(0);
    expect(getDrafts().getDraft(keyA)).toBeUndefined();
    expect(screen.getByTestId("chat-send-btn")).toBeDisabled();
  });
});

/**
 * Second Code Quality Review, finding C: a recording finalized after the
 * composer was remounted. The recording belongs to the draft, so the
 * instance on screen has to show it — without another navigation.
 */
describe("a recording finalized after the recorder's remount (#929 review 2)", () => {
  it("adopts the recording the draft received while this instance was already mounted", async () => {
    FakeMediaRecorder.deferStop = true;
    const { getDrafts, switchTo } = mount();
    fireEvent.click(await screen.findByTestId("chat-composer-record-btn"));
    await screen.findByTestId("chat-voice-stop");

    // Leaving stops the microphone; the blob is still being finalized.
    await switchTo(keyB);
    await switchTo(keyA);
    expect(screen.queryByTestId("chat-voice-recorder")).toBeNull();
    expect(getDrafts().getDraft(keyA)?.voiceMessage ?? null).toBeNull();
    expect(pendingFinalizations).toHaveLength(1);

    await act(async () => pendingFinalizations.pop()!());

    // The draft has it, and so does the composer the reader is looking at.
    expect(getDrafts().getDraft(keyA)?.voiceMessage?.previewUrl).toBe("blob:voice-1");
    expect(await screen.findByTestId("chat-voice-send")).toBeVisible();
    expect(URL.createObjectURL).toHaveBeenCalledTimes(1);
    expect(URL.revokeObjectURL).not.toHaveBeenCalled();
  });

  it("sends the adopted recording, and the store releases its URL exactly once", async () => {
    FakeMediaRecorder.deferStop = true;
    mockUploadAttachment.mockResolvedValue({ id: "att-voice" });
    const onSend = vi.fn<SendFn>().mockResolvedValue({ status: "sent" });
    const { getDrafts, switchTo } = mount(onSend);
    fireEvent.click(await screen.findByTestId("chat-composer-record-btn"));
    await screen.findByTestId("chat-voice-stop");
    await switchTo(keyB);
    await switchTo(keyA);
    await act(async () => pendingFinalizations.pop()!());
    await screen.findByTestId("chat-voice-send");

    fireEvent.click(screen.getByTestId("chat-voice-send"));

    await waitFor(() => expect(onSend).toHaveBeenCalledWith("", ["att-voice"], expect.anything()));
    await waitFor(() => expect(screen.queryByTestId("chat-voice-recorder")).toBeNull());
    expect(getDrafts().getDraft(keyA)).toBeUndefined();
    expect(URL.revokeObjectURL).toHaveBeenCalledTimes(1);
    expect(URL.revokeObjectURL).toHaveBeenCalledWith("blob:voice-1");
  });
});

/**
 * The same window reached through a path the reader really can take: the
 * draft receives a recording finalized elsewhere while this composer is
 * idle with text in it, and Enter is pressed in the very commit that
 * announces it. The editor is editable here — nothing was pending — so the
 * composer's own send path runs, and only the draft's guard can refuse it.
 */
describe("Enter pressed in the commit an external change lands in (#929 review 3)", () => {
  it("refuses the send until this composer has taken in what the draft received", async () => {
    FakeMediaRecorder.deferStop = true;
    const onSend = vi.fn<SendFn>().mockResolvedValue({ status: "sent" });
    const { getDrafts, switchTo } = mount(onSend);
    fireEvent.click(await screen.findByTestId("chat-composer-record-btn"));
    await screen.findByTestId("chat-voice-stop");
    await switchTo(keyB);
    await switchTo(keyA);
    await type("T1");
    expect(screen.getByTestId("chat-send-btn")).toBeEnabled();

    probe.armed = true;
    await act(async () => pendingFinalizations.pop()!());

    // The commit was exercised, and the Enter pressed in it sent nothing.
    expect(probe.frames).toBe(1);
    expect(onSend).not.toHaveBeenCalled();
    // Once the recording is in, the composer is usable again — with it.
    expect(await screen.findByTestId("chat-voice-send")).toBeVisible();
    expect(getDrafts().getDraft(keyA)?.voiceMessage?.previewUrl).toBe("blob:voice-1");
  });
});

/**
 * Third Code Quality Review, finding A: an upload outlives the session it
 * was started in. `clearAllDrafts` empties the store, but a request already
 * in flight still holds the store, the draft key and the local id — and a
 * queue built afterwards hands out those same local ids again.
 */
describe("an upload that lands after the drafts were cleared (#929 review 3)", () => {
  async function attachOne(name: string) {
    const file = new File(["x"], name, { type: "application/pdf" });
    fireEvent.change(screen.getByTestId("chat-composer-file-input"), { target: { files: [file] } });
  }

  /** Starts an upload that never settles on its own, then clears the session. */
  async function uploadAcrossAClear() {
    let settle!: (result: { id: string } | Error) => void;
    mockUploadAttachment.mockReturnValueOnce(
      new Promise<{ id: string }>((resolve, reject) => {
        settle = (result) => (result instanceof Error ? reject(result) : resolve(result));
      }),
    );
    const view = mount();
    await attachOne("X.pdf");
    await waitFor(() => expect(mockUploadAttachment).toHaveBeenCalledOnce());

    act(() => view.getDrafts().clearAllDrafts());

    // A new composer for the same conversation: its queue starts over, so
    // the next file it takes is handed the very local id the upload still
    // in flight was given.
    await view.switchTo(keyB);
    await view.switchTo(keyA);
    mockUploadAttachment.mockResolvedValue({ id: "att-new" });
    await attachOne("X.pdf");
    await screen.findByTestId("chat-composer-pending-attachment");
    const current = view.getDrafts().getDraft(keyA)?.attachments[0];
    expect(current?.localId).toBe("attachment-1");
    expect(current?.attachment?.id).toBe("att-new");
    return { ...view, settle, current, revision: view.getDrafts().getMirrorRevision(keyA) };
  }

  it("leaves the new queue untouched when the old upload succeeds, and cleans up its attachment", async () => {
    const { getDrafts, settle, current, revision } = await uploadAcrossAClear();

    await act(async () => settle({ id: "att-old" }));

    expect(getDrafts().getDraft(keyA)?.attachments[0]).toBe(current);
    expect(getDrafts().getMirrorRevision(keyA)).toBe(revision);
    await waitFor(() => expect(mockDeleteAttachment).toHaveBeenCalledWith("att-old"));
    expect(screen.getByTestId("chat-composer-pending-attachment")).toHaveTextContent(
      "Pronto para enviar",
    );
  });

  it("leaves the new queue untouched when the old upload fails", async () => {
    const { getDrafts, settle, current, revision } = await uploadAcrossAClear();

    await act(async () => {
      settle(new Error("rede"));
      await Promise.resolve();
    });

    expect(getDrafts().getDraft(keyA)?.attachments[0]).toBe(current);
    expect(getDrafts().getMirrorRevision(keyA)).toBe(revision);
    expect(screen.getByTestId("chat-composer-upload-status")).not.toHaveTextContent(
      "Não foi possível enviar o arquivo.",
    );
  });
});

/**
 * Fourth Code Quality Review, finding A: the progress reports of an upload
 * outlive the session it was started in just as its result does — and the
 * queue they update is mirrored straight into the draft store.
 */
describe("progress reported after the drafts were cleared (#929 review 4)", () => {
  /** Starts an upload and hands back the progress callback the request was given. */
  async function uploadReporting() {
    let report!: (progress: { loaded: number; total: number }) => void;
    mockUploadAttachment.mockImplementationOnce(
      (
        _target: unknown,
        _file: File,
        _limit: unknown,
        _signal: unknown,
        onProgress: (progress: { loaded: number; total: number }) => void,
      ) => {
        report = onProgress;
        return new Promise<{ id: string }>(() => undefined);
      },
    );
    const view = mount();
    const file = new File(["x"], "X.pdf", { type: "application/pdf" });
    fireEvent.change(screen.getByTestId("chat-composer-file-input"), { target: { files: [file] } });
    await waitFor(() => expect(mockUploadAttachment).toHaveBeenCalledOnce());
    await waitFor(() => expect(view.getDrafts().getDraft(keyA)?.attachments).toHaveLength(1));
    return { ...view, report: () => report({ loaded: 512, total: 1024 }) };
  }

  it("brings back neither the draft, nor its summary, nor its persistence", async () => {
    const { getDrafts, report } = await uploadReporting();

    act(() => getDrafts().clearAllDrafts());
    act(() => report());

    expect(getDrafts().getDraft(keyA)).toBeUndefined();
    expect(getDrafts().summaries.size).toBe(0);
    expect(getDrafts().getMirrorRevision(keyA)).toBe(0);
    expect(loadDraftPersistence(userId, keyA)).toBeNull();
  });

  /**
   * Here the queue that must not be touched belongs to a composer mounted
   * after the clear. Two things protect it: the old hook is unmounted, so
   * its local write is inert, and the report itself belongs to a session
   * that is over.
   */
  it("does not touch the queue a new session built under the same local id", async () => {
    const { getDrafts, switchTo, report } = await uploadReporting();
    act(() => getDrafts().clearAllDrafts());

    await switchTo(keyB);
    await switchTo(keyA);
    mockUploadAttachment.mockResolvedValue({ id: "att-new" });
    const replacement = new File(["y"], "NEW.pdf", { type: "application/pdf" });
    fireEvent.change(screen.getByTestId("chat-composer-file-input"), {
      target: { files: [replacement] },
    });
    await screen.findByTestId("chat-composer-pending-attachment");
    const item = getDrafts().getDraft(keyA)?.attachments[0];
    expect(item?.localId).toBe("attachment-1");
    const revision = getDrafts().getMirrorRevision(keyA);

    act(() => report());

    expect(getDrafts().getDraft(keyA)?.attachments).toEqual([item]);
    expect(getDrafts().getDraft(keyA)?.attachments[0]).toBe(item);
    expect(getDrafts().getMirrorRevision(keyA)).toBe(revision);
    expect(screen.getByTestId("chat-composer-pending-attachment")).toHaveTextContent("NEW.pdf");
  });

  it("keeps reporting normally while the session is the one it started in", async () => {
    const { getDrafts, report } = await uploadReporting();

    act(() => report());

    expect(getDrafts().getDraft(keyA)?.attachments[0]?.progress).toEqual({
      loaded: 512,
      total: 1024,
    });
    expect(screen.getByTestId("chat-composer-upload-status")).toHaveTextContent("50%");
  });
});

/**
 * Fifth Code Quality Review: `clearAllDrafts` ends a session in the store,
 * but a composer that stays mounted keeps its own mirrors — a document, a
 * queue, a recorder. Content left there belongs to a session that is over,
 * and must not be able to start anything new in the one that replaced it.
 */
describe("a composer still mounted when the drafts are cleared (#929 review 5)", () => {
  function pdf(name: string) {
    return new File(["x"], name, { type: "application/pdf" });
  }

  async function attachFiles(...names: string[]) {
    fireEvent.change(screen.getByTestId("chat-composer-file-input"), {
      target: { files: names.map(pdf) },
    });
  }

  it("does not start an upload queued before the clear, and takes new files normally", async () => {
    // Two requests that never settle: the third file has to wait for a slot.
    const held: Array<(value: { id: string }) => void> = [];
    mockUploadAttachment.mockImplementation(
      () => new Promise<{ id: string }>((resolve) => held.push(resolve)),
    );
    const { getDrafts } = mount();
    await screen.findByTestId("chat-composer-file-input");
    await attachFiles("X1.pdf", "X2.pdf", "X3.pdf");
    await waitFor(() => expect(mockUploadAttachment).toHaveBeenCalledTimes(2));
    const started = () =>
      mockUploadAttachment.mock.calls.map((call) => (call[1] as File).name).sort();
    expect(started()).toEqual(["X1.pdf", "X2.pdf"]);

    act(() => getDrafts().clearAllDrafts());

    // A slot frees up: the file queued in the session that ended must not
    // take it, and nothing of that session may be left in the queue.
    await act(async () => held[0]({ id: "att-X1" }));
    await waitFor(() =>
      expect(screen.queryAllByTestId("chat-composer-upload-status")).toHaveLength(0),
    );
    expect(started()).toEqual(["X1.pdf", "X2.pdf"]);
    expect(getDrafts().getDraft(keyA)).toBeUndefined();

    // The composer still works: a file chosen now belongs to this session.
    mockUploadAttachment.mockResolvedValue({ id: "att-Y" });
    await attachFiles("Y.pdf");
    await screen.findByTestId("chat-composer-pending-attachment");
    expect(screen.getByTestId("chat-composer-pending-attachment")).toHaveTextContent("Y.pdf");
    expect(
      getDrafts()
        .getDraft(keyA)
        ?.attachments.map((item) => item.file.name),
    ).toEqual(["Y.pdf"]);
  });

  it("empties the editor, and takes what is typed afterwards as this session's", async () => {
    const { getDrafts } = mount();
    const input = await type("T1");
    expect(getDrafts().getDraft(keyA)?.text).toBeTruthy();
    expect(screen.getByTestId("chat-send-btn")).toBeEnabled();
    // Nothing here ever moved a mirror revision: text alone never does.
    expect(getDrafts().getMirrorRevision(keyA)).toBe(0);

    act(() => getDrafts().clearAllDrafts());

    await waitFor(() => expect(input).toHaveTextContent(""));
    expect(screen.getByTestId("chat-send-btn")).toBeDisabled();
    expect(getDrafts().getDraft(keyA)).toBeUndefined();
    expect(getDrafts().summaries.size).toBe(0);
    expect(loadDraftPersistence(userId, keyA)).toBeNull();

    await type("T2");
    expect(getDrafts().getDraft(keyA)?.text).toMatchObject({
      content: [{ content: [{ text: "T2" }] }],
    });
  });
});

/**
 * Sixth Code Quality Review: an upload holds a concurrency slot, and only
 * the upload that took a slot may give it back. A settlement belonging to a
 * session that ended must not hand a slot to the session that replaced it.
 */
describe("upload slots belong to the upload that took them (#929 review 6)", () => {
  /** Files whose requests never settle on their own. */
  function heldUploads() {
    const settle: Array<(value: { id: string }) => void> = [];
    mockUploadAttachment.mockImplementation(
      () => new Promise<{ id: string }>((resolve) => settle.push(resolve)),
    );
    return settle;
  }

  const startedNames = () => mockUploadAttachment.mock.calls.map((call) => (call[1] as File).name);

  async function choose(...names: string[]) {
    fireEvent.change(screen.getByTestId("chat-composer-file-input"), {
      target: { files: names.map((name) => new File(["x"], name, { type: "application/pdf" })) },
    });
  }

  it("does not let a settlement from the ended session free a slot of the new one", async () => {
    const settle = heldUploads();
    const { getDrafts } = mount();
    await screen.findByTestId("chat-composer-file-input");
    await choose("X1.pdf", "X2.pdf");
    await waitFor(() => expect(startedNames()).toEqual(["X1.pdf", "X2.pdf"]));

    act(() => getDrafts().clearAllDrafts());

    // The new session fills both slots and queues a third file.
    await choose("Y1.pdf", "Y2.pdf", "Y3.pdf");
    await waitFor(() => expect(startedNames()).toEqual(["X1.pdf", "X2.pdf", "Y1.pdf", "Y2.pdf"]));

    // Now the ended session's requests settle. Neither may admit Y3.
    await act(async () => settle[0]({ id: "att-X1" }));
    expect(startedNames()).not.toContain("Y3.pdf");
    await act(async () => settle[1]({ id: "att-X2" }));
    expect(startedNames()).not.toContain("Y3.pdf");

    // Only a slot of this session admits it.
    await act(async () => settle[2]({ id: "att-Y1" }));
    await waitFor(() => expect(startedNames()).toContain("Y3.pdf"));
    expect(startedNames().filter((name) => name === "Y3.pdf")).toHaveLength(1);
  });
});

/**
 * Sixth Code Quality Review: a queue rebuilt from the draft after an
 * ordinary navigation is this session's. Its files must still be able to
 * start and to be retried — the session never ended.
 */
describe("a queue seeded from the draft after navigation (#929 review 6)", () => {
  const startedNames = () => mockUploadAttachment.mock.calls.map((call) => (call[1] as File).name);

  async function choose(...names: string[]) {
    fireEvent.change(screen.getByTestId("chat-composer-file-input"), {
      target: { files: names.map((name) => new File(["x"], name, { type: "application/pdf" })) },
    });
  }

  it("starts a file that was still queued when the reader left", async () => {
    const settle: Array<(value: { id: string }) => void> = [];
    mockUploadAttachment.mockImplementation(
      () => new Promise<{ id: string }>((resolve) => settle.push(resolve)),
    );
    const { switchTo } = mount();
    await screen.findByTestId("chat-composer-file-input");
    await choose("X1.pdf", "X2.pdf", "X3.pdf");
    await waitFor(() => expect(startedNames()).toEqual(["X1.pdf", "X2.pdf"]));

    await switchTo(keyB);
    await switchTo(keyA);
    await waitFor(() =>
      expect(screen.getByTestId("chat-composer-upload-status")).toHaveTextContent("X3.pdf"),
    );

    // A slot frees up: the file queued before the trip takes it.
    await act(async () => settle[0]({ id: "att-X1" }));

    await waitFor(() => expect(startedNames()).toContain("X3.pdf"));
  });

  it("retries a file that failed before the reader left", async () => {
    mockUploadAttachment.mockRejectedValueOnce(new Error("rede"));
    const { switchTo } = mount();
    await screen.findByTestId("chat-composer-file-input");
    await choose("X.pdf");
    await screen.findByRole("button", { name: "Tentar novamente" });

    await switchTo(keyB);
    await switchTo(keyA);
    mockUploadAttachment.mockResolvedValue({ id: "att-X" });
    fireEvent.click(await screen.findByRole("button", { name: "Tentar novamente" }));

    await waitFor(() => expect(startedNames()).toEqual(["X.pdf", "X.pdf"]));
    await screen.findByTestId("chat-composer-pending-attachment");
  });
});

/**
 * Sixth Code Quality Review: the send guard has to see the end of the
 * session at the instant it happens, not at the next render. Enter pressed
 * in the same turn as the clear must send nothing.
 */
describe("a send attempted in the same turn as the clear (#929 review 6)", () => {
  it("sends nothing, and the composer is empty once React has caught up", async () => {
    const onSend = vi.fn<SendFn>().mockResolvedValue({ status: "sent" });
    const { getDrafts } = mount(onSend);
    const input = await type("T1");
    expect(screen.getByTestId("chat-send-btn")).toBeEnabled();

    // One turn: the session ends and Enter is pressed, with no render in
    // between — exactly what a keystroke racing a token refresh looks like.
    await act(async () => {
      getDrafts().clearAllDrafts();
      fireEvent.keyDown(input, { key: "Enter", code: "Enter" });
    });

    expect(onSend).not.toHaveBeenCalled();
    await waitFor(() => expect(input).toHaveTextContent(""));
    expect(getDrafts().getDraft(keyA)).toBeUndefined();

    await type("T2");
    expect(getDrafts().getDraft(keyA)?.text).toMatchObject({
      content: [{ content: [{ text: "T2" }] }],
    });
  });
});
