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

import type { UploadProgress } from "../lib/api";
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
  /**
   * The persisted attachment the last successful upload produced (RF-32).
   *
   * It is kept, not discarded, because the upload and the message are two
   * separate acts: the bytes are already on the server, and this is the
   * reference the composer sends when the user finally presses Enviar. Nothing
   * is re-uploaded at that point.
   *
   * Null in every other state, including after `dismiss`, so the composer's
   * pending attachment and this value are the same fact.
   */
  uploadedAttachment: ChannelAttachment | null;
  /**
   * Bytes actually sent, as the transport reports them, or null when it has
   * reported none yet — which is also what a body of unknown length looks like.
   *
   * Null is the instruction to show an indeterminate state. It is never
   * substituted with a guess, a timer or a percentage derived from the file's
   * own size: those would describe a transfer nothing observed.
   */
  progress: UploadProgress | null;
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
  const [uploadedAttachment, setUploadedAttachment] = useState<ChannelAttachment | null>(null);
  const [progress, setProgress] = useState<UploadProgress | null>(null);

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
    setUploadedAttachment(null);
    setProgress(null);
  }, []);

  /**
   * A finished upload belongs to the destination it was uploaded to, and to no
   * other. Switching channel, opening a DM, or navigating away therefore drops
   * it: the server would refuse the link anyway — it re-reads the attachment's
   * own destination — but a stale pending file must never even appear in the
   * new conversation's composer, which would suggest it is about to be sent
   * there.
   *
   * Keyed on kind *and* id, so the two ways a destination can change are one
   * rule. An in-flight upload is not cancelled here; its own mountedRef and
   * target guards already stop it from writing into the new destination's
   * state.
   *
   * Adjusted during render rather than from an effect — React's documented
   * pattern for state that must follow a prop — so no frame is ever painted in
   * which the new destination's composer shows the previous one's file.
   */
  const targetKey = target ? `${target.kind}:${target.id}` : "";
  const [stateOwner, setStateOwner] = useState(targetKey);
  if (stateOwner !== targetKey) {
    setStateOwner(targetKey);
    setStatus("idle");
    setError(null);
    setUploadedName(null);
    setUploadedAttachment(null);
    setProgress(null);
  }

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
      // A new attempt also replaces the pending attachment: the composer holds
      // one at a time, and the previous one stops being the file about to be
      // sent the moment another is chosen.
      setUploadedAttachment(null);
      // A fresh attempt starts unmeasured: the previous attempt's byte counts
      // describe a request that is over, and the limit lookup below runs before
      // a single byte of this one is sent.
      setProgress(null);

      void (async () => {
        try {
          // Read on every attempt, so a policy changed between two tries is the
          // one the second try is judged against.
          const limit = await resolveLimit();
          if (limit !== null && file.size > limit) {
            // Rejected locally: no request is made at all.
            throw new AttachmentUploadError("too_large", tooLargeMessage(limit));
          }
          const attachment: ChannelAttachment = await uploadAttachment(
            target,
            file,
            limit,
            undefined,
            (sent) => {
              // Guarded like every other write here: progress keeps arriving
              // for a moment after the composer unmounts.
              if (mountedRef.current) setProgress(sent);
            },
          );
          if (!mountedRef.current) return;
          setStatus("success");
          setUploadedName(attachment.filename || file.name);
          setUploadedAttachment(attachment);
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
          // The bar belongs to a request in flight; neither outcome has one.
          if (mountedRef.current) setProgress(null);
        }
      })();
    },
    [onUploaded, resolveLimit, target],
  );

  return { status, error, uploadedName, uploadedAttachment, progress, selectFile, dismiss };
}
