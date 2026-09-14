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
 * `summaries` is the one piece of real state, and it is deliberately coarse —
 * it changes only when a draft's sidebar-relevant *kind* changes (empty <->
 * has-text, attachment count, voice present/absent), never on every
 * character, which is what keeps ChatSidebar from re-rendering on every
 * keystroke (issue #769, "PERFORMANCE DA SIDEBAR").
 */

import { useCallback, useEffect, useMemo, useRef, useState } from "react";

import type { AttachmentUploadItem } from "./useAttachmentUpload";
import type { TTNode } from "./tiptapSerializer";
import {
  clearDraftPersistence,
  clearUserDraftPersistence,
  loadAllDraftPersistence,
  loadDraftPersistence,
  saveDraftPersistence,
} from "./chatDraftPersistence";

export interface DraftVoiceMessage {
  blob: Blob;
  previewUrl: string;
  durationMs: number;
  mimeType: string;
}

export interface ConversationDraft {
  text: TTNode | null;
  attachments: AttachmentUploadItem[];
  voiceMessage: DraftVoiceMessage | null;
  replyToMessageId: string | null;
  /** Monotonic; bumped by every mutation. The ACK-race guard for #769 ("REVISION"). */
  revision: number;
  updatedAt: number;
}

export type DraftSummaryKind = "text" | "attachments" | "voice" | "mixed";

export interface DraftSummary {
  kind: DraftSummaryKind;
  text: string | null;
  attachmentCount: number;
}

export interface ConversationDraftsApi {
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
  /** Removes the draft entirely — a confirmed send, or GC of an emptied draft. */
  clearDraft: (draftKey: string) => void;
  /** Logout / account switch (issue #769, "FASE 14 — LOGOUT"): every draft, every object URL. */
  clearAllDrafts: () => void;
  /** Sidebar-relevant summaries only, keyed by draftKey. Coarse — see module doc. */
  summaries: ReadonlyMap<string, DraftSummary>;
}

const emptyDraft = (): ConversationDraft => ({
  text: null,
  attachments: [],
  voiceMessage: null,
  replyToMessageId: null,
  revision: 0,
  updatedAt: Date.now(),
});

/** No mention, no non-blank text anywhere in the document. */
function isTextMeaningful(node: TTNode | null): boolean {
  if (!node) return false;
  if (node.type === "mention") return true;
  if (node.text && node.text.trim().length > 0) return true;
  return (node.content ?? []).some(isTextMeaningful);
}

function firstLine(node: TTNode | null): string | null {
  if (!node) return null;
  const parts: string[] = [];
  const walk = (n: TTNode) => {
    if (n.type === "mention") {
      const label = n.attrs?.label;
      if (typeof label === "string") parts.push(`@${label}`);
    } else if (n.text) {
      parts.push(n.text);
    }
    for (const child of n.content ?? []) walk(child);
  };
  walk(node);
  const text = parts.join("").trim();
  return text.length > 0 ? text : null;
}

/** Draft #769 "REGRA DE DRAFT VAZIO": empty only when none of these hold. */
function isDraftEmpty(draft: ConversationDraft): boolean {
  return (
    !isTextMeaningful(draft.text) &&
    draft.attachments.length === 0 &&
    draft.voiceMessage === null &&
    !draft.replyToMessageId
  );
}

function summaryOf(draft: ConversationDraft): DraftSummary {
  const hasText = isTextMeaningful(draft.text);
  const hasAttachments = draft.attachments.length > 0;
  const hasVoice = draft.voiceMessage !== null;
  const kinds = [hasText, hasAttachments, hasVoice].filter(Boolean).length;
  const kind: DraftSummaryKind =
    kinds > 1 ? "mixed" : hasVoice ? "voice" : hasAttachments ? "attachments" : "text";
  return {
    kind,
    text: hasText ? firstLine(draft.text) : null,
    attachmentCount: draft.attachments.length,
  };
}

function sameSummary(a: DraftSummary | undefined, b: DraftSummary): boolean {
  return a?.kind === b.kind && a?.text === b.text && a?.attachmentCount === b.attachmentCount;
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
  clearDraft: () => undefined,
  clearAllDrafts: () => undefined,
  summaries: new Map(),
};

export function useConversationDrafts(userId: string): ConversationDraftsApi {
  const draftsRef = useRef(new Map<string, ConversationDraft>());
  const [summaries, setSummaries] = useState<ReadonlyMap<string, DraftSummary>>(new Map());
  const userIdRef = useRef(userId);
  useEffect(() => {
    userIdRef.current = userId;
  });
  const persistTimersRef = useRef(new Map<string, ReturnType<typeof setTimeout>>());

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
      const hydrated: ConversationDraft = {
        text: payload.text,
        attachments: [],
        voiceMessage: null,
        replyToMessageId: payload.replyToMessageId,
        revision: 0,
        updatedAt: payload.updatedAt,
      };
      if (isDraftEmpty(hydrated)) continue;
      draftsRef.current.set(draftKey, hydrated);
      changed = true;
    }
    if (!changed) return;
    setSummaries((prev) => {
      const copy = new Map(prev);
      for (const [draftKey, draft] of draftsRef.current) {
        if (!copy.has(draftKey)) copy.set(draftKey, summaryOf(draft));
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
    timers.set(
      draftKey,
      setTimeout(() => {
        timers.delete(draftKey);
        const uid = userIdRef.current;
        if (!uid) return;
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

  const applyMutation = useCallback(
    (draftKey: string, mutate: (current: ConversationDraft) => ConversationDraft) => {
      const current = draftsRef.current.get(draftKey) ?? emptyDraft();
      const next = mutate(current);
      next.revision = current.revision + 1;
      next.updatedAt = Date.now();

      if (isDraftEmpty(next)) {
        draftsRef.current.delete(draftKey);
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

      const nextSummary = isDraftEmpty(next) ? undefined : summaryOf(next);
      setSummaries((prev) => {
        const prevSummary = prev.get(draftKey);
        if (nextSummary === undefined) {
          if (prevSummary === undefined) return prev;
          const copy = new Map(prev);
          copy.delete(draftKey);
          return copy;
        }
        if (sameSummary(prevSummary, nextSummary)) return prev;
        const copy = new Map(prev);
        copy.set(draftKey, nextSummary);
        return copy;
      });
    },
    [schedulePersist],
  );

  const getDraft = useCallback((draftKey: string): ConversationDraft | undefined => {
    const inMemory = draftsRef.current.get(draftKey);
    if (inMemory) return inMemory;
    const uid = userIdRef.current;
    if (!uid) return undefined;
    const persisted = loadDraftPersistence(uid, draftKey);
    if (!persisted) return undefined;
    const hydrated: ConversationDraft = {
      text: persisted.text,
      attachments: [],
      voiceMessage: null,
      replyToMessageId: persisted.replyToMessageId,
      revision: 0,
      updatedAt: persisted.updatedAt,
    };
    if (isDraftEmpty(hydrated)) return undefined;
    draftsRef.current.set(draftKey, hydrated);
    return hydrated;
  }, []);

  const setText = useCallback(
    (draftKey: string, text: TTNode | null) => {
      applyMutation(draftKey, (current) => ({ ...current, text }));
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
    },
    [applyMutation],
  );

  const setVoiceMessage = useCallback(
    (draftKey: string, voiceMessage: DraftVoiceMessage | null) => {
      applyMutation(draftKey, (current) => {
        if (current.voiceMessage && current.voiceMessage.previewUrl !== voiceMessage?.previewUrl) {
          URL.revokeObjectURL(current.voiceMessage.previewUrl);
        }
        return { ...current, voiceMessage };
      });
    },
    [applyMutation],
  );

  const setReply = useCallback(
    (draftKey: string, replyToMessageId: string | null) => {
      applyMutation(draftKey, (current) => ({ ...current, replyToMessageId }));
    },
    [applyMutation],
  );

  const clearDraft = useCallback((draftKey: string) => {
    const existing = draftsRef.current.get(draftKey);
    if (existing?.voiceMessage) URL.revokeObjectURL(existing.voiceMessage.previewUrl);
    const timer = persistTimersRef.current.get(draftKey);
    if (timer) {
      clearTimeout(timer);
      persistTimersRef.current.delete(draftKey);
    }
    draftsRef.current.delete(draftKey);
    clearDraftPersistence(userIdRef.current, draftKey);
    setSummaries((prev) => {
      if (!prev.has(draftKey)) return prev;
      const copy = new Map(prev);
      copy.delete(draftKey);
      return copy;
    });
  }, []);

  const clearAllDrafts = useCallback(() => {
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
  }, []);

  return useMemo(
    () => ({
      getDraft,
      setText,
      setAttachments,
      updateAttachment,
      setVoiceMessage,
      setReply,
      clearDraft,
      clearAllDrafts,
      summaries,
    }),
    [
      getDraft,
      setText,
      setAttachments,
      updateAttachment,
      setVoiceMessage,
      setReply,
      clearDraft,
      clearAllDrafts,
      summaries,
    ],
  );
}
