import type { KeyboardEvent } from "react";

import { maxAddMembersPerRequest } from "./addMembersLimits";
import { CandidateIdentity, PeopleSearchField, PeopleSearchResults } from "./PeopleSearch";
import type { MemberPicker } from "./useMemberPicker";

interface PrivateChannelMembersFieldProps {
  picker: MemberPicker;
  workspaceId: string;
  disabled: boolean;
  /** Called after every edit, so the form can clear a stale submit error. */
  onEdit: () => void;
}

/** "1 membro" / "3 membros": the total always counts the creator. */
function privateChannelMemberCount(inviteeCount: number): string {
  const total = inviteeCount + 1;
  return `${total} ${total === 1 ? "membro" : "membros"}`;
}

/**
 * Enter in the search box looks for people; it must not submit the channel
 * form around it and create the channel halfway through choosing its members.
 */
function keepEnterInSearch(event: KeyboardEvent<HTMLInputElement>) {
  if (event.key === "Enter") event.preventDefault();
}

/**
 * The members step of a private channel (issue #1025).
 *
 * Presentation only. The selection lives in the form's useMemberPicker, so it
 * survives Privado → Público → Privado: this section unmounts while Público is
 * chosen and finds the same people when it comes back. The creator is shown as
 * a fixed member with no remove control — the server adds them on its own, and
 * nothing here could take that away. Which people are eligible is the server's
 * decision at submit time; the search result is never trusted for it.
 */
export function PrivateChannelMembersField({
  picker,
  workspaceId,
  disabled,
  onEdit,
}: PrivateChannelMembersFieldProps) {
  return (
    <fieldset className="new-dm-dialog__members" aria-describedby="new-channel-members-count">
      <legend>Membros do canal</legend>
      <PeopleSearchField
        inputId="new-channel-member-search"
        search={picker}
        onEdit={onEdit}
        onKeyDown={keepEnterInSearch}
      >
        <ul className="new-dm-dialog__chips" aria-label="Membros selecionados">
          <li className="new-dm-dialog__chip new-dm-dialog__chip--fixed">
            <span>Você (criador)</span>
          </li>
          {picker.selected.map((member) => (
            <li key={member.userId} className="new-dm-dialog__chip">
              <span>{member.displayName}</span>
              <button
                type="button"
                aria-label={`Remover ${member.displayName}`}
                disabled={disabled}
                onClick={() => {
                  picker.remove(member.userId);
                  onEdit();
                }}
              >
                <span className="material-symbols-outlined" aria-hidden="true">
                  close
                </span>
              </button>
            </li>
          ))}
        </ul>
      </PeopleSearchField>

      <PeopleSearchResults
        search={picker}
        submitError=""
        renderCandidate={(candidate) => (
          <button
            type="button"
            disabled={disabled || picker.atCapacity}
            onClick={() => {
              picker.select(candidate);
              onEdit();
            }}
          >
            <CandidateIdentity candidate={candidate} workspaceId={workspaceId} />
          </button>
        )}
      />

      <p id="new-channel-members-count" className="new-dm-dialog__footer-hint" aria-live="polite">
        {privateChannelMemberCount(picker.selected.length)}
        {picker.atCapacity
          ? ` — limite de ${maxAddMembersPerRequest} convidados por criação atingido.`
          : ", incluindo você."}
      </p>
    </fieldset>
  );
}
