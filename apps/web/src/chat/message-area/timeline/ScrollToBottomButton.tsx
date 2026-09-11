/**
 * The floating "Ir para o final" action (#492), moved out of ChatMessageArea
 * (issue #834).
 *
 * A real <button>, reachable and Enter/Space-operable for free — no bespoke
 * keyboard handling needed. The accessible name alone carries the pending count
 * so a screen reader announces it as part of the control's name rather than as
 * a live region that would fire on every increment.
 */

import { formatPendingCount, goToBottomAccessibleName } from "../../chatViewportState";

export default function ScrollToBottomButton({
  visible,
  pendingCount,
  onClick,
}: {
  visible: boolean;
  pendingCount: number;
  onClick: () => void;
}) {
  if (!visible) return null;
  return (
    <button
      type="button"
      className="chat-msg-area__scroll-bottom-btn"
      aria-label={goToBottomAccessibleName(pendingCount)}
      title="Ir para o final"
      onClick={onClick}
    >
      <span className="material-symbols-outlined" aria-hidden="true">
        arrow_downward
      </span>
      {pendingCount > 0 && (
        <span className="chat-msg-area__scroll-bottom-badge" aria-hidden="true">
          {formatPendingCount(pendingCount)}
        </span>
      )}
    </button>
  );
}
