/**
 * Tab-local draft text persistence (issue #769) — survives a same-tab
 * refresh only, mirroring chatViewportPersistence.ts's (#492) key-scoping and
 * never-throw/validate-on-read discipline.
 *
 * Deliberately narrow: only the editor's TipTap document and the id of the
 * message being replied to ever reach sessionStorage. Never attachments,
 * never a voice recording's Blob/object URL, never a token — those stay
 * in-memory only for the life of the SPA tab (issue #769, "NÃO USE
 * LOCALSTORAGE PARA BLOBS" / "PRIMEIRA VERSÃO ACEITÁVEL"). This is a
 * deliberate, narrow exception to the "no message content in storage"
 * invariant documented in ChatMessageArea.tsx, reviewed for #769.
 */

import type { TTNode } from "./tiptapSerializer";

export interface DraftPersistencePayload {
  text: TTNode | null;
  replyToMessageId: string | null;
  updatedAt: number;
}

const keyPrefix = (userId: string) => `nchat.chat.draft.v1:${encodeURIComponent(userId)}:`;

function storageKey(userId: string, draftKey: string): string {
  return `${keyPrefix(userId)}${draftKey}`;
}

const maxIdLength = 256;

function isValidNode(raw: unknown): raw is TTNode {
  if (typeof raw !== "object" || raw === null) return false;
  const node = raw as Record<string, unknown>;
  if (node.type !== undefined && typeof node.type !== "string") return false;
  if (node.text !== undefined && typeof node.text !== "string") return false;
  if (node.content !== undefined) {
    if (!Array.isArray(node.content)) return false;
    if (!node.content.every(isValidNode)) return false;
  }
  return true;
}

function isValidPayload(raw: unknown): raw is DraftPersistencePayload {
  if (typeof raw !== "object" || raw === null) return false;
  const payload = raw as Record<string, unknown>;
  return (
    (payload.text === null || isValidNode(payload.text)) &&
    (payload.replyToMessageId === null ||
      (typeof payload.replyToMessageId === "string" &&
        payload.replyToMessageId.length > 0 &&
        payload.replyToMessageId.length <= maxIdLength)) &&
    typeof payload.updatedAt === "number" &&
    Number.isFinite(payload.updatedAt)
  );
}

/** Never throws. Returns null for missing, corrupt, or invalid data. */
export function loadDraftPersistence(
  userId: string,
  draftKey: string,
): DraftPersistencePayload | null {
  try {
    const raw = sessionStorage.getItem(storageKey(userId, draftKey));
    if (!raw) return null;
    const parsed: unknown = JSON.parse(raw);
    return isValidPayload(parsed) ? parsed : null;
  } catch {
    return null;
  }
}

/** Never throws; a failed write is a no-op, never an app-breaking error. */
export function saveDraftPersistence(
  userId: string,
  draftKey: string,
  payload: DraftPersistencePayload,
): void {
  try {
    sessionStorage.setItem(storageKey(userId, draftKey), JSON.stringify(payload));
  } catch {
    // Best-effort mirror; a failed write must never block the composer.
  }
}

/** Never throws. */
export function clearDraftPersistence(userId: string, draftKey: string): void {
  try {
    sessionStorage.removeItem(storageKey(userId, draftKey));
  } catch {
    // Nothing to do — the tab is going away or storage is unavailable either way.
  }
}

/**
 * Every valid persisted draft for this user in this tab, keyed by draftKey —
 * issue #769: lets the sidebar show "Rascunho" for a conversation right
 * after an F5, before the reader has reopened it (which is what would
 * otherwise be the first read, and therefore the first hydration, of any
 * one draft). Never throws; a corrupt individual entry is skipped, not
 * fatal to the others.
 */
export function loadAllDraftPersistence(userId: string): Array<[string, DraftPersistencePayload]> {
  if (!userId) return [];
  try {
    const prefix = keyPrefix(userId);
    const entries: Array<[string, DraftPersistencePayload]> = [];
    for (let i = 0; i < sessionStorage.length; i += 1) {
      const key = sessionStorage.key(i);
      if (!key || !key.startsWith(prefix)) continue;
      const draftKey = key.slice(prefix.length);
      try {
        const raw = sessionStorage.getItem(key);
        if (!raw) continue;
        const parsed: unknown = JSON.parse(raw);
        if (isValidPayload(parsed)) entries.push([draftKey, parsed]);
      } catch {
        // Skip this one entry; the rest of the scan still proceeds.
      }
    }
    return entries;
  } catch {
    return [];
  }
}

/** Every persisted draft for this user in this tab — issue #769, logout cleanup. Never throws. */
export function clearUserDraftPersistence(userId: string): void {
  if (!userId) return;
  try {
    const prefix = keyPrefix(userId);
    const toRemove: string[] = [];
    for (let i = 0; i < sessionStorage.length; i += 1) {
      const key = sessionStorage.key(i);
      if (key && key.startsWith(prefix)) toRemove.push(key);
    }
    for (const key of toRemove) sessionStorage.removeItem(key);
  } catch {
    // Best-effort cleanup only.
  }
}
