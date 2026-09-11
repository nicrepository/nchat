/**
 * Which message dialog is open, and where choosing in it sends the reader
 * (moved out of ChatMessageArea, issue #834).
 *
 * Both dialogs are "pick a destination for this message", so they share a
 * lifetime and a single place that closes them — the alternative is two pieces
 * of state and four callbacks spread across the page component.
 */

import { useCallback, useState } from "react";

import type { Message } from "../../chatTypes";
import type { ForwardSourceContext } from "../../ForwardMessageDialog";

interface Params {
  kind: "channel" | "dm";
  targetId: string;
  navigate: (path: string, options?: { state?: unknown }) => void;
}

export interface MessageDialogsState {
  /** The message being quoted elsewhere, or null. */
  referenceSource: Message | null;
  /** The message being forwarded, or null. */
  forwardSource: ForwardSourceContext | null;
  openReference: (message: Message) => void;
  closeReference: () => void;
  selectReferenceDestination: (target: { kind: "channel" | "dm"; id: string }) => void;
  openForward: (message: Message) => void;
  closeForward: () => void;
}

export function useMessageDialogs({ kind, targetId, navigate }: Params): MessageDialogsState {
  const [referenceSource, setReferenceSource] = useState<Message | null>(null);
  const [forwardSource, setForwardSource] = useState<ForwardSourceContext | null>(null);

  const selectReferenceDestination = useCallback(
    (target: { kind: "channel" | "dm"; id: string }) => {
      if (!referenceSource) return;
      const sourceID = referenceSource.id;
      setReferenceSource(null);
      navigate(`/chat/${target.kind}/${encodeURIComponent(target.id)}`, {
        state: {
          referencedMessageId: sourceID,
          referenceTargetKind: kind,
          referenceTargetId: targetId,
        },
      });
    },
    [kind, navigate, referenceSource, targetId],
  );

  const openForward = useCallback(
    (message: Message) => {
      if (kind === "channel" && targetId) {
        setForwardSource({ messageID: message.id, sourceChannelID: targetId });
      }
    },
    [kind, targetId],
  );

  return {
    referenceSource,
    forwardSource,
    openReference: setReferenceSource,
    closeReference: useCallback(() => setReferenceSource(null), []),
    selectReferenceDestination,
    openForward,
    closeForward: useCallback(() => setForwardSource(null), []),
  };
}
