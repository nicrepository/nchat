import { useId } from "react";

import { reactionAccessibleDescription } from "./reactionAuthors";
import type { MessageReaction } from "./chatTypes";

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
 * description, and is *shown* by a stylesheet on hover and on keyboard focus.
 * That is deliberate: a tooltip built out of state and listeners would re-render
 * a message on every mouse crossing and would leave a screen reader with
 * nothing, whereas this costs one element, no listener, no state, and reads the
 * same to both.
 *
 * The pop when a reaction lands is a CSS animation on an element keyed by the
 * count, so it replays exactly when the count changes and never when the badge
 * is merely hovered. It animates transform and opacity only — nothing here can
 * move the message above it.
 *
 * A badge that is leaving is drawn until its own animation ends, and while it
 * does it is inert: not clickable, not focusable, and hidden from assistive
 * technology, because a reaction that no longer exists must not be announced as
 * one that does.
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
  return (
    <span
      className={`chat-msg-area__reaction-slot${exiting ? " chat-msg-area__reaction-slot--exiting" : ""}`}
      aria-hidden={exiting || undefined}
      data-exiting={exiting || undefined}
      onAnimationEnd={exiting ? () => onExited?.(reaction.emoji) : undefined}
    >
      <button
        type="button"
        className={`chat-msg-area__reaction${reaction.reactedByMe ? " chat-msg-area__reaction--mine" : ""}`}
        aria-label={`${reaction.reactedByMe ? "Remover" : "Adicionar"} reação ${reaction.emoji}`}
        aria-pressed={reaction.reactedByMe}
        aria-describedby={descriptionId}
        disabled={exiting}
        tabIndex={exiting ? -1 : undefined}
        onClick={() => onToggle(messageId, reaction.emoji)}
      >
        <span key={reaction.count} className="chat-msg-area__reaction-emoji" aria-hidden="true">
          {reaction.emoji}
        </span>{" "}
        {reaction.count}
      </button>
      <span
        id={descriptionId}
        role="tooltip"
        className="chat-msg-area__reaction-authors"
        data-testid="reaction-authors"
      >
        {reactionAccessibleDescription(reaction, currentUserId)}
      </span>
    </span>
  );
}
