/**
 * The one floating navigation control (#492, contextual since #880), moved out
 * of ChatMessageArea (issue #834).
 *
 * One button, two destinations: while unread messages are still below the
 * reader it offers the boundary they start at, and once that boundary is
 * behind them it offers the end of the conversation. Never two controls — the
 * reader would have to work out which of them is the one they want, and the
 * answer is always "the nearest thing I have not seen".
 *
 * A real <button>, reachable and Enter/Space-operable for free — no bespoke
 * keyboard handling needed. The accessible name alone carries the count and
 * the destination, so a screen reader announces them as part of the control's
 * name rather than as a live region that would fire on every increment.
 */

import {
  formatPendingCount,
  goToBottomAccessibleName,
  goToFirstUnreadAccessibleName,
} from "../../chatViewportState";
import type { ScrollButtonState } from "../viewport/navigation";

export default function ScrollToBottomButton({
  state,
  onClick,
}: {
  state: ScrollButtonState;
  onClick: () => void;
}) {
  if (!state.visible) return null;
  const toBoundary = state.mode === "first-unread";
  return (
    <button
      type="button"
      className="chat-msg-area__scroll-bottom-btn"
      aria-label={
        toBoundary
          ? goToFirstUnreadAccessibleName(state.count)
          : goToBottomAccessibleName(state.count)
      }
      title={toBoundary ? "Começar pelas novas mensagens" : "Ir para o final"}
      onClick={onClick}
    >
      <span className="material-symbols-outlined" aria-hidden="true">
        arrow_downward
      </span>
      {state.count > 0 && (
        <span className="chat-msg-area__scroll-bottom-badge" aria-hidden="true">
          {formatPendingCount(state.count)}
        </span>
      )}
    </button>
  );
}
