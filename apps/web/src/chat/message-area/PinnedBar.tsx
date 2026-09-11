/**
 * The pinned-message bar above the timeline (RF-05), moved out of
 * ChatMessageArea (issue #834).
 */

import type { PinnedItem } from "../chatTypes";
import { senderLabel } from "../messageDisplay";

interface PinnedBarProps {
  /**
   * The one pin selectLatestPin chose, not the list. The details panel receives
   * the same object, so "the bar and the panel show the same message" holds by
   * construction instead of by two components agreeing on a rule.
   */
  pin: PinnedItem | null;
  onUnpin: (messageId: string, pin: boolean) => void;
}

export default function PinnedBar({ pin, onUnpin }: PinnedBarProps) {
  if (pin === null) return null;
  return (
    <section className="chat-msg-area__pins" aria-label="Mensagem fixada" data-testid="chat-pins">
      <div className="chat-msg-area__pins-item">
        <span className="material-symbols-outlined" aria-hidden="true">
          keep
        </span>
        <span className="chat-msg-area__pins-text">
          <span className="chat-msg-area__pins-sender">{senderLabel(pin.message)}: </span>
          {pin.message.isRemoved ? "Mensagem removida." : pin.message.bodyText}
        </span>
        <button
          type="button"
          aria-label="Desafixar mensagem"
          onClick={() => onUnpin(pin.message.id, false)}
        >
          <span className="material-symbols-outlined" aria-hidden="true">
            close
          </span>
        </button>
      </div>
    </section>
  );
}
