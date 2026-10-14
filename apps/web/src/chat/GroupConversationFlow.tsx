import { useEffect, useRef, useState } from "react";

import { ApiRequestError } from "../lib/api";
import { createGroupDM, searchDMCandidates } from "./chatApi";
import type { DMCandidate } from "./chatTypes";
import { MAX_GROUP_MEMBERS, MIN_GROUP_MEMBERS, toggleGroupMember } from "./dmGroupForm";
import { AUTOMATIC_IDENTITY, type GroupIdentity, persistedEmoji } from "./groupIdentity";
import GroupIdentityStep from "./GroupIdentityStep";
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
  onRemove: (member: DMCandidate) => void;
}

function GroupMemberChips({ members, onRemove }: GroupMemberChipsProps) {
  if (members.length === 0) return null;
  return (
    <ul className="new-dm-dialog__chips" aria-label="Pessoas selecionadas">
      {members.map((member) => (
        <li key={member.userId} className="new-dm-dialog__chip">
          <span>{member.displayName}</span>
          <button
            type="button"
            aria-label={`Remover ${member.displayName}`}
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

interface GroupContinueFooterProps {
  selectedCount: number;
  disabled: boolean;
  onContinue: () => void;
}

function GroupContinueFooter({ selectedCount, disabled, onContinue }: GroupContinueFooterProps) {
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
        onClick={onContinue}
      >
        Continuar
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
 * "Nova conversa" → Grupo (RF-02): pick people (Participantes), then name the
 * group and choose its identity (Identidade, issue #1026), then create it.
 *
 * The draft — selection, query, title and identity — is plain local state
 * held here, above both steps, so moving between them loses nothing and no
 * request is made before "Criar grupo". The shell
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
  const [identity, setIdentity] = useState<GroupIdentity>(AUTOMATIC_IDENTITY);
  const [step, setStep] = useState<"participants" | "identity">("participants");
  const searchRef = useRef<HTMLInputElement>(null);
  const returnedRef = useRef(false);
  const busy = submission.pending;
  const atCapacity = selected.length >= MAX_GROUP_MEMBERS;

  // Back from Identidade, focus lands where the person left off: the search.
  // Only on that return — the first render must not take focus from the mode
  // selector, and the Identidade step focuses its own first field.
  useEffect(() => {
    if (step === "participants" && returnedRef.current) searchRef.current?.focus();
  }, [step]);

  function goTo(next: "participants" | "identity") {
    submission.setError("");
    returnedRef.current = next === "participants";
    setStep(next);
  }

  function toggle(candidate: DMCandidate) {
    setSelected((current) => toggleGroupMember(current, candidate));
  }

  function submit() {
    submission.run(
      (signal) =>
        createGroupDM(
          selected.map((member) => member.userId),
          title,
          persistedEmoji(identity),
          signal,
        ),
      onOpened,
      groupErrorMessage,
    );
  }

  if (step === "identity") {
    return (
      <GroupIdentityStep
        title={title}
        identity={identity}
        currentUserId={currentUserId}
        pending={busy}
        submitError={submission.error}
        onTitleChange={setTitle}
        onIdentityChange={setIdentity}
        onBack={() => goTo("participants")}
        onSubmit={submit}
      />
    );
  }

  return (
    <>
      <PeopleSearchField
        inputId="new-dm-group-search"
        inputRef={searchRef}
        search={search}
        onEdit={() => submission.setError("")}
      >
        <GroupMemberChips members={selected} onRemove={toggle} />
      </PeopleSearchField>

      <PeopleSearchResults
        search={search}
        submitError=""
        renderCandidate={(candidate) => {
          const picked = selected.some((member) => member.userId === candidate.userId);
          return (
            <button
              type="button"
              aria-pressed={picked}
              disabled={atCapacity && !picked}
              onClick={() => toggle(candidate)}
            >
              <CandidateIdentity candidate={candidate} workspaceId={workspaceId} />
            </button>
          );
        }}
      />

      <GroupContinueFooter
        selectedCount={selected.length}
        disabled={selected.length < MIN_GROUP_MEMBERS}
        onContinue={() => goTo("identity")}
      />
    </>
  );
}
