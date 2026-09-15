/**
 * useConversationDrafts (issue #769) — the store two conversations' composer
 * state lives in once it is lifted out of the remounted ChatComposer.
 */

import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { useConversationDrafts } from "./useConversationDrafts";
import { loadDraftPersistence } from "./chatDraftPersistence";
import type { AttachmentUploadItem } from "./useAttachmentUpload";
import type { TTNode } from "./tiptapSerializer";

const textDoc = (text: string): TTNode => ({
  type: "doc",
  content: [{ type: "paragraph", content: [{ type: "text", text }] }],
});

const emptyDoc: TTNode = { type: "doc", content: [{ type: "paragraph" }] };

function fakeAttachment(localId: string): AttachmentUploadItem {
  return {
    localId,
    file: new File(["x"], `${localId}.txt`),
    status: "queued",
    progress: null,
    error: null,
    attachment: null,
  };
}

describe("useConversationDrafts", () => {
  beforeEach(() => {
    sessionStorage.clear();
    vi.useFakeTimers();
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it("keeps text for different conversations independent (RF-769 minimal scenario)", () => {
    const { result } = renderHook(() => useConversationDrafts("u1"));
    act(() => {
      result.current.setText("dm:caio", textDoc("texto X"));
      result.current.setText("dm:juliane", textDoc("texto Y"));
    });
    expect(result.current.getDraft("dm:caio")?.text).toEqual(textDoc("texto X"));
    expect(result.current.getDraft("dm:juliane")?.text).toEqual(textDoc("texto Y"));
  });

  it("never leaks an attachment added in one conversation into another", () => {
    const { result } = renderHook(() => useConversationDrafts("u1"));
    act(() => {
      result.current.setAttachments("dm:caio", [fakeAttachment("a1")]);
    });
    expect(result.current.getDraft("dm:caio")?.attachments).toHaveLength(1);
    expect(result.current.getDraft("dm:juliane")?.attachments ?? []).toHaveLength(0);
  });

  it("does not remove a draft that holds only an attachment or only a voice message", () => {
    const { result } = renderHook(() => useConversationDrafts("u1"));
    act(() => result.current.setAttachments("dm:caio", [fakeAttachment("a1")]));
    expect(result.current.getDraft("dm:caio")).toBeDefined();

    act(() => {
      result.current.setVoiceMessage("dm:juliane", {
        blob: new Blob(["x"]),
        previewUrl: "blob:voice",
        durationMs: 1200,
        mimeType: "audio/webm",
      });
    });
    expect(result.current.getDraft("dm:juliane")).toBeDefined();
  });

  it("removes a draft once it becomes truly empty (text, attachments, voice, reply all gone)", () => {
    const { result } = renderHook(() => useConversationDrafts("u1"));
    act(() => result.current.setText("dm:caio", textDoc("oi")));
    expect(result.current.getDraft("dm:caio")).toBeDefined();
    act(() => result.current.setText("dm:caio", emptyDoc));
    expect(result.current.getDraft("dm:caio")).toBeUndefined();
  });

  it("does not consider an empty paragraph document meaningful text", () => {
    const { result } = renderHook(() => useConversationDrafts("u1"));
    act(() => result.current.setText("dm:caio", emptyDoc));
    expect(result.current.getDraft("dm:caio")).toBeUndefined();
  });

  it("bumps revision on every mutation, monotonically", () => {
    const { result } = renderHook(() => useConversationDrafts("u1"));
    act(() => result.current.setText("dm:caio", textDoc("a")));
    const r1 = result.current.getDraft("dm:caio")?.revision;
    act(() => result.current.setText("dm:caio", textDoc("ab")));
    const r2 = result.current.getDraft("dm:caio")?.revision;
    expect(r1).toBeDefined();
    expect(r2).toBeGreaterThan(r1 as number);
  });

  it("clearDraft removes the draft and revokes its voice preview URL", () => {
    const revokeSpy = vi.spyOn(URL, "revokeObjectURL");
    const { result } = renderHook(() => useConversationDrafts("u1"));
    act(() => {
      result.current.setVoiceMessage("dm:caio", {
        blob: new Blob(["x"]),
        previewUrl: "blob:voice-1",
        durationMs: 500,
        mimeType: "audio/webm",
      });
    });
    act(() => result.current.clearDraft("dm:caio"));
    expect(result.current.getDraft("dm:caio")).toBeUndefined();
    expect(revokeSpy).toHaveBeenCalledWith("blob:voice-1");
  });

  it("clearAllDrafts clears memory, revokes every voice URL, and clears this user's sessionStorage", () => {
    const revokeSpy = vi.spyOn(URL, "revokeObjectURL");
    const { result } = renderHook(() => useConversationDrafts("u1"));
    act(() => {
      result.current.setText("dm:caio", textDoc("oi"));
      result.current.setVoiceMessage("dm:juliane", {
        blob: new Blob(["x"]),
        previewUrl: "blob:voice-2",
        durationMs: 500,
        mimeType: "audio/webm",
      });
    });
    act(() => vi.advanceTimersByTime(500));
    expect(loadDraftPersistence("u1", "dm:caio")).not.toBeNull();

    act(() => result.current.clearAllDrafts());
    expect(result.current.getDraft("dm:caio")).toBeUndefined();
    expect(result.current.getDraft("dm:juliane")).toBeUndefined();
    expect(revokeSpy).toHaveBeenCalledWith("blob:voice-2");
    expect(loadDraftPersistence("u1", "dm:caio")).toBeNull();
  });

  it("mirrors text + replyToMessageId to sessionStorage after a debounce, and hydrates a fresh store from it", () => {
    const { result, unmount } = renderHook(() => useConversationDrafts("u1"));
    act(() => {
      result.current.setText("dm:caio", textDoc("sobrevive ao F5"));
      result.current.setReply("dm:caio", "m1");
    });
    act(() => vi.advanceTimersByTime(500));
    unmount();

    const { result: fresh } = renderHook(() => useConversationDrafts("u1"));
    const draft = fresh.current.getDraft("dm:caio");
    expect(draft?.text).toEqual(textDoc("sobrevive ao F5"));
    expect(draft?.replyToMessageId).toBe("m1");
  });

  it("shows the sidebar Rascunho tag right after an F5, before the conversation is reopened", () => {
    const { result, unmount } = renderHook(() => useConversationDrafts("u1"));
    act(() => result.current.setText("dm:caio", textDoc("vou verificar")));
    act(() => vi.advanceTimersByTime(500));
    unmount();

    // A fresh store — same shape as a full page reload — must surface the
    // summary without anything having called getDraft("dm:caio") first:
    // that call is what opening the conversation would do, and the whole
    // point of the sidebar tag is to be visible before that happens.
    const { result: fresh } = renderHook(() => useConversationDrafts("u1"));
    expect(fresh.current.summaries.get("dm:caio")).toEqual({ hasDraft: true });
  });

  it("does not let a debounced persist scheduled before a send resurrect the sent text (issue #845, race)", () => {
    const { result } = renderHook(() => useConversationDrafts("u1"));
    // A keystroke schedules a 400ms debounced sessionStorage write for
    // "teste" — deliberately not advanced yet, so the timer is still
    // pending when the draft is emptied below.
    act(() => result.current.setText("dm:caio", textDoc("teste")));
    // Mirrors what a confirmed send does: the editor clears and its
    // onUpdate mirrors the now-empty doc back via setText, which is the
    // only path that empties the draft on send (see useChatEditor.ts).
    act(() => result.current.setText("dm:caio", emptyDoc));
    expect(result.current.getDraft("dm:caio")).toBeUndefined();
    expect(loadDraftPersistence("u1", "dm:caio")).toBeNull();

    // The stale timer for "teste" fires here. It must not have survived
    // the transition to EMPTY above.
    act(() => vi.advanceTimersByTime(500));
    expect(loadDraftPersistence("u1", "dm:caio")).toBeNull();
    expect(result.current.getDraft("dm:caio")).toBeUndefined();
  });

  it("never mirrors attachments or voice messages to sessionStorage", () => {
    const { result } = renderHook(() => useConversationDrafts("u1"));
    act(() => {
      result.current.setAttachments("dm:caio", [fakeAttachment("a1")]);
      result.current.setVoiceMessage("dm:caio", {
        blob: new Blob(["x"]),
        previewUrl: "blob:voice-3",
        durationMs: 500,
        mimeType: "audio/webm",
      });
    });
    act(() => vi.advanceTimersByTime(500));
    expect(loadDraftPersistence("u1", "dm:caio")).toBeNull();
  });

  it("scopes sessionStorage hydration by user — a different user never sees another's draft", () => {
    const { result: u1 } = renderHook(() => useConversationDrafts("u1"));
    act(() => u1.current.setText("dm:caio", textDoc("segredo")));
    act(() => vi.advanceTimersByTime(500));

    const { result: u2 } = renderHook(() => useConversationDrafts("u2"));
    expect(u2.current.getDraft("dm:caio")).toBeUndefined();
  });

  it("summaries only changes at the EMPTY<->HAS_DRAFT boundary, never on a keystroke that keeps text present (issue #845)", () => {
    const { result } = renderHook(() => useConversationDrafts("u1"));
    act(() => result.current.setText("dm:caio", textDoc("t")));
    const summariesAfterFirstChar = result.current.summaries;
    // "t" -> "te" -> "tes" -> "teste": still has meaningful text the whole
    // time, so the summary map identity must not move at all.
    act(() => result.current.setText("dm:caio", textDoc("te")));
    act(() => result.current.setText("dm:caio", textDoc("tes")));
    act(() => result.current.setText("dm:caio", textDoc("teste")));
    expect(result.current.summaries).toBe(summariesAfterFirstChar);

    // Only the EMPTY -> HAS_DRAFT / HAS_DRAFT -> EMPTY crossing changes it.
    act(() => result.current.setText("dm:caio", emptyDoc));
    expect(result.current.summaries).not.toBe(summariesAfterFirstChar);
    expect(result.current.summaries.has("dm:caio")).toBe(false);
  });

  it("summaries reports draft presence only — never text, kind or attachment count (issue #845, privacy/performance)", () => {
    const { result } = renderHook(() => useConversationDrafts("u1"));
    act(() => result.current.setText("dm:caio", textDoc("vou verificar")));
    expect(result.current.summaries.get("dm:caio")).toEqual({ hasDraft: true });
    act(() =>
      result.current.setAttachments("dm:caio", [fakeAttachment("a1"), fakeAttachment("a2")]),
    );
    expect(result.current.summaries.get("dm:caio")).toEqual({ hasDraft: true });
  });

  it("treats a @mention as meaningful text, even with no plain text alongside it", () => {
    const mentionDoc: TTNode = {
      type: "doc",
      content: [
        {
          type: "paragraph",
          content: [{ type: "mention", attrs: { label: "juliane" } }],
        },
      ],
    };
    const { result } = renderHook(() => useConversationDrafts("u1"));
    act(() => result.current.setText("dm:caio", mentionDoc));
    expect(result.current.getDraft("dm:caio")).toBeDefined();
    expect(result.current.summaries.get("dm:caio")).toEqual({ hasDraft: true });
  });

  it("updateAttachment on a localId no longer in the draft never resurrects it", () => {
    const { result } = renderHook(() => useConversationDrafts("u1"));
    act(() => result.current.setText("dm:caio", textDoc("oi")));
    act(() => result.current.updateAttachment("dm:caio", "not-a-real-id", { status: "success" }));
    expect(result.current.getDraft("dm:caio")?.attachments).toEqual([]);
  });

  it("setVoiceMessage does not revoke the preview URL when it is unchanged", () => {
    const revokeSpy = vi.spyOn(URL, "revokeObjectURL");
    const { result } = renderHook(() => useConversationDrafts("u1"));
    const voice = {
      blob: new Blob(["x"]),
      previewUrl: "blob:same",
      durationMs: 100,
      mimeType: "audio/webm",
    };
    act(() => result.current.setVoiceMessage("dm:caio", voice));
    revokeSpy.mockClear();
    act(() => result.current.setVoiceMessage("dm:caio", { ...voice }));
    expect(revokeSpy).not.toHaveBeenCalled();
  });

  it("eager hydration skips a draftKey already live in memory", () => {
    const { result, rerender } = renderHook(({ uid }) => useConversationDrafts(uid), {
      initialProps: { uid: "u1" },
    });
    act(() => result.current.setText("dm:caio", textDoc("memoria ganha")));
    act(() => vi.advanceTimersByTime(500));
    // Overwrite what is on disk directly, bypassing the store, so a
    // hydration that actually reads it back would clobber the in-memory
    // value — proving the "already live" skip, not just that hydration
    // happens to agree with storage.
    const raw = sessionStorage.getItem("nchat.chat.draft.v1:u1:dm:caio")!;
    const parsed = JSON.parse(raw) as { text: TTNode };
    sessionStorage.setItem(
      "nchat.chat.draft.v1:u1:dm:caio",
      JSON.stringify({ ...parsed, text: textDoc("versao antiga no disco") }),
    );
    // Force the hydration effect to run again on this same instance.
    rerender({ uid: "u2" });
    rerender({ uid: "u1" });
    expect(result.current.getDraft("dm:caio")?.text).toEqual(textDoc("memoria ganha"));
  });

  it("eager hydration skips a persisted entry that is actually empty", () => {
    sessionStorage.setItem(
      "nchat.chat.draft.v1:u1:dm:stale",
      JSON.stringify({ text: null, replyToMessageId: null, updatedAt: 1 }),
    );
    const { result } = renderHook(() => useConversationDrafts("u1"));
    expect(result.current.getDraft("dm:stale")).toBeUndefined();
    expect(result.current.summaries.has("dm:stale")).toBe(false);
  });
});
