import { type KeyboardEvent, useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";

import { ApiRequestError } from "../lib/api";
import { createGroupDM, getOrCreateDirectDM, searchDMCandidates } from "./chatApi";
import type { DMCandidate } from "./chatTypes";
import {
  limitGroupTitleInput,
  MAX_GROUP_MEMBERS,
  MIN_GROUP_MEMBERS,
  toggleGroupMember,
} from "./dmGroupForm";

const SEARCH_MIN_LENGTH = 2;
const SEARCH_DEBOUNCE_MS = 150;

type ConversationMode = "direct" | "group";

/**
 * The one in-flight write, if any. A single value instead of a pair of booleans:
 * "opening a DM" and "creating a group" are mutually exclusive, and modelling
 * them separately allows a fourth, meaningless state where both are true.
 */
type Submission = { kind: "idle" } | { kind: "direct"; userId: string } | { kind: "group" };

interface NewDirectMessageDialogProps {
  currentUserId: string;
  onClose: () => void;
  onOpened: (conversationId: string) => void;
}

type SearchStatus = "idle" | "loading" | "ready" | "error";

function initials(name: string): string {
  return name
    .trim()
    .split(/\s+/)
    .slice(0, 2)
    .map((part) => part[0])
    .join("")
    .toUpperCase();
}

function searchErrorMessage(error: unknown): string {
  if (error instanceof ApiRequestError && error.status === 429) {
    return "Muitas buscas em sequência. Aguarde um momento e tente novamente.";
  }
  if (error instanceof ApiRequestError && error.status === 403) {
    return "Você não tem acesso à busca de pessoas.";
  }
  return "Não foi possível buscar pessoas. Tente novamente.";
}

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

interface ConversationModeChoiceProps {
  mode: ConversationMode;
  disabled: boolean;
  onChange: (mode: ConversationMode) => void;
}

function ConversationModeChoice({ mode, disabled, onChange }: ConversationModeChoiceProps) {
  return (
    <fieldset className="new-dm-dialog__mode">
      <legend>Tipo de conversa</legend>
      {(
        [
          ["direct", "Pessoa"],
          ["group", "Grupo"],
        ] as const
      ).map(([value, label]) => (
        <label
          key={value}
          className={mode === value ? "new-dm-dialog__mode-option--on" : undefined}
        >
          <input
            type="radio"
            name="new-dm-mode"
            value={value}
            checked={mode === value}
            disabled={disabled}
            onChange={() => onChange(value)}
          />
          {label}
        </label>
      ))}
    </fieldset>
  );
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

export default function NewDirectMessageDialog({
  currentUserId,
  onClose,
  onOpened,
}: NewDirectMessageDialogProps) {
  const [mode, setMode] = useState<ConversationMode>("direct");
  const [query, setQuery] = useState("");
  const [candidates, setCandidates] = useState<DMCandidate[]>([]);
  const [searchStatus, setSearchStatus] = useState<SearchStatus>("idle");
  const [searchError, setSearchError] = useState("");
  const [searchAttempt, setSearchAttempt] = useState(0);
  const [openError, setOpenError] = useState("");
  const [submission, setSubmission] = useState<Submission>({ kind: "idle" });
  // Selected people live outside `candidates` so a new query never drops them,
  // and outside any store because they are meaningless once the modal closes.
  const [selected, setSelected] = useState<DMCandidate[]>([]);
  const [groupTitle, setGroupTitle] = useState("");
  const searchInputRef = useRef<HTMLInputElement>(null);
  const dialogRef = useRef<HTMLDivElement>(null);
  // State updates are asynchronous, so `submission` cannot stop a second click
  // fired in the same tick; this ref is what actually makes submission single.
  const submittingRef = useRef(false);
  const createAbortRef = useRef<AbortController>(null);
  const mountedRef = useRef(true);

  const normalizedQuery = query.trim();
  const isGroup = mode === "group";
  const busy = submission.kind !== "idle";
  const atCapacity = selected.length >= MAX_GROUP_MEMBERS;

  useEffect(() => {
    mountedRef.current = true;
    searchInputRef.current?.focus();
    return () => {
      mountedRef.current = false;
      createAbortRef.current?.abort();
    };
  }, []);

  useEffect(() => {
    if (normalizedQuery.length < SEARCH_MIN_LENGTH) return;

    const controller = new AbortController();
    let active = true;

    const timer = window.setTimeout(() => {
      void searchDMCandidates(normalizedQuery, controller.signal).then(
        (results) => {
          if (!active) return;
          setCandidates(results.filter((candidate) => candidate.userId !== currentUserId));
          setSearchStatus("ready");
        },
        (error: unknown) => {
          if (!active) return;
          setSearchError(searchErrorMessage(error));
          setSearchStatus("error");
        },
      );
    }, SEARCH_DEBOUNCE_MS);

    return () => {
      active = false;
      window.clearTimeout(timer);
      controller.abort();
    };
  }, [currentUserId, normalizedQuery, searchAttempt]);

  function requestClose() {
    if (!submittingRef.current) onClose();
  }

  function handleKeyDown(event: KeyboardEvent<HTMLDivElement>) {
    if (event.key === "Escape") {
      event.preventDefault();
      requestClose();
      return;
    }
    if (event.key !== "Tab") return;

    const focusable = dialogRef.current?.querySelectorAll<HTMLElement>(
      "button:not(:disabled), input:not(:disabled)",
    );
    if (!focusable?.length) return;
    const first = focusable[0];
    const last = focusable[focusable.length - 1];
    if (event.shiftKey && document.activeElement === first) {
      event.preventDefault();
      last.focus();
    } else if (!event.shiftKey && document.activeElement === last) {
      event.preventDefault();
      first.focus();
    }
  }

  // Selecting the caller is impossible by construction: the search effect filters
  // them out of `candidates`, and chips can only hold what was selected there.
  // Both entry points are also disabled while a write is in flight, so neither
  // this nor changeMode re-checks it.
  function toggleMember(candidate: DMCandidate) {
    setOpenError("");
    setSelected((current) => toggleGroupMember(current, candidate));
  }

  function changeMode(next: ConversationMode) {
    setMode(next);
    setOpenError("");
  }

  /**
   * Runs the single write this dialog can make, whichever it is.
   *
   * On failure the modal stays open with the selection and the title intact, so
   * a retry costs one click. Nothing is cleared on success either — the parent
   * unmounts the dialog once the conversation is open. An unmount aborts the
   * request and the mounted guard keeps a late response from touching state.
   */
  async function submit(
    pending: Submission,
    action: (signal: AbortSignal) => Promise<string>,
    toMessage: (error: unknown) => string,
  ) {
    if (submittingRef.current) return;
    submittingRef.current = true;
    setSubmission(pending);
    setOpenError("");
    const controller = new AbortController();
    createAbortRef.current = controller;

    try {
      const conversationId = await action(controller.signal);
      if (mountedRef.current) onOpened(conversationId);
    } catch (error) {
      if (mountedRef.current) setOpenError(toMessage(error));
    } finally {
      submittingRef.current = false;
      if (mountedRef.current) setSubmission({ kind: "idle" });
    }
  }

  function openConversation(candidate: DMCandidate) {
    void submit(
      { kind: "direct", userId: candidate.userId },
      (signal) =>
        getOrCreateDirectDM(candidate.userId, signal).then((result) => result.conversationId),
      openErrorMessage,
    );
  }

  function submitGroup() {
    void submit(
      { kind: "group" },
      (signal) =>
        createGroupDM(
          selected.map((member) => member.userId),
          groupTitle,
          signal,
        ),
      groupErrorMessage,
    );
  }

  return createPortal(
    <div className="new-dm-dialog__backdrop" onMouseDown={requestClose}>
      <div
        ref={dialogRef}
        className="new-dm-dialog"
        role="dialog"
        aria-modal="true"
        aria-labelledby="new-dm-title"
        aria-describedby="new-dm-description"
        onKeyDown={handleKeyDown}
        onMouseDown={(event) => event.stopPropagation()}
      >
        <header className="new-dm-dialog__header">
          <div>
            <h2 id="new-dm-title">Nova mensagem</h2>
            <p id="new-dm-description">
              {isGroup
                ? "Selecione pelo menos 2 pessoas do workspace para criar um grupo."
                : "Encontre uma pessoa do workspace para conversar."}
            </p>
          </div>
          <button
            type="button"
            className="new-dm-dialog__close"
            aria-label="Fechar nova mensagem"
            disabled={busy}
            onClick={requestClose}
          >
            <span className="material-symbols-outlined" aria-hidden="true">
              close
            </span>
          </button>
        </header>

        <ConversationModeChoice mode={mode} disabled={busy} onChange={changeMode} />

        <div className="new-dm-dialog__search">
          <label htmlFor="new-dm-search">Pesquisar pessoa</label>
          <div className="new-dm-dialog__search-field">
            <span className="material-symbols-outlined" aria-hidden="true">
              search
            </span>
            <input
              ref={searchInputRef}
              id="new-dm-search"
              type="search"
              autoComplete="off"
              maxLength={64}
              placeholder="Digite um nome"
              value={query}
              onChange={(event) => {
                const value = event.target.value;
                setQuery(value);
                setCandidates([]);
                setSearchError("");
                setSearchStatus(value.trim().length >= SEARCH_MIN_LENGTH ? "loading" : "idle");
                setOpenError("");
              }}
            />
          </div>

          {isGroup && (
            <>
              <GroupMemberChips members={selected} disabled={busy} onRemove={toggleMember} />

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
                value={groupTitle}
                disabled={busy}
                onChange={(event) => setGroupTitle(limitGroupTitleInput(event.target.value))}
              />
            </>
          )}
        </div>

        <div className="new-dm-dialog__results" aria-live="polite">
          {searchStatus === "idle" && (
            <p className="new-dm-dialog__hint">Digite pelo menos 2 caracteres.</p>
          )}
          {searchStatus === "loading" && (
            <p className="new-dm-dialog__status" role="status">
              Buscando pessoas…
            </p>
          )}
          {searchStatus === "error" && (
            <div className="new-dm-dialog__error" role="alert">
              <span>{searchError}</span>
              <button
                type="button"
                onClick={() => {
                  setSearchStatus("loading");
                  setSearchError("");
                  setSearchAttempt((attempt) => attempt + 1);
                }}
              >
                Tentar novamente
              </button>
            </div>
          )}
          {searchStatus === "ready" && candidates.length === 0 && (
            <p className="new-dm-dialog__status">Nenhuma pessoa encontrada.</p>
          )}
          {searchStatus === "ready" && candidates.length > 0 && (
            <ul className="new-dm-dialog__list" aria-label="Pessoas encontradas">
              {candidates.map((candidate) => {
                const picked = selected.some((member) => member.userId === candidate.userId);
                return (
                  <li key={candidate.userId}>
                    <button
                      type="button"
                      aria-pressed={isGroup ? picked : undefined}
                      disabled={busy || (isGroup && atCapacity && !picked)}
                      onClick={() =>
                        isGroup ? toggleMember(candidate) : openConversation(candidate)
                      }
                    >
                      <span className="new-dm-dialog__avatar" aria-hidden="true">
                        {initials(candidate.displayName) || "?"}
                      </span>
                      <span>{candidate.displayName}</span>
                      {submission.kind === "direct" && submission.userId === candidate.userId && (
                        <span className="new-dm-dialog__opening" role="status">
                          Abrindo…
                        </span>
                      )}
                    </button>
                  </li>
                );
              })}
            </ul>
          )}
          {openError && (
            <p className="new-dm-dialog__error new-dm-dialog__error--open" role="alert">
              {openError}
            </p>
          )}
        </div>

        {isGroup && (
          <GroupSubmitFooter
            selectedCount={selected.length}
            pending={submission.kind === "group"}
            disabled={selected.length < MIN_GROUP_MEMBERS || busy}
            onSubmit={submitGroup}
          />
        )}
      </div>
    </div>,
    document.body,
  );
}
