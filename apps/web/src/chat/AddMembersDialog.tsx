/**
 * AddMembersDialog — "Adicionar membros" for a channel or a group (issue #398).
 *
 * Opened from the details panel, never from the sidebar and never from the
 * composer, so the conversation, its scroll position and the message editor are
 * untouched: this renders into a portal as a sibling of the app root, and the
 * panel that owns it is itself a layout sibling of the conversation column.
 *
 * Security invariants:
 * - Only user IDs leave this component. The workspace, the actor and every
 *   membership field come from the session server-side.
 * - Nothing here is an authorization check. The action is hidden when the server
 *   said the caller may not manage members, but that is presentation: the POST
 *   is re-authorized, and a 403 is rendered as a refusal rather than swallowed.
 * - Every server-supplied name is a React text node. No dangerouslySetInnerHTML,
 *   no URL is built from a display name.
 *
 * The dialog shell (portal, backdrop, role="dialog", Escape, focus trap) mirrors
 * NewConversationDialog rather than introducing a second modal system.
 */

import { type KeyboardEvent, useCallback, useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";

import "./AddMembersDialog.css";
import { ApiRequestError } from "../lib/api";
import {
  addChannelMembers,
  addGroupParticipants,
  searchChannelMemberCandidates,
  searchGroupParticipantCandidates,
} from "./chatApi";
import type { AddMembersResult } from "./chatTypes";
import { maxAddMembersPerRequest } from "./addMembersLimits";
import { useMemberPicker } from "./useMemberPicker";

/**
 * Which conversation the dialog is adding to.
 *
 * A discriminated union rather than a pair of optional IDs: it makes "a channel
 * ID and a conversation ID at the same time" unrepresentable, so the submit
 * function cannot pick the wrong endpoint for the ID it was handed.
 */
export type AddMembersTarget =
  | { kind: "channel"; channelId: string }
  | { kind: "group"; conversationId: string };

interface AddMembersDialogProps {
  target: AddMembersTarget;
  /**
   * Locally-known IDs to hide from results — in practice the viewer.
   *
   * Explicitly *not* the eligibility rule: current members are excluded by the
   * search endpoint itself, in SQL. The panel only ever knows a capped,
   * presence-filtered preview of the membership, so using it here is what let
   * an offline member (channel) or the 31st participant (group) be offered.
   */
  excludedUserIds: readonly string[];
  onClose: () => void;
  /** Called after a committed add so the panel can refetch from the server. */
  onAdded: (result: AddMembersResult) => void;
}

const titleId = "chat-add-members-title";
const descriptionId = "chat-add-members-description";
const errorId = "chat-add-members-error";

/**
 * Maps a failure to copy, by status code only.
 *
 * The server's message is never surfaced. A refused *user* and a refused
 * *caller* both arrive as 403 and are told apart by nothing here, because the
 * server itself refuses to distinguish "suspended", "deleted", "another
 * workspace" and "no such account" — repeating that distinction in the UI would
 * hand back the account oracle the API declines to be.
 */
function submitErrorMessage(error: unknown): string {
  if (error instanceof ApiRequestError) {
    if (error.status === 400) return "Revise as pessoas selecionadas e tente novamente.";
    if (error.status === 403) {
      return "Você não tem permissão para adicionar pessoas a esta conversa, ou alguma pessoa selecionada não está disponível.";
    }
    if (error.status === 404) return "Esta conversa não está mais disponível.";
    // No 409 branch: channels and groups have no fixed participant capacity, so
    // "the conversation is full" is not a state this endpoint can report. An
    // oversized selection is a 400/413 about the request, handled above.
    if (error.status === 413)
      return "Seleção grande demais. Remova algumas pessoas e tente novamente.";
    if (error.status === 429) {
      return "Muitas solicitações em sequência. Aguarde um momento e tente novamente.";
    }
    if (error.status === 0) return "Sem conexão. Verifique sua rede e tente novamente.";
  }
  return "Não foi possível adicionar as pessoas. Tente novamente.";
}

function initials(name: string): string {
  return name
    .trim()
    .split(/\s+/)
    .slice(0, 2)
    .map((part) => part[0])
    .join("")
    .toUpperCase();
}

export default function AddMembersDialog({
  target,
  excludedUserIds,
  onClose,
  onAdded,
}: AddMembersDialogProps) {
  // The search is bound to the target, so a channel picker cannot query the
  // group route and vice versa. Memoised on the target identity: when the
  // conversation changes the dialog unmounts anyway, but a stable identity keeps
  // the picker's debounce effect from re-running on every render.
  const targetKind = target.kind;
  const targetId = target.kind === "channel" ? target.channelId : target.conversationId;
  const search = useCallback(
    (query: string, signal: AbortSignal) =>
      targetKind === "channel"
        ? searchChannelMemberCandidates(targetId, query, signal)
        : searchGroupParticipantCandidates(targetId, query, signal),
    [targetKind, targetId],
  );

  const picker = useMemberPicker({
    search,
    excludedUserIds,
    maxSelection: maxAddMembersPerRequest,
  });
  const [submitError, setSubmitError] = useState("");
  const [pending, setPending] = useState(false);
  const searchInputRef = useRef<HTMLInputElement>(null);
  const dialogRef = useRef<HTMLDivElement>(null);
  // State updates are asynchronous, so `pending` alone cannot stop a second
  // click fired in the same tick. This ref is what actually makes submit single.
  const submittingRef = useRef(false);
  const abortRef = useRef<AbortController | null>(null);
  const mountedRef = useRef(true);

  useEffect(() => {
    mountedRef.current = true;
    // Focus opens on the search field: the first thing this dialog exists to do
    // is find someone. The control that opened it gets focus back on close,
    // which the panel handles because it owns that button.
    searchInputRef.current?.focus();
    return () => {
      mountedRef.current = false;
      abortRef.current?.abort();
    };
  }, []);

  function requestClose() {
    // Closing mid-write would leave the user unable to see whether it landed.
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

  async function submit() {
    if (submittingRef.current || picker.selected.length === 0) return;
    submittingRef.current = true;
    setPending(true);
    setSubmitError("");
    const controller = new AbortController();
    abortRef.current = controller;
    const userIds = picker.selected.map((member) => member.userId);

    try {
      const result =
        targetKind === "channel"
          ? await addChannelMembers(targetId, userIds, controller.signal)
          : await addGroupParticipants(targetId, userIds, controller.signal);
      if (mountedRef.current) onAdded(result);
    } catch (error) {
      // The selection is deliberately left intact: a rate limit or a dropped
      // connection is recoverable, and re-picking six people to retry is not a
      // reasonable cost. The dialog also stays open so the message is readable.
      if (mountedRef.current) setSubmitError(submitErrorMessage(error));
    } finally {
      submittingRef.current = false;
      if (mountedRef.current) setPending(false);
    }
  }

  const label = target.kind === "channel" ? "ao canal" : "ao grupo";
  const hasSelection = picker.selected.length > 0;

  return createPortal(
    <div className="add-members__backdrop" onMouseDown={requestClose}>
      <div
        ref={dialogRef}
        className="add-members"
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        aria-describedby={descriptionId}
        onKeyDown={handleKeyDown}
        onMouseDown={(event) => event.stopPropagation()}
      >
        <header className="add-members__header">
          <div>
            <h2 id={titleId}>Adicionar membros</h2>
            <p id={descriptionId}>
              Encontre pessoas do workspace para adicionar {label}. Elas passam a participar
              imediatamente.
            </p>
          </div>
          <button
            type="button"
            className="add-members__close"
            aria-label="Fechar adicionar membros"
            disabled={pending}
            onClick={requestClose}
          >
            <span className="material-symbols-outlined" aria-hidden="true">
              close
            </span>
          </button>
        </header>

        <div className="add-members__search">
          <label htmlFor="add-members-search">Pesquisar pessoa</label>
          <div className="add-members__search-field">
            <span className="material-symbols-outlined" aria-hidden="true">
              search
            </span>
            <input
              ref={searchInputRef}
              id="add-members-search"
              type="search"
              autoComplete="off"
              maxLength={64}
              placeholder="Digite um nome"
              value={picker.query}
              disabled={pending}
              onChange={(event) => {
                picker.setQuery(event.target.value);
                setSubmitError("");
              }}
            />
          </div>
        </div>

        {hasSelection && (
          <ul className="add-members__chips" aria-label="Pessoas selecionadas">
            {picker.selected.map((member) => (
              <li key={member.userId} className="add-members__chip">
                <span>{member.displayName}</span>
                <button
                  type="button"
                  aria-label={`Remover ${member.displayName}`}
                  disabled={pending}
                  onClick={() => picker.remove(member.userId)}
                >
                  <span className="material-symbols-outlined" aria-hidden="true">
                    close
                  </span>
                </button>
              </li>
            ))}
          </ul>
        )}

        <div className="add-members__results" aria-live="polite">
          {picker.status === "idle" && (
            <p className="add-members__hint">Digite pelo menos 2 caracteres.</p>
          )}
          {picker.status === "loading" && (
            <p className="add-members__status" role="status">
              Buscando pessoas…
            </p>
          )}
          {picker.status === "error" && (
            <div className="add-members__error" role="alert">
              <span>Não foi possível buscar pessoas. Tente novamente.</span>
              <button type="button" onClick={picker.retry}>
                Tentar novamente
              </button>
            </div>
          )}
          {picker.status === "ready" && picker.results.length === 0 && (
            // Covers both "no match" and "everyone matching is already here":
            // the picker removes current participants from the results, so an
            // exhausted workspace lands in the same honest empty state.
            <p className="add-members__status">Nenhuma pessoa disponível para adicionar.</p>
          )}
          {picker.status === "ready" && picker.results.length > 0 && (
            <ul className="add-members__list" aria-label="Pessoas encontradas">
              {picker.results.map((candidate) => (
                <li key={candidate.userId}>
                  <button
                    type="button"
                    disabled={pending || picker.atCapacity}
                    onClick={() => {
                      picker.select(candidate);
                      setSubmitError("");
                    }}
                  >
                    <span className="add-members__avatar" aria-hidden="true">
                      {initials(candidate.displayName) || "?"}
                    </span>
                    <span>{candidate.displayName}</span>
                  </button>
                </li>
              ))}
            </ul>
          )}
        </div>

        {submitError && (
          <p id={errorId} className="add-members__error add-members__error--submit" role="alert">
            {submitError}
          </p>
        )}

        <footer className="add-members__footer">
          <p className="add-members__footer-hint">
            {picker.atCapacity
              ? `Limite de ${maxAddMembersPerRequest} pessoas por vez atingido.`
              : `${picker.selected.length} selecionada(s).`}
          </p>
          <button
            type="button"
            className="add-members__cancel"
            disabled={pending}
            onClick={requestClose}
          >
            Cancelar
          </button>
          <button
            type="button"
            className="add-members__submit"
            disabled={!hasSelection || pending}
            aria-busy={pending}
            aria-describedby={submitError ? errorId : undefined}
            onClick={() => void submit()}
          >
            {pending ? "Adicionando…" : "Adicionar"}
          </button>
        </footer>
      </div>
    </div>,
    document.body,
  );
}
