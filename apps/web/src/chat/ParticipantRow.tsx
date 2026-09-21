/**
 * One row of the participant roster in the conversation details panel
 * (issue #895).
 *
 * Extracted from ConversationDetailsPanel because turning an identity into a
 * navigation target is a concern that panel does not otherwise have, and it is
 * not the shared expansion primitive's either (issue #892) — that component
 * receives rows already rendered and never looks inside them.
 *
 * Security: `displayName` and `subtitle` are React text nodes and `userId` is
 * never displayed. It is a key, an identity comparison, and the argument the
 * open-DM flow is addressed by — nothing here builds a URL, and the destination
 * of a row is whatever conversation id the server answers with, resolved
 * entirely inside the flow the caller passes in.
 */

import PresenceDot from "./PresenceDot";
import { useDirectMessagePending, type DirectMessagePendingSource } from "./directMessage";
import { avatarColorFor, initialsFrom } from "./messageDisplay";
import { presenceLabel, type PresenceState } from "./presence";
import type { RosterParticipant } from "./participantRosterOrder";

/** The identity block: avatar with its presence dot, name, and the status line. */
function ParticipantIdentity({
  participant,
  presence,
  isCurrentUser,
}: {
  participant: RosterParticipant;
  presence: PresenceState;
  isCurrentUser: boolean;
}) {
  const color = avatarColorFor(participant.userId);
  return (
    <>
      <span
        className={`chat-details__avatar chat-details__avatar--${color}`}
        aria-hidden="true"
        data-testid="chat-details-member-avatar"
      >
        {participant.avatarUrl ? (
          <img
            className="chat-details__avatar-img"
            src={participant.avatarUrl}
            alt=""
            referrerPolicy="no-referrer"
          />
        ) : (
          initialsFrom(participant.displayName)
        )}
        <PresenceDot state={presence} size="md" />
      </span>
      <span className="chat-details__member-text">
        <span className="chat-details__member-name">
          {participant.displayName}
          {isCurrentUser && <span className="chat-details__badge">Você</span>}
        </span>
        {/* The state as a word, beside the dot rather than instead of it: the
            row survives greyscale, a screen reader and a colour-blind reader. */}
        <span className="chat-details__member-role">
          {participant.subtitle}
          {presence !== "unknown" && ` · ${presenceLabel(presence)}`}
        </span>
      </span>
    </>
  );
}

/**
 * What activating this row announces it will do, with the same facts the row
 * shows.
 *
 * The button's accessible name is stated rather than composed from its
 * children, because the children name a *person* and the control performs an
 * *action* — "Álvaro Neto, Participante · Online, botão" leaves a screen-reader
 * user to guess what pressing it does. The status is carried along so naming
 * the action does not cost the information the visible row already gives.
 */
function openDMLabel(participant: RosterParticipant, presence: PresenceState): string {
  const status = presence === "unknown" ? "" : `, ${presenceLabel(presence)}`;
  return `Abrir conversa com ${participant.displayName}. ${participant.subtitle}${status}`;
}

/**
 * One roster row.
 *
 * The row is an `<li>` and the navigable region is a `<button>` inside it,
 * never the other way round and never both. That is what keeps a secondary
 * action — the removal control issue #469 will add — composable: it is a
 * sibling of this button within the same list item, so it is reachable by
 * keyboard on its own, its click cannot bubble into a navigation, and no nested
 * interactive element is ever produced.
 *
 * `onOpenDM` absent is the honest "there is nothing to activate here": the
 * viewer's own row (you do not open a conversation with yourself) and any host
 * that has not wired the flow. Those render the identical content in a plain
 * element, so the row never advertises an action that does not exist.
 *
 * The busy state is `aria-busy`, not `disabled`: a disabled control leaves the
 * tab order mid-interaction, moving focus out from under the person who just
 * pressed it. Repeated activation is harmless anyway — the open-DM flow
 * deduplicates by recipient, so a second press while the first is in flight
 * starts nothing.
 *
 * That state is *subscribed here*, for this participant only (issue #895). The
 * row used to be handed a boolean the section computed from the set of everyone
 * pending, which meant a request for anybody rebuilt every row of the roster —
 * and, in the message list, every message. A row knows one person and watches
 * one person.
 */
export default function ParticipantRow({
  participant,
  presence,
  isCurrentUser,
  onOpenDM,
  pendingSource,
}: {
  participant: RosterParticipant;
  presence: PresenceState;
  isCurrentUser: boolean;
  onOpenDM?: (userId: string) => void;
  pendingSource?: DirectMessagePendingSource;
}) {
  const pending = useDirectMessagePending(pendingSource, participant.userId);
  const identity = (
    <ParticipantIdentity
      participant={participant}
      presence={presence}
      isCurrentUser={isCurrentUser}
    />
  );
  if (!onOpenDM) {
    return (
      <li className="chat-details__member">
        <span className="chat-details__member-main">{identity}</span>
      </li>
    );
  }
  return (
    <li className="chat-details__member">
      <button
        type="button"
        className="chat-details__member-main chat-details__member-main--action"
        aria-label={openDMLabel(participant, presence)}
        aria-busy={pending}
        onClick={() => onOpenDM(participant.userId)}
        data-testid="chat-details-participant-open-dm"
      >
        {identity}
      </button>
    </li>
  );
}
