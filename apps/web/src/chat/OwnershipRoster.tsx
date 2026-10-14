import { useMemo, useState } from "react";
import type { OpenOwnershipAction } from "./OwnershipDialogs";
import OwnershipMemberMenu from "./OwnershipMemberMenu";
import { roleLabels } from "./ownershipPresentation";
import { emptyTargetPresence, selectTargetPresence, type TargetPresence } from "./presence";
import { UserAvatar } from "./UserAvatar";
import { type OwnershipDetails, type OwnershipMember } from "./ownershipApi";
import {
  filterOwnershipParticipants,
  ownershipParticipants,
  participantCopy,
  type OwnershipParticipantView,
  type ParticipantMenuAction,
} from "./ownershipParticipants";
import "./OwnershipRoster.css";

interface Props {
  workspaceId: string;
  ownership: OwnershipDetails;
  currentUserId: string;
  onAdd: () => void;
  onOpenDM?: (userId: string) => void;
  addButtonRef?: React.Ref<HTMLButtonElement>;
  presence?: TargetPresence;
  onAction: OpenOwnershipAction;
  onRemove: (member: OwnershipMember, trigger: HTMLElement) => void;
}
function OwnershipIdentity({
  member,
  isSelf,
  onOpenDM,
}: {
  member: OwnershipMember;
  isSelf: boolean;
  onOpenDM?: (userId: string) => void;
}) {
  return (
    <span className="ownership-roster__person">
      <span className="ownership-roster__name">
        {onOpenDM && !isSelf ? (
          <button
            type="button"
            className="ownership-roster__identity"
            onClick={() => onOpenDM(member.userId)}
          >
            {member.displayName}
          </button>
        ) : (
          member.displayName
        )}
        {isSelf && <span className="ownership-roster__self"> {participantCopy.self}</span>}
      </span>
      {member.role !== "member" && (
        <small className={`ownership-roster__badge ownership-roster__badge--${member.role}`}>
          <span className="material-symbols-outlined" aria-hidden="true">
            {member.role === "owner" ? "key" : "shield"}
          </span>
          <span>{roleLabels[member.role]}</span>
        </small>
      )}
    </span>
  );
}

function OwnershipRow({
  participant,
  onAction,
  onRemove,
  workspaceId,
  presence,
  onOpenDM,
}: {
  workspaceId: string;
  presence: TargetPresence;
  onOpenDM?: (userId: string) => void;
  participant: OwnershipParticipantView;
  onAction: OpenOwnershipAction;
  onRemove: Props["onRemove"];
}) {
  const { member, isCurrentUser: isSelf, actions } = participant;
  function dispatch(id: ParticipantMenuAction["id"], trigger: HTMLButtonElement) {
    if (id === "remove") onRemove(member, trigger);
    else if (id === "transfer") onAction({ type: "transfer", member }, trigger);
    else onAction({ type: "role", member, role: id }, trigger);
  }
  return (
    <li className="ownership-roster__row">
      <span className="ownership-roster__avatar">
        <UserAvatar
          workspaceId={workspaceId}
          displayName={member.displayName}
          avatarUrl={member.avatarUrl}
          userId={member.userId}
          presence={selectTargetPresence(presence, member.userId)}
        />
      </span>
      <OwnershipIdentity member={member} isSelf={isSelf} onOpenDM={onOpenDM} />
      {actions.length > 0 && (
        <OwnershipMemberMenu
          label={participantCopy.actions(member.displayName)}
          actions={actions}
          onAction={dispatch}
        />
      )}
    </li>
  );
}

export default function OwnershipRoster({ addButtonRef, ...props }: Props) {
  const [expanded, setExpanded] = useState(false);
  const [search, setSearch] = useState("");
  const [filter, setFilter] = useState("");
  const participants = useMemo(
    () => ownershipParticipants(props.ownership.members, props.currentUserId),
    [props.ownership.members, props.currentUserId],
  );
  const matching = filterOwnershipParticipants(participants, search, filter);
  const visible = expanded ? matching : matching.slice(0, 5);
  return (
    <section className="ownership-roster" aria-labelledby="ownership-roster-heading">
      <h3 className="chat-details__label" id="ownership-roster-heading" tabIndex={-1}>
        Participantes ({props.ownership.members.length})
      </h3>
      {expanded && (
        <div className="ownership-roster__filters">
          <label>
            Buscar participante
            <input
              placeholder="Nome do participante"
              value={search}
              onChange={(event) => setSearch(event.target.value)}
            />
          </label>
          <label>
            Papel
            <select value={filter} onChange={(event) => setFilter(event.target.value)}>
              {Object.entries(participantCopy.filters).map(([role, label]) => (
                <option key={role} value={role}>
                  {label}
                </option>
              ))}
            </select>
          </label>
        </div>
      )}
      <ul>
        {visible.map((participant) => (
          <OwnershipRow
            key={participant.member.userId}
            participant={participant}
            onAction={props.onAction}
            onRemove={props.onRemove}
            workspaceId={props.workspaceId}
            presence={props.presence ?? emptyTargetPresence}
            onOpenDM={props.onOpenDM}
          />
        ))}
      </ul>
      {visible.length === 0 && (
        <p className="ownership-roster__empty">Nenhum participante encontrado.</p>
      )}
      {!expanded && props.ownership.members.length > 0 && (
        <button
          type="button"
          className="chat-details__link-action"
          onClick={() => setExpanded(true)}
        >
          Ver todos
        </button>
      )}
      {props.ownership.capabilities.addMembers === true && (
        <button
          type="button"
          ref={addButtonRef}
          className="chat-details__wide-action"
          onClick={props.onAdd}
        >
          Adicionar membros
        </button>
      )}
      {props.ownership.capabilities.leave === true && (
        <button
          type="button"
          className="ownership-roster__leave"
          onClick={(event) => props.onAction({ type: "leave" }, event.currentTarget)}
        >
          Sair da conversa
        </button>
      )}
    </section>
  );
}
