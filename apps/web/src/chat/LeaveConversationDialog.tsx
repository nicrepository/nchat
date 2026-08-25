/**
 * LeaveConversationDialog — "Sair do canal" / "Sair do grupo" (issue #527).
 *
 * The one destructive confirmation in this menu. It renders into a portal as a
 * sibling of the app root, so opening it selects nothing and navigates nowhere.
 *
 * The shell (portal, backdrop, role="dialog", Escape, focus trap, the
 * `submittingRef` that makes submit single) mirrors RenameChannelDialog rather
 * than introducing a third modal system.
 *
 * Initial focus is on Cancel, not on the destructive action: a confirmation that
 * lands focus on "Sair" turns a stray Enter into a departure.
 */

import { type KeyboardEvent, useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";

import "./LeaveConversationDialog.css";
import { ApiRequestError } from "../lib/api";
import type { ConversationTargetKind } from "./conversationActions";

interface LeaveConversationDialogProps {
  kind: Exclude<ConversationTargetKind, "dm">;
  name: string;
  /** Private channels lose their history on the way out; public ones do not. */
  isPrivate?: boolean;
  onClose: () => void;
  /** Resolves once the membership is removed; rejects with the API error. */
  onConfirm: () => Promise<void>;
}

const titleId = "chat-leave-conversation-title";
const descriptionId = "chat-leave-conversation-description";
const errorId = "chat-leave-conversation-error";

/**
 * What the person actually loses, stated per kind.
 *
 * A private channel is unreachable without a membership, so leaving it really
 * does end access to the history. A public one stays readable to any member of
 * the workspace, so claiming otherwise would be a lie — the honest consequence
 * is that it leaves the sidebar and stops notifying.
 */
function consequence(kind: "channel" | "group", isPrivate: boolean): string {
  if (kind === "group") {
    return "Você deixará de participar do grupo e poderá precisar ser adicionado novamente para voltar.";
  }
  if (isPrivate) {
    return "Você perderá acesso ao canal e ao histórico até ser adicionado novamente.";
  }
  return "O canal sai da sua barra lateral e você deixa de receber notificações dele. Por ser público, você pode entrar novamente depois.";
}

function leaveErrorMessage(error: unknown): string {
  if (error instanceof ApiRequestError) {
    if (error.status === 403) return "Você não pode sair desta conversa.";
    if (error.status === 404) return "Esta conversa não está mais disponível.";
    if (error.status === 429) {
      return "Muitas solicitações em sequência. Aguarde um momento e tente novamente.";
    }
    if (error.status === 0) return "Sem conexão. Verifique sua rede e tente novamente.";
  }
  return "Não foi possível sair. Tente novamente.";
}

export default function LeaveConversationDialog({
  kind,
  name,
  isPrivate = false,
  onClose,
  onConfirm,
}: LeaveConversationDialogProps) {
  const [error, setError] = useState("");
  const [pending, setPending] = useState(false);
  const cancelRef = useRef<HTMLButtonElement>(null);
  const dialogRef = useRef<HTMLDivElement>(null);
  // State updates are asynchronous, so `pending` alone cannot stop a second
  // click fired in the same tick. This ref is what actually makes submit single.
  const submittingRef = useRef(false);
  const mountedRef = useRef(true);

  useEffect(() => {
    mountedRef.current = true;
    // The safe action holds focus.
    cancelRef.current?.focus();
    return () => {
      mountedRef.current = false;
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
    } catch (failure) {
      // The dialog stays open and recoverable: a refusal or a dropped connection
      // must never leave the UI showing a departure that did not happen.
      if (mountedRef.current) {
        setError(leaveErrorMessage(failure));
        cancelRef.current?.focus();
      }
    } finally {
      submittingRef.current = false;
      if (mountedRef.current) setPending(false);
    }
  }

  const confirmLabel = kind === "group" ? "Sair do grupo" : "Sair do canal";

  return createPortal(
    <div className="leave-conversation__backdrop" onMouseDown={requestClose}>
      <div
        ref={dialogRef}
        className="leave-conversation"
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        aria-describedby={descriptionId}
        onKeyDown={handleKeyDown}
        onMouseDown={(event) => event.stopPropagation()}
      >
        <h2 id={titleId} className="leave-conversation__title">
          Sair de {name}?
        </h2>
        <p id={descriptionId} className="leave-conversation__description">
          {consequence(kind, isPrivate)}
        </p>
        {error && (
          <p id={errorId} className="leave-conversation__error" role="alert">
            {error}
          </p>
        )}
        <div className="leave-conversation__actions">
          <button
            ref={cancelRef}
            type="button"
            className="leave-conversation__cancel"
            disabled={pending}
            onClick={requestClose}
          >
            Cancelar
          </button>
          <button
            type="button"
            className="leave-conversation__confirm"
            disabled={pending}
            aria-busy={pending}
            aria-describedby={error ? errorId : undefined}
            onClick={confirm}
          >
            {pending ? "Saindo…" : confirmLabel}
          </button>
        </div>
      </div>
    </div>,
    document.body,
  );
}
