import { useState } from "react";

import { ApiRequestError } from "../lib/api";
import { createGroupDM, searchDMCandidates } from "./chatApi";
import type { DMCandidate } from "./chatTypes";
import {
  limitGroupTitleInput,
  MAX_GROUP_MEMBERS,
  MIN_GROUP_MEMBERS,
  toggleGroupMember,
} from "./dmGroupForm";
import { CandidateIdentity, PeopleSearchField, PeopleSearchResults } from "./PeopleSearch";
import { useMemberSearch } from "./useMemberPicker";
import { useSingleSubmission } from "./useSingleSubmission";

/**
 * Group creation fails for reasons a 1:1 cannot have (an invalid participant
 * set, a rejected title), so it needs its own wording. Server detail is never
 * surfaced: the status code alone selects a generic message, and a participant
 * who is suspended, deleted, unknown or from another workspace all read the
 * same, because the server itself refuses to distinguish them.
 */
function groupErrorMessage(error: unknown): string {
  if (error instanceof ApiRequestError) {
    if (error.status === 400) return "Revise as pessoas selecionadas e o nome do grupo.";
    if (error.status === 403 || error.status === 404) {
      return "Alguma pessoa selecionada não está disponível para conversar.";
    }
    if (error.status === 429) {
      return "Muitas solicitações em sequência. Aguarde um momento e tente novamente.";
    }
    if (error.status === 0) return "Sem conexão. Verifique sua rede e tente novamente.";
  }
  return "Não foi possível criar o grupo. Tente novamente.";
}

interface GroupMemberChipsProps {
  members: DMCandidate[];
  disabled: boolean;
  onRemove: (member: DMCandidate) => void;
}

function GroupMemberChips({ members, disabled, onRemove }: GroupMemberChipsProps) {
  if (members.length === 0) return null;
  return (
    <ul className="new-dm-dialog__chips" aria-label="Pessoas selecionadas">
      {members.map((member) => (
        <li key={member.userId} className="new-dm-dialog__chip">
          <span>{member.displayName}</span>
          <button
            type="button"
            aria-label={`Remover ${member.displayName}`}
            disabled={disabled}
            onClick={() => onRemove(member)}
          >
            <span className="material-symbols-outlined" aria-hidden="true">
              close
            </span>
          </button>
        </li>
      ))}
    </ul>
  );
}

interface GroupSubmitFooterProps {
  selectedCount: number;
  pending: boolean;
  disabled: boolean;
  onSubmit: () => void;
}

function GroupSubmitFooter({ selectedCount, pending, disabled, onSubmit }: GroupSubmitFooterProps) {
  return (
    <footer className="new-dm-dialog__footer">
      <p className="new-dm-dialog__footer-hint">
        {selectedCount >= MAX_GROUP_MEMBERS
          ? `Limite de ${MAX_GROUP_MEMBERS} pessoas atingido.`
          : `${selectedCount} de no mínimo ${MIN_GROUP_MEMBERS} pessoas selecionadas.`}
      </p>
      <button
        type="button"
        className="new-dm-dialog__submit"
        disabled={disabled}
        aria-busy={pending}
        onClick={onSubmit}
      >
        {pending ? "Criando…" : "Criar grupo"}
      </button>
    </footer>
  );
}

interface GroupConversationFlowProps {
  /** False while another mode is shown: the draft stays, the search stops. */
  active: boolean;
  currentUserId: string;
  workspaceId: string;
  onOpened: (conversationId: string) => void;
  onPendingChange: (pending: boolean) => void;
}

/**
 * "Nova conversa" → Grupo (RF-02): pick people, optionally name the group,
 * create it.
 *
 * The draft — selection, title and query — is plain local state. The shell
 * keeps this flow mounted (hidden) while another mode is visited, so the draft
 * survives a detour and disappears with the dialog; nothing reaches a store.
 * Hidden is not idle by itself, so `active` turns the search off meanwhile.
 *
 * Selection is a toggle (dmGroupForm) and lives outside the search results, so
 * a new query never drops a chosen person. None of the rules here authorises
 * anything: chat-service re-validates the participant set and the title.
 */
export default function GroupConversationFlow({
  active,
  currentUserId,
  workspaceId,
  onOpened,
  onPendingChange,
}: GroupConversationFlowProps) {
  // Selecting the caller is impossible by construction: they are filtered out
  // of the results, and chips can only hold what was selected there.
  const search = useMemberSearch({
    search: searchDMCandidates,
    excludedUserIds: [currentUserId],
    enabled: active,
  });
  const submission = useSingleSubmission(onPendingChange);
  const [selected, setSelected] = useState<DMCandidate[]>([]);
  const [title, setTitle] = useState("");
  const busy = submission.pending;
  const atCapacity = selected.length >= MAX_GROUP_MEMBERS;

  function toggle(candidate: DMCandidate) {
    submission.setError("");
    setSelected((current) => toggleGroupMember(current, candidate));
  }

  function submit() {
    submission.run(
      (signal) =>
        createGroupDM(
          selected.map((member) => member.userId),
          title,
          signal,
        ),
      onOpened,
      groupErrorMessage,
    );
  }

  return (
    <>
      <PeopleSearchField
        inputId="new-dm-group-search"
        search={search}
        onEdit={() => submission.setError("")}
      >
        <GroupMemberChips members={selected} disabled={busy} onRemove={toggle} />

        <label className="new-dm-dialog__group-name" htmlFor="new-dm-group-name">
          Nome do grupo (opcional)
        </label>
        {/* Truncation is by Unicode code point, the unit the server counts;
        the maxLength attribute would count UTF-16 units and cut an
        emoji-heavy name in half of its allowance. */}
        <input
          id="new-dm-group-name"
          type="text"
          autoComplete="off"
          placeholder="Ex.: Infraestrutura"
          value={title}
          disabled={busy}
          onChange={(event) => setTitle(limitGroupTitleInput(event.target.value))}
        />
      </PeopleSearchField>

      <PeopleSearchResults
        search={search}
        submitError={submission.error}
        renderCandidate={(candidate) => {
          const picked = selected.some((member) => member.userId === candidate.userId);
          return (
            <button
              type="button"
              aria-pressed={picked}
              disabled={busy || (atCapacity && !picked)}
              onClick={() => toggle(candidate)}
            >
              <CandidateIdentity candidate={candidate} workspaceId={workspaceId} />
            </button>
          );
        }}
      />

      <GroupSubmitFooter
        selectedCount={selected.length}
        pending={busy}
        disabled={selected.length < MIN_GROUP_MEMBERS || busy}
        onSubmit={submit}
      />
    </>
  );
}
