import { useRef, useState } from "react";
import OwnershipActionDialog from "./OwnershipActionDialog";
import OwnershipMemberMenu from "./OwnershipMemberMenu";
import { type OwnershipAction as Action, roleLabels } from "./ownershipPresentation";
import { emptyTargetPresence, selectTargetPresence, type TargetPresence } from "./presence";
import { UserAvatar } from "./UserAvatar";
import { type ConversationRole, type OwnershipDetails, type OwnershipMember } from "./ownershipApi";
import "./OwnershipRoster.css";

interface Props {
  kind: "channel" | "group";
  id: string;
  workspaceId: string;
  ownership: OwnershipDetails;
  currentUserId: string;
  reload: () => void;
  onAdd: () => void;
  onCommitted?: (message: string) => void;
  onOpenDM?: (userId: string) => void;
  addButtonRef?: React.Ref<HTMLButtonElement>;
  presence?: TargetPresence;
  onRemove: (member: OwnershipMember, trigger: HTMLElement) => void;
}
function RoleActions({
  member,
  onRole,
}: {
  member: OwnershipMember;
  onRole: (role: ConversationRole) => void;
}) {
  if (member.actions.assignRole !== true) return null;
  return (
    <>
      {(["owner", "admin", "member"] as const)
        .filter((role) => role !== member.role)
        .map((role) => (
          <button key={role} type="button" onClick={() => onRole(role)}>
            {role === "owner"
              ? "Tornar proprietário"
              : role === "admin"
                ? "Tornar administrador"
                : "Tornar membro"}
          </button>
        ))}
    </>
  );
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
        {isSelf && <span className="ownership-roster__self"> [Você]</span>}
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
  member,
  isSelf,
  onAction,
  onRemove,
  workspaceId,
  presence,
  onOpenDM,
}: {
  workspaceId: string;
  presence: TargetPresence;
  onOpenDM?: (userId: string) => void;
  member: OwnershipMember;
  isSelf: boolean;
  onAction: (action: Action) => void;
  onRemove: Props["onRemove"];
}) {
  const canAct =
    member.actions.assignRole === true ||
    member.actions.remove === true ||
    member.actions.transfer === true;
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
      {canAct && (
        <OwnershipMemberMenu label={`Ações de ${member.displayName}`}>
          <RoleActions
            member={member}
            onRole={(role) => onAction({ type: "role", member, role })}
          />
          {isSelf && member.actions.transfer === true && (
            <button type="button" onClick={() => onAction({ type: "transfer" })}>
              Transferir minha propriedade
            </button>
          )}
          {member.actions.remove === true && (
            <button
              type="button"
              className="ownership-member-menu__danger"
              onClick={() => onRemove(member, document.activeElement as HTMLElement)}
            >
              Remover membro
            </button>
          )}
        </OwnershipMemberMenu>
      )}
    </li>
  );
}

export default function OwnershipRoster({ addButtonRef, ...props }: Props) {
  const [expanded, setExpanded] = useState(false);
  const [search, setSearch] = useState("");
  const [filter, setFilter] = useState("");
  const [action, setAction] = useState<Action | null>(null);
  const focus = useRef<HTMLElement | null>(null);
  const heading = useRef<HTMLHeadingElement>(null);
  function openAction(next: Action) {
    focus.current = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    setAction(next);
  }
  function closeAction() {
    setAction(null);
    if (focus.current?.isConnected) focus.current.focus();
    else heading.current?.focus();
  }
  const matching = props.ownership.members.filter(
    (member) =>
      member.displayName.toLocaleLowerCase("pt-BR").includes(search.toLocaleLowerCase("pt-BR")) &&
      (filter === "" || member.role === filter),
  );
  const visible = expanded ? matching : matching.slice(0, 5);
  return (
    <section className="ownership-roster" aria-labelledby="ownership-roster-heading">
      <h3 className="chat-details__label" id="ownership-roster-heading" ref={heading} tabIndex={-1}>
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
              <option value="">Todos</option>
              {Object.entries(roleLabels).map(([role, label]) => (
                <option key={role} value={role}>
                  {label}
                </option>
              ))}
            </select>
          </label>
        </div>
      )}
      <ul>
        {visible.map((member) => (
          <OwnershipRow
            key={member.userId}
            member={member}
            isSelf={member.userId === props.currentUserId}
            onAction={openAction}
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
      {!expanded && props.ownership.members.length > 5 && (
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
          onClick={() => openAction({ type: "leave" })}
        >
          Sair da conversa
        </button>
      )}
      {action && <OwnershipActionDialog action={action} props={props} onClose={closeAction} />}
    </section>
  );
}
