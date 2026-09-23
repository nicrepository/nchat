/**
 * useConversationDrafts — the per-conversation composer state (issue #769).
 *
 * `ChatComposer` used to be the *only* place a draft lived, and it was keyed
 * by `${kind}:${targetId}` in ChatMessageArea specifically so switching
 * conversations threw the draft away — see the "Drafts are deliberately not
 * persisted" comment this issue replaces. That key stays (it is still what
 * keeps ChatComposer's TipTap instance, upload queue and voice recorder from
 * leaking between conversations by construction), but the *content* those
 * three own now survives the remount: it is lifted into this Map, held one
 * level above every place that gets remounted on a target change.
 *
 * Mounted once in AppShell.tsx — the one component that survives both a
 * conversation switch (channel<->dm, a route/element change) and a trip to
 * `/profile` and back — and threaded down through ChatOutletContext, the same
 * way `currentUserId`/`channels`/`dms` already are.
 *
 * A plain `Map` in a ref, not React state: nothing here needs a render from a
 * single keystroke (the composer reads its own `editor.getJSON()` for that).
 * `summaries` is the one piece of real state, and it carries presence only
 * (issue #845: no text, kind or attachment count) — it changes only at the
 * EMPTY <-> HAS_DRAFT boundary, never on every character, which is what
 * keeps ChatSidebar from re-rendering on every keystroke (issue #769,
 * "PERFORMANCE DA SIDEBAR"; tightened by issue #845, "PERFORMANCE").
 */

import { useCallback, useEffect, useMemo, useRef, useState } from "react";

import type { AttachmentUploadItem } from "./useAttachmentUpload";
import type { TTNode } from "./tiptapSerializer";
import {
  emptyDraft,
  hydratedDraft,
  isDraftEmpty,
  isTextMeaningful,
  releaseVoice,
  type ConversationDraft,
  type DraftVoiceMessage,
} from "./conversationDraft";
import {
  consumeSnapshot,
  sendSnapshotOf,
  type SendParts,
  type SendSnapshot,
} from "./draftSendSnapshot";
import { useDraftBoundary, type DraftBoundaryApi } from "./useDraftBoundary";
import { useDraftMirrorSync, type DraftMirrorSyncApi } from "./useDraftMirrorSync";
import { useDraftSendLifecycle, type DraftSendLifecycleApi } from "./useDraftSendLifecycle";
import {
  clearDraftPersistence,
  clearUserDraftPersistence,
  loadAllDraftPersistence,
  loadDraftPersistence,
  saveDraftPersistence,
} from "./chatDraftPersistence";

export type { DraftGeneration } from "./useDraftBoundary";
export type { ConversationDraft, DraftVoiceMessage } from "./conversationDraft";
export type { SendParts, SendSnapshot } from "./draftSendSnapshot";
export type { DraftSendLifecycle, SendAttempt, SendOutcome } from "./useDraftSendLifecycle";
export type { DraftMirrorSyncApi } from "./useDraftMirrorSync";

/**
 * Sidebar-relevant summary — issue #845: presence only, never content.
 * The sidebar's "Rascunho" badge only needs to know a draft exists; it must
 * never receive text, a preview, a filename or a voice description (issue
 * #845, "PRIVACIDADE"). Because this carries no content, it is also
 * trivially the same value across every mutation of a still-non-empty
 * draft, which is what keeps `summaries` from changing identity on every
 * keystroke (issue #845, "PERFORMANCE") — it now only flips at the
 * EMPTY<->HAS_DRAFT boundary.
 */
export interface DraftSummary {
  hasDraft: true;
}

/**
 * The draft store, plus the two lifecycles a composer needs to read above
 * its own mount (issue #929): whether a send of this draft is still in
 * flight (useDraftSendLifecycle) and whether the authoritative draft has
 * changed in a way its mirrors must re-read (useDraftMirrorSync).
 */
export interface ConversationDraftsApi
  extends
    Omit<DraftSendLifecycleApi, "clearSendLifecycle">,
    Omit<DraftMirrorSyncApi, "clearMirrorSync">,
    Omit<DraftBoundaryApi, "endSession"> {
  getDraft: (draftKey: string) => ConversationDraft | undefined;
  setText: (draftKey: string, text: TTNode | null) => void;
  setAttachments: (draftKey: string, attachments: AttachmentUploadItem[]) => void;
  /**
   * Patches one attachment by identity (issue #769, "IDENTIDADE DE
   * ATTACHMENT"). Safe to call from an upload callback that resolves after
   * the composer for `draftKey` has unmounted (conversation switched away):
   * unlike setAttachments, it never depends on a component's own state
   * still being current, and it is a no-op if `localId` is no longer in the
   * draft (the user removed it, or the draft is gone) rather than
   * resurrecting a stale entry.
   */
  updateAttachment: (
    draftKey: string,
    localId: string,
    patch: Partial<AttachmentUploadItem>,
  ) => void;
  setVoiceMessage: (draftKey: string, voice: DraftVoiceMessage | null) => void;
  setReply: (draftKey: string, replyToMessageId: string | null) => void;
  /**
   * Captures, at submit, the identity of everything a send is taking out of
   * `draftKey` (issue #929). Cheap and side-effect free; the value is handed
   * back to consumeSentSnapshot once the server acknowledges the send.
   */
  createSendSnapshot: (draftKey: string, parts: SendParts) => SendSnapshot;
  /**
   * The one transition a confirmed send performs on its draft (issue #929):
   * consumes each field still matching the snapshot, preserves everything
   * composed since, and — as a single mutation — persists, updates the
   * summary and removes the entry only if what remains is empty.
   */
  consumeSentSnapshot: (snapshot: SendSnapshot) => void;
  /** Removes the draft entirely — a confirmed send, or GC of an emptied draft. */
  clearDraft: (draftKey: string) => void;
  /** Logout / account switch (issue #769, "FASE 14 — LOGOUT"): every draft, every object URL. */
  clearAllDrafts: () => void;
  /** Sidebar-relevant summaries only, keyed by draftKey. Coarse — see module doc. */
  summaries: ReadonlyMap<string, DraftSummary>;
  /* The session boundary — see useDraftBoundary: `captureGeneration`,
     `isGenerationCurrent` and `resetRevision` come from there. */
}

function summaryOf(): DraftSummary {
  return { hasDraft: true };
}

/**
 * The fallback for a not-yet-ready outlet context (mirrors emptyOutletContext
 * in useConversationTarget.ts) — every call is inert, never throws, and
 * getDraft always reports "nothing here", so a screen rendered before
 * AppShell's real store is available never behaves as if drafts exist.
 */
export const noopConversationDrafts: ConversationDraftsApi = {
  getDraft: () => undefined,
  setText: () => undefined,
  setAttachments: () => undefined,
  updateAttachment: () => undefined,
  setVoiceMessage: () => undefined,
  setReply: () => undefined,
  createSendSnapshot: (draftKey, parts) => sendSnapshotOf(draftKey, undefined, parts),
  consumeSentSnapshot: () => undefined,
  clearDraft: () => undefined,
  clearAllDrafts: () => undefined,
  summaries: new Map(),
  captureGeneration: () => 0,
  isGenerationCurrent: () => true,
  resetRevision: 0,
  getResetRevision: () => 0,
  hasUnobservedReset: () => false,
  sendLifecycle: new Map(),
  hasPendingSend: () => false,
  beginSend: (snapshot) => ({ id: 0, snapshot }),
  settleSend: () => undefined,
  mirrorRevisions: new Map(),
  getMirrorRevision: () => 0,
  hasUnreconciledMirror: () => false,
  notifyMirrors: () => undefined,
};

export function useConversationDrafts(userId: string): ConversationDraftsApi {
  const draftsRef = useRef(new Map<string, ConversationDraft>());
  const [summaries, setSummaries] = useState<ReadonlyMap<string, DraftSummary>>(new Map());
  const userIdRef = useRef(userId);
  useEffect(() => {
    userIdRef.current = userId;
  });
  const persistTimersRef = useRef(new Map<string, ReturnType<typeof setTimeout>>());

  // Issue #845: the one place a pending debounced persist for a draftKey is
  // invalidated. Used both by an explicit clearDraft and by applyMutation's
  // own EMPTY transition — without this, a timer scheduled while the draft
  // still had text can outlive a send that emptied it, and fire afterwards
  // with the pre-send text closed over at schedule time, writing it back
  // into sessionStorage ("sent draft resurrects" — issue #845 bug 1).
  const cancelPersist = useCallback((draftKey: string) => {
    const timer = persistTimersRef.current.get(draftKey);
    if (timer) {
      clearTimeout(timer);
      persistTimersRef.current.delete(draftKey);
    }
  }, []);

  // Issue #769: hydrates every persisted draft for this user up front, once
  // a real userId is known (login, or an F5 that lands back in an
  // authenticated session) — not lazily, one at a time, the first time each
  // conversation happens to be opened. Without this, the sidebar's
  // "Rascunho" tag would only ever appear for a conversation the reader had
  // already reopened since the last full page load, which defeats the
  // point of it: the tag exists precisely so a draft is visible *without*
  // opening the conversation.
  useEffect(() => {
    if (!userId) return;
    const persisted = loadAllDraftPersistence(userId);
    if (persisted.length === 0) return;
    let changed = false;
    for (const [draftKey, payload] of persisted) {
      if (draftsRef.current.has(draftKey)) continue;
      const hydrated = hydratedDraft(payload);
      if (isDraftEmpty(hydrated)) continue;
      draftsRef.current.set(draftKey, hydrated);
      changed = true;
    }
    if (!changed) return;
    setSummaries((prev) => {
      const copy = new Map(prev);
      for (const draftKey of draftsRef.current.keys()) {
        if (!copy.has(draftKey)) copy.set(draftKey, summaryOf());
      }
      return copy;
    });
    // Runs once per userId becoming available (login, or the first render
    // after an F5) — not on every draft mutation, which already updates
    // `summaries` itself through applyMutation.
  }, [userId]);

  const schedulePersist = useCallback((draftKey: string, draft: ConversationDraft) => {
    const timers = persistTimersRef.current;
    const existing = timers.get(draftKey);
    if (existing) clearTimeout(existing);
    const scheduledRevision = draft.revision;
    timers.set(
      draftKey,
      setTimeout(() => {
        timers.delete(draftKey);
        const uid = userIdRef.current;
        if (!uid) return;
        // Issue #845 defense-in-depth: the primary fix is that every path
        // that empties/replaces this draft cancels this timer outright (see
        // cancelPersist). This is a second, independent guard in case some
        // future mutation path forgets to — if the draft in memory is gone
        // or has moved past the revision this timer was scheduled for,
        // something else already superseded what this timer would write,
        // so it must not write at all.
        if (draftsRef.current.get(draftKey)?.revision !== scheduledRevision) return;
        // Only text + the id of what is being replied to ever leave memory
        // (issue #769, "NÃO USE LOCALSTORAGE PARA BLOBS" / narrow F5 exception
        // to the "no message content in storage" invariant — see
        // ChatMessageArea.tsx's security header).
        saveDraftPersistence(uid, draftKey, {
          text: draft.text,
          replyToMessageId: draft.replyToMessageId,
          updatedAt: draft.updatedAt,
        });
      }, 400),
    );
  }, []);

  const mirrors = useDraftMirrorSync();
  const { notifyMirrors, clearMirrorSync } = mirrors;
  // Per store instance, never module-global: two stores (two tests, two
  // roots) must not be able to invalidate each other's work.
  const boundary = useDraftBoundary();
  const { captureGeneration, isGenerationCurrent, resetRevision, endSession } = boundary;
  const { getResetRevision, hasUnobservedReset } = boundary;

  const applyMutation = useCallback(
    (draftKey: string, mutate: (current: ConversationDraft) => ConversationDraft) => {
      const current = draftsRef.current.get(draftKey) ?? emptyDraft();
      const next = mutate(current);
      next.revision = current.revision + 1;
      next.updatedAt = Date.now();

      if (isDraftEmpty(next)) {
        draftsRef.current.delete(draftKey);
        // Issue #845 bug 1: a debounce timer scheduled while this draft
        // still had text (e.g. right before a send cleared it) must not be
        // allowed to outlive this transition and write stale text back.
        cancelPersist(draftKey);
        clearDraftPersistence(userIdRef.current, draftKey);
      } else {
        draftsRef.current.set(draftKey, next);
        // Only worth mirroring when there is text or a reply to restore on
        // F5 — an attachment/voice-only draft has nothing this persistence
        // layer is allowed to carry (see module doc), so writing a
        // text:null/replyToMessageId:null entry for it would be pure noise.
        if (isTextMeaningful(next.text) || next.replyToMessageId) {
          schedulePersist(draftKey, next);
        } else {
          clearDraftPersistence(userIdRef.current, draftKey);
        }
      }

      // Issue #845, "PERFORMANCE": the summary carries no content, so it can
      // only ever meaningfully change at the EMPTY<->HAS_DRAFT boundary —
      // never mid-draft, which is what previously made this fire (and
      // re-render every sidebar row) on every keystroke.
      const hasDraft = !isDraftEmpty(next);
      setSummaries((prev) => {
        const hadDraft = prev.has(draftKey);
        if (hasDraft === hadDraft) return prev;
        const copy = new Map(prev);
        if (hasDraft) copy.set(draftKey, summaryOf());
        else copy.delete(draftKey);
        return copy;
      });
    },
    [schedulePersist, cancelPersist],
  );

  const getDraft = useCallback((draftKey: string): ConversationDraft | undefined => {
    const inMemory = draftsRef.current.get(draftKey);
    if (inMemory) return inMemory;
    const uid = userIdRef.current;
    if (!uid) return undefined;
    const persisted = loadDraftPersistence(uid, draftKey);
    if (!persisted) return undefined;
    const hydrated = hydratedDraft(persisted);
    if (isDraftEmpty(hydrated)) return undefined;
    draftsRef.current.set(draftKey, hydrated);
    return hydrated;
  }, []);

  const setText = useCallback(
    (draftKey: string, text: TTNode | null) => {
      applyMutation(draftKey, (current) => ({
        ...current,
        text,
        textRevision: current.textRevision + 1,
      }));
    },
    [applyMutation],
  );

  const setAttachments = useCallback(
    (draftKey: string, attachments: AttachmentUploadItem[]) => {
      applyMutation(draftKey, (current) => ({ ...current, attachments }));
    },
    [applyMutation],
  );

  const updateAttachment = useCallback(
    (draftKey: string, localId: string, patch: Partial<AttachmentUploadItem>) => {
      applyMutation(draftKey, (current) => ({
        ...current,
        attachments: current.attachments.map((item) =>
          item.localId === localId ? { ...item, ...patch } : item,
        ),
      }));
      // Only where the upload ended (issue #929, second review): a queue
      // mounted for this conversation — very possibly not the one that
      // started the upload — has to show the result. Progress reports stay
      // between an upload and its own component, so a file going up does
      // not re-render anything above the composer.
      if (patch.status === "success" || patch.status === "failed") notifyMirrors(draftKey);
    },
    [applyMutation, notifyMirrors],
  );

  const setVoiceMessage = useCallback(
    (draftKey: string, voiceMessage: DraftVoiceMessage | null) => {
      applyMutation(draftKey, (current) => {
        releaseVoice(current.voiceMessage, voiceMessage);
        return { ...current, voiceMessage };
      });
      // A recording is finalized, or leaves the draft, exactly once —
      // sparse by nature, and possibly produced by a recorder whose
      // composer is already gone (issue #929, second review).
      notifyMirrors(draftKey);
    },
    [applyMutation, notifyMirrors],
  );

  const setReply = useCallback(
    (draftKey: string, replyToMessageId: string | null) => {
      applyMutation(draftKey, (current) => ({ ...current, replyToMessageId }));
    },
    [applyMutation],
  );

  const createSendSnapshot = useCallback(
    (draftKey: string, parts: SendParts): SendSnapshot =>
      sendSnapshotOf(draftKey, getDraft(draftKey), parts),
    [getDraft],
  );

  // One applyMutation, so the draft goes from "before the ACK" to "after
  // the ACK" in a single step: one revision bump, one persist decision, one
  // summary check, and no intermediate state another callback could observe
  // (issue #929, "ATOMICIDADE").
  const consumeSentSnapshot = useCallback(
    (snapshot: SendSnapshot) => {
      applyMutation(snapshot.draftKey, (current) => {
        const next = consumeSnapshot(current, snapshot);
        releaseVoice(current.voiceMessage, next.voiceMessage);
        return next;
      });
      // Announced as part of the same acknowledgement: from here on a
      // composer still showing what this send carried knows it is behind,
      // and its send path stays closed until it has caught up.
      notifyMirrors(snapshot.draftKey);
    },
    [applyMutation, notifyMirrors],
  );

  const clearDraft = useCallback(
    (draftKey: string) => {
      const existing = draftsRef.current.get(draftKey);
      if (existing?.voiceMessage) URL.revokeObjectURL(existing.voiceMessage.previewUrl);
      cancelPersist(draftKey);
      draftsRef.current.delete(draftKey);
      clearDraftPersistence(userIdRef.current, draftKey);
      setSummaries((prev) => {
        if (!prev.has(draftKey)) return prev;
        const copy = new Map(prev);
        copy.delete(draftKey);
        return copy;
      });
    },
    [cancelPersist],
  );

  const lifecycle = useDraftSendLifecycle(consumeSentSnapshot);
  const { clearSendLifecycle } = lifecycle;

  const clearAllDrafts = useCallback(() => {
    // First: the session being cleared ends here — for the operations
    // already under way (the generation) and for the composers still on
    // screen holding its content (the reset revision). See useDraftBoundary.
    endSession();
    clearSendLifecycle();
    clearMirrorSync();
    for (const timer of persistTimersRef.current.values()) clearTimeout(timer);
    persistTimersRef.current.clear();
    for (const draft of draftsRef.current.values()) {
      if (draft.voiceMessage) URL.revokeObjectURL(draft.voiceMessage.previewUrl);
    }
    draftsRef.current.clear();
    clearUserDraftPersistence(userIdRef.current);
    // A fresh, empty Map only when there was something to actually clear —
    // an unconditional new Map() here would change the identity every
    // single call (e.g. a spurious/duplicate auth-change notification with
    // nothing to clear), and this value flows straight into
    // DraftSummariesContext: any identity change re-renders every row in
    // the sidebar, which a popup menu mid-interaction does not appreciate.
    setSummaries((prev) => (prev.size === 0 ? prev : new Map()));
  }, [clearMirrorSync, clearSendLifecycle, endSession]);

  const { sendLifecycle, hasPendingSend, beginSend, settleSend } = lifecycle;
  const { mirrorRevisions, getMirrorRevision, hasUnreconciledMirror } = mirrors;
  return useMemo(
    () => ({
      getDraft,
      setText,
      setAttachments,
      updateAttachment,
      setVoiceMessage,
      setReply,
      createSendSnapshot,
      consumeSentSnapshot,
      clearDraft,
      clearAllDrafts,
      summaries,
      captureGeneration,
      isGenerationCurrent,
      resetRevision,
      getResetRevision,
      hasUnobservedReset,
      sendLifecycle,
      hasPendingSend,
      beginSend,
      settleSend,
      mirrorRevisions,
      getMirrorRevision,
      hasUnreconciledMirror,
      notifyMirrors,
    }),
    [
      getDraft,
      setText,
      setAttachments,
      updateAttachment,
      setVoiceMessage,
      setReply,
      createSendSnapshot,
      consumeSentSnapshot,
      clearDraft,
      clearAllDrafts,
      summaries,
      captureGeneration,
      isGenerationCurrent,
      resetRevision,
      getResetRevision,
      hasUnobservedReset,
      sendLifecycle,
      hasPendingSend,
      beginSend,
      settleSend,
      mirrorRevisions,
      getMirrorRevision,
      hasUnreconciledMirror,
      notifyMirrors,
    ],
  );
}
