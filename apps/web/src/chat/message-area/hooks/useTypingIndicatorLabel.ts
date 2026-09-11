/**
 * The one line that says who is typing (moved out of ChatMessageArea, issue
 * #834).
 *
 * Two tiers of name resolution, and the order between them is the point. The
 * server resolves and sends the authoritative display name on the typing event
 * itself, and that always wins. This hook's own map — the DM roster, else the
 * name most recently seen on that user's own message — is a second-tier
 * fallback that only shows through when the server's resolution was empty (a
 * lookup failure, or no display name on file). "Alguém", inside
 * typingIndicatorText, is the last resort after that.
 */

import { useMemo } from "react";

import type { DMConversation, Message } from "../../chatTypes";
import { typingIndicatorText } from "../conversationText";

interface Params {
  kind: "channel" | "dm";
  activeDM: DMConversation | undefined;
  messages: Message[];
  typingUserIds: readonly string[];
  typingDisplayNameByUserId: ReadonlyMap<string, string>;
}

export function useTypingIndicatorLabel({
  kind,
  activeDM,
  messages,
  typingUserIds,
  typingDisplayNameByUserId,
}: Params): string | null {
  const heuristicNames = useMemo(() => {
    const names = new Map<string, string>();
    if (kind === "dm" && activeDM) {
      for (const participant of activeDM.participants) {
        names.set(participant.id, participant.displayName);
      }
    }
    for (const message of messages) {
      if (!names.has(message.senderId)) names.set(message.senderId, message.senderDisplayName);
    }
    return names;
  }, [kind, activeDM, messages]);

  return useMemo(() => {
    const names = new Map(heuristicNames); // heuristic, tier 2
    for (const [userId, name] of typingDisplayNameByUserId) {
      if (name) names.set(userId, name); // server-authoritative, wins
    }
    return typingIndicatorText(typingUserIds, names);
  }, [typingUserIds, typingDisplayNameByUserId, heuristicNames]);
}
