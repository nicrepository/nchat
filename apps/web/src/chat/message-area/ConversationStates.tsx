/**
 * The three states a conversation shows instead of a timeline: still loading,
 * failed to load, and loaded but empty (moved out of ChatMessageArea, issue
 * #834).
 *
 * Each owns its own role/aria contract — role="status" + aria-busy while
 * loading, role="alert" for the failure, and a plain region for the empty
 * case — so which of them is on screen is announced without the timeline
 * having to say anything about it.
 */

import { useEffect, useRef } from "react";

import { IconForum, IconLock, IconWarning } from "./icons";

export function LoadingSkeleton() {
  return (
    <div
      className="chat-msg-area__loading"
      aria-busy="true"
      aria-label="Carregando mensagens"
      role="status"
    >
      {[
        { mine: false, w: "180px" },
        { mine: true, w: "220px" },
        { mine: false, w: "140px" },
        { mine: true, w: "260px" },
        { mine: false, w: "200px" },
      ].map(({ mine, w }, i) => (
        <div
          key={i}
          className={`chat-msg-area__skel-row${mine ? " chat-msg-area__skel-row--right" : ""}`}
        >
          {!mine && <div className="chat-msg-area__skel-avatar" />}
          <div className="chat-msg-area__skel-bubble" style={{ width: w }} />
        </div>
      ))}
    </div>
  );
}

interface ErrorStateProps {
  onRetry: () => void;
}

export function ErrorState({ onRetry }: ErrorStateProps) {
  return (
    <div className="chat-msg-area__error" role="alert" data-testid="chat-msg-error">
      <div className="chat-msg-area__error-icon">
        <IconWarning />
      </div>
      <p className="chat-msg-area__error-msg">Não foi possível carregar as mensagens.</p>
      <button type="button" className="chat-msg-area__retry-btn" onClick={onRetry}>
        Tentar novamente
      </button>
    </div>
  );
}

interface AccessDeniedStateProps {
  onBack: () => void;
}

/**
 * Issue #475: what a conversation the reader is not a member of shows,
 * instead of the generic loading-failure state — the backend answers a
 * non-member with the same non-enumerating 404/room_access_denied it uses
 * for an id that does not exist at all, so this copy stays neutral rather
 * than confirming the conversation exists.
 *
 * Renders in place of the whole conversation column (header, timeline,
 * composer, details), never alongside it — see ChatMessageArea — so there is
 * no residual chrome from a conversation this reader cannot see into.
 *
 * Focus moves to the heading on mount, the same way a route change would
 * normally land focus on new page content, so a screen reader announces the
 * denial immediately instead of leaving focus on whatever the reader last
 * touched in the sidebar.
 */
export function AccessDeniedState({ onBack }: AccessDeniedStateProps) {
  const headingRef = useRef<HTMLHeadingElement>(null);

  useEffect(() => {
    headingRef.current?.focus();
  }, []);

  return (
    <div className="chat-msg-area__error" role="alert" data-testid="chat-msg-access-denied">
      <div className="chat-msg-area__denied-icon">
        <IconLock />
      </div>
      <h2 className="chat-msg-area__empty-title" tabIndex={-1} ref={headingRef}>
        Você não tem acesso a esta conversa
      </h2>
      <p className="chat-msg-area__error-msg">
        Esta conversa é privada ou você não faz parte dela.
        <br />
        Volte para suas conversas para continuar usando o NChat.
      </p>
      <button type="button" className="chat-msg-area__retry-btn" onClick={onBack}>
        Voltar para minhas conversas
      </button>
    </div>
  );
}

interface EmptyStateProps {
  kind: "channel" | "dm";
  name: string;
}

export function EmptyState({ kind, name }: EmptyStateProps) {
  return (
    <div className="chat-msg-area__empty" data-testid="chat-msg-empty">
      <div className="chat-msg-area__empty-icon">
        <IconForum />
      </div>
      <h2 className="chat-msg-area__empty-title">Nenhuma mensagem ainda</h2>
      <p className="chat-msg-area__empty-sub">
        {kind === "channel"
          ? `Este é o início do canal #${name}. Envie a primeira mensagem!`
          : `Esta é a sua conversa com ${name}. Diga olá!`}
      </p>
    </div>
  );
}
