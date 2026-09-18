/**
 * useVoiceRecorder state machine tests (issue #670).
 *
 * MediaRecorder does not exist in jsdom, so this file provides a minimal fake
 * that drives the same callback contract (ondataavailable/onstop/onerror) a
 * real browser would, and mocks getUserMedia the same way mediaPermission.test.ts
 * does for the call-permission preflight.
 */

import { act, renderHook, waitFor } from "@testing-library/react";
import { StrictMode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { AttachmentUploadTarget } from "./useAttachmentUpload";
import { useConversationDrafts } from "./useConversationDrafts";
import type { ConversationDraftsApi } from "./useConversationDrafts";
import { useVoiceRecorder } from "./useVoiceRecorder";

const mockUploadAttachment = vi.hoisted(() => vi.fn());
vi.mock("./filesApi", () => ({
  uploadAttachment: mockUploadAttachment,
}));

class FakeTrack {
  stop = vi.fn();
}

// Tracked so a test can assert a recording was never actually started —
// the sharpest available proxy for "MediaRecorder.start() and the elapsed
// timer never ran", since both live behind the same guard inside the
// getUserMedia continuation (see useVoiceRecorder's `abandonedRef`).
let mediaRecorderInstances: FakeMediaRecorder[] = [];

class FakeMediaRecorder {
  static isTypeSupported(type: string): boolean {
    return type === "audio/webm;codecs=opus";
  }
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
    this.ondataavailable?.({ data: new Blob(["chunk"]) });
    this.onstop?.();
  }
}

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
  mediaRecorderInstances = [];
  getUserMedia = vi.fn(async () => fakeStream());
  Object.defineProperty(navigator, "mediaDevices", {
    value: { getUserMedia },
    configurable: true,
  });
  vi.stubGlobal("MediaRecorder", FakeMediaRecorder);
  vi.stubGlobal("URL", {
    ...URL,
    createObjectURL: vi.fn(() => "blob:voice-1"),
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
   */
  describe("what a confirmed send leaves in the conversation's draft (issue #875)", () => {
    const draftKey = "channel:ch-1";

    function setupWithDraft(onUploaded: (id: string) => Promise<boolean>) {
      let drafts!: ConversationDraftsApi;
      const view = renderHook(() => {
        drafts = useConversationDrafts("u1");
        return useVoiceRecorder({
          target,
          maxUploadBytes: null,
          onUploaded,
          drafts,
          draftKey,
        });
      });
      return { result: view.result, getDrafts: () => drafts };
    }

    async function recordAndReview(result: { current: ReturnType<typeof useVoiceRecorder> }) {
      act(() => result.current.start());
      await waitFor(() => expect(result.current.phase).toBe("recording"));
      act(() => result.current.stop());
      expect(result.current.phase).toBe("reviewing");
    }

    it("consumes the recording it sent", async () => {
      mockUploadAttachment.mockResolvedValue({ id: "att-1" });
      const { result, getDrafts } = setupWithDraft(async () => true);
      await recordAndReview(result);
      await waitFor(() => expect(getDrafts().getDraft(draftKey)?.voiceMessage).toBeTruthy());

      act(() => result.current.send());

      await waitFor(() => expect(result.current.phase).toBe("idle"));
      expect(getDrafts().getDraft(draftKey)).toBeUndefined();
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
      const { result, getDrafts } = setupWithDraft(
        () => new Promise<boolean>((resolve) => (confirmSend = resolve)),
      );

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
