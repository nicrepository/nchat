import type { KeyboardEvent, ReactNode, Ref } from "react";

import { ApiRequestError } from "../lib/api";
import type { DMCandidate } from "./chatTypes";
import type { MemberSearch } from "./useMemberPicker";
import { UserAvatar } from "./UserAvatar";

/**
 * The people-search surface shared by the Pessoa and Grupo flows of "Nova
 * conversa" (issue #1023): the field, the idle/loading/error/empty states and
 * the result list. The search itself is useMemberSearch; what a result row
 * *does* — open a DM, toggle a group member — is the flow's, passed in as
 * `renderCandidate`.
 */

function searchErrorMessage(error: unknown): string {
  if (error instanceof ApiRequestError && error.status === 429) {
    return "Muitas buscas em sequência. Aguarde um momento e tente novamente.";
  }
  if (error instanceof ApiRequestError && error.status === 403) {
    return "Você não tem acesso à busca de pessoas.";
  }
  return "Não foi possível buscar pessoas. Tente novamente.";
}

interface PeopleSearchFieldProps {
  inputId: string;
  search: MemberSearch;
  inputRef?: Ref<HTMLInputElement>;
  /** Called after every edit, so the flow can clear a stale submit error. */
  onEdit: () => void;
  /** Lets a flow inside a <form> keep Enter from submitting it. */
  onKeyDown?: (event: KeyboardEvent<HTMLInputElement>) => void;
  /** Flow-specific fields rendered under the search box. */
  children?: ReactNode;
}

export function PeopleSearchField({
  inputId,
  search,
  inputRef,
  onEdit,
  onKeyDown,
  children,
}: PeopleSearchFieldProps) {
  return (
    <div className="new-dm-dialog__search">
      <label htmlFor={inputId}>Pesquisar pessoa</label>
      <div className="new-dm-dialog__search-field">
        <span className="material-symbols-outlined" aria-hidden="true">
          search
        </span>
        <input
          ref={inputRef}
          id={inputId}
          type="search"
          autoComplete="off"
          maxLength={64}
          placeholder="Digite um nome"
          value={search.query}
          onKeyDown={onKeyDown}
          onChange={(event) => {
            search.setQuery(event.target.value);
            onEdit();
          }}
        />
      </div>
      {children}
    </div>
  );
}

function SearchStatusMessage({ search }: { search: MemberSearch }) {
  switch (search.status) {
    case "idle":
      return <p className="new-dm-dialog__hint">Digite pelo menos 2 caracteres.</p>;
    case "loading":
      return (
        <p className="new-dm-dialog__status" role="status">
          Buscando pessoas…
        </p>
      );
    case "error":
      return (
        <div className="new-dm-dialog__error" role="alert">
          <span>{searchErrorMessage(search.error)}</span>
          <button type="button" onClick={search.retry}>
            Tentar novamente
          </button>
        </div>
      );
    default:
      return search.results.length === 0 ? (
        <p className="new-dm-dialog__status">Nenhuma pessoa encontrada.</p>
      ) : null;
  }
}

interface PeopleSearchResultsProps {
  search: MemberSearch;
  renderCandidate: (candidate: DMCandidate) => ReactNode;
  /** The flow's own submit failure, already mapped to generic copy. */
  submitError: string;
}

export function PeopleSearchResults({
  search,
  renderCandidate,
  submitError,
}: PeopleSearchResultsProps) {
  const showList = search.status === "ready" && search.results.length > 0;
  return (
    <div className="new-dm-dialog__results" aria-live="polite">
      <SearchStatusMessage search={search} />
      {showList && (
        <ul className="new-dm-dialog__list" aria-label="Pessoas encontradas">
          {search.results.map((candidate) => (
            <li key={candidate.userId}>{renderCandidate(candidate)}</li>
          ))}
        </ul>
      )}
      {submitError && (
        <p className="new-dm-dialog__error new-dm-dialog__error--open" role="alert">
          {submitError}
        </p>
      )}
    </div>
  );
}

/** Avatar and name of a result row; the row's button belongs to the flow. */
export function CandidateIdentity({
  candidate,
  workspaceId,
}: {
  candidate: DMCandidate;
  workspaceId: string;
}) {
  return (
    <>
      <span className="new-dm-dialog__avatar" aria-hidden="true">
        <UserAvatar
          userId={candidate.userId}
          workspaceId={workspaceId}
          displayName={candidate.displayName}
        />
      </span>
      <span>{candidate.displayName}</span>
    </>
  );
}
