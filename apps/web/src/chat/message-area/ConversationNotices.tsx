/**
 * The notices strip between the timeline and the composer (moved out of
 * ChatMessageArea, issue #834).
 */

import { IconWarning } from "./icons";

/**
 * The strip between the timeline and the composer: a failed send, an unstable
 * connection, a refused action, and who is typing.
 *
 * The refusals share one line because only one of them can usefully be read at
 * a time, and the order is the order they matter in.
 *
 * A refused open-DM is deliberately not among them (issue #895). That flow is
 * owned by the shell, above this conversation, because the surfaces that can
 * start it are not all inside one — and a single state reported here *and* by
 * the details panel beside this strip produced two alerts for one failure. The
 * shell reports it once.
 */
export default function ConversationNotices({
  sendError,
  realtimeError,
  actionError,
  pinError,
  acknowledgeError,
  typingLabel,
}: {
  sendError: string | null;
  realtimeError: string | null;
  actionError: string | null;
  pinError: string | null;
  /** Issue #824: a confirmation that could not be recorded. */
  acknowledgeError?: string | null;
  typingLabel: string | null;
}) {
  const refusal = actionError ?? pinError ?? acknowledgeError ?? null;
  return (
    <>
      {sendError && (
        <div className="chat-msg-area__send-error" role="alert" data-testid="chat-send-error">
          <IconWarning />
          {sendError}
        </div>
      )}
      {realtimeError && (
        <div
          className="chat-msg-area__realtime-error"
          role="status"
          data-testid="chat-realtime-error"
        >
          <IconWarning />
          Conexão em tempo real instável. Tentando reconectar...
        </div>
      )}
      {refusal && (
        <div className="chat-msg-area__reaction-error" role="alert">
          <IconWarning />
          {refusal}
        </div>
      )}
      {typingLabel && (
        <div
          className="chat-msg-area__typing-indicator"
          role="status"
          data-testid="chat-typing-indicator"
        >
          <span className="chat-msg-area__typing-dots" aria-hidden="true">
            <span />
            <span />
            <span />
          </span>
          {typingLabel}
        </div>
      )}
    </>
  );
}
