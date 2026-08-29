/**
 * RevokeSessionDialog — confirm-only dialog for revoking one session or all
 * other sessions (issue #672 §4.3).
 *
 * One component parameterized by `target` rather than two near-identical
 * dialogs. The shell (portal, `role="dialog"`, focus starting on Cancel
 * rather than the destructive action, Tab focus-trap, `submittingRef` /
 * `mountedRef`) mirrors `LeaveConversationDialog` rather than introducing a
 * fourth modal system.
 */

import { type KeyboardEvent, useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";

import "./RevokeSessionDialog.css";

interface RevokeSessionDialogProps {
  target: "single" | "others";
  onClose: () => void;
  /** Resolves once the revoke lands; rejects with the API error. */
  onConfirm: () => Promise<void>;
}

const titleId = "profile-revoke-session-title";
const descriptionId = "profile-revoke-session-description";
const errorId = "profile-revoke-session-error";

const copy = {
  single: {
    title: "Revogar sessão?",
    description: "Este dispositivo será desconectado do NChat e precisará autenticar novamente.",
    confirmLabel: "Revogar sessão",
    pendingLabel: "Revogando…",
  },
  others: {
    title: "Revogar outras sessões?",
    description:
      "Esses dispositivos serão desconectados do NChat e precisarão autenticar novamente.",
    confirmLabel: "Revogar sessões",
    pendingLabel: "Revogando…",
  },
} as const;

export default function RevokeSessionDialog({
  target,
  onClose,
  onConfirm,
}: RevokeSessionDialogProps) {
  const [error, setError] = useState("");
  const [pending, setPending] = useState(false);
  const cancelRef = useRef<HTMLButtonElement>(null);
  const dialogRef = useRef<HTMLDivElement>(null);
  // State updates are asynchronous, so `pending` alone cannot stop a second
  // click fired in the same tick. This ref is what actually makes submit single.
  const submittingRef = useRef(false);
  const mountedRef = useRef(true);
  const openerRef = useRef<HTMLElement | null>(null);

  useEffect(() => {
    mountedRef.current = true;
    openerRef.current =
      document.activeElement instanceof HTMLElement ? document.activeElement : null;
    // The safe action holds focus.
    cancelRef.current?.focus();
    return () => {
      mountedRef.current = false;
      if (openerRef.current?.isConnected) openerRef.current.focus();
    };
  }, []);

  function requestClose() {
    if (!submittingRef.current) onClose();
  }

  function handleKeyDown(event: KeyboardEvent<HTMLDivElement>) {
    if (event.key === "Escape") {
      event.preventDefault();
      requestClose();
      return;
    }
    if (event.key !== "Tab") return;
    const focusable = dialogRef.current?.querySelectorAll<HTMLElement>("button:not(:disabled)");
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

  async function confirm() {
    if (submittingRef.current) return;
    submittingRef.current = true;
    setPending(true);
    setError("");
    try {
      await onConfirm();
      if (mountedRef.current) onClose();
    } catch {
      // The dialog stays open and recoverable: a refusal or a dropped
      // connection must never leave the UI showing a revoke that did not
      // happen. The caller already knows the specific reason (SessionsApiError);
      // this is one static fallback, matching LeaveConversationDialog's pattern.
      if (mountedRef.current) {
        setError("Não foi possível concluir a revogação. Tente novamente.");
        cancelRef.current?.focus();
      }
    } finally {
      submittingRef.current = false;
      if (mountedRef.current) setPending(false);
    }
  }

  const text = copy[target];

  return createPortal(
    <div className="revoke-session__backdrop" onMouseDown={requestClose}>
      <div
        ref={dialogRef}
        className="revoke-session"
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        aria-describedby={descriptionId}
        onKeyDown={handleKeyDown}
        onMouseDown={(event) => event.stopPropagation()}
      >
        <h2 id={titleId} className="revoke-session__title">
          {text.title}
        </h2>
        <p id={descriptionId} className="revoke-session__description">
          {text.description}
        </p>
        {error && (
          <p id={errorId} className="revoke-session__error" role="alert">
            {error}
          </p>
        )}
        <div className="revoke-session__actions">
          <button
            ref={cancelRef}
            type="button"
            className="revoke-session__cancel"
            disabled={pending}
            onClick={requestClose}
          >
            Cancelar
          </button>
          <button
            type="button"
            className="revoke-session__confirm"
            disabled={pending}
            aria-busy={pending}
            aria-describedby={error ? errorId : undefined}
            onClick={confirm}
          >
            {pending ? text.pendingLabel : text.confirmLabel}
          </button>
        </div>
      </div>
    </div>,
    document.body,
  );
}
