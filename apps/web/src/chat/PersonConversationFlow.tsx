import { type Ref, useState } from "react";

import { ApiRequestError } from "../lib/api";
import { getOrCreateDirectDM, searchDMCandidates } from "./chatApi";
import type { DMCandidate } from "./chatTypes";
import { CandidateIdentity, PeopleSearchField, PeopleSearchResults } from "./PeopleSearch";
import { useMemberSearch } from "./useMemberPicker";
import { useSingleSubmission } from "./useSingleSubmission";

function openErrorMessage(error: unknown): string {
  if (error instanceof ApiRequestError) {
    if (error.status === 403 || error.status === 404) {
      return "Esta pessoa não está disponível para mensagens.";
    }
    if (error.status === 409) return "A conversa mudou. Tente novamente.";
    if (error.status === 429) {
      return "Muitas solicitações em sequência. Aguarde um momento e tente novamente.";
    }
    if (error.status === 0) return "Sem conexão. Verifique sua rede e tente novamente.";
  }
  return "Não foi possível abrir a conversa. Tente novamente.";
}

interface PersonConversationFlowProps {
  currentUserId: string;
  workspaceId: string;
  /** The dialog's initial focus target; the shell decides when to focus it. */
  inputRef: Ref<HTMLInputElement>;
  onOpened: (conversationId: string) => void;
  onPendingChange: (pending: boolean) => void;
}

/**
 * "Nova conversa" → Pessoa: one click on a result opens (or creates) the 1:1.
 *
 * There is no draft to keep: the only state is the query, so the shell unmounts
 * this flow when another mode is chosen, which also cancels its search.
 * `getOrCreateDirectDM` is idempotent server-side and the server decides
 * whether the person may be messaged; a refusal arrives as a status mapped to
 * generic copy.
 */
export default function PersonConversationFlow({
  currentUserId,
  workspaceId,
  inputRef,
  onOpened,
  onPendingChange,
}: PersonConversationFlowProps) {
  // The caller is filtered even if the backend returns them.
  const search = useMemberSearch({
    search: searchDMCandidates,
    excludedUserIds: [currentUserId],
  });
  const submission = useSingleSubmission(onPendingChange);
  const [openingUserId, setOpeningUserId] = useState("");

  function open(candidate: DMCandidate) {
    const started = submission.run(
      (signal) =>
        getOrCreateDirectDM(candidate.userId, signal).then((result) => result.conversationId),
      onOpened,
      openErrorMessage,
    );
    if (started) setOpeningUserId(candidate.userId);
  }

  return (
    <>
      <PeopleSearchField
        inputId="new-dm-search"
        search={search}
        inputRef={inputRef}
        onEdit={() => submission.setError("")}
      />
      <PeopleSearchResults
        search={search}
        submitError={submission.error}
        renderCandidate={(candidate) => (
          <button type="button" disabled={submission.pending} onClick={() => open(candidate)}>
            <CandidateIdentity candidate={candidate} workspaceId={workspaceId} />
            {submission.pending && openingUserId === candidate.userId && (
              <span className="new-dm-dialog__opening" role="status">
                Abrindo…
              </span>
            )}
          </button>
        )}
      />
    </>
  );
}
