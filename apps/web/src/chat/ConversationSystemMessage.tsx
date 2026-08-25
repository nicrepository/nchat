/**
 * ConversationSystemMessage — a conversation event in the timeline (issue #527).
 *
 * A rename or a departure is something that happened *to* the conversation, not
 * something a person said in it, so it is deliberately not a MessageBubble: no
 * speech bubble, no avatar, no reactions, no reply, no edit or delete, no
 * message menu. It follows the day divider's visual language, which is the
 * product's existing way of putting a neutral marker in the timeline.
 *
 * It renders text and nothing else. `old_name` and `new_name` are data written
 * by whoever renamed the conversation, and they reach the DOM as a React text
 * node — never through dangerouslySetInnerHTML and never concatenated into
 * markup — so a name containing `<`, `&` or quotes stays a name.
 */

import "./ConversationSystemMessage.css";
import type { Message } from "./chatTypes";
import { systemMessagePresentation, type SystemMessageScope } from "./conversationSystemMessage";

interface ConversationSystemMessageProps {
  message: Message;
  /** Whether the sentence says "canal", "grupo" or "conversa". */
  scope: SystemMessageScope;
}

export default function ConversationSystemMessage({
  message,
  scope,
}: ConversationSystemMessageProps) {
  const presentation = systemMessagePresentation(message, scope);
  // An event this build cannot describe renders nothing at all, rather than an
  // empty line: a newer server's event must not leave a blank row behind.
  if (!presentation) return null;
  return (
    <p
      className="chat-system-message"
      data-testid="chat-system-message"
      data-event={message.eventType}
    >
      <span className="chat-system-message__text">{presentation.text}</span>
    </p>
  );
}
