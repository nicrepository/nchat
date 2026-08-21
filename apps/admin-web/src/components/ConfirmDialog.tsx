import { useEffect, useRef, type ReactNode } from "react";

interface ConfirmDialogProps {
  title: string;
  /** What the operator is about to change, and to what. */
  description: ReactNode;
  /** What it will affect beyond the obvious. Rendered as a warning. */
  impact?: ReactNode;
  confirmLabel: string;
  pending: boolean;
  onConfirm: () => void;
  onCancel: () => void;
}

/**
 * The confirmation the console shows before an action with real consequences.
 *
 * It is a modal dialog in the accessibility sense, not only the visual one:
 * `role="dialog"` with `aria-modal`, labelled and described by its own content,
 * focus moved into it on open and returned to whatever opened it on close, and
 * Escape cancels. Without the focus return, keyboard and screen-reader users
 * land back at the top of the document after every action.
 *
 * It is used only where the impact justifies it — suspending an account,
 * archiving a channel, changing who administers the platform — because a
 * confirmation on everything is a confirmation nobody reads.
 */
export default function ConfirmDialog({
  title,
  description,
  impact,
  confirmLabel,
  pending,
  onConfirm,
  onCancel,
}: ConfirmDialogProps) {
  const confirmRef = useRef<HTMLButtonElement>(null);
  const opener = useRef<Element | null>(null);

  useEffect(() => {
    opener.current = document.activeElement;
    confirmRef.current?.focus();
    return () => {
      if (opener.current instanceof HTMLElement) opener.current.focus();
    };
  }, []);

  return (
    <div className="admin-dialog-backdrop">
      <div
        className="admin-dialog"
        role="dialog"
        aria-modal="true"
        aria-labelledby="admin-dialog-title"
        aria-describedby="admin-dialog-description"
        onKeyDown={(event) => {
          if (event.key === "Escape" && !pending) onCancel();
        }}
      >
        <h2 id="admin-dialog-title">{title}</h2>
        <div id="admin-dialog-description">
          <p>{description}</p>
          {impact !== undefined && (
            <p className="admin-warning">
              <strong>Impacto:</strong> {impact}
            </p>
          )}
        </div>
        <div className="admin-dialog__actions">
          <button
            type="button"
            className="admin-button admin-button--ghost"
            onClick={onCancel}
            disabled={pending}
          >
            Cancelar
          </button>
          <button
            type="button"
            className="admin-button admin-button--danger"
            ref={confirmRef}
            onClick={onConfirm}
            disabled={pending}
          >
            {pending ? "Aplicando…" : confirmLabel}
          </button>
        </div>
      </div>
    </div>
  );
}
