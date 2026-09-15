/**
 * The composer's priority selector (issue #822).
 *
 * A popover anchored to the toolbar button on desktop and a bottom sheet on a
 * small viewport — the same component, the same state and the same options
 * either way. The two are a class apart and nothing else, because "the sheet
 * and the popover disagreed about what Aplicar does" is a bug that can only
 * exist when there are two of them.
 *
 * The shell is the modal language the rest of this app already speaks (see
 * LeaveConversationDialog): a portal, a backdrop, role="dialog", aria-modal, a
 * Tab trap and Escape. It is deliberately not the emoji picker's non-modal
 * anchored surface — this one edits a value and has a Cancelar, so a click that
 * lands elsewhere has to mean something definite rather than "focus moved".
 *
 * ## Transactional
 *
 * Opening mounts this component, and the draft is seeded from the applied
 * intent at that moment. Every control edits the draft alone; Cancelar, Escape
 * and a click outside all discard it untouched, and only Aplicar hands a value
 * back. There is no effect syncing a draft to a prop, which is what makes
 * "selected Urgente, ticked both options, pressed Cancelar and it was sent
 * urgent anyway" unreachable rather than merely untested.
 */

import { useLayoutEffect, useRef, useState, type KeyboardEvent, type RefObject } from "react";
import { createPortal } from "react-dom";

import "./MessagePriorityDialog.css";
import { placeAgainstAnchor } from "./emoji/useAnchoredPicker";
import { useMediaQuery } from "./useMediaQuery";
import type { MessagePriority } from "./chatTypes";
import {
  allowsAttentionOptions,
  normalizePriorityIntent,
  priorityLabels,
  type MessagePriorityIntent,
} from "./messagePriority";

const titleId = "composer-priority-title";
const persistentHintId = "composer-priority-persistent-hint";

/**
 * Below this the popover becomes a sheet. The same breakpoint the navigation
 * drawer uses, evaluated by matchMedia through the app's own hook — no width is
 * measured here, and nothing re-renders on resize that the query did not change.
 */
const sheetQuery = "(max-width: 1023.98px)";

/** Distance kept between the popover and the button it belongs to. */
const anchorGap = 7;

const orderedPriorities: MessagePriority[] = ["standard", "important", "urgent"];

/**
 * Places the panel beside its trigger, and reports whether it managed to.
 *
 * A failure is not hidden: placeAgainstAnchor leaves an unplaceable surface
 * invisible, which is right for a picker that can simply close and wrong for a
 * dialog the reader just opened. So the inline placement is dropped and the
 * panel falls back to the sheet layout, which needs no room beside anything.
 */
function useAnchoredPlacement(
  compact: boolean,
  anchorRef: RefObject<HTMLElement | null>,
  panelRef: RefObject<HTMLDivElement | null>,
): boolean {
  const [anchored, setAnchored] = useState(!compact);

  useLayoutEffect(() => {
    const panel = panelRef.current;
    const anchor = anchorRef.current;
    if (compact || !panel || !anchor) {
      setAnchored(false);
      return;
    }
    const box = anchor.getBoundingClientRect();
    const placed = placeAgainstAnchor(
      panel,
      box,
      panel.getBoundingClientRect(),
      box.left,
      anchorGap,
      anchorGap,
    );
    if (!placed) panel.removeAttribute("style");
    setAnchored(placed);
  }, [anchorRef, compact, panelRef]);

  return anchored;
}

/** Keeps Tab inside the dialog, the way every other modal in this app does. */
function trapTab(event: KeyboardEvent<HTMLDivElement>, panel: HTMLDivElement | null): void {
  const focusable = panel?.querySelectorAll<HTMLElement>(
    "input:not(:disabled), button:not(:disabled)",
  );
  if (!focusable?.length) return;
  const first = focusable[0];
  const last = focusable[focusable.length - 1];
  if (event.shiftKey && document.activeElement === first) {
    event.preventDefault();
    last.focus();
  } else if (!event.shiftKey && document.activeElement === last) {
    event.preventDefault();
    first.focus();
  }
}

/**
 * The two options Urgente unlocks.
 *
 * Rendered only where the policy allows them rather than rendered disabled: a
 * control the first version never grants is noise beside the priority it does
 * not belong to. Hiding them is presentation, never enforcement — the value
 * itself is cleared by normalizePriorityIntent, and the server refuses the
 * combination regardless of what this draws.
 */
function UrgentOptions({
  draft,
  onChange,
}: {
  draft: MessagePriorityIntent;
  onChange: (patch: Partial<MessagePriorityIntent>) => void;
}) {
  return (
    <div className="msg-priority__options">
      <label className="msg-priority__option">
        <input
          type="checkbox"
          checked={draft.acknowledgementRequired}
          data-testid="priority-acknowledgement"
          onChange={(event) => onChange({ acknowledgementRequired: event.target.checked })}
        />
        <span>Solicitar confirmação</span>
      </label>
      <label className="msg-priority__option">
        <input
          type="checkbox"
          checked={draft.persistentNotifications}
          aria-describedby={persistentHintId}
          data-testid="priority-persistent"
          onChange={(event) => onChange({ persistentNotifications: event.target.checked })}
        />
        <span>Notificações persistentes</span>
      </label>
      <p id={persistentHintId} className="msg-priority__hint">
        lembretes a cada 5 minutos até confirmação ou resposta
      </p>
    </div>
  );
}

export interface MessagePriorityDialogProps {
  /** The applied intent this draft starts from. */
  intent: MessagePriorityIntent;
  /** The trigger the popover hangs off, and where focus returns. */
  anchorRef: RefObject<HTMLElement | null>;
  /** Escape, Cancelar and a click outside all arrive here. Nothing is applied. */
  onCancel: () => void;
  onApply: (intent: MessagePriorityIntent) => void;
}

export default function MessagePriorityDialog({
  intent,
  anchorRef,
  onCancel,
  onApply,
}: MessagePriorityDialogProps) {
  const panelRef = useRef<HTMLDivElement>(null);
  const compact = useMediaQuery(sheetQuery);
  const anchored = useAnchoredPlacement(compact, anchorRef, panelRef);
  const [draft, setDraft] = useState(() => normalizePriorityIntent(intent));

  // Every edit goes through the normaliser, so a draft never holds an option
  // its own priority does not allow — not even for the moment between picking
  // Padrão and pressing Aplicar.
  function patchDraft(patch: Partial<MessagePriorityIntent>) {
    setDraft((current) => normalizePriorityIntent({ ...current, ...patch }));
  }

  function handleKeyDown(event: KeyboardEvent<HTMLDivElement>) {
    if (event.key === "Escape") {
      event.preventDefault();
      onCancel();
      return;
    }
    if (event.key === "Tab") trapTab(event, panelRef.current);
  }

  return createPortal(
    <div
      className={`msg-priority__backdrop${anchored ? " msg-priority__backdrop--anchored" : ""}`}
      onMouseDown={onCancel}
    >
      <div
        ref={panelRef}
        className={`chat-theme msg-priority${anchored ? " msg-priority--anchored" : ""}`}
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        data-testid="composer-priority-dialog"
        onKeyDown={handleKeyDown}
        onMouseDown={(event) => event.stopPropagation()}
      >
        <fieldset className="msg-priority__group">
          <legend id={titleId} className="msg-priority__title">
            Prioridade da mensagem
          </legend>
          {orderedPriorities.map((priority) => (
            <label key={priority} className="msg-priority__choice">
              <input
                type="radio"
                name="composer-priority"
                value={priority}
                checked={draft.priority === priority}
                // Autofocus on the applied choice: the reader arrives on the
                // value they are changing, and the arrow keys a native radio
                // group already answers move from there.
                autoFocus={draft.priority === priority}
                data-testid={`priority-option-${priority}`}
                onChange={() => patchDraft({ priority })}
              />
              <span>{priorityLabels[priority]}</span>
            </label>
          ))}
        </fieldset>

        {allowsAttentionOptions(draft.priority) && (
          <UrgentOptions draft={draft} onChange={patchDraft} />
        )}

        <div className="msg-priority__actions">
          <button
            type="button"
            className="msg-priority__cancel"
            data-testid="priority-cancel"
            onClick={onCancel}
          >
            Cancelar
          </button>
          <button
            type="button"
            className="msg-priority__apply"
            data-testid="priority-apply"
            onClick={() => onApply(normalizePriorityIntent(draft))}
          >
            Aplicar
          </button>
        </div>
      </div>
    </div>,
    document.body,
  );
}
