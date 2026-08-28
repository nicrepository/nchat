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

export function useAttachmentUpload(
  target: AttachmentUploadTarget | null | undefined,
  limits: WorkspaceAttachmentLimits = defaultLimits,
  onUploaded?: () => void,
): AttachmentUploadState {
  const [items, setItems] = useState<AttachmentUploadItem[]>([]);
  const [notice, setNotice] = useState<string | null>(null);
  const itemsRef = useRef(items);
  const mountedRef = useRef(true);
  const activeRef = useRef(0);
  const sequenceRef = useRef(0);
  const startedRef = useRef(new Set<string>());
  const controllersRef = useRef(new Map<string, AbortController>());
  const targetRef = useRef(target);
  targetRef.current = target;
  const pumpRef = useRef<() => void>(() => undefined);
  const targetKey = target ? `${target.kind}:${target.id}` : "";
  const ownerRef = useRef(targetKey);
  const limitsRef = useRef(limits);
  limitsRef.current = limits;

  const replaceItems = useCallback(
    (update: (current: AttachmentUploadItem[]) => AttachmentUploadItem[]) => {
      if (!mountedRef.current) return;
      setItems((current) => {
        const next = update(current);
        itemsRef.current = next;
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
        const stillOwned =
          mountedRef.current &&
          controllersRef.current.get(item.localId) === controller &&
          itemsRef.current.some((entry) => entry.localId === item.localId);
        if (!stillOwned) {
          void deleteAttachmentDraft(attachment.id).catch(() => undefined);
          return;
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
      for (const controller of controllers.values()) controller.abort();
      controllers.clear();
      for (const item of itemsRef.current) {
        if (item.attachment) void deleteAttachmentDraft(item.attachment.id).catch(() => undefined);
      }
      mountedRef.current = false;
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
