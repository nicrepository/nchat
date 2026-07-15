import { type KeyboardEvent, useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";

import { ApiRequestError } from "../lib/api";
import { getOrCreateDirectDM, searchDMCandidates } from "./chatApi";
import type { DMCandidate } from "./chatTypes";

const SEARCH_MIN_LENGTH = 2;
const SEARCH_DEBOUNCE_MS = 150;

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

export default function NewDirectMessageDialog({
  currentUserId,
  onClose,
  onOpened,
}: NewDirectMessageDialogProps) {
  const [query, setQuery] = useState("");
  const [candidates, setCandidates] = useState<DMCandidate[]>([]);
  const [searchStatus, setSearchStatus] = useState<SearchStatus>("idle");
  const [searchError, setSearchError] = useState("");
  const [searchAttempt, setSearchAttempt] = useState(0);
  const [pendingUserId, setPendingUserId] = useState<string>();
  const [openError, setOpenError] = useState("");
  const searchInputRef = useRef<HTMLInputElement>(null);
  const dialogRef = useRef<HTMLDivElement>(null);
  const submittingRef = useRef(false);
  const createAbortRef = useRef<AbortController>(null);
  const mountedRef = useRef(true);

  const normalizedQuery = query.trim();

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

  async function openConversation(candidate: DMCandidate) {
    if (submittingRef.current || candidate.userId === currentUserId) return;
    submittingRef.current = true;
    setPendingUserId(candidate.userId);
    setOpenError("");
    const controller = new AbortController();
    createAbortRef.current = controller;

    try {
      const result = await getOrCreateDirectDM(candidate.userId, controller.signal);
      if (mountedRef.current) onOpened(result.conversationId);
    } catch (error) {
      if (mountedRef.current) setOpenError(openErrorMessage(error));
    } finally {
      submittingRef.current = false;
      if (mountedRef.current) setPendingUserId(undefined);
    }
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
            <p id="new-dm-description">Encontre uma pessoa do workspace para conversar.</p>
          </div>
          <button
            type="button"
            className="new-dm-dialog__close"
            aria-label="Fechar nova mensagem"
            disabled={pendingUserId !== undefined}
            onClick={requestClose}
          >
            <span className="material-symbols-outlined" aria-hidden="true">
              close
            </span>
          </button>
        </header>

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
              {candidates.map((candidate) => (
                <li key={candidate.userId}>
                  <button
                    type="button"
                    disabled={pendingUserId !== undefined}
                    onClick={() => void openConversation(candidate)}
                  >
                    <span className="new-dm-dialog__avatar" aria-hidden="true">
                      {initials(candidate.displayName) || "?"}
                    </span>
                    <span>{candidate.displayName}</span>
                    {pendingUserId === candidate.userId && (
                      <span className="new-dm-dialog__opening" role="status">
                        Abrindo…
                      </span>
                    )}
                  </button>
                </li>
              ))}
            </ul>
          )}
          {openError && (
            <p className="new-dm-dialog__error new-dm-dialog__error--open" role="alert">
              {openError}
            </p>
          )}
        </div>
      </div>
    </div>,
    document.body,
  );
}
