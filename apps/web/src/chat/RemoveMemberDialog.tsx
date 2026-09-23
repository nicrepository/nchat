/**
 * RemoveMemberDialog — "Remover membro?" (issue #469).
 *
 * The confirmation between the row's minus button and the DELETE. Removal is
 * destructive and not undoable from here, so it never happens on a first
 * click: this dialog states who is being removed, from which conversation, and
 * what that person actually loses.
 *
 * The shell — portal, backdrop, role="dialog", Escape, focus trap, the
 * `submittingRef` that makes submit single — mirrors LeaveConversationDialog
 * rather than introducing a third modal system, and deliberately does not
 * abstract one out of the two: they are edited for different reasons, and a
 * shared framework would couple a departure to a removal.
 *
 * Initial focus is on Cancel, exactly like the leave dialog and for the same
 * reason: a confirmation that lands focus on the destructive action turns a
 * stray Enter into a removal.
 *
 * Security: every value shown is a React text node — a display name is never
 * markup and never part of a URL — and the error text is chosen from this
 * file by HTTP status, never taken from the response body.
 */

import { type KeyboardEvent, useEffect, useLayoutEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";

import "./RemoveMemberDialog.css";
import { removalConsequence, removeMemberErrorMessage } from "./removeMemberCopy";

/** What the dialog is about: one person, in one conversation. */
export interface RemoveMemberTarget {
  userId: string;
  displayName: string;
}

interface RemoveMemberDialogProps {
  kind: "channel" | "group";
  /** Private channels revoke access on the way out; public ones do not. */
  isPrivateChannel?: boolean;
  conversationName: string;
  member: RemoveMemberTarget;
  onClose: () => void;
  /** Resolves once the server has removed them; rejects with the API error. */
  onConfirm: () => Promise<void>;
}

const titleId = "chat-remove-member-title";
const descriptionId = "chat-remove-member-description";
const errorId = "chat-remove-member-error";

export default function RemoveMemberDialog({
  kind,
  isPrivateChannel = false,
  conversationName,
  member,
  onClose,
  onConfirm,
}: RemoveMemberDialogProps) {
  const [error, setError] = useState("");
  const [pending, setPending] = useState(false);
  const cancelRef = useRef<HTMLButtonElement>(null);
  const dialogRef = useRef<HTMLDivElement>(null);
  // State updates are asynchronous, so `pending` alone cannot stop a second
  // click fired in the same tick. This ref is what actually makes submit
  // single — and a removal is exactly the operation where a double submit
  // would produce a second, pointless destructive request.
  const submittingRef = useRef(false);
  // Whether this dialog is still on screen.
  //
  // Invalidated in the *layout* phase, which is the only one that runs inside
  // the commit that removes the dialog. A passive cleanup is scheduled after
  // that commit, so a request settling in between would find the flag still
  // true and hand its outcome to a dialog nobody can see: an error message
  // rendered into a detached tree, and — the part a user would actually feel —
  // `focus()` on a button that is no longer in the document, which drops focus
  // to <body> away from whatever the reader moved on to.
  //
  // Each mount gets its own ref, so a new dialog never revives the flag an old
  // operation captured: the closure of a finished request keeps pointing at
  // the instance it belonged to, which stays false. That also makes the effect
  // idempotent under StrictMode's setup → cleanup → setup, which ends true for
  // the instance that is really mounted.
  const mountedRef = useRef(true);
  useLayoutEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
    };
  }, []);

  // Focus stays in a passive effect: it is the dialog's opening behaviour, not
  // its lifecycle, and moving it into the commit would buy nothing. The safe
  // action holds it, so a stray Enter cannot remove anybody.
  useEffect(() => {
    cancelRef.current?.focus();
  }, []);

  // Escape and the backdrop are refused while the write is in flight: closing
  // then would leave the user unable to tell whether the removal happened.
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
      // Closing is the caller's job on success: it owns where focus goes once
      // the row this dialog was opened from no longer exists. Nothing local
      // happens here, which is why there is nothing here to guard.
    } catch (failure) {
      // The dialog stays open and recoverable: a refusal or a dropped
      // connection must never leave the panel showing a removal that did not
      // happen, and retrying must not require finding the row again — unless
      // the dialog is gone, in which case there is nobody to tell and nothing
      // to focus.
      if (mountedRef.current) {
        setError(removeMemberErrorMessage(failure));
        cancelRef.current?.focus();
      }
    } finally {
      // The submit lock is this instance's own bookkeeping and no longer
      // observable once it is unmounted; the state it renders is, so it is
      // set only while there is something rendering it. `finally` runs after
      // the await like the branches above and needs the same guard.
      submittingRef.current = false;
      if (mountedRef.current) setPending(false);
    }
  }

  return createPortal(
    <div className="remove-member__backdrop" onMouseDown={requestClose}>
      <div
        ref={dialogRef}
        className="remove-member"
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        aria-describedby={descriptionId}
        onKeyDown={handleKeyDown}
        onMouseDown={(event) => event.stopPropagation()}
      >
        <h2 id={titleId} className="remove-member__title">
          Remover membro?
        </h2>
        <p id={descriptionId} className="remove-member__description">
          {/* Who, and from where, before what it costs. */}
          <strong className="remove-member__subject">{member.displayName}</strong> será removido de{" "}
          <strong className="remove-member__subject">{conversationName}</strong>.{" "}
          {removalConsequence(kind, isPrivateChannel)}
        </p>
        {error && (
          <p id={errorId} className="remove-member__error" role="alert">
            {error}
          </p>
        )}
        <div className="remove-member__actions">
          <button
            ref={cancelRef}
            type="button"
            className="remove-member__cancel"
            disabled={pending}
            onClick={requestClose}
          >
            Cancelar
          </button>
          <button
            type="button"
            className="remove-member__confirm"
            disabled={pending}
            aria-busy={pending}
            aria-describedby={error ? errorId : undefined}
            onClick={confirm}
          >
            {pending ? "Removendo…" : "Remover membro"}
          </button>
        </div>
      </div>
    </div>,
    document.body,
  );
}
