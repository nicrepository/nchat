/**
 * The two lifecycles a composer reads above its own mount (issue #929 and
 * its second review), through the real store: pending sends by attempt
 * identity, and the mirror revision that says an authoritative change is
 * still to be reflected by the composer on screen.
 */

import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { useConversationDrafts } from "./useConversationDrafts";
import { useDraftMirrors } from "./useDraftMirrors";
import type { AttachmentUploadItem } from "./useAttachmentUpload";
import type { TTNode } from "./tiptapSerializer";

const textDoc = (text: string): TTNode => ({
  type: "doc",
  content: [{ type: "paragraph", content: [{ type: "text", text }] }],
});

const textOnly = { text: true, attachmentLocalIds: [] as string[], voice: false };
const keyA = "dm:caio";
const keyB = "dm:juliane";

describe("draft send lifecycle", () => {
  beforeEach(() => {
    sessionStorage.clear();
    vi.useFakeTimers();
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  function withText(result: { current: ReturnType<typeof useConversationDrafts> }) {
    act(() => result.current.setText(keyA, textDoc("T1")));
    return result.current.createSendSnapshot(keyA, textOnly);
  }

  it("beginSend marks the draft pending, synchronously and reactively, for that draft only", () => {
    const { result } = renderHook(() => useConversationDrafts("u1"));
    const snapshot = withText(result);

    let attempt!: ReturnType<typeof result.current.beginSend>;
    act(() => {
      attempt = result.current.beginSend(snapshot);
      // The guard on the send path must not wait for a render.
      expect(result.current.hasPendingSend(keyA)).toBe(true);
    });

    expect(attempt.snapshot).toBe(snapshot);
    expect(result.current.sendLifecycle.get(keyA)).toEqual({ pendingSends: 1 });
    expect(result.current.hasPendingSend(keyB)).toBe(false);
    expect(result.current.sendLifecycle.get(keyB)).toBeUndefined();
  });

  it("settling as sent consumes the snapshot and moves the mirror revision in the same update that clears pending", () => {
    const { result } = renderHook(() => useConversationDrafts("u1"));
    const snapshot = withText(result);
    let attempt!: ReturnType<typeof result.current.beginSend>;
    act(() => {
      attempt = result.current.beginSend(snapshot);
    });

    act(() => result.current.settleSend(attempt, "sent"));

    expect(result.current.getDraft(keyA)).toBeUndefined();
    expect(result.current.hasPendingSend(keyA)).toBe(false);
    // Nothing in flight any more — and no entry left behind for it.
    expect(result.current.sendLifecycle.has(keyA)).toBe(false);
    // …but the draft moved, and a composer that has not re-read it is behind.
    expect(result.current.getMirrorRevision(keyA)).toBe(1);
    expect(result.current.hasUnreconciledMirror(keyA, 0)).toBe(true);
  });

  it("settling as unsent (failed, stale, thrown) releases the attempt and consumes nothing", () => {
    const { result } = renderHook(() => useConversationDrafts("u1"));
    const snapshot = withText(result);
    let attempt!: ReturnType<typeof result.current.beginSend>;
    act(() => {
      attempt = result.current.beginSend(snapshot);
    });

    act(() => result.current.settleSend(attempt, "unsent"));

    expect(result.current.getDraft(keyA)?.text).toEqual(textDoc("T1"));
    expect(result.current.hasPendingSend(keyA)).toBe(false);
    expect(result.current.sendLifecycle.has(keyA)).toBe(false);
    // Nothing was consumed, so no mirror has anything to catch up with.
    expect(result.current.getMirrorRevision(keyA)).toBe(0);
  });

  it("settling an attempt twice, or one the store never issued, changes nothing", () => {
    const { result } = renderHook(() => useConversationDrafts("u1"));
    const snapshot = withText(result);
    let attempt!: ReturnType<typeof result.current.beginSend>;
    act(() => {
      attempt = result.current.beginSend(snapshot);
    });
    act(() => result.current.settleSend(attempt, "sent"));
    // Composed after the acknowledgement: a duplicate callback of S1 must not take it.
    act(() => result.current.setText(keyA, textDoc("T2")));
    const lifecycle = result.current.sendLifecycle;

    const revision = result.current.getMirrorRevision(keyA);

    act(() => {
      result.current.settleSend(attempt, "sent");
      result.current.settleSend({ id: 999, snapshot }, "sent");
    });

    expect(result.current.getDraft(keyA)?.text).toEqual(textDoc("T2"));
    expect(result.current.sendLifecycle).toBe(lifecycle);
    expect(result.current.getMirrorRevision(keyA)).toBe(revision);
  });

  it("settling S1 never releases a still-open S2 of the same draft", () => {
    const { result } = renderHook(() => useConversationDrafts("u1"));
    const s1 = withText(result);
    let a1!: ReturnType<typeof result.current.beginSend>;
    let a2!: ReturnType<typeof result.current.beginSend>;
    act(() => {
      a1 = result.current.beginSend(s1);
      a2 = result.current.beginSend(result.current.createSendSnapshot(keyA, textOnly));
    });
    expect(result.current.sendLifecycle.get(keyA)?.pendingSends).toBe(2);

    act(() => result.current.settleSend(a1, "unsent"));

    expect(result.current.hasPendingSend(keyA)).toBe(true);
    expect(result.current.sendLifecycle.get(keyA)?.pendingSends).toBe(1);
    act(() => result.current.settleSend(a2, "unsent"));
    expect(result.current.hasPendingSend(keyA)).toBe(false);
  });

  it("is not the sidebar's summary: begin and settle leave `summaries` untouched (issue #845)", () => {
    const { result } = renderHook(() => useConversationDrafts("u1"));
    const snapshot = withText(result);
    const summaries = result.current.summaries;
    let attempt!: ReturnType<typeof result.current.beginSend>;
    act(() => {
      attempt = result.current.beginSend(snapshot);
    });
    expect(result.current.summaries).toBe(summaries);
    expect(result.current.summaries.get(keyA)).toEqual({ hasDraft: true });

    act(() => result.current.settleSend(attempt, "unsent"));
    expect(result.current.summaries).toBe(summaries);
  });

  it("clearAllDrafts forgets every attempt and counter", () => {
    const { result } = renderHook(() => useConversationDrafts("u1"));
    const snapshot = withText(result);
    act(() => {
      result.current.beginSend(snapshot);
    });

    act(() => result.current.clearAllDrafts());

    expect(result.current.hasPendingSend(keyA)).toBe(false);
    expect(result.current.sendLifecycle.size).toBe(0);
    expect(result.current.getMirrorRevision(keyA)).toBe(0);
  });
});

/**
 * The mirror revision, and what a composer does with it (issue #929,
 * second review). It moves only on authoritative changes a mounted mirror
 * has to re-read, and a composer that has not re-read one may not send.
 */
describe("draft mirror synchronisation", () => {
  beforeEach(() => {
    sessionStorage.clear();
    vi.useFakeTimers();
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  function attachment(localId: string, status: AttachmentUploadItem["status"]) {
    return {
      localId,
      file: new File(["x"], `${localId}.txt`),
      status,
      progress: null,
      error: null,
      attachment: null,
    } satisfies AttachmentUploadItem;
  }

  /** A composer of `keyA`, mounted now — so already reconciled with the draft it was seeded from. */
  function mountedComposer() {
    return renderHook(() => {
      const drafts = useConversationDrafts("u1");
      return { drafts, mirrors: useDraftMirrors(drafts, keyA) };
    });
  }

  it("does not move for a keystroke, a reply, a queue update or an upload's progress", () => {
    const { result } = mountedComposer();
    const { drafts } = result.current;

    act(() => {
      drafts.setText(keyA, textDoc("T1"));
      drafts.setReply(keyA, "R1");
      drafts.setAttachments(keyA, [attachment("X", "uploading")]);
      drafts.updateAttachment(keyA, "X", { progress: { loaded: 1, total: 2 } });
      drafts.updateAttachment(keyA, "X", { status: "uploading" });
    });

    expect(result.current.drafts.getMirrorRevision(keyA)).toBe(0);
    expect(result.current.mirrors.blocked).toBe(false);
    expect(result.current.mirrors.canIssueSend()).toBe(true);
  });

  it("moves when an upload ends, and when a recording is finalized or leaves the draft", () => {
    const { result } = mountedComposer();
    const { drafts } = result.current;
    act(() => drafts.setAttachments(keyA, [attachment("X", "uploading")]));

    act(() => drafts.updateAttachment(keyA, "X", { status: "success" }));
    expect(result.current.drafts.getMirrorRevision(keyA)).toBe(1);

    act(() => drafts.updateAttachment(keyA, "X", { status: "failed" }));
    expect(result.current.drafts.getMirrorRevision(keyA)).toBe(2);

    act(() =>
      drafts.setVoiceMessage(keyA, {
        id: "v1",
        blob: new Blob(["x"]),
        previewUrl: "blob:v1",
        durationMs: 10,
        mimeType: "audio/webm",
      }),
    );
    expect(result.current.drafts.getMirrorRevision(keyA)).toBe(3);
    // Another conversation is untouched by any of it.
    expect(result.current.drafts.getMirrorRevision(keyB)).toBe(0);
  });

  it("closes the send path from the acknowledgement until this composer has caught up", () => {
    const { result } = mountedComposer();
    const { drafts } = result.current;
    act(() => drafts.setText(keyA, textDoc("T1")));
    let attempt!: ReturnType<typeof drafts.beginSend>;
    act(() => {
      attempt = drafts.beginSend(drafts.createSendSnapshot(keyA, textOnly));
    });
    expect(result.current.mirrors.canIssueSend()).toBe(false);

    act(() => drafts.settleSend(attempt, "sent"));

    // Nothing is pending any more, and the send path is still closed.
    expect(result.current.drafts.hasPendingSend(keyA)).toBe(false);
    expect(result.current.mirrors.canIssueSend()).toBe(false);
    expect(result.current.mirrors.blocked).toBe(true);

    const converged: string[] = [];
    act(() => result.current.mirrors.reconcile((_store, key) => converged.push(key)));

    expect(converged).toEqual([keyA]);
    expect(result.current.mirrors.canIssueSend()).toBe(true);
    expect(result.current.mirrors.blocked).toBe(false);
    // Reconciling again, with nothing new to read, does nothing.
    act(() => result.current.mirrors.reconcile((_store, key) => converged.push(key)));
    expect(converged).toEqual([keyA]);
  });

  it("a composer mounted after the change starts already reconciled", () => {
    const { result } = mountedComposer();
    act(() => result.current.drafts.notifyMirrors(keyA));
    expect(result.current.mirrors.blocked).toBe(true);

    // A second composer of the same draft, mounted now, was seeded from the
    // draft as it is — it has nothing to catch up with.
    const drafts = result.current.drafts;
    const { result: fresh } = renderHook(() => useDraftMirrors(drafts, keyA));
    expect(fresh.current.blocked).toBe(false);
    expect(fresh.current.canIssueSend()).toBe(true);
  });

  it("a composer with no destination is never blocked by another conversation's lifecycle", () => {
    const { result } = renderHook(() => {
      const drafts = useConversationDrafts("u1");
      return { drafts, mirrors: useDraftMirrors(drafts, null) };
    });

    act(() => {
      result.current.drafts.notifyMirrors(keyA);
      result.current.drafts.beginSend(result.current.drafts.createSendSnapshot(keyA, textOnly));
    });

    expect(result.current.mirrors.sendPending).toBe(false);
    expect(result.current.mirrors.blocked).toBe(false);
    expect(result.current.mirrors.canIssueSend()).toBe(true);
  });

  it("tells a composer the session ended even when no draft of it ever moved a revision", () => {
    const { result } = mountedComposer();
    const { drafts } = result.current;
    // Text alone never moves a mirror revision, so 0 → 0 across the clear:
    // only the reset says anything, and it has to be enough.
    act(() => drafts.setText(keyA, textDoc("T1")));
    expect(result.current.drafts.getMirrorRevision(keyA)).toBe(0);
    expect(result.current.mirrors.blocked).toBe(false);

    act(() => drafts.clearAllDrafts());

    expect(result.current.drafts.getMirrorRevision(keyA)).toBe(0);
    expect(result.current.mirrors.blocked).toBe(true);
    expect(result.current.mirrors.canIssueSend()).toBe(false);

    const reasons: string[] = [];
    act(() => result.current.mirrors.reconcile((_s, _k, reason) => reasons.push(reason)));

    expect(reasons).toEqual(["cleared"]);
    expect(result.current.mirrors.blocked).toBe(false);
    expect(result.current.mirrors.canIssueSend()).toBe(true);
    // Nothing new to converge on until something else happens.
    act(() => result.current.mirrors.reconcile((_s, _k, reason) => reasons.push(reason)));
    expect(reasons).toEqual(["cleared"]);
  });

  it("a composer mounted after the clear has nothing to abandon", () => {
    const { result } = mountedComposer();
    act(() => result.current.drafts.clearAllDrafts());
    const drafts = result.current.drafts;

    const { result: fresh } = renderHook(() => useDraftMirrors(drafts, keyA));

    expect(fresh.current.blocked).toBe(false);
    expect(fresh.current.canIssueSend()).toBe(true);
  });

  it("navigation changes neither the generation nor the reset", () => {
    const { result } = mountedComposer();
    const { drafts } = result.current;
    const generation = drafts.captureGeneration();
    const reset = drafts.resetRevision;

    act(() => {
      drafts.setText(keyA, textDoc("T1"));
      drafts.setText(keyB, textDoc("B"));
    });

    expect(result.current.drafts.captureGeneration()).toBe(generation);
    expect(result.current.drafts.resetRevision).toBe(reset);
    expect(result.current.mirrors.blocked).toBe(false);
  });
});
