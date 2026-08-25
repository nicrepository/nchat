/**
 * RenameChannelDialog — "Renomear canal" (issue #527).
 *
 * Opened from the sidebar row's action menu. It renders into a portal as a
 * sibling of the app root, so opening it selects nothing, navigates nowhere and
 * leaves the open conversation, its scroll position and the composer untouched.
 *
 * The dialog shell (portal, backdrop, role="dialog", Escape, focus trap, the
 * `submittingRef` that makes submit single) mirrors AddMembersDialog rather than
 * introducing a second modal system.
 *
 * Security invariants:
 * - Only the channel ID and the new name leave this component. The workspace,
 *   the actor and the authorization all come from the session server-side.
 * - Nothing here is an authorization check. The menu item is hidden when the
 *   server said the caller may not rename, but that is presentation: the PATCH
 *   is re-authorized, and a 403 is rendered as a refusal rather than swallowed.
 * - The channel name is a React text node, here and everywhere else.
 */

import { type FormEvent, type KeyboardEvent, useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";

import "./RenameChannelDialog.css";
import { ApiRequestError } from "../lib/api";

interface RenameChannelDialogProps {
  /**
   * Which vocabulary the dialog uses (issue #527). A channel and a group are
   * renamed through different endpoints but through the same dialog: the only
   * thing that varies is the wording, so this is a label choice and never a
   * behavioural switch.
   */
  kind?: "channel" | "group";
  channelId: string;
  currentName: string;
  onClose: () => void;
  /** Resolves once the new name is persisted; rejects with the API error. */
  onRename: (channelId: string, displayName: string) => Promise<void>;
}

const titleId = "chat-rename-channel-title";
const inputId = "chat-rename-channel-name";
const errorId = "chat-rename-channel-error";

/**
 * The server's own limits, restated here only as input bounds.
 *
 * They are not a second validation rule: the backend counts the same Unicode
 * code points — 100 for a channel (domain.MaxChannelDisplayNameCodePoints), 120
 * for a group (maxDMTitleRunes) — and refuses anything longer whatever this
 * attribute says. Their whole job is to stop a user typing past the limit before
 * being told.
 */
const maxNameLength = { channel: 100, group: 120 } as const;

/**
 * Maps a failure to copy, by status code only. The server's message is never
 * surfaced: a rejected name can be tens of kilobytes of caller-controlled text,
 * and the endpoint deliberately declines to say whether a refused channel exists.
 */
function renameErrorMessage(error: unknown): string {
  if (error instanceof ApiRequestError) {
    if (error.status === 400) return "Escolha um nome válido para esta conversa.";
    if (error.status === 403) return "Você não tem permissão para renomear este canal.";
    if (error.status === 404) return "Este canal não está mais disponível.";
    if (error.status === 409)
      return "O canal mudou enquanto você editava. Recarregue e tente de novo.";
    if (error.status === 429) {
      return "Muitas solicitações em sequência. Aguarde um momento e tente novamente.";
    }
    if (error.status === 0) return "Sem conexão. Verifique sua rede e tente novamente.";
  }
  return "Não foi possível renomear o canal. Tente novamente.";
}

const dialogCopy = {
  channel: { title: "Renomear canal", field: "Nome do canal" },
  group: { title: "Renomear grupo", field: "Nome do grupo" },
} as const;

export default function RenameChannelDialog({
  kind = "channel",
  channelId,
  currentName,
  onClose,
  onRename,
}: RenameChannelDialogProps) {
  const copy = dialogCopy[kind];
  // Seeded with the current name and owned from here on: the field is what the
  // user is editing, so a refetch landing mid-edit must not overwrite it.
  const [name, setName] = useState(currentName);
  const [error, setError] = useState("");
  const [pending, setPending] = useState(false);
  const inputRef = useRef<HTMLInputElement>(null);
  const dialogRef = useRef<HTMLDivElement>(null);
  // State updates are asynchronous, so `pending` alone cannot stop a second
  // submit fired in the same tick. This ref is what actually makes submit single.
  const submittingRef = useRef(false);
  const mountedRef = useRef(true);

  useEffect(() => {
    mountedRef.current = true;
    // Focus opens on the field, with the current name selected: the common
    // gesture is replacing the name, and the second most common is editing it.
    inputRef.current?.focus();
    inputRef.current?.select();
    return () => {
      mountedRef.current = false;
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

  async function submit(event: FormEvent) {
    event.preventDefault();
    const trimmed = name.trim();
    // A fast local answer for the one case the user can see for themselves. The
    // backend enforces the same rule and remains the authority — this only saves
    // a round trip, and every other verdict comes from the server.
    if (!trimmed) {
      setError("Escolha um nome para esta conversa.");
      inputRef.current?.focus();
      return;
    }
    if (submittingRef.current) return;
    submittingRef.current = true;
    setPending(true);
    setError("");
    try {
      await onRename(channelId, trimmed);
      if (mountedRef.current) onClose();
    } catch (failure) {
      // The dialog stays open with the typed name intact: a rate limit or a
      // dropped connection is recoverable, and the UI must never be left showing
      // a name that was not persisted.
      if (mountedRef.current) {
        setError(renameErrorMessage(failure));
        inputRef.current?.focus();
      }
    } finally {
      submittingRef.current = false;
      if (mountedRef.current) setPending(false);
    }
  }

  return createPortal(
    <div className="rename-channel__backdrop" onMouseDown={requestClose}>
      <div
        ref={dialogRef}
        className="rename-channel"
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        onKeyDown={handleKeyDown}
        onMouseDown={(event) => event.stopPropagation()}
      >
        <h2 id={titleId} className="rename-channel__title">
          {copy.title}
        </h2>
        <form onSubmit={submit}>
          <label className="rename-channel__label" htmlFor={inputId}>
            {copy.field}
          </label>
          <input
            ref={inputRef}
            id={inputId}
            className="rename-channel__input"
            type="text"
            autoComplete="off"
            maxLength={maxNameLength[kind]}
            value={name}
            disabled={pending}
            aria-invalid={error ? true : undefined}
            aria-describedby={error ? errorId : undefined}
            onChange={(event) => {
              setName(event.target.value);
              setError("");
            }}
          />
          {error && (
            <p id={errorId} className="rename-channel__error" role="alert">
              {error}
            </p>
          )}
          <div className="rename-channel__actions">
            <button
              type="button"
              className="rename-channel__cancel"
              disabled={pending}
              onClick={requestClose}
            >
              Cancelar
            </button>
            <button
              type="submit"
              className="rename-channel__submit"
              disabled={pending}
              aria-busy={pending}
            >
              {pending ? "Salvando…" : "Salvar"}
            </button>
          </div>
        </form>
      </div>
    </div>,
    document.body,
  );
}
