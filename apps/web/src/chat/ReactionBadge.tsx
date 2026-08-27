import { useId, useLayoutEffect, useRef, useState, type RefObject } from "react";
import { createPortal } from "react-dom";

import { anchorIsVisible, placeAgainstAnchor, type VisibleBounds } from "./emoji/useAnchoredPicker";
import { reactionAccessibleDescription } from "./reactionAuthors";
import type { MessageReaction } from "./chatTypes";

/** Distance kept between the badge and the names floating above it. */
const authorsGap = 6;

/**
 * Where the badge can actually be seen.
 *
 * The message list clips vertically, so the window alone is the wrong answer: a
 * badge scrolled past the list's edge is invisible even while the window still
 * has room for it. The list is the one clipping ancestor a reaction badge ever
 * has — it is the only place they are rendered — so this asks for it by name
 * rather than walking the tree looking for scroll parents. Without it (a badge
 * somewhere else one day) the window is the boundary, which is still an
 * improvement on none.
 */
function visibleBounds(anchor: Element): VisibleBounds {
  const viewport = { top: 0, bottom: window.innerHeight, left: 0, right: window.innerWidth };
  const clip = anchor.closest(".chat-msg-area__list")?.getBoundingClientRect();
  if (!clip) return viewport;
  return {
    top: Math.max(viewport.top, clip.top),
    bottom: Math.min(viewport.bottom, clip.bottom),
    left: Math.max(viewport.left, clip.left),
    right: Math.min(viewport.right, clip.right),
  };
}

/**
 * Who reacted, floating above the badge while it is hovered or focused.
 *
 * Owns everything about being a floating layer: the portal, the placement, the
 * listeners that keep it against its badge, and whether that badge is on screen
 * at all — all of which live exactly as long as it does. The badge above only
 * decides whether the reader is asking for it.
 *
 * Portalled rather than drawn inside the badge, because this tooltip must never
 * be able to make the conversation scroll: an absolutely positioned box counts
 * towards the scrollable overflow of its scroll container, and the message list
 * is one. Placement is the emoji picker's own, so the tooltip is clamped inside
 * the viewport by the same rules and stays whole against either edge.
 *
 * Decorative: the same text is on the button as its accessible description at
 * all times, so a screen reader never depends on a pointer being anywhere.
 */
function ReactionAuthors({
  anchorRef,
  children,
}: {
  anchorRef: RefObject<HTMLElement | null>;
  children: string;
}) {
  const tooltipRef = useRef<HTMLSpanElement>(null);

  // Read layout and write coordinates in the same commit, so the tooltip is
  // never painted once at the top-left of the viewport before being placed.
  useLayoutEffect(() => {
    const place = () => {
      const tooltip = tooltipRef.current;
      const badge = anchorRef.current;
      if (!tooltip || !badge) return;
      // Read once, and reuse: this runs on every scroll frame.
      const anchor = badge.getBoundingClientRect();
      if (!anchorIsVisible(anchor, visibleBounds(badge))) {
        // Nothing to point at, so nothing is drawn — and no placement is
        // computed for a position no one would see. placeAgainstAnchor turns
        // this back on the moment the badge returns.
        tooltip.style.visibility = "hidden";
        return;
      }
      const box = tooltip.getBoundingClientRect();
      const centred = anchor.left + anchor.width / 2 - box.width / 2;
      placeAgainstAnchor(tooltip, anchor, box, centred, authorsGap, authorsGap);
    };
    place();

    // The tooltip is fixed, so it does not travel with the badge on its own: a
    // conversation scrolled under an open tooltip would leave it behind. Capture
    // is what makes one listener enough — scroll does not bubble, but it is
    // observable on the way down, so this sees the message list scrolling
    // without having to go looking for scroll parents.
    //
    // Registered only while a tooltip is open, which is at most one at a time —
    // an idle badge costs nothing.
    window.addEventListener("scroll", place, true);
    window.addEventListener("resize", place);
    return () => {
      window.removeEventListener("scroll", place, true);
      window.removeEventListener("resize", place);
    };
  }, [anchorRef, children]);

  return createPortal(
    <span
      ref={tooltipRef}
      // Portalled out of the conversation, so it carries the chat scope with it
      // — see the emoji picker's own container for why the theme class travels.
      className="chat-theme chat-msg-area__reaction-authors"
      aria-hidden="true"
      data-testid="reaction-authors"
      style={{ visibility: "hidden" }}
    >
      {children}
    </span>,
    document.body,
  );
}

/**
 * The reaction itself: the emoji, how many, and whether the reader is among
 * them. Inert while the badge it belongs to is leaving — a reaction that no
 * longer exists must not be clickable, focusable, or announced as one that does.
 *
 * The pop when a reaction lands is a CSS animation on an element keyed by the
 * count, so it replays exactly when the count changes and never when the badge
 * is merely hovered.
 */
function ReactionButton({
  reaction,
  descriptionId,
  inert,
  onClick,
}: {
  reaction: MessageReaction;
  descriptionId: string;
  inert: boolean;
  onClick: () => void;
}) {
  const mine = reaction.reactedByMe;
  return (
    <button
      type="button"
      className={`chat-msg-area__reaction${mine ? " chat-msg-area__reaction--mine" : ""}`}
      aria-label={`${mine ? "Remover" : "Adicionar"} reação ${reaction.emoji}`}
      aria-pressed={mine}
      aria-describedby={descriptionId}
      disabled={inert}
      tabIndex={inert ? -1 : undefined}
      onClick={onClick}
    >
      <span key={reaction.count} className="chat-msg-area__reaction-emoji" aria-hidden="true">
        {reaction.emoji}
      </span>{" "}
      {reaction.count}
    </button>
  );
}

export interface ReactionBadgeProps {
  messageId: string;
  reaction: MessageReaction;
  /** The reader, so the tooltip can say "Você" instead of repeating their name. */
  currentUserId: string;
  onToggle: (messageId: string, emoji: string) => void;
  /** True while this badge is animating out after its reaction was removed. */
  exiting?: boolean;
  /** Called when the exit animation finishes and the badge may be unmounted. */
  onExited?: (emoji: string) => void;
}

/**
 * One reaction under a message: the emoji, how many, whether the reader is
 * among them, and who (issue #496).
 *
 * Who reacted is present in the DOM at all times as the button's accessible
 * description, so a screen reader reads it without a pointer and without a
 * request. The *visible* tooltip is a separate, decorative layer shown only
 * while the badge is hovered or focused — one badge's worth of state, never the
 * message's, so a mouse crossing the row re-renders nothing above this badge.
 *
 * A badge that is leaving is drawn until its own animation ends, and while it
 * does it is inert — see ReactionButton.
 */
export default function ReactionBadge({
  messageId,
  reaction,
  currentUserId,
  onToggle,
  exiting = false,
  onExited,
}: ReactionBadgeProps) {
  const descriptionId = useId();
  const slotRef = useRef<HTMLSpanElement>(null);
  // Two channels, two states. One flag would let either one close what the other
  // opened: moving the mouse away from a badge the keyboard is still on, or
  // tabbing away from a badge the pointer is still over, would both hide names
  // the reader is still asking for.
  const [hovered, setHovered] = useState(false);
  const [focused, setFocused] = useState(false);
  const description = reactionAccessibleDescription(reaction, currentUserId);
  // A badge on its way out is inert, so it answers neither channel.
  const authorsVisible = (hovered || focused) && !exiting;
  return (
    <span
      ref={slotRef}
      className={`chat-msg-area__reaction-slot${exiting ? " chat-msg-area__reaction-slot--exiting" : ""}`}
      aria-hidden={exiting || undefined}
      data-exiting={exiting || undefined}
      onAnimationEnd={exiting ? () => onExited?.(reaction.emoji) : undefined}
      onMouseEnter={() => setHovered(true)}
      onMouseLeave={() => setHovered(false)}
      // React's focus events bubble, so focusing the button inside is enough:
      // the tooltip answers the keyboard exactly as it answers the pointer.
      onFocus={() => setFocused(true)}
      onBlur={() => setFocused(false)}
    >
      <ReactionButton
        reaction={reaction}
        descriptionId={descriptionId}
        inert={exiting}
        onClick={() => onToggle(messageId, reaction.emoji)}
      />
      <span id={descriptionId} className="sr-only">
        {description}
      </span>
      {authorsVisible && <ReactionAuthors anchorRef={slotRef}>{description}</ReactionAuthors>}
    </span>
  );
}
