import { useEffect, useMemo, useReducer } from "react";

import type { Message } from "../chatTypes";
import { fetchConversationAttachments } from "../filesApi";
import { isPreviewWorkPending } from "../useAttachmentPreview";
import { previewReconcileDelayMs, previewReconcileMaxAttempts } from "../useConversationDetails";
import { initialPreviewReconcile, previewReconcileReducer } from "./previewReconcileWindow";
import { isAbortError } from "./useRequestRegistry";
import { conversationKey, type Action, type ConversationTarget } from "./types";

/** How many of a destination's most recent attachments one reconciliation poll reads back. */
const messagePreviewReconcileLimit = 20;

/** Everything about the attachments on screen that a finished preview would change. */
function previewProgressOf(messages: Message[]): string {
  return messages
    .flatMap((message) => message.attachments ?? [])
    .map((attachment) => `${attachment.id}:${attachment.status}:${attachment.previewStatus}`)
    .join("|");
}

interface Options {
  target: ConversationTarget;
  messages: Message[];
  dispatch: (action: Action) => void;
}

/**
 * Preview reconciliation for inline attachments (RF-31/#464).
 *
 * A message posts with previewStatus "pending" the instant its upload
 * finishes — the malware scan and the render both still have to run. There
 * is no socket event for "the preview finished" (attachment.status fires on
 * the scan verdict, before the render even starts), so without this the card
 * sits on its icon fallback until the thread is reloaded.
 *
 * Bounded and backed off exactly like the details panel's own
 * reconciliation: the render is normally done within the worker's ~10s
 * poll, so the delay starts there and doubles up to a ceiling, and the
 * window ends after previewReconcileMaxAttempts unchanged polls — giving up
 * costs nothing but a late thumbnail, and one reload opens a fresh window.
 */
export function usePreviewReconciliation({ target, messages, dispatch }: Options): void {
  const [reconcile, dispatchReconcile] = useReducer(
    previewReconcileReducer,
    initialPreviewReconcile,
  );

  const progressKey = useMemo(() => previewProgressOf(messages), [messages]);
  const awaitingPreview = useMemo(
    () => messages.some((message) => message.attachments?.some(isPreviewWorkPending)),
    [messages],
  );
  const targetKey = conversationKey(target);
  const sameWindow = reconcile.target === targetKey && reconcile.progressKey === progressKey;
  const attempt = sameWindow ? reconcile.attempt : 0;
  const active = awaitingPreview && attempt < previewReconcileMaxAttempts;
  const { kind, targetId } = target;
  const round = reconcile.round;

  useEffect(() => {
    if (!active) return;
    const onVisibilityChange = () => dispatchReconcile({ type: "resumed" });
    document.addEventListener("visibilitychange", onVisibilityChange);
    return () => document.removeEventListener("visibilitychange", onVisibilityChange);
  }, [active]);

  useEffect(() => {
    if (!active) return;
    if (document.visibilityState === "hidden") return;

    const controller = new AbortController();
    const polled = () => dispatchReconcile({ type: "polled", target: targetKey, progressKey });
    const timer = window.setTimeout(() => {
      fetchConversationAttachments(
        { kind, id: targetId },
        messagePreviewReconcileLimit,
        controller.signal,
      ).then(
        (attachments) => {
          if (controller.signal.aborted) return;
          dispatch({ type: "attachments_reconciled", attachments });
          polled();
        },
        (error: unknown) => {
          if (controller.signal.aborted || isAbortError(error)) return;
          // A transient failure leaves the timeline exactly as it is and
          // costs only the next backoff step, never an immediate retry.
          polled();
        },
      );
    }, previewReconcileDelayMs(attempt));

    return () => {
      window.clearTimeout(timer);
      controller.abort();
    };
  }, [active, attempt, dispatch, kind, progressKey, round, targetId, targetKey]);
}
