/**
 * GroupDetailsPanel — the "Detalhes do grupo" side panel (issues #441, #398).
 *
 * A sibling of ChannelDetailsPanel rather than a mode of it. The two render
 * different things — a group has participants and nothing else; a channel has
 * online members, a visibility, a pinned message and files — so folding them
 * into one component would mean a conditional around almost every section, and
 * the shared part (the shell, the member row, the add-members flow) is already
 * shared as CSS classes and as AddMembersDialog.
 *
 * Security invariants, identical to the channel panel's:
 * - every server-supplied string is a React text node; no dangerouslySetInnerHTML
 *   and no URL is ever built from a display name;
 * - the viewer is identified by ID, never by display name, so two participants
 *   with the same name can never both be marked "Você";
 * - avatar URLs arrive already filtered by chatApi's same-origin rule;
 * - the add action is rendered from a server-derived permission and is not the
 *   control: POST .../members re-derives the decision inside its transaction.
 *
 * The panel is a layout sibling of the conversation, never a modal and never a
 * route, so opening it cannot unmount the message list or the composer.
 */

import { useCallback, useEffect, useRef, useState } from "react";

import "./ChannelDetailsPanel.css";
import AddMembersDialog from "./AddMembersDialog";
import type { AddMembersResult, GroupParticipant } from "./chatTypes";
import { avatarColorFor, formatLongDate, initialsFrom } from "./messageDisplay";
import type { GroupDetailsState } from "./useGroupDetails";
import { groupDetailsPanelId, groupDetailsTitleId } from "./channelDetailsDisplay";

const presenceLabel = {
  online: "Online",
  away: "Ausente",
  offline: "Offline",
} as const;

interface ParticipantRowProps {
  participant: GroupParticipant;
  isCurrentUser: boolean;
}

function ParticipantRow({ participant, isCurrentUser }: ParticipantRowProps) {
  const color = avatarColorFor(participant.userId);
  return (
    <li className="chat-details__member">
      <span
        className={`chat-details__avatar chat-details__avatar--${color}`}
        aria-hidden="true"
        data-testid="chat-group-participant-avatar"
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
        {participant.presence && (
          <span
            className={`chat-details__presence chat-details__presence--${participant.presence}`}
            data-testid="chat-group-presence"
          />
        )}
      </span>
      <span className="chat-details__member-text">
        <span className="chat-details__member-name">
          {participant.displayName}
          {isCurrentUser && <span className="chat-details__badge">Você</span>}
        </span>
        {/*
          A group has no role to show — chat.dm_members.role is closed by CHECK
          to 'member' — so this line carries presence alone, and only when the
          server tracks it.
        */}
        {participant.presence && (
          <span className="chat-details__member-role">{presenceLabel[participant.presence]}</span>
        )}
      </span>
    </li>
  );
}

interface GroupDetailsPanelProps {
  state: GroupDetailsState;
  /** Identifies the viewer by ID; a display name would be ambiguous. */
  currentUserId: string;
  onClose: () => void;
}

export default function GroupDetailsPanel({
  state,
  currentUserId,
  onClose,
}: GroupDetailsPanelProps) {
  const closeButtonRef = useRef<HTMLButtonElement>(null);
  const addMembersButtonRef = useRef<HTMLButtonElement>(null);
  const [addedNotice, setAddedNotice] = useState<{ conversationId: string; text: string } | null>(
    null,
  );

  // Focus moves into the panel once, on open. Deliberately not re-run on data
  // changes: a refetch must never steal focus from wherever the user moved it.
  useEffect(() => {
    closeButtonRef.current?.focus();
  }, []);

  const details = state.details;
  const conversationId = details.status === "ready" ? details.data.id : "";

  /**
   * The picker is open only while the conversation it was opened for is still
   * the one on screen.
   *
   * Storing the identity instead of a boolean is the whole protection against
   * confirming a selection into the wrong conversation: when the panel is handed
   * a different group, this comparison fails during render, the dialog unmounts,
   * its AbortController cancels any in-flight search or submit, and its
   * selection goes with it. One structural mechanism, no effect, and no way for
   * a stale dialog to survive long enough to post to the new ID.
   */
  const [pickerFor, setPickerFor] = useState<string | null>(null);
  const pickerOpen = pickerFor !== null && pickerFor === conversationId && conversationId !== "";

  const closePicker = useCallback(() => {
    setPickerFor(null);
    // Only if the control is still mounted; focusing a detached node would drop
    // focus to <body>.
    addMembersButtonRef.current?.focus();
  }, []);

  const reload = state.reload;
  const handleAdded = useCallback(
    (result: AddMembersResult) => {
      closePicker();
      setAddedNotice({
        conversationId,
        text:
          result.added === 0
            ? "Todas as pessoas selecionadas já participam deste grupo."
            : result.added === 1
              ? "1 pessoa adicionada ao grupo."
              : `${result.added} pessoas adicionadas ao grupo.`,
      });
      // Single reconciliation path: refetch rather than merging the response, so
      // a concurrent members.added event cannot double anything.
      reload();
    },
    [closePicker, conversationId, reload],
  );

  return (
    <aside
      id={groupDetailsPanelId}
      className="chat-details"
      aria-labelledby={groupDetailsTitleId}
      data-testid="chat-group-details"
    >
      <div className="chat-details__head">
        <h2 id={groupDetailsTitleId} className="chat-details__title">
          Detalhes do grupo
        </h2>
        <button
          ref={closeButtonRef}
          type="button"
          className="chat-details__close"
          aria-label="Fechar detalhes do grupo"
          onClick={onClose}
        >
          <span className="material-symbols-outlined" aria-hidden="true">
            close
          </span>
        </button>
      </div>

      <div className="chat-details__body">
        {/* ── Sobre ─────────────────────────────────────────────────────── */}
        <section className="chat-details__section" aria-labelledby="chat-group-about">
          <h3 id="chat-group-about" className="chat-details__label">
            Sobre
          </h3>
          {details.status === "loading" && (
            <p className="chat-details__note" role="status">
              Carregando informações do grupo…
            </p>
          )}
          {details.status === "error" && (
            <p className="chat-details__note" role="alert">
              Não foi possível carregar as informações do grupo.
            </p>
          )}
          {details.status === "ready" && (
            <>
              <p className="chat-details__meta">
                <span className="material-symbols-outlined" aria-hidden="true">
                  calendar_today
                </span>
                {details.data.createdAt
                  ? `Criado em ${formatLongDate(details.data.createdAt)}`
                  : "Data de criação indisponível"}
              </p>
              {/*
                No visibility line: chat.dm_conversations has no public/private
                column, and inventing one here would be inventing a domain.
              */}
              <p className="chat-details__meta">
                <span className="material-symbols-outlined" aria-hidden="true">
                  group
                </span>
                {details.data.participantCount === 1
                  ? "1 participante"
                  : `${details.data.participantCount} participantes`}
              </p>
            </>
          )}
        </section>

        {/* ── Participantes ─────────────────────────────────────────────── */}
        <section className="chat-details__section" aria-labelledby="chat-group-participants">
          <div className="chat-details__section-head">
            {/*
              The heading counts participantCount, the server's total — not the
              rendered list, which is a capped preview.
            */}
            <h3 id="chat-group-participants" className="chat-details__label">
              Participantes
              {details.status === "ready" && ` (${details.data.participantCount})`}
            </h3>
          </div>
          {details.status === "loading" && (
            <p className="chat-details__note" role="status">
              Carregando participantes…
            </p>
          )}
          {details.status === "error" && (
            <p className="chat-details__note" role="alert">
              Não foi possível carregar os participantes.
            </p>
          )}
          {details.status === "ready" &&
            (details.data.participants.length === 0 ? (
              <p className="chat-details__note">Nenhum participante ativo.</p>
            ) : (
              <ul className="chat-details__members" aria-label="Participantes do grupo">
                {details.data.participants.map((participant) => (
                  <ParticipantRow
                    key={participant.userId}
                    participant={participant}
                    isCurrentUser={Boolean(currentUserId) && participant.userId === currentUserId}
                  />
                ))}
              </ul>
            ))}
          {/*
            Rendered only once the server has answered and said this caller may
            manage participants. Loading, error and "not permitted" all leave it
            absent — the safe default, since canManageMembers is false unless the
            server sent exactly true.
          */}
          {details.status === "ready" && details.data.canManageMembers && (
            <button
              ref={addMembersButtonRef}
              type="button"
              className="chat-details__wide-action"
              onClick={() => setPickerFor(conversationId)}
              data-testid="chat-group-add-members"
            >
              <span className="material-symbols-outlined" aria-hidden="true">
                person_add
              </span>
              Adicionar membros
            </button>
          )}
          {addedNotice?.conversationId === conversationId && (
            <p className="chat-details__note" role="status">
              {addedNotice.text}
            </p>
          )}
        </section>
      </div>

      {pickerOpen && (
        <AddMembersDialog
          target={{ kind: "group", conversationId }}
          /*
            The viewer plus every participant the panel can see. Partial by
            construction — `participants` is a capped preview — and harmlessly
            so: the server reports an existing participant under
            already_members and writes no duplicate row, which is why this is a
            UX filter and never a check.
          */
          /*
            Only the viewer. Current members are excluded by the search endpoint
            itself, in SQL — this list deliberately does not carry them, because
            the section above is a capped preview and passing it made members it
            could not show appear as selectable.
          */
          excludedUserIds={currentUserId ? [currentUserId] : []}
          onClose={closePicker}
          onAdded={handleAdded}
        />
      )}
    </aside>
  );
}
