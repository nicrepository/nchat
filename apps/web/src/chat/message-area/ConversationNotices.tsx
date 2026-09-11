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
 */
export default function ConversationNotices({
  sendError,
  realtimeError,
  actionError,
  openDMError,
  pinError,
  typingLabel,
}: {
  sendError: string | null;
  realtimeError: string | null;
  actionError: string | null;
  openDMError: string | null;
  pinError: string | null;
  typingLabel: string | null;
}) {
  const refusal = actionError ?? openDMError ?? pinError;
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
