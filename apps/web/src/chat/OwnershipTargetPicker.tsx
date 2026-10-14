import { useId, useState } from "react";
import type { OwnershipMember } from "./ownershipApi";
import { roleLabels } from "./ownershipPresentation";
import { ownershipCopy } from "./ownershipDialogState";
import { UserAvatar } from "./UserAvatar";

export function OwnershipPerson({
  member,
  workspaceId,
}: {
  member: OwnershipMember;
  workspaceId: string;
}) {
  return (
    <span className="ownership-picker__person">
      <span className="ownership-picker__avatar">
        <UserAvatar
          workspaceId={workspaceId}
          userId={member.userId}
          displayName={member.displayName}
          avatarUrl={member.avatarUrl}
          size="sm"
        />
      </span>
      <span>
        <strong>{member.displayName}</strong>
        <small>{roleLabels[member.role]}</small>
      </span>
    </span>
  );
}

export default function OwnershipTargetPicker({
  candidates,
  target,
  onChange,
  disabled,
  workspaceId,
}: {
  candidates: OwnershipMember[];
  target: string;
  onChange: (id: string) => void;
  disabled: boolean;
  workspaceId: string;
}) {
  const [search, setSearch] = useState("");
  const name = useId();
  const normalize = (value: string) =>
    value
      .normalize("NFD")
      .replace(/\p{Diacritic}/gu, "")
      .toLowerCase();
  const matching = candidates.filter((member) =>
    normalize(member.displayName).includes(normalize(search.trim())),
  );
  return (
    <>
      <div className="ownership-picker__search">
        <label htmlFor={`${name}-search`}>{ownershipCopy.search}</label>
        <span className="ownership-picker__search-field">
          <span className="material-symbols-outlined" aria-hidden="true">
            search
          </span>
          <input
            id={`${name}-search`}
            type="search"
            placeholder="Nome do participante"
            value={search}
            onChange={(event) => setSearch(event.target.value)}
            disabled={disabled}
          />
        </span>
      </div>
      <fieldset className="ownership-picker" disabled={disabled}>
        <legend>{ownershipCopy.target}</legend>
        {matching.map((member) => (
          <label key={member.userId} className="ownership-picker__option">
            <OwnershipPerson member={member} workspaceId={workspaceId} />
            <input
              type="radio"
              name={name}
              value={member.userId}
              checked={target === member.userId}
              onChange={() => onChange(member.userId)}
              aria-label={`${member.displayName}, ${roleLabels[member.role]}`}
            />
          </label>
        ))}
        {matching.length === 0 && <p role="status">{ownershipCopy.noCandidates}</p>}
      </fieldset>
    </>
  );
}
