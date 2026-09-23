/**
 * useVoiceRecorder state machine tests (issue #670).
 *
 * MediaRecorder does not exist in jsdom, so this file provides a minimal fake
 * that drives the same callback contract (ondataavailable/onstop/onerror) a
 * real browser would, and mocks getUserMedia the same way mediaPermission.test.ts
 * does for the call-permission preflight.
 */

import { act, renderHook, waitFor } from "@testing-library/react";
import { StrictMode, useLayoutEffect } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { AttachmentUploadTarget } from "./useAttachmentUpload";
import { useConversationDrafts } from "./useConversationDrafts";
import { loadDraftPersistence } from "./chatDraftPersistence";
import type { ConversationDraftsApi } from "./useConversationDrafts";
import { useDraftMirrors } from "./useDraftMirrors";
import { useVoiceRecorder } from "./useVoiceRecorder";

const { mockUploadAttachment, mockDeleteAttachmentDraft } = vi.hoisted(() => ({
  mockUploadAttachment: vi.fn(),
  mockDeleteAttachmentDraft: vi.fn(),
}));
vi.mock("./filesApi", () => ({
  uploadAttachment: mockUploadAttachment,
  deleteAttachmentDraft: mockDeleteAttachmentDraft,
}));

class FakeTrack {
  stop = vi.fn();
}

// Tracked so a test can assert a recording was never actually started —
// the sharpest available proxy for "MediaRecorder.start() and the elapsed
// timer never ran", since both live behind the same guard inside the
// getUserMedia continuation (see useVoiceRecorder's `abandonedRef`).
let mediaRecorderInstances: FakeMediaRecorder[] = [];
let createdObjectUrls = 0;

const pendingFinalizations: Array<() => void> = [];

class FakeMediaRecorder {
  static isTypeSupported(type: string): boolean {
    return type === "audio/webm;codecs=opus";
  }
  /** `onstop` waits in `pendingFinalizations`, as a real one may. */
  static deferStop = false;
  state: "inactive" | "recording" | "paused" = "inactive";
  ondataavailable: ((event: { data: Blob }) => void) | null = null;
  onstop: (() => void) | null = null;
  onerror: (() => void) | null = null;
  stream: MediaStream;
  options: { mimeType: string };

  constructor(stream: MediaStream, options: { mimeType: string }) {
    this.stream = stream;
    this.options = options;
    mediaRecorderInstances.push(this);
  }

  start(): void {
    this.state = "recording";
  }
  pause(): void {
    this.state = "paused";
  }
  resume(): void {
    this.state = "recording";
  }
  stop(): void {
    this.state = "inactive";
    const finalize = () => {
      this.ondataavailable?.({ data: labelledChunk("chunk") });
      this.onstop?.();
    };
    if (FakeMediaRecorder.deferStop) pendingFinalizations.push(finalize);
    else finalize();
  }
}

/**
 * The chunks a Blob was built from. jsdom's Blob has no `text()`, and what
 * matters here is *which* pieces of audio ended up in it, not their bytes:
 * the fake recorder's chunks are recorded as they are handed over.
 */
const chunkLabels = new WeakMap<Blob, string>();

/** A chunk of audio this test can recognise once it is inside a Blob. */
function labelledChunk(label: string): Blob {
  const chunk = new Blob([label]);
  chunkLabels.set(chunk, label);
  return chunk;
}

function blobChunks(blob: Blob): string[] {
  return (assembledFrom.get(blob) ?? []).map((part) => chunkLabels.get(part) ?? "?");
}

/** What each Blob the hook assembled was built from, in order. */
const assembledFrom = new WeakMap<Blob, Blob[]>();
const RealBlob = globalThis.Blob;

function fakeStream(): MediaStream {
  const tracks = [new FakeTrack(), new FakeTrack()];
  return { getTracks: () => tracks } as unknown as MediaStream;
}

/** A controllable promise, for driving a getUserMedia call from outside it. */
function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

const target = { kind: "channel" as const, id: "ch-1" };

const originalMediaDevices = navigator.mediaDevices;
let getUserMedia: ReturnType<typeof vi.fn>;

beforeEach(() => {
  mockUploadAttachment.mockReset();
  mockDeleteAttachmentDraft.mockReset().mockResolvedValue(undefined);
  mediaRecorderInstances = [];
  FakeMediaRecorder.deferStop = false;
  pendingFinalizations.length = 0;
  createdObjectUrls = 0;
  getUserMedia = vi.fn(async () => fakeStream());
  Object.defineProperty(navigator, "mediaDevices", {
    value: { getUserMedia },
    configurable: true,
  });
  vi.stubGlobal("MediaRecorder", FakeMediaRecorder);
  // Remembers which chunks each assembled Blob came from, so a test can ask
  // whose audio ended up in a recording without reading bytes jsdom does
  // not give it.
  vi.stubGlobal(
    "Blob",
    class extends RealBlob {
      constructor(parts: BlobPart[] = [], options?: BlobPropertyBag) {
        super(parts, options);
        assembledFrom.set(
          this,
          parts.filter((part): part is Blob => part instanceof RealBlob),
        );
      }
    },
  );
  vi.stubGlobal("URL", {
    ...URL,
    createObjectURL: vi.fn(() => `blob:voice-${++createdObjectUrls}`),
    revokeObjectURL: vi.fn(),
  });
});

afterEach(() => {
  Object.defineProperty(navigator, "mediaDevices", {
    value: originalMediaDevices,
    configurable: true,
  });
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

function setup(onUploaded: (id: string) => Promise<boolean> = async () => true) {
  return renderHook(
    ({ target: t }) => useVoiceRecorder({ target: t, maxUploadBytes: null, onUploaded }),
    { initialProps: { target } },
  );
}

describe("useVoiceRecorder", () => {
  it("reports supported when MediaRecorder offers an accepted format", () => {
    const { result } = setup();
    expect(result.current.supported).toBe(true);
  });

  it("reports unsupported when no candidate format is available", () => {
    vi.stubGlobal("MediaRecorder", {
      isTypeSupported: () => false,
    });
    const { result } = setup();
    expect(result.current.supported).toBe(false);
  });

  it("goes idle -> requesting_permission -> recording", async () => {
    const { result } = setup();
    act(() => result.current.start());
    expect(result.current.phase).toBe("requesting_permission");

    await waitFor(() => expect(result.current.phase).toBe("recording"));
    expect(getUserMedia).toHaveBeenCalledExactlyOnceWith({ audio: true });
  });

  it("goes to denied on a permission rejection, not the generic failed state", async () => {
    getUserMedia.mockRejectedValue(new DOMException("no", "NotAllowedError"));
    const { result } = setup();
    act(() => result.current.start());

    await waitFor(() => expect(result.current.phase).toBe("denied"));
  });

  it("goes to failed on any other getUserMedia error", async () => {
    getUserMedia.mockRejectedValue(new Error("device busy"));
    const { result } = setup();
    act(() => result.current.start());

    await waitFor(() => expect(result.current.phase).toBe("failed"));
    expect(result.current.error).toBeTruthy();
  });

  /**
   * Fourth Code Quality Review, finding B: what the UI offers and what the
   * handlers believe must not be one commit apart. A reader whose click
   * lands in the same frame as the Stop button appearing is doing exactly
   * what this does — acting on the phase that was just rendered, before any
   * passive effect of that render has run.
   */
  it("stops a recording asked to stop in the same turn it started in", async () => {
    const { result } = setup();

    await act(async () => {
      result.current.start();
      // The microphone answers: the hook commits `recording` and the panel
      // draws Stop. No effect of that commit has run yet.
      await Promise.resolve();
      result.current.stop();
    });

    expect(mediaRecorderInstances).toHaveLength(1);
    expect(mediaRecorderInstances[0].state).toBe("inactive");
    expect(result.current.phase).toBe("reviewing");
    expect(result.current.previewUrl).toBe("blob:voice-1");
  });

  it("pauses and resumes without losing the recording", async () => {
    const { result } = setup();
    act(() => result.current.start());
    await waitFor(() => expect(result.current.phase).toBe("recording"));

    act(() => result.current.pause());
    expect(result.current.phase).toBe("paused");

    act(() => result.current.resume());
    expect(result.current.phase).toBe("recording");
  });

  it("stop() moves to reviewing with a local preview URL and no live tracks", async () => {
    const { result } = setup();
    act(() => result.current.start());
    await waitFor(() => expect(result.current.phase).toBe("recording"));
    const stream = await getUserMedia.mock.results[0]!.value;

    act(() => result.current.stop());

    expect(result.current.phase).toBe("reviewing");
    expect(result.current.previewUrl).toBe("blob:voice-1");
    for (const track of stream.getTracks()) {
      expect(track.stop).toHaveBeenCalled();
    }
  });

  it("discard() from recording never reaches reviewing, and releases the microphone", async () => {
    const { result } = setup();
    act(() => result.current.start());
    await waitFor(() => expect(result.current.phase).toBe("recording"));
    const stream = await getUserMedia.mock.results[0]!.value;

    act(() => result.current.discard());

    expect(result.current.phase).toBe("idle");
    expect(result.current.previewUrl).toBeNull();
    for (const track of stream.getTracks()) {
      expect(track.stop).toHaveBeenCalled();
    }
  });

  it("discard() from reviewing revokes the preview URL", async () => {
    const revokeObjectURL = vi.fn();
    vi.stubGlobal("URL", { ...URL, createObjectURL: vi.fn(() => "blob:voice-1"), revokeObjectURL });
    const { result } = setup();
    act(() => result.current.start());
    await waitFor(() => expect(result.current.phase).toBe("recording"));
    act(() => result.current.stop());
    expect(result.current.phase).toBe("reviewing");

    act(() => result.current.discard());

    expect(result.current.phase).toBe("idle");
    expect(revokeObjectURL).toHaveBeenCalledWith("blob:voice-1");
  });

  it("send() uploads as a voice message with the declared duration and consumes the recording", async () => {
    mockUploadAttachment.mockResolvedValue({ id: "att-1" });
    const onUploaded = vi.fn().mockResolvedValue(true);
    const { result } = setup(onUploaded);
    act(() => result.current.start());
    await waitFor(() => expect(result.current.phase).toBe("recording"));
    act(() => result.current.stop());
    expect(result.current.phase).toBe("reviewing");

    act(() => result.current.send());
    expect(result.current.phase).toBe("uploading");

    await waitFor(() => expect(result.current.phase).toBe("idle"));
    expect(onUploaded).toHaveBeenCalledWith("att-1");
    const call = mockUploadAttachment.mock.calls[0]!;
    expect(call[0]).toEqual(target);
    expect(call[5]).toEqual({ purpose: "voice_message", durationMs: expect.any(Number) });
  });

  it("send() failure returns to reviewing with an error, keeping the recording for a retry", async () => {
    mockUploadAttachment.mockRejectedValue(new Error("network"));
    const { result } = setup();
    act(() => result.current.start());
    await waitFor(() => expect(result.current.phase).toBe("recording"));
    act(() => result.current.stop());

    act(() => result.current.send());
    await waitFor(() => expect(result.current.phase).toBe("reviewing"));
    expect(result.current.error).toBeTruthy();
    expect(result.current.previewUrl).toBe("blob:voice-1");
  });

  it("a message-send failure after a successful upload also returns to reviewing", async () => {
    mockUploadAttachment.mockResolvedValue({ id: "att-1" });
    const { result } = setup(async () => false);
    act(() => result.current.start());
    await waitFor(() => expect(result.current.phase).toBe("recording"));
    act(() => result.current.stop());

    act(() => result.current.send());
    await waitFor(() => expect(result.current.phase).toBe("reviewing"));
    expect(result.current.error).toBeTruthy();
  });

  /**
   * Issue #875: a recording the server acknowledged is not a draft either.
   * The hook's own phase going back to idle is not enough — what the sidebar
   * badge and a conversation switch read is the draft, so that is what has
   * to be empty.
   *
   * Issue #929 moved the consumption itself out of this hook: a confirmed
   * send reconciles the draft through the composer's send snapshot, by the
   * recording's identity, in the same single transition as the reply it
   * answered. `onUploaded` below therefore plays the composer's part — it
   * captures the snapshot when the upload lands and consumes it when the
   * send confirms — and the hook is only expected to reset itself.
   */
  describe("what a confirmed send leaves in the conversation's draft (issues #875, #929)", () => {
    const draftKey = "channel:ch-1";
    const voiceOnly = { text: false, attachmentLocalIds: [] as string[], voice: true };
    const textAfterClear = {
      type: "doc",
      content: [{ type: "paragraph", content: [{ type: "text", text: "nova sessão" }] }],
    };

    /**
     * `onUploaded` is handed the store, as ChatComposer's own closure has
     * it — and the recorder is wired to the store the same way ChatComposer
     * wires it: through useDraftMirrors, in a layout effect, so an
     * acknowledgement elsewhere or the end of the session reaches this
     * recorder exactly as it does in the app.
     */
    function setupWithDraft(
      onUploaded: (id: string, drafts: ConversationDraftsApi) => Promise<boolean>,
    ) {
      let drafts!: ConversationDraftsApi;
      const view = renderHook(() => {
        drafts = useConversationDrafts("u1");
        const recorder = useVoiceRecorder({
          target,
          maxUploadBytes: null,
          onUploaded: (id) => onUploaded(id, drafts),
          drafts,
          draftKey,
        });
        const mirrors = useDraftMirrors(drafts, draftKey);
        useLayoutEffect(() => {
          mirrors.reconcile((store, key, reason) => {
            if (reason === "cleared") recorder.resetForSessionEnd();
            else recorder.reconcileWithDraft(store.getDraft(key)?.voiceMessage ?? null);
          });
        });
        return recorder;
      });
      return { result: view.result, getDrafts: () => drafts, unmount: view.unmount };
    }

    async function recordAndReview(result: { current: ReturnType<typeof useVoiceRecorder> }) {
      act(() => result.current.start());
      await waitFor(() => expect(result.current.phase).toBe("recording"));
      act(() => result.current.stop());
      expect(result.current.phase).toBe("reviewing");
    }

    it("hands the finished recording to the draft with a stable identity", async () => {
      const { result, getDrafts } = setupWithDraft(async () => true);
      await recordAndReview(result);
      const voice = getDrafts().getDraft(draftKey)?.voiceMessage;
      expect(voice?.id).toEqual(expect.any(String));
      expect(voice?.previewUrl).toBe("blob:voice-1");
    });

    it("resets to idle once the send is confirmed, leaving the draft to the snapshot that consumed it", async () => {
      mockUploadAttachment.mockResolvedValue({ id: "att-1" });
      const { result, getDrafts } = setupWithDraft(async (_id, drafts) => {
        drafts.consumeSentSnapshot(drafts.createSendSnapshot(draftKey, voiceOnly));
        return true;
      });
      await recordAndReview(result);
      await waitFor(() => expect(getDrafts().getDraft(draftKey)?.voiceMessage).toBeTruthy());

      act(() => result.current.send());

      await waitFor(() => expect(result.current.phase).toBe("idle"));
      expect(getDrafts().getDraft(draftKey)).toBeUndefined();
      // The store released the URL when the recording left the draft; the
      // hook, which no longer owns it, did not revoke it a second time.
      expect(URL.revokeObjectURL).toHaveBeenCalledTimes(1);
      expect(URL.revokeObjectURL).toHaveBeenCalledWith("blob:voice-1");
    });

    /**
     * Issue #875, I8: the acknowledgement of *this* recording may not take
     * anything the reader started after submitting it.
     *
     * The reply is the state that can genuinely appear in this window. While
     * a recording is uploading, `recording` is still true, so ChatComposer
     * replaces the editor with the voice panel and refuses drops
     * (`canAcceptAttachments = attachEnabled && !recording`) — but the
     * message list is untouched, so "Responder" on a message is still one
     * click away. The text is the other half: it predates the recording,
     * which never cleared it, and must come through just as intact.
     */
    it("consumes only its own recording, never state created after the submit", async () => {
      mockUploadAttachment.mockResolvedValue({ id: "att-1" });
      // The acknowledgement, held open deliberately — no timers anywhere.
      let confirmSend!: (consumed: boolean) => void;
      const { result, getDrafts } = setupWithDraft((_id, drafts) => {
        const snapshot = drafts.createSendSnapshot(draftKey, voiceOnly);
        return new Promise<boolean>((resolve) => {
          confirmSend = (consumed) => {
            if (consumed) drafts.consumeSentSnapshot(snapshot);
            resolve(consumed);
          };
        });
      });

      const textBeforeRecording = {
        type: "doc",
        content: [{ type: "paragraph", content: [{ type: "text", text: "rascunho anterior" }] }],
      };
      act(() => getDrafts().setText(draftKey, textBeforeRecording));
      await recordAndReview(result);
      await waitFor(() => expect(getDrafts().getDraft(draftKey)?.voiceMessage).toBeTruthy());

      act(() => result.current.send());
      await waitFor(() => expect(result.current.phase).toBe("uploading"));

      // After the submit, before the acknowledgement: the reader answers a
      // message. This belongs to the *next* send, not to the one in flight.
      act(() => getDrafts().setReply(draftKey, "msg-42"));
      const inFlight = getDrafts().getDraft(draftKey);
      expect(inFlight?.voiceMessage).toBeTruthy();
      expect(inFlight?.replyToMessageId).toBe("msg-42");

      act(() => confirmSend(true));

      await waitFor(() => expect(result.current.phase).toBe("idle"));
      const after = getDrafts().getDraft(draftKey);
      // The recording it sent is gone...
      expect(after?.voiceMessage).toBeNull();
      // ...and nothing else is. A global clear would have taken the draft
      // itself, and with it both of these.
      expect(after?.replyToMessageId).toBe("msg-42");
      expect(after?.text).toEqual(textBeforeRecording);
    });

    // The counterweight: without a confirmation there is nothing to consume,
    // and the recording must still be there to retry.
    it("keeps the recording in the draft when the send is not confirmed", async () => {
      mockUploadAttachment.mockResolvedValue({ id: "att-1" });
      const { result, getDrafts } = setupWithDraft(async () => false);
      await recordAndReview(result);

      act(() => result.current.send());

      await waitFor(() => expect(result.current.phase).toBe("reviewing"));
      expect(getDrafts().getDraft(draftKey)?.voiceMessage).toBeTruthy();
      expect(URL.revokeObjectURL).not.toHaveBeenCalled();
    });

    /**
     * Second review, finding C: a recording finalized by a recorder whose
     * composer is already gone. The draft holds it; this instance must show
     * it — as it is, without creating a blob or a URL of its own.
     */
    it("adopts a different recording the draft now holds, and drops one it no longer holds", async () => {
      const { result, getDrafts } = setupWithDraft(async () => true);
      await recordAndReview(result);
      const v1 = getDrafts().getDraft(draftKey)?.voiceMessage;
      expect(v1?.previewUrl).toBe("blob:voice-1");
      const createdSoFar = vi.mocked(URL.createObjectURL).mock.calls.length;

      const v2 = {
        id: "v2",
        blob: new Blob(["outra"]),
        previewUrl: "blob:voice-2",
        durationMs: 4200,
        mimeType: "audio/webm",
      };
      act(() => result.current.reconcileWithDraft(v2));

      expect(result.current.phase).toBe("reviewing");
      expect(result.current.previewUrl).toBe("blob:voice-2");
      expect(result.current.elapsedMs).toBe(4200);
      // Adopted, not rebuilt: no blob and no URL were created here, and the
      // URL the draft owns was not revoked by this hook.
      expect(URL.createObjectURL).toHaveBeenCalledTimes(createdSoFar);
      expect(URL.revokeObjectURL).not.toHaveBeenCalled();

      // The same recording again says nothing new.
      act(() => result.current.reconcileWithDraft({ ...v2 }));
      expect(result.current.previewUrl).toBe("blob:voice-2");

      // And when the draft no longer holds one, this goes back to idle
      // without revoking a URL whose owner is the store.
      act(() => result.current.reconcileWithDraft(null));
      expect(result.current.phase).toBe("idle");
      expect(result.current.previewUrl).toBeNull();
      expect(URL.revokeObjectURL).not.toHaveBeenCalled();
    });

    it("never interrupts a live recording or an upload in progress", async () => {
      mockUploadAttachment.mockReturnValue(new Promise(() => undefined));
      const { result } = setupWithDraft(async () => true);
      act(() => result.current.start());
      await waitFor(() => expect(result.current.phase).toBe("recording"));

      act(() => result.current.reconcileWithDraft(null));
      expect(result.current.phase).toBe("recording");

      act(() => result.current.stop());
      expect(result.current.phase).toBe("reviewing");
      act(() => result.current.send());
      await waitFor(() => expect(result.current.phase).toBe("uploading"));

      act(() => result.current.reconcileWithDraft(null));
      expect(result.current.phase).toBe("uploading");
    });

    /**
     * Third Code Quality Review, finding A: the finalization outlives the
     * session it belongs to. `clearAllDrafts` empties the store, and the
     * recorder's own onstop still holds it — with a blob and a preview URL
     * that would otherwise be handed to a session that never asked for them.
     */
    it("discards a finalization that arrives after the drafts were cleared, and releases its URL", async () => {
      // Nothing is left to reset this recorder — its composer is gone —
      // so the finalization itself is what has to notice the session ended.
      FakeMediaRecorder.deferStop = true;
      const { result, getDrafts, unmount } = setupWithDraft(async () => true);
      act(() => result.current.start());
      await waitFor(() => expect(result.current.phase).toBe("recording"));
      unmount();
      expect(pendingFinalizations).toHaveLength(1);

      act(() => getDrafts().clearAllDrafts());
      act(() => pendingFinalizations.pop()!());

      expect(getDrafts().getDraft(draftKey)).toBeUndefined();
      expect(getDrafts().summaries.size).toBe(0);
      expect(getDrafts().getMirrorRevision(draftKey)).toBe(0);
      expect(loadDraftPersistence("u1", draftKey)).toBeNull();
      // Audio nobody will hear never becomes a blob, let alone a URL:
      // there is nothing to leak and nothing to revoke.
      expect(URL.createObjectURL).not.toHaveBeenCalled();
      expect(URL.revokeObjectURL).not.toHaveBeenCalled();
    });

    it("leaves a draft composed after the clear exactly as it is", async () => {
      const { result, getDrafts } = setupWithDraft(async () => true);
      act(() => result.current.start());
      await waitFor(() => expect(result.current.phase).toBe("recording"));
      act(() => getDrafts().clearAllDrafts());
      act(() => getDrafts().setText(draftKey, textAfterClear));
      const after = getDrafts().getDraft(draftKey);

      act(() => result.current.stop());

      expect(getDrafts().getDraft(draftKey)).toBe(after);
      expect(getDrafts().getDraft(draftKey)?.voiceMessage).toBeNull();
      expect(getDrafts().getMirrorRevision(draftKey)).toBe(0);
    });

    /**
     * Fifth Code Quality Review: the recorder is a local mirror too. A
     * session that ends while it holds a recording — waiting for the
     * microphone, capturing, under review, or going out — must not be able
     * to finish anything in the session that replaces it.
     */
    describe("when the drafts are cleared while this recorder is live", () => {
      it("never opens the microphone for a permission granted after the clear", async () => {
        const stream = deferred<MediaStream>();
        const tracks = fakeStream();
        getUserMedia.mockReturnValueOnce(stream.promise);
        const { result, getDrafts } = setupWithDraft(async () => true);
        act(() => result.current.start());
        expect(result.current.phase).toBe("requesting_permission");

        act(() => getDrafts().clearAllDrafts());
        await act(async () => {
          stream.resolve(tracks);
          await stream.promise;
        });

        expect(mediaRecorderInstances).toHaveLength(0);
        for (const track of tracks.getTracks()) expect(track.stop).toHaveBeenCalled();
        expect(result.current.phase).toBe("idle");
        expect(getDrafts().getDraft(draftKey)).toBeUndefined();
        expect(URL.createObjectURL).not.toHaveBeenCalled();
      });

      it("does not report a permission refused after the clear", async () => {
        const stream = deferred<MediaStream>();
        getUserMedia.mockReturnValueOnce(stream.promise);
        const { result, getDrafts } = setupWithDraft(async () => true);
        act(() => result.current.start());

        act(() => getDrafts().clearAllDrafts());
        await act(async () => {
          stream.reject(new DOMException("denied", "NotAllowedError"));
          await stream.promise.catch(() => undefined);
        });

        expect(result.current.phase).toBe("idle");
        expect(result.current.error).toBeNull();
      });

      it("stops a recording in progress and leaves no recording behind", async () => {
        const { result, getDrafts } = setupWithDraft(async () => true);
        act(() => result.current.start());
        await waitFor(() => expect(result.current.phase).toBe("recording"));

        act(() => getDrafts().clearAllDrafts());

        expect(result.current.phase).toBe("idle");
        expect(mediaRecorderInstances[0].state).toBe("inactive");
        expect(getDrafts().getDraft(draftKey)).toBeUndefined();
        expect(getDrafts().summaries.size).toBe(0);
        expect(URL.createObjectURL).not.toHaveBeenCalled();
      });

      it("drops a recording under review without revoking a URL the store already released", async () => {
        const { result, getDrafts } = setupWithDraft(async () => true);
        await recordAndReview(result);
        expect(getDrafts().getDraft(draftKey)?.voiceMessage).toBeTruthy();

        act(() => getDrafts().clearAllDrafts());

        expect(result.current.phase).toBe("idle");
        expect(result.current.previewUrl).toBeNull();
        expect(getDrafts().getDraft(draftKey)).toBeUndefined();
        // The store owned it and released it: exactly one revoke.
        expect(URL.revokeObjectURL).toHaveBeenCalledTimes(1);
        expect(URL.revokeObjectURL).toHaveBeenCalledWith("blob:voice-1");
      });

      it("abandons a voice message being sent: no message, no retry, and the attachment is cleaned up", async () => {
        const upload = deferred<{ id: string }>();
        mockUploadAttachment.mockReturnValueOnce(upload.promise);
        const onUploaded = vi.fn(async () => true);
        const { result, getDrafts } = setupWithDraft(onUploaded);
        await recordAndReview(result);
        act(() => result.current.send());
        await waitFor(() => expect(result.current.phase).toBe("uploading"));

        act(() => getDrafts().clearAllDrafts());
        await act(async () => {
          upload.resolve({ id: "att-voice-old" });
          await upload.promise;
        });

        expect(onUploaded).not.toHaveBeenCalled();
        expect(result.current.phase).toBe("idle");
        expect(getDrafts().getDraft(draftKey)).toBeUndefined();
        expect(getDrafts().summaries.size).toBe(0);
        expect(getDrafts().getMirrorRevision(draftKey)).toBe(0);
        expect(mockDeleteAttachmentDraft).toHaveBeenCalledWith("att-voice-old");
      });

      it("shows no failure from a send abandoned by the clear", async () => {
        const upload = deferred<{ id: string }>();
        mockUploadAttachment.mockReturnValueOnce(upload.promise);
        const { result, getDrafts } = setupWithDraft(async () => true);
        await recordAndReview(result);
        act(() => result.current.send());
        await waitFor(() => expect(result.current.phase).toBe("uploading"));

        act(() => getDrafts().clearAllDrafts());
        await act(async () => {
          upload.reject(new Error("rede"));
          await upload.promise.catch(() => undefined);
        });

        expect(result.current.phase).toBe("idle");
        expect(result.current.error).toBeNull();
        expect(getDrafts().getDraft(draftKey)).toBeUndefined();
      });

      it("records again normally in the session that replaced the cleared one", async () => {
        const { result, getDrafts } = setupWithDraft(async () => true);
        await recordAndReview(result);
        act(() => getDrafts().clearAllDrafts());
        expect(result.current.phase).toBe("idle");

        await recordAndReview(result);

        expect(result.current.phase).toBe("reviewing");
        expect(getDrafts().getDraft(draftKey)?.voiceMessage?.previewUrl).toBe("blob:voice-2");
      });
    });

    it("discard() from reviewing takes the recording out of the draft, and the store revokes its URL once", async () => {
      const { result, getDrafts } = setupWithDraft(async () => true);
      await recordAndReview(result);

      act(() => result.current.discard());

      expect(result.current.phase).toBe("idle");
      expect(getDrafts().getDraft(draftKey)).toBeUndefined();
      expect(URL.revokeObjectURL).toHaveBeenCalledTimes(1);
      expect(URL.revokeObjectURL).toHaveBeenCalledWith("blob:voice-1");
    });

    it("unmounting while reviewing (a conversation switch) leaves the recording and its URL to the draft", async () => {
      const { result, getDrafts, unmount } = setupWithDraft(async () => true);
      await recordAndReview(result);

      unmount();

      expect(getDrafts().getDraft(draftKey)?.voiceMessage?.previewUrl).toBe("blob:voice-1");
      expect(URL.revokeObjectURL).not.toHaveBeenCalled();
    });

    it("unmounting while the send is in flight lets it finish and consume the recording it sent", async () => {
      let resolveUpload!: (value: { id: string }) => void;
      mockUploadAttachment.mockReturnValue(
        new Promise<{ id: string }>((resolve) => (resolveUpload = resolve)),
      );
      const onUploaded = vi.fn(async (_id: string, drafts: ConversationDraftsApi) => {
        drafts.consumeSentSnapshot(drafts.createSendSnapshot(draftKey, voiceOnly));
        return true;
      });
      const setup = setupWithDraft(onUploaded);
      const { getDrafts } = setup;
      await recordAndReview(setup.result);
      act(() => setup.result.current.send());
      await waitFor(() => expect(setup.result.current.phase).toBe("uploading"));

      setup.unmount();
      await act(async () => resolveUpload({ id: "att-1" }));

      await waitFor(() => expect(onUploaded).toHaveBeenCalledWith("att-1", expect.anything()));
      await waitFor(() => expect(getDrafts().getDraft(draftKey)).toBeUndefined());
      expect(URL.revokeObjectURL).toHaveBeenCalledTimes(1);
    });
  });

  it("switching the destination discards any in-progress recording", async () => {
    const revokeObjectURL = vi.fn();
    vi.stubGlobal("URL", { ...URL, createObjectURL: vi.fn(() => "blob:voice-1"), revokeObjectURL });
    const { result, rerender } = renderHook<
      ReturnType<typeof useVoiceRecorder>,
      { target: AttachmentUploadTarget }
    >(
      ({ target: t }) =>
        useVoiceRecorder({ target: t, maxUploadBytes: null, onUploaded: async () => true }),
      { initialProps: { target } },
    );
    act(() => result.current.start());
    await waitFor(() => expect(result.current.phase).toBe("recording"));
    act(() => result.current.stop());
    expect(result.current.phase).toBe("reviewing");

    rerender({ target: { kind: "dm", id: "dm-1" } });

    expect(result.current.phase).toBe("idle");
    expect(revokeObjectURL).toHaveBeenCalledWith("blob:voice-1");
  });

  it("unmounting mid-recording stops every track", async () => {
    const { result, unmount } = setup();
    act(() => result.current.start());
    await waitFor(() => expect(result.current.phase).toBe("recording"));
    const stream = await getUserMedia.mock.results[0]!.value;

    unmount();

    for (const track of stream.getTracks()) {
      expect(track.stop).toHaveBeenCalled();
    }
  });

  // Security follow-up regression (issue #670): React StrictMode's
  // development-only double-invoke — effect setup, its cleanup, then setup
  // again on the same instance — must never leave a real, still-mounted hook
  // looking abandoned. A fix that only ever sets `abandonedRef.current = true`
  // in the cleanup, and never resets it to false in the matching setup, gets
  // this exactly backwards: the synthetic first cleanup latches it true
  // forever, and every later getUserMedia continuation on this instance
  // wrongly stops its own tracks and never records anything.
  it("starts recording normally under React StrictMode's synthetic double-invoke", async () => {
    const { result } = renderHook<
      ReturnType<typeof useVoiceRecorder>,
      { target: AttachmentUploadTarget }
    >(
      ({ target: t }) =>
        useVoiceRecorder({ target: t, maxUploadBytes: null, onUploaded: async () => true }),
      { initialProps: { target }, wrapper: StrictMode },
    );

    act(() => result.current.start());
    expect(getUserMedia).toHaveBeenCalledExactlyOnceWith({ audio: true });

    await waitFor(() => expect(result.current.phase).toBe("recording"));

    expect(mediaRecorderInstances).toHaveLength(1);
    const stream: MediaStream = await getUserMedia.mock.results[0]!.value;
    for (const track of stream.getTracks()) {
      expect(track.stop).not.toHaveBeenCalled();
    }
  });

  // Security review regression (issue #670): unmounting while getUserMedia is
  // still pending used to leave nothing marking the hook abandoned.
  // `stateRef.current.phase` cannot substitute for that — no render ever runs
  // again after unmount, so it stays frozen at "requesting_permission", the
  // one value the old guard read as "still fine, proceed". A stream that
  // arrived after that point was handed a live MediaRecorder and a running
  // timer with no UI left to ever stop them: an abandoned hot microphone.
  describe("unmount while getUserMedia is still pending", () => {
    it("stops every track and never starts a recording once the stream arrives late", async () => {
      const pending = deferred<MediaStream>();
      getUserMedia.mockReturnValue(pending.promise);
      const { result, unmount } = setup();

      act(() => result.current.start());
      expect(getUserMedia).toHaveBeenCalledExactlyOnceWith({ audio: true });
      expect(result.current.phase).toBe("requesting_permission");

      unmount();

      const lateStream = fakeStream();
      await act(async () => {
        pending.resolve(lateStream);
        // Two microtask turns: one for getUserMedia's own .then, one for
        // whatever it chains — enough for the continuation to have run.
        await Promise.resolve();
        await Promise.resolve();
      });

      for (const track of lateStream.getTracks()) {
        expect(track.stop).toHaveBeenCalled();
      }
      expect(mediaRecorderInstances).toHaveLength(0);
      expect(result.current.phase).not.toBe("recording");
    });

    it("rejecting after unmount updates nothing and throws nothing", async () => {
      const pending = deferred<MediaStream>();
      getUserMedia.mockReturnValue(pending.promise);
      const { result, unmount } = setup();

      act(() => result.current.start());
      unmount();

      const before = result.current.phase;
      await act(async () => {
        pending.reject(new DOMException("no", "NotAllowedError"));
        await Promise.resolve().catch(() => undefined);
        await Promise.resolve();
      });

      // Nobody is left to show "denied" to — the last snapshot never moves.
      expect(result.current.phase).toBe(before);
    });
  });
});

/**
 * Sixth Code Quality Review: one recording's callbacks must never touch
 * another's. Generation alone cannot say this — two recordings can follow
 * one another inside the same session — so each attempt has to be its own
 * owner, and a late callback of an attempt that is over says nothing.
 */
describe("useVoiceRecorder — one recording's callbacks never touch the next", () => {
  const draftKey = "channel:ch-1";

  function setupWithDraft() {
    let drafts!: ConversationDraftsApi;
    const view = renderHook(() => {
      drafts = useConversationDrafts("u1");
      const recorder = useVoiceRecorder({
        target,
        maxUploadBytes: null,
        onUploaded: async () => true,
        drafts,
        draftKey,
      });
      const mirrors = useDraftMirrors(drafts, draftKey);
      useLayoutEffect(() => {
        mirrors.reconcile((store, key, reason) => {
          if (reason === "cleared") recorder.resetForSessionEnd();
          else recorder.reconcileWithDraft(store.getDraft(key)?.voiceMessage ?? null);
        });
      });
      return recorder;
    });
    return { result: view.result, getDrafts: () => drafts };
  }

  /** Starts a recording whose permission is answered by the test. */
  function askForMicrophone() {
    const permission = deferred<MediaStream>();
    getUserMedia.mockReturnValueOnce(permission.promise);
    return permission;
  }

  async function recordUpTo(result: { current: ReturnType<typeof useVoiceRecorder> }) {
    act(() => result.current.start());
    await waitFor(() => expect(result.current.phase).toBe("recording"));
    return mediaRecorderInstances[mediaRecorderInstances.length - 1];
  }

  it("ignores a microphone granted to an attempt that is over, and keeps the current one recording", async () => {
    const abandoned = askForMicrophone();
    const abandonedStream = fakeStream();
    const { result, getDrafts } = setupWithDraft();
    act(() => result.current.start());
    expect(result.current.phase).toBe("requesting_permission");

    // The session ends; a new one records.
    act(() => getDrafts().clearAllDrafts());
    const live = await recordUpTo(result);

    await act(async () => {
      abandoned.resolve(abandonedStream);
      await abandoned.promise;
    });

    expect(result.current.phase).toBe("recording");
    expect(live.state).toBe("recording");
    // Only the abandoned attempt's own microphone was released.
    for (const track of abandonedStream.getTracks()) expect(track.stop).toHaveBeenCalled();
    expect(getDrafts().getDraft(draftKey)).toBeUndefined();
  });

  it("ignores a microphone refused for an attempt that is over", async () => {
    const abandoned = askForMicrophone();
    const { result, getDrafts } = setupWithDraft();
    act(() => result.current.start());
    act(() => getDrafts().clearAllDrafts());
    await recordUpTo(result);

    await act(async () => {
      abandoned.reject(new DOMException("denied", "NotAllowedError"));
      await abandoned.promise.catch(() => undefined);
    });

    expect(result.current.phase).toBe("recording");
    expect(result.current.error).toBeNull();
  });

  it("keeps the current recording whole when an earlier attempt of the same session reports late", async () => {
    const { result, getDrafts } = setupWithDraft();
    // Two attempts, one session: only their identities tell them apart.
    const abandoned = await recordUpTo(result);
    act(() => result.current.stop());
    act(() => result.current.discard());
    const live = await recordUpTo(result);

    act(() => {
      abandoned.ondataavailable?.({ data: labelledChunk("antigo") });
      abandoned.onstop?.();
      abandoned.onerror?.();
    });

    // The recording on screen is untouched by any of it.
    expect(result.current.phase).toBe("recording");
    expect(result.current.error).toBeNull();
    expect(live.state).toBe("recording");
    expect(getDrafts().getDraft(draftKey)).toBeUndefined();

    // …and finishes normally, with only its own audio.
    act(() => result.current.stop());
    expect(result.current.phase).toBe("reviewing");
    const voice = getDrafts().getDraft(draftKey)?.voiceMessage;
    expect(voice).toBeTruthy();
    // Only this attempt's own audio: the abandoned chunk went nowhere.
    expect(blobChunks(voice!.blob)).toEqual(["chunk"]);
  });
});

/**
 * Seventh Code Quality Review: the intention to throw a recording away
 * belongs to the recording it was formed about. Kept in one flag shared by
 * every attempt, a discard that its own attempt never came back to consume
 * — the session ended, so its finalization was stale and said nothing —
 * stayed behind and silently swallowed the next take.
 */
describe("useVoiceRecorder — a discard belongs to the recording it was asked for", () => {
  const draftKey = "channel:ch-1";

  function setupWithDraft() {
    let drafts!: ConversationDraftsApi;
    const view = renderHook(() => {
      drafts = useConversationDrafts("u1");
      const recorder = useVoiceRecorder({
        target,
        maxUploadBytes: null,
        onUploaded: async () => true,
        drafts,
        draftKey,
      });
      const mirrors = useDraftMirrors(drafts, draftKey);
      useLayoutEffect(() => {
        mirrors.reconcile((store, key, reason) => {
          if (reason === "cleared") recorder.resetForSessionEnd();
          else recorder.reconcileWithDraft(store.getDraft(key)?.voiceMessage ?? null);
        });
      });
      return recorder;
    });
    return { result: view.result, getDrafts: () => drafts };
  }

  async function recordUpTo(result: { current: ReturnType<typeof useVoiceRecorder> }) {
    act(() => result.current.start());
    await waitFor(() => expect(result.current.phase).toBe("recording"));
    return mediaRecorderInstances[mediaRecorderInstances.length - 1];
  }

  it("records normally after a session ended mid-recording", async () => {
    const { result, getDrafts } = setupWithDraft();
    const abandoned = await recordUpTo(result);
    act(() => abandoned.ondataavailable?.({ data: labelledChunk("sessão antiga") }));

    // The session ends while the microphone is open: this attempt's
    // finalization is stale and writes nothing — including nothing that
    // the next attempt then has to obey.
    act(() => getDrafts().clearAllDrafts());
    expect(result.current.phase).toBe("idle");

    await recordUpTo(result);
    act(() => result.current.stop());

    expect(result.current.phase).toBe("reviewing");
    const voice = getDrafts().getDraft(draftKey)?.voiceMessage;
    expect(voice).toBeTruthy();
    expect(result.current.previewUrl).toBe(voice!.previewUrl);
    expect(blobChunks(voice!.blob)).toEqual(["chunk"]);
  });

  it("keeps the new recording when the abandoned one finalizes before it", async () => {
    FakeMediaRecorder.deferStop = true;
    const { result, getDrafts } = setupWithDraft();
    const abandoned = await recordUpTo(result);
    act(() => abandoned.ondataavailable?.({ data: labelledChunk("sessão antiga") }));
    act(() => getDrafts().clearAllDrafts());

    // The abandoned recorder gets around to stopping first.
    act(() => pendingFinalizations.shift()?.());
    expect(result.current.phase).toBe("idle");
    expect(URL.createObjectURL).not.toHaveBeenCalled();

    await recordUpTo(result);
    act(() => result.current.stop());
    act(() => pendingFinalizations.shift()?.());

    expect(result.current.phase).toBe("reviewing");
    expect(blobChunks(getDrafts().getDraft(draftKey)!.voiceMessage!.blob)).toEqual(["chunk"]);
  });

  it("keeps the new recording when the abandoned one finalizes after it", async () => {
    FakeMediaRecorder.deferStop = true;
    const { result, getDrafts } = setupWithDraft();
    const abandoned = await recordUpTo(result);
    act(() => abandoned.ondataavailable?.({ data: labelledChunk("sessão antiga") }));
    act(() => getDrafts().clearAllDrafts());

    await recordUpTo(result);
    act(() => result.current.stop());
    // Two finalizations waiting: the new one runs first, the abandoned one
    // arrives afterwards and must change nothing it finds.
    act(() => pendingFinalizations.pop()?.());
    expect(result.current.phase).toBe("reviewing");
    const voice = getDrafts().getDraft(draftKey)?.voiceMessage;

    act(() => pendingFinalizations.shift()?.());

    expect(result.current.phase).toBe("reviewing");
    expect(getDrafts().getDraft(draftKey)?.voiceMessage).toBe(voice);
    expect(blobChunks(voice!.blob)).toEqual(["chunk"]);
    expect(URL.createObjectURL).toHaveBeenCalledTimes(1);
  });

  it("discards only the take it was asked about, inside one session", async () => {
    FakeMediaRecorder.deferStop = true;
    const { result, getDrafts } = setupWithDraft();
    await recordUpTo(result);

    act(() => result.current.discard());
    act(() => pendingFinalizations.shift()?.());
    expect(result.current.phase).toBe("idle");
    expect(URL.createObjectURL).not.toHaveBeenCalled();

    await recordUpTo(result);
    act(() => result.current.stop());
    act(() => pendingFinalizations.shift()?.());

    expect(result.current.phase).toBe("reviewing");
    expect(getDrafts().getDraft(draftKey)?.voiceMessage).toBeTruthy();
  });

  /**
   * The same review's second finding: a finished attempt is no longer the
   * one on screen, so a failure its recorder reports afterwards is a
   * failure of nothing — it must not take away a recording already handed
   * to the draft.
   */
  it("ignores a failure reported by the recorder of the recording under review", async () => {
    const { result, getDrafts } = setupWithDraft();
    const finished = await recordUpTo(result);
    act(() => result.current.stop());
    expect(result.current.phase).toBe("reviewing");
    const voice = getDrafts().getDraft(draftKey)?.voiceMessage;

    act(() => finished.onerror?.());

    expect(result.current.phase).toBe("reviewing");
    expect(result.current.error).toBeNull();
    expect(getDrafts().getDraft(draftKey)?.voiceMessage).toBe(voice);
  });
});
