/**
 * useAttachmentUpload — the composer's one attachment pipeline (RF-32, #458).
 *
 * There is a single entry point, `selectFile`, and both ways of choosing a file
 * go through it: the toolbar's file input and a drop on the composer. Limit
 * lookup, size validation, the POST, error normalisation and the busy state all
 * live here once, so the picker and the drop cannot drift apart or validate
 * differently.
 *
 * The hook is destination-agnostic: it takes the target the composer is already
 * bound to, so a channel and a DM use the same code and differ only in the
 * route `uploadAttachment` derives from `target.kind`.
 *
 * Security: nothing here is a control. file-service authenticates, authorises
 * the destination, re-reads the workspace's policy and counts the bytes it
 * actually receives; the size check below only spares the user an upload that
 * cannot succeed, and its verdict is always superseded by the service's.
 */

import { useCallback, useEffect, useRef, useState } from "react";

import { fetchWorkspaceUploadLimit } from "./chatApi";
import { AttachmentUploadError, tooLargeMessage, uploadAttachment } from "./filesApi";
import type { ChannelAttachment } from "./chatTypes";

export interface AttachmentUploadTarget {
  kind: "channel" | "dm";
  id: string;
}

export type AttachmentUploadStatus = "idle" | "uploading" | "failed" | "success";

export interface AttachmentUploadState {
  status: AttachmentUploadStatus;
  /** User-facing message for a failure. Never server text. */
  error: string | null;
  /** Name of the last successfully uploaded file, for the success notice. */
  uploadedName: string | null;
  selectFile: (file: File) => void;
  /** Clears a finished outcome so the composer returns to its resting state. */
  dismiss: () => void;
}

/**
 * @param target      destination the composer is bound to.
 * @param onUploaded  called after a successful upload so the caller can
 *                    reconcile whatever renders the destination's files.
 */
export function useAttachmentUpload(
  target: AttachmentUploadTarget | null,
  onUploaded?: () => void,
): AttachmentUploadState {
  const [status, setStatus] = useState<AttachmentUploadStatus>("idle");
  const [error, setError] = useState<string | null>(null);
  const [uploadedName, setUploadedName] = useState<string | null>(null);

  // Guards every state write, so an upload that resolves after the composer
  // unmounted (target switch, navigation) dispatches nothing.
  const mountedRef = useRef(true);
  // Distinct from `status`: it is read synchronously inside selectFile, which
  // is what actually stops a second drop landing before React re-renders.
  const busyRef = useRef(false);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
    };
  }, []);

  const dismiss = useCallback(() => {
    if (!mountedRef.current) return;
    setStatus("idle");
    setError(null);
    setUploadedName(null);
  }, []);

  /**
   * Reads the workspace's current limit.
   *
   * Deliberately not cached — not for the life of the composer, not behind a
   * TTL, not in a module-level map. An administrator can change the policy at
   * any moment, and a composer that has been open since before the change would
   * otherwise validate against a limit that no longer exists: refusing files the
   * workspace now accepts, or promising a size the service will reject. One read
   * per attempt is one request against an action the user takes rarely and that
   * is about to move far more bytes than the lookup costs.
   *
   * The workspace is never passed in: fetchWorkspaceUploadLimit resolves it from
   * the caller's own session, so the value always belongs to the workspace the
   * session is in and a stale identifier cannot be carried in a closure.
   *
   * A failure resolves to null — "unknown" — rather than to a compiled-in
   * default: substituting one would replace the administrator's policy with a
   * number this client invented. Unknown means the pre-flight check is skipped
   * and file-service decides, which is the safe direction: enforcement is never
   * weakened, only the local courtesy check is lost.
   */
  const resolveLimit = useCallback(async (): Promise<number | null> => {
    try {
      return await fetchWorkspaceUploadLimit();
    } catch {
      return null;
    }
  }, []);

  const selectFile = useCallback(
    (file: File) => {
      if (!target || busyRef.current) return;
      busyRef.current = true;
      setStatus("uploading");
      // A new attempt clears the previous outcome, so a stale error is never
      // shown next to a running upload.
      setError(null);
      setUploadedName(null);

      void (async () => {
        try {
          // Read on every attempt, so a policy changed between two tries is the
          // one the second try is judged against.
          const limit = await resolveLimit();
          if (limit !== null && file.size > limit) {
            // Rejected locally: no request is made at all.
            throw new AttachmentUploadError("too_large", tooLargeMessage(limit));
          }
          const attachment: ChannelAttachment = await uploadAttachment(target, file, limit);
          if (!mountedRef.current) return;
          setStatus("success");
          setUploadedName(attachment.filename || file.name);
          onUploaded?.();
        } catch (cause) {
          if (!mountedRef.current) return;
          setStatus("failed");
          setError(
            cause instanceof AttachmentUploadError
              ? cause.message
              : "Não foi possível enviar o arquivo.",
          );
        } finally {
          // Released on every path, so a failure always leaves the composer
          // able to accept the same file again.
          busyRef.current = false;
        }
      })();
    },
    [onUploaded, resolveLimit, target],
  );

  return { status, error, uploadedName, selectFile, dismiss };
}
