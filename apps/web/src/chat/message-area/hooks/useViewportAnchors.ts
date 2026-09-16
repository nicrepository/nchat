/**
 * Where each conversation was left, for the life of this SPA tab (#492 — moved
 * out of ChatMessageArea, issue #834).
 *
 * Owned here rather than inside the timeline, which unmounts on every
 * conversation switch (the timeline swaps itself for the loading skeleton while
 * useMessages is "loading"). This hook's owner does not unmount, so this is
 * where anchors survive.
 */

import { useCallback, useMemo, useState } from "react";

import type { Channel, DMConversation } from "../../chatTypes";
import {
  loadViewportAnchor,
  saveViewportAnchor,
  type ViewportAnchor,
} from "../../chatViewportPersistence";

interface Params {
  kind: "channel" | "dm";
  targetId: string;
  currentUserId: string;
  channels: Channel[];
  dms: DMConversation[];
  /** Marks the conversation read; called only on a confirmed arrival at the tail. */
  markRead?: (target: { kind: "channel" | "dm"; targetId: string }) => void;
}

export interface ViewportAnchorsState {
  /** `${kind}:${targetId}`, or "" before a target is resolved. */
  conversationKey: string;
  /** The sidebar's unread_count for this target as of opening it. */
  unreadCountAtOpen: number;
  /** The anchor to open at: this tab's own cache first, then sessionStorage. */
  initialAnchor: ViewportAnchor | null;
  onCaptureAnchor: (key: string, anchor: ViewportAnchor) => void;
  onReachedBottom: () => void;
}

export function useViewportAnchors({
  kind,
  targetId,
  currentUserId,
  channels,
  dms,
  markRead,
}: Params): ViewportAnchorsState {
  // A Map held in useState (its setter never called) rather than useRef:
  // react-hooks/refs forbids reading a ref's .current during render — even a
  // ref object merely passed down as a prop and read in the receiving
  // component — and the timeline's render-time resolution genuinely needs this
  // value synchronously. A plain object identity that happens to be stable
  // across renders (never reassigned, only mutated via .set()) is not a ref and
  // carries none of that rule's tearing concerns: nothing here ever depends on
  // React noticing a mutation to it.
  const [viewportAnchors] = useState(() => new Map<string, ViewportAnchor>());
  const conversationKey = targetId ? `${kind}:${targetId}` : "";

  const unreadCountAtOpen = useMemo(() => {
    if (!targetId) return 0;
    const list = kind === "channel" ? channels : dms;
    return list.find((item) => item.id === targetId)?.unreadCount ?? 0;
  }, [kind, targetId, channels, dms]);

  // Plain function calls, safe during render.
  const initialAnchor =
    (conversationKey ? viewportAnchors.get(conversationKey) : undefined) ??
    (conversationKey && currentUserId ? loadViewportAnchor(currentUserId, kind, targetId) : null) ??
    null;

  const onCaptureAnchor = useCallback(
    (key: string, anchor: ViewportAnchor) => {
      viewportAnchors.set(key, anchor);
      if (currentUserId) saveViewportAnchor(currentUserId, kind, targetId, anchor);
    },
    [viewportAnchors, currentUserId, kind, targetId],
  );

  const onReachedBottom = useCallback(() => {
    if (!targetId) return;
    markRead?.({ kind, targetId });
  }, [markRead, kind, targetId]);

  return { conversationKey, unreadCountAtOpen, initialAnchor, onCaptureAnchor, onReachedBottom };
}
