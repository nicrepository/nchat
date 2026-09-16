import { useCallback, useEffect, useMemo, useRef, useState } from "react";

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
  resetAfterPublish: () => void;
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
  const activeRef = useRef(0);
  const sequenceRef = useRef(nextSequenceFrom(items));
  const startedRef = useRef(new Set<string>());
  const controllersRef = useRef(new Map<string, AbortController>());
  const targetRef = useRef(target);
  targetRef.current = target;
  const pumpRef = useRef<() => void>(() => undefined);
  const targetKey = target ? `${target.kind}:${target.id}` : "";
  const ownerRef = useRef(targetKey);
  const limitsRef = useRef(limits);
  limitsRef.current = limits;
  // Stable across this hook instance's whole life in practice (ChatComposer
  // remounts — new instance, new draftKey — rather than reusing one across a
  // conversation switch), refs only for the same defensive-closure reason
  // targetRef exists.
  const draftsRef = useRef(drafts);
  const draftKeyRef = useRef(draftKey);
  useEffect(() => {
    draftsRef.current = drafts;
    draftKeyRef.current = draftKey;
  });

  const replaceItems = useCallback(
    (update: (current: AttachmentUploadItem[]) => AttachmentUploadItem[]) => {
      if (!mountedRef.current) return;
      setItems((current) => {
        const next = update(current);
        itemsRef.current = next;
        const key = draftKeyRef.current;
        if (draftsRef.current && key) draftsRef.current.setAttachments(key, next);
        return next;
      });
    },
    [],
  );

  const runItem = useCallback(
    async (item: AttachmentUploadItem) => {
      const currentTarget = targetRef.current;
      if (!currentTarget) return;
      activeRef.current += 1;
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
          (progress) =>
            replaceItems((current) =>
              current.map((entry) =>
                entry.localId === item.localId ? { ...entry, progress } : entry,
              ),
            ),
        );
        // Issue #769 ("UPLOAD EM BACKGROUND"): once a draft store is wired,
        // it — not this component's own mountedRef/itemsRef — is the
        // authority on whether the attachment this upload just produced is
        // still wanted. That is what lets an upload started in conversation
        // X keep going, and land correctly in X's draft, after the reader
        // has already switched to Y and this ChatComposer instance (and the
        // hook instance running this very callback) has unmounted.
        const originDraftKey = draftKeyRef.current;
        const store = draftsRef.current;
        const stillWanted =
          store && originDraftKey
            ? (store
                .getDraft(originDraftKey)
                ?.attachments.some((entry) => entry.localId === item.localId) ?? false)
            : mountedRef.current &&
              controllersRef.current.get(item.localId) === controller &&
              itemsRef.current.some((entry) => entry.localId === item.localId);
        if (!stillWanted) {
          void deleteAttachmentDraft(attachment.id).catch(() => undefined);
          return;
        }
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
        if (!(cause instanceof DOMException && cause.name === "AbortError")) {
          const originDraftKey = draftKeyRef.current;
          const store = draftsRef.current;
          if (store && originDraftKey) {
            store.updateAttachment(originDraftKey, item.localId, {
              status: "failed",
              progress: null,
              error: failureMessage(cause),
            });
          }
          replaceItems((current) =>
            current.map((entry) =>
              entry.localId === item.localId
                ? { ...entry, status: "failed", progress: null, error: failureMessage(cause) }
                : entry,
            ),
          );
        }
      } finally {
        controllersRef.current.delete(item.localId);
        activeRef.current -= 1;
        queueMicrotask(() => pumpRef.current());
      }
    },
    [onUploaded, replaceItems],
  );

  pumpRef.current = () => {
    if (!mountedRef.current || !targetRef.current) return;
    while (activeRef.current < MAX_CONCURRENT) {
      const next = itemsRef.current.find(
        (item) => item.status === "queued" && !startedRef.current.has(item.localId),
      );
      if (!next) break;
      startedRef.current.add(next.localId);
      void runItem(next);
    }
  };

  const applySelection = useCallback(
    (selected: readonly File[], limits: { maxFiles: number; maxBytes: number }) => {
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
        additions.push({
          localId: `attachment-${++sequenceRef.current}`,
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
    [replaceItems],
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
      replaceItems((current) => current.filter((item) => item.localId !== localId));
      queueMicrotask(() => pumpRef.current());
    },
    [replaceItems],
  );

  const retry = useCallback(
    (localId: string) => {
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
    [replaceItems],
  );

  const dismiss = useCallback(() => {
    for (const item of itemsRef.current) {
      if (item.attachment) void deleteAttachmentDraft(item.attachment.id).catch(() => undefined);
    }
    for (const controller of controllersRef.current.values()) controller.abort();
    controllersRef.current.clear();
    startedRef.current.clear();
    replaceItems(() => []);
    setNotice(null);
  }, [replaceItems]);

  const resetAfterPublish = useCallback(() => {
    controllersRef.current.clear();
    startedRef.current.clear();
    replaceItems(() => []);
    setNotice(null);
  }, [replaceItems]);

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
    resetAfterPublish,
  };
}
