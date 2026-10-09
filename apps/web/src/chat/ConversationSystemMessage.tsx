/**
 * ConversationSystemMessage — a conversation event in the timeline (issue #527).
 *
 * A rename or a departure is something that happened *to* the conversation, not
 * something a person said in it, so it is deliberately not a MessageBubble: no
 * speech bubble, no avatar, no reactions, no reply, no edit or delete, no
 * message menu. It follows the day divider's visual language, which is the
 * product's existing way of putting a neutral marker in the timeline.
 *
 * It renders a decorative icon plus text, nothing else. `old_name` and
 * `new_name` are data written by whoever renamed the conversation, and they
 * reach the DOM as a React text node — never through dangerouslySetInnerHTML
 * and never concatenated into markup — so a name containing `<`, `&` or
 * quotes stays a name. The icon is a fixed ligature name chosen by this
 * build from the event type (never server data), so it carries nothing a
 * server could forge.
 */

import "./ConversationSystemMessage.css";
import type { Message } from "./chatTypes";
import { systemMessagePresentation, type SystemMessageScope } from "./conversationSystemMessage";

interface ConversationSystemMessageProps {
  message: Message;
  /** Whether the sentence says "canal", "grupo" or "conversa". */
  scope: SystemMessageScope;
  /**
   * The reader's own user id (issue #685), used only to pick the "você"
   * phrasing when the reader is the event's actor or one of its targets.
   * Optional: omitting it degrades to the third-party phrasing everywhere.
   */
  viewerId?: string;
  /**
   * Registers the rendered row with the timeline (issue #1082), exactly as a
   * bubble does: a conversation event on screen is read by the same geometry as
   * any message, and a row that renders nothing never registers at all.
   */
  setMessageRef?: (messageId: string, el: HTMLElement | null) => void;
}

export default function ConversationSystemMessage({
  message,
  scope,
  viewerId,
  setMessageRef,
}: ConversationSystemMessageProps) {
  const presentation = systemMessagePresentation(message, scope, viewerId);
  // Only a message that is not a conversation event has no presentation; every
  // event — even one this build cannot phrase — draws a line (issue #1082).
  if (!presentation) return null;
  return (
    <p
      ref={setMessageRef ? (el) => setMessageRef(message.id, el) : undefined}
      data-message-id={message.id}
      className={
        presentation.tone === "call"
          ? "chat-system-message chat-system-message--call"
          : "chat-system-message"
      }
      data-testid="chat-system-message"
      data-event={message.eventType}
    >
      <span className="material-symbols-outlined chat-system-message__icon" aria-hidden="true">
        {presentation.icon}
      </span>
      <span className="chat-system-message__text">{presentation.text}</span>
    </p>
  );
}
