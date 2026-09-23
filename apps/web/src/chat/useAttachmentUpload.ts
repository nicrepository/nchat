import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";

import type { UploadProgress } from "../lib/api";
import type { WorkspaceAttachmentLimits } from "./chatApi";
import {
  AttachmentUploadError,
  deleteAttachmentDraft,
  tooLargeMessage,
  uploadAttachment,
} from "./filesApi";
import type { ChannelAttachment } from "./chatTypes";
import type { ConversationDraftsApi } from "./useConversationDrafts";

export interface AttachmentUploadTarget {
  kind: "channel" | "dm";
  id: string;
}

export type AttachmentUploadStatus = "queued" | "uploading" | "failed" | "success";

export interface AttachmentUploadItem {
  localId: string;
  file: File;
  status: AttachmentUploadStatus;
  progress: UploadProgress | null;
  error: string | null;
  attachment: ChannelAttachment | null;
}

export interface AttachmentUploadState {
  items: AttachmentUploadItem[];
  status: "idle" | "uploading" | "failed" | "success";
  error: string | null;
  uploadedName: string | null;
  uploadedAttachment: ChannelAttachment | null;
  progress: UploadProgress | null;
  aggregateProgress: UploadProgress | null;
  busy: boolean;
  notice: string | null;
  selectFile: (file: File) => void;
  selectFiles: (files: Iterable<File>) => void;
  remove: (localId: string) => void;
  retry: (localId: string) => void;
  dismiss: () => void;
  /**
   * Drops from the queue exactly the attachments a confirmed send published,
   * by localId (issues #875, #929). Anything added while that send was still
   * in flight belongs to the next message and is left in the queue.
   *
   * The queue's own bookkeeping only: the conversation's draft has already
   * consumed the same identities through its send snapshot, and this must
   * not write the list back into it.
   */
  forgetPublished: (publishedLocalIds: readonly string[]) => void;
  /**
   * Abandons everything this queue holds for a session that has ended
   * (`clearAllDrafts` — issue #929, fifth review). Uploads still running
   * are aborted, queued files never start, and the files themselves are
   * released. Local only: the store cleared itself first, and writing an
   * empty queue back into it would be writing into the *next* session.
   */
  resetForSessionEnd: () => void;
  /**
   * Makes the queue reflect the conversation draft's authoritative
   * attachments (issue #929): a send acknowledged by a previous instance of
   * this composer consumed items this queue was seeded with. Local only —
   * never written back — and never a reason to start, abort or delete an
   * upload: an item that left the draft was published by that send, and one
   * still running is still in the draft.
   */
  reconcileWithDraft: (attachments: readonly AttachmentUploadItem[]) => void;
}

const MAX_CONCURRENT = 2;

const fileKey = (file: File) => `${file.name}\0${file.size}\0${file.lastModified}`;
const explicitlyUnsupportedFile = (file: File) => {
  const type = file.type.toLowerCase();
  const extension = file.name.toLowerCase().split(".").pop() ?? "";
  return (
    type === "image/svg+xml" ||
    type === "text/html" ||
    type === "application/x-msdownload" ||
    type === "application/x-executable" ||
    ["exe", "dll", "com", "bat", "cmd", "msi"].includes(extension)
  );
};
const failureMessage = (cause: unknown) =>
  cause instanceof AttachmentUploadError ? cause.message : "Não foi possível enviar o arquivo.";

/** What a workspace allows when it has not said otherwise. */
const defaultLimits: WorkspaceAttachmentLimits = {
  maxUploadBytes: null,
  maxFiles: 1,
  maxBytes: Number.MAX_SAFE_INTEGER,
};

/**
 * Where an upload came from: the draft it was started for, and the store
 * generation that draft belonged to (issue #929, third review). A result
 * that arrives after `clearAllDrafts` — logout, account switch — belongs to
 * a session that is over, and a queue built since may well have handed the
 * same local id to a different file.
 */
interface UploadOrigin {
  store: ConversationDraftsApi | undefined;
  draftKey: string | null | undefined;
  generation: number;
}

/** Whether `origin` still belongs to the session its upload was started in. */
function isOriginCurrent(origin: UploadOrigin): boolean {
  return !origin.store || origin.store.isGenerationCurrent(origin.generation);
}

/**
 * Whether a finished upload's result still has somewhere to go.
 *
 * Issue #769 ("UPLOAD EM BACKGROUND"): once a draft store is wired, it —
 * not the component's own state — is the authority, which is what lets an
 * upload started in conversation X land in X's draft after the reader has
 * switched to Y and the hook instance running the callback has unmounted.
 * `whileUnstored` is that component-scoped answer, asked only when no
 * draft store is wired at all. A result from a session that has since been
 * cleared (issue #929, third review) is wanted by nobody.
 */
function isResultWanted(
  origin: UploadOrigin,
  localId: string,
  whileUnstored: () => boolean,
): boolean {
  if (!isOriginCurrent(origin)) return false;
  const { store, draftKey } = origin;
  if (!store || !draftKey) return whileUnstored();
  return store.getDraft(draftKey)?.attachments.some((item) => item.localId === localId) ?? false;
}

/**
 * Whether a failed upload has anything to report. An abort is the queue's
 * own doing, and a failure belonging to a cleared session must not reach
 * the queue that replaced it (issue #929, third review).
 */
function isFailureReportable(origin: UploadOrigin, cause: unknown): boolean {
  if (cause instanceof DOMException && cause.name === "AbortError") return false;
  return isOriginCurrent(origin);
}

/**
 * Whether the queue already shows exactly these items. Identity per entry,
 * because the draft hands back the very objects it holds: a reconciliation
 * with nothing to change must not re-render (issue #929, second review).
 */
function sameQueue(
  current: readonly AttachmentUploadItem[],
  next: readonly AttachmentUploadItem[],
): boolean {
  return current.length === next.length && current.every((item, index) => item === next[index]);
}

/** So a file added in a fresh mount never reuses a localId a hydrated draft already has. */
function nextSequenceFrom(items: readonly AttachmentUploadItem[]): number {
  let max = 0;
  for (const item of items) {
    const match = /^attachment-(\d+)$/.exec(item.localId);
    if (match) max = Math.max(max, Number(match[1]));
  }
  return max;
}

export function useAttachmentUpload(
  target: AttachmentUploadTarget | null | undefined,
  limits: WorkspaceAttachmentLimits = defaultLimits,
  onUploaded?: () => void,
  /**
   * Issue #769: the conversation's draft, and the key this upload's items
   * belong to. Both optional so every pre-#769 caller (and test) that does
   * not pass them keeps today's behavior — an upload queue scoped purely to
   * this component's own lifetime.
   */
  drafts?: ConversationDraftsApi,
  draftKey?: string | null,
): AttachmentUploadState {
  const [items, setItems] = useState<AttachmentUploadItem[]>(
    () => drafts?.getDraft(draftKey ?? "")?.attachments ?? [],
  );
  const [notice, setNotice] = useState<string | null>(null);
  const itemsRef = useRef(items);
  const mountedRef = useRef(true);
  /**
   * The uploads holding a concurrency slot right now, by the identity of
   * the attempt that took it (issue #929, sixth review). A plain counter
   * could be decremented by a settlement belonging to a session that has
   * ended — handing a slot of *this* session to nobody, or to a third file
   * that then runs past MAX_CONCURRENT. An attempt can only ever remove
   * its own id, and after a session reset there is nothing of it to remove.
   */
  const activeOperationsRef = useRef(new Set<number>());
  const operationSequenceRef = useRef(0);
  const sequenceRef = useRef(nextSequenceFrom(items));
  const startedRef = useRef(new Set<string>());
  const controllersRef = useRef(new Map<string, AbortController>());
  /**
   * The session each queued file was chosen in (issue #929, fifth review).
   * Kept beside the queue rather than inside AttachmentUploadItem: it is
   * operational bookkeeping of this hook, and that item is draft content —
   * it is mirrored into the store, and nothing there should carry it.
   *
   * A file waiting for a free slot when the session ends must not take one
   * afterwards: the upload would start in the *new* session and look
   * perfectly current to every check downstream.
   */
  const itemGenerationsRef = useRef(new Map<string, number>());
  const targetRef = useRef(target);
  const pumpRef = useRef<() => void>(() => undefined);
  const targetKey = target ? `${target.kind}:${target.id}` : "";
  const ownerRef = useRef(targetKey);
  const limitsRef = useRef(limits);
  // Stable across this hook instance's whole life in practice (ChatComposer
  // remounts — new instance, new draftKey — rather than reusing one across a
  // conversation switch), refs only for the same defensive-closure reason
  // targetRef exists.
  const draftsRef = useRef(drafts);
  const draftKeyRef = useRef(draftKey);
  // Every prop a later callback reads is mirrored here, in the layout
  // phase: written after the commit, and before anything can interact with
  // it. Writing them during the render itself is what React's own compiler
  // refuses, and it bought nothing — the readers are all callbacks,
  // promises and effects, none of which run mid-render.
  useLayoutEffect(() => {
    targetRef.current = target;
    limitsRef.current = limits;
    draftsRef.current = drafts;
    draftKeyRef.current = draftKey;
  });

  /**
   * The queue this instance was built from is the draft's, and the draft is
   * this session's — a session that ended took its drafts with it, so
   * anything seeded here was chosen in the one running now (issue #929,
   * sixth review). Without an owner these files would be refused: a file
   * still queued, or one that failed, would never start or be retried
   * again after an ordinary conversation switch.
   *
   * In the layout phase, and once: the pump runs from a passive effect, so
   * every file has its owner before anything can start.
   */
  const ownersSeededRef = useRef(false);
  useLayoutEffect(() => {
    if (ownersSeededRef.current) return;
    ownersSeededRef.current = true;
    const generation = draftsRef.current?.captureGeneration() ?? 0;
    for (const item of itemsRef.current) itemGenerationsRef.current.set(item.localId, generation);
  });

  /**
   * The queue's local state — and, through `replaceItems`, its mirror in the
   * conversation's draft. `replaceLocalItems` is for the one direction the
   * draft never hears about: a change the draft itself made first (issue
   * #929), which must not be echoed back into it.
   */
  const commitItems = useCallback(
    (update: (current: AttachmentUploadItem[]) => AttachmentUploadItem[], mirror: boolean) => {
      if (!mountedRef.current) return;
      setItems((current) => {
        const next = update(current);
        itemsRef.current = next;
        const key = draftKeyRef.current;
        if (mirror && draftsRef.current && key) draftsRef.current.setAttachments(key, next);
        return next;
      });
    },
    [],
  );
  const replaceItems = useCallback(
    (update: (current: AttachmentUploadItem[]) => AttachmentUploadItem[]) =>
      commitItems(update, true),
    [commitItems],
  );
  const replaceLocalItems = useCallback(
    (update: (current: AttachmentUploadItem[]) => AttachmentUploadItem[]) =>
      commitItems(update, false),
    [commitItems],
  );

  /**
   * What a finished upload does with the attachment the server produced.
   *
   * Three ways it can belong to nobody: the session it was started in is
   * over (issue #929, third review), the reader removed the file while it
   * was going up, or — with no draft store wired — this component is gone.
   * All three drop the result and clean up the attachment draft the server
   * created for it, exactly as removing a finished file does.
   */
  /**
   * A progress report from an upload still belonging to its session. The
   * queue this writes to is mirrored straight into the conversation's
   * draft, so a report from a session that has been cleared would put that
   * draft — and its "Rascunho" badge — back (issue #929, fourth review).
   * Every asynchronous writer of this queue passes through `isOriginCurrent`
   * for that reason: the result, the failure, and this.
   */
  const reportProgress = useCallback(
    (origin: UploadOrigin, localId: string, progress: UploadProgress) => {
      if (!isOriginCurrent(origin)) return;
      replaceItems((current) =>
        current.map((entry) => (entry.localId === localId ? { ...entry, progress } : entry)),
      );
    },
    [replaceItems],
  );

  /**
   * Gives back what one upload attempt took, and only that: the slot it
   * holds and the controller it registered — which a newer attempt for the
   * same file may since have replaced (issue #929, sixth review). Runs
   * whatever became of the result, including one discarded with its
   * session, so nothing is left holding a slot in the pump.
   */
  const releaseOperation = useCallback(
    (operationId: number, localId: string, controller: AbortController) => {
      activeOperationsRef.current.delete(operationId);
      if (controllersRef.current.get(localId) === controller) {
        controllersRef.current.delete(localId);
      }
      queueMicrotask(() => pumpRef.current());
    },
    [],
  );

  const runItem = useCallback(
    async (item: AttachmentUploadItem) => {
      const currentTarget = targetRef.current;
      if (!currentTarget) return;
      // Issue #929: the upload's *origin* — the draft it was started for,
      // and the session that draft belonged to — captured before the first
      // await and never re-read. What the result lands in is decided by
      // where the file was dropped, not by whatever this hook instance's
      // refs say by the time the server answers.
      const origin: UploadOrigin = {
        store: draftsRef.current,
        draftKey: draftKeyRef.current,
        generation: draftsRef.current?.captureGeneration() ?? 0,
      };
      const operationId = ++operationSequenceRef.current;
      activeOperationsRef.current.add(operationId);
      const controller = new AbortController();
      controllersRef.current.set(item.localId, controller);
      replaceItems((current) =>
        current.map((entry) =>
          entry.localId === item.localId ? { ...entry, status: "uploading", error: null } : entry,
        ),
      );
      try {
        const limit = limitsRef.current.maxUploadBytes;
        if (limit !== null && item.file.size > limit) {
          throw new AttachmentUploadError("too_large", tooLargeMessage(limit));
        }
        const attachment = await uploadAttachment(
          currentTarget,
          item.file,
          limit,
          controller.signal,
          (progress) => reportProgress(origin, item.localId, progress),
        );
        const wanted = isResultWanted(
          origin,
          item.localId,
          () =>
            mountedRef.current &&
            controllersRef.current.get(item.localId) === controller &&
            itemsRef.current.some((entry) => entry.localId === item.localId),
        );
        if (!wanted) {
          // Nobody is waiting for it: the attachment draft the server
          // created goes the same way one the reader removed does.
          void deleteAttachmentDraft(attachment.id).catch(() => undefined);
          return;
        }
        const { store, draftKey: originDraftKey } = origin;
        if (store && originDraftKey) {
          store.updateAttachment(originDraftKey, item.localId, {
            status: "success",
            progress: null,
            attachment,
          });
        }
        replaceItems((current) =>
          current.map((entry) =>
            entry.localId === item.localId
              ? { ...entry, status: "success", progress: null, attachment }
              : entry,
          ),
        );
        onUploaded?.();
      } catch (cause) {
        if (isFailureReportable(origin, cause)) {
          const { store, draftKey: originDraftKey } = origin;
          const error = failureMessage(cause);
          if (store && originDraftKey) {
            store.updateAttachment(originDraftKey, item.localId, {
              status: "failed",
              progress: null,
              error,
            });
          }
          replaceItems((current) =>
            current.map((entry) =>
              entry.localId === item.localId
                ? { ...entry, status: "failed", progress: null, error }
                : entry,
            ),
          );
        }
      } finally {
        releaseOperation(operationId, item.localId, controller);
      }
    },
    [onUploaded, releaseOperation, replaceItems, reportProgress],
  );

  /**
   * Starts as many queued uploads as the concurrency allows. Held in a ref
   * so the callbacks that ask for it — a selection, a removal, an upload
   * settling — never have to depend on the newest `runItem`, and kept
   * current in the layout phase for the same reason the mirrors above are.
   */
  /** The session a file chosen now belongs to. */
  const currentGeneration = useCallback(() => draftsRef.current?.captureGeneration() ?? 0, []);

  /** Whether this file may still be uploaded: it was chosen in this session. */
  const belongsToThisSession = useCallback((localId: string) => {
    const store = draftsRef.current;
    if (!store) return true;
    return store.isGenerationCurrent(itemGenerationsRef.current.get(localId) ?? 0);
  }, []);

  const pump = useCallback(() => {
    if (!mountedRef.current || !targetRef.current) return;
    while (activeOperationsRef.current.size < MAX_CONCURRENT) {
      const next = itemsRef.current.find(
        (item) => item.status === "queued" && !startedRef.current.has(item.localId),
      );
      if (!next) break;
      startedRef.current.add(next.localId);
      // Defence in depth: a session that ended takes its queue with it
      // (resetForSessionEnd), and a file left over from one never starts.
      if (!belongsToThisSession(next.localId)) continue;
      void runItem(next);
    }
  }, [belongsToThisSession, runItem]);
  useLayoutEffect(() => {
    pumpRef.current = pump;
  });

  const applySelection = useCallback(
    (selected: readonly File[], limits: { maxFiles: number; maxBytes: number }) => {
      // Every file this selection adds belongs to the session it was chosen
      // in, and to no later one (issue #929, fifth review).
      const generation = currentGeneration();
      const existing = new Set(itemsRef.current.map((item) => fileKey(item.file)));
      const additions: AttachmentUploadItem[] = [];
      let total = itemsRef.current.reduce((sum, item) => sum + item.file.size, 0);
      let duplicate = false;
      let unsupported = false;
      let tooMany = false;
      let tooLarge = false;
      for (const file of selected) {
        if (itemsRef.current.length + additions.length >= limits.maxFiles) {
          tooMany = true;
          break;
        }
        const key = fileKey(file);
        if (explicitlyUnsupportedFile(file)) {
          unsupported = true;
          continue;
        }
        if (existing.has(key)) {
          duplicate = true;
          continue;
        }
        if (total + file.size > limits.maxBytes) {
          tooLarge = true;
          continue;
        }
        existing.add(key);
        total += file.size;
        const localId = `attachment-${++sequenceRef.current}`;
        itemGenerationsRef.current.set(localId, generation);
        additions.push({
          localId,
          file,
          status: "queued",
          progress: null,
          error: null,
          attachment: null,
        });
      }
      const messages: string[] = [];
      if (duplicate) messages.push("Arquivos duplicados foram ignorados.");
      if (unsupported) messages.push("HTML, SVG e executáveis não podem ser anexados.");
      if (tooMany)
        messages.push(`Esta conversa permite até ${limits.maxFiles} anexos por mensagem.`);
      if (tooLarge) messages.push("O tamanho total dos anexos excede o limite da conversa.");
      setNotice(messages.length ? messages.join(" ") : null);
      if (additions.length === 0) return;
      replaceItems((current) => [...current, ...additions]);
      queueMicrotask(() => pumpRef.current());
    },
    [currentGeneration, replaceItems],
  );

  const selectFiles = useCallback(
    (selected: Iterable<File>) => {
      if (!targetRef.current) return;
      const files = Array.from(selected);
      if (files.length === 0) return;
      applySelection(files, limitsRef.current);
    },
    [applySelection],
  );

  const remove = useCallback(
    (localId: string) => {
      const completed = itemsRef.current.find((item) => item.localId === localId)?.attachment;
      if (completed) void deleteAttachmentDraft(completed.id).catch(() => undefined);
      controllersRef.current.get(localId)?.abort();
      controllersRef.current.delete(localId);
      startedRef.current.delete(localId);
      itemGenerationsRef.current.delete(localId);
      replaceItems((current) => current.filter((item) => item.localId !== localId));
      queueMicrotask(() => pumpRef.current());
    },
    [replaceItems],
  );

  const retry = useCallback(
    (localId: string) => {
      if (!belongsToThisSession(localId)) return;
      startedRef.current.delete(localId);
      replaceItems((current) =>
        current.map((item) =>
          item.localId === localId
            ? { ...item, status: "queued", progress: null, error: null, attachment: null }
            : item,
        ),
      );
      queueMicrotask(() => pumpRef.current());
    },
    [belongsToThisSession, replaceItems],
  );

  const dismiss = useCallback(() => {
    for (const item of itemsRef.current) {
      if (item.attachment) void deleteAttachmentDraft(item.attachment.id).catch(() => undefined);
    }
    for (const controller of controllersRef.current.values()) controller.abort();
    controllersRef.current.clear();
    startedRef.current.clear();
    itemGenerationsRef.current.clear();
    replaceItems(() => []);
    setNotice(null);
  }, [replaceItems]);

  const forgetPublished = useCallback(
    (publishedLocalIds: readonly string[]) => {
      const published = new Set(publishedLocalIds);
      // Only the consumed items' bookkeeping goes: clearing these maps
      // wholesale would orphan the AbortController of an upload that is
      // still running for a file added after the send started, and runItem
      // checks its own controller is still the registered one before
      // writing a result back.
      for (const localId of published) {
        controllersRef.current.delete(localId);
        startedRef.current.delete(localId);
        itemGenerationsRef.current.delete(localId);
      }
      replaceLocalItems((current) => current.filter((item) => !published.has(item.localId)));
      setNotice(null);
    },
    [replaceLocalItems],
  );

  const resetForSessionEnd = useCallback(() => {
    for (const controller of controllersRef.current.values()) controller.abort();
    controllersRef.current.clear();
    startedRef.current.clear();
    itemGenerationsRef.current.clear();
    // The slots of the session that ended go with it. A settlement of one
    // of those uploads finds its id gone and takes nothing from the
    // session that replaced it.
    activeOperationsRef.current.clear();
    // Local only: the store ended the session itself, and an empty queue
    // written back now would land in the session that replaced it.
    replaceLocalItems(() => []);
    setNotice(null);
  }, [replaceLocalItems]);

  const reconcileWithDraft = useCallback(
    (attachments: readonly AttachmentUploadItem[]) => {
      if (sameQueue(itemsRef.current, attachments)) return;
      const kept = new Set(attachments.map((item) => item.localId));
      for (const item of itemsRef.current) {
        if (kept.has(item.localId)) continue;
        controllersRef.current.delete(item.localId);
        startedRef.current.delete(item.localId);
        itemGenerationsRef.current.delete(item.localId);
      }
      // A file this instance is seeing for the first time came from the
      // authoritative draft of the session running now, and belongs to it
      // (issue #929, sixth review); one it already knows keeps its owner.
      const generation = currentGeneration();
      for (const item of attachments) {
        if (!itemGenerationsRef.current.has(item.localId)) {
          itemGenerationsRef.current.set(item.localId, generation);
        }
      }
      replaceLocalItems(() => [...attachments]);
    },
    [currentGeneration, replaceLocalItems],
  );

  useEffect(() => {
    if (ownerRef.current !== targetKey) {
      ownerRef.current = targetKey;
      dismiss();
    }
  }, [dismiss, targetKey]);

  useEffect(() => {
    itemsRef.current = items;
    pumpRef.current();
  }, [items]);

  useEffect(() => {
    mountedRef.current = true;
    const controllers = controllersRef.current;
    return () => {
      mountedRef.current = false;
      // Issue #769: a draft store means this unmount is very likely just a
      // conversation switch, not "the user is done with these files" — so,
      // unlike the pre-#769 behavior, neither in-flight uploads nor
      // already-uploaded server-side attachment drafts are torn down here.
      // Uploads keep going (runItem's completion write-through targets the
      // draft store directly, by draftKey + localId, not this now-gone
      // component's state) and the finished/queued items stay in the
      // conversation's draft until it is explicitly cleared (send,
      // removal, logout — see dismiss()/clearAllSensitiveDrafts callers).
      if (draftsRef.current && draftKeyRef.current) return;
      for (const controller of controllers.values()) controller.abort();
      controllers.clear();
      for (const item of itemsRef.current) {
        if (item.attachment) void deleteAttachmentDraft(item.attachment.id).catch(() => undefined);
      }
    };
  }, []);

  const busy = items.some((item) => item.status !== "success");
  const failed = items.find((item) => item.status === "failed");
  const single = items.length === 1 ? items[0] : null;
  const status = items.length === 0 ? "idle" : failed ? "failed" : busy ? "uploading" : "success";
  const aggregateProgress = useMemo(() => {
    if (!items.some((item) => item.status === "uploading")) return null;
    const total = items.reduce((sum, item) => sum + item.file.size, 0);
    const loaded = items.reduce(
      (sum, item) =>
        sum + (item.status === "success" ? item.file.size : (item.progress?.loaded ?? 0)),
      0,
    );
    return total > 0 ? { loaded, total } : null;
  }, [items]);

  return {
    items,
    status,
    error: failed?.error ?? null,
    uploadedName: single?.attachment?.filename ?? null,
    uploadedAttachment: single?.attachment ?? null,
    progress: single?.progress ?? null,
    aggregateProgress,
    busy,
    notice,
    selectFile: (file) => selectFiles([file]),
    selectFiles,
    remove,
    retry,
    dismiss,
    forgetPublished,
    resetForSessionEnd,
    reconcileWithDraft,
  };
}
