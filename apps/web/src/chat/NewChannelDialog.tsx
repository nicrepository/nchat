import { type FormEvent, type KeyboardEvent, useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";

import { ApiRequestError } from "../lib/api";
import { createChannel } from "./chatApi";
import {
  MAX_CHANNEL_SLUG_LENGTH,
  slugifyChannelName,
  validateChannelForm,
  type ChannelFormType,
} from "./channelForm";

interface NewChannelDialogProps {
  onClose: () => void;
  /** Called with the new channel's ID once the server has created it. */
  onCreated: (channelId: string) => void;
}

/**
 * Turns a failed creation into something the user can act on.
 *
 * 401 and 403 keep their own wording: being signed out and lacking the workspace
 * role are different problems with different fixes, and collapsing either into
 * "tente novamente" would send the user in circles. Server detail is never
 * echoed — the status alone selects the message.
 */
function createErrorMessage(error: unknown): string {
  if (error instanceof ApiRequestError) {
    if (error.status === 401) return "Sua sessão expirou. Entre novamente para criar canais.";
    if (error.status === 403) {
      return "Você não tem permissão para criar canais neste workspace.";
    }
    if (error.status === 409) return "Já existe um canal com esse identificador.";
    if (error.status === 400) return "Revise o nome e o identificador do canal.";
    if (error.status === 429) {
      return "Muitas solicitações em sequência. Aguarde um momento e tente novamente.";
    }
    if (error.status === 0) return "Sem conexão. Verifique sua rede e tente novamente.";
  }
  return "Não foi possível criar o canal. Tente novamente.";
}

export default function NewChannelDialog({ onClose, onCreated }: NewChannelDialogProps) {
  const [displayName, setDisplayName] = useState("");
  const [slug, setSlug] = useState("");
  // Once the slug is edited by hand it stops following the name: silently
  // overwriting a deliberate identifier on the next keystroke would be worse
  // than asking the user to keep it in sync themselves.
  const [slugEdited, setSlugEdited] = useState(false);
  const [type, setType] = useState<ChannelFormType>("public");
  const [error, setError] = useState("");
  const [pending, setPending] = useState(false);
  const nameInputRef = useRef<HTMLInputElement>(null);
  const dialogRef = useRef<HTMLDivElement>(null);
  // State updates are asynchronous, so `pending` cannot stop a second click
  // fired in the same tick; this ref is what actually makes submission single.
  const submittingRef = useRef(false);
  const abortRef = useRef<AbortController>(null);
  const mountedRef = useRef(true);

  const effectiveSlug = slugEdited ? slug : slugifyChannelName(displayName);

  useEffect(() => {
    mountedRef.current = true;
    nameInputRef.current?.focus();
    return () => {
      mountedRef.current = false;
      abortRef.current?.abort();
    };
  }, []);

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

  /**
   * The form's only entry point, for both Enter and the submit button.
   *
   * preventDefault stops the browser's native navigation; the guards all live in
   * submit(), so a keyboard submission cannot bypass a check that a click honours.
   * A disabled submit button already blocks Enter in most browsers, but Enter is
   * re-validated in submit() rather than trusted to that.
   */
  function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    void submit();
  }

  /**
   * Creates the channel, at most once per submission.
   *
   * The local validation only spares a round trip; chat-service applies the same
   * rules and the role check no matter what is sent. On failure the dialog stays
   * open with the fields intact so a retry costs one click, and the mounted guard
   * keeps a late response from touching state after the parent unmounts.
   */
  async function submit() {
    if (submittingRef.current) return;
    const message = validateChannelForm({ displayName, slug: effectiveSlug });
    if (message) {
      setError(message);
      return;
    }
    submittingRef.current = true;
    setPending(true);
    setError("");
    const controller = new AbortController();
    abortRef.current = controller;

    try {
      const channel = await createChannel(
        { slug: effectiveSlug, displayName, type },
        controller.signal,
      );
      if (mountedRef.current) onCreated(channel.id);
    } catch (err) {
      if (mountedRef.current) setError(createErrorMessage(err));
    } finally {
      submittingRef.current = false;
      if (mountedRef.current) setPending(false);
    }
  }

  return createPortal(
    // The modal shell reuses the dialog styles introduced with the new-DM modal;
    // they are shared chrome, named after the feature that shipped them first.
    <div className="new-dm-dialog__backdrop" onMouseDown={requestClose}>
      <div
        ref={dialogRef}
        className="new-dm-dialog"
        role="dialog"
        aria-modal="true"
        aria-labelledby="new-channel-title"
        aria-describedby="new-channel-description"
        onKeyDown={handleKeyDown}
        onMouseDown={(event) => event.stopPropagation()}
      >
        <header className="new-dm-dialog__header">
          <div>
            <h2 id="new-channel-title">Novo canal</h2>
            <p id="new-channel-description">
              Canais públicos ficam visíveis para todo o workspace. Canais privados só aparecem para
              quem for adicionado.
            </p>
          </div>
          <button
            type="button"
            className="new-dm-dialog__close"
            aria-label="Fechar novo canal"
            disabled={pending}
            onClick={requestClose}
          >
            <span className="material-symbols-outlined" aria-hidden="true">
              close
            </span>
          </button>
        </header>

        {/* A real <form> is what makes Enter submit from any field. The submit
            button is the form's, so the keyboard and the mouse take the exact
            same path through handleSubmit → submit(), including every guard. */}
        <form onSubmit={handleSubmit}>
          <fieldset className="new-dm-dialog__mode">
            <legend>Tipo de canal</legend>
            {(
              [
                ["public", "Público"],
                ["private", "Privado"],
              ] as const
            ).map(([value, label]) => (
              <label
                key={value}
                className={type === value ? "new-dm-dialog__mode-option--on" : undefined}
              >
                <input
                  type="radio"
                  name="new-channel-type"
                  value={value}
                  checked={type === value}
                  disabled={pending}
                  onChange={() => setType(value)}
                />
                {label}
              </label>
            ))}
          </fieldset>

          <div className="new-dm-dialog__search">
            <label htmlFor="new-channel-name">Nome do canal</label>
            <div className="new-dm-dialog__search-field">
              <input
                ref={nameInputRef}
                id="new-channel-name"
                type="text"
                autoComplete="off"
                placeholder="Ex.: Infraestrutura"
                value={displayName}
                disabled={pending}
                onChange={(event) => {
                  setDisplayName(event.target.value);
                  setError("");
                }}
              />
            </div>

            <label className="new-dm-dialog__group-name" htmlFor="new-channel-slug">
              Identificador
            </label>
            <div className="new-dm-dialog__search-field">
              <input
                id="new-channel-slug"
                type="text"
                autoComplete="off"
                maxLength={MAX_CHANNEL_SLUG_LENGTH}
                placeholder="infraestrutura"
                aria-describedby="new-channel-slug-hint"
                value={effectiveSlug}
                disabled={pending}
                onChange={(event) => {
                  setSlugEdited(true);
                  setSlug(event.target.value);
                  setError("");
                }}
              />
            </div>
            <p id="new-channel-slug-hint" className="new-dm-dialog__footer-hint">
              Letras minúsculas, números e hifens internos. Aparece como #{effectiveSlug || "canal"}
              .
            </p>

            {error && (
              <p className="new-dm-dialog__error new-dm-dialog__error--open" role="alert">
                {error}
              </p>
            )}
          </div>

          <footer className="new-dm-dialog__footer">
            <p className="new-dm-dialog__footer-hint">
              {type === "public"
                ? "Todo o workspace poderá entrar."
                : "Somente convidados verão este canal."}
            </p>
            <button
              type="submit"
              className="new-dm-dialog__submit"
              // The accessible name stays fixed while the visible label switches to
              // "Criando…", so assistive tech announces the busy state instead of a
              // control that appears to have been replaced mid-action.
              aria-label="Criar canal"
              disabled={pending || displayName.trim() === ""}
              aria-busy={pending}
            >
              {pending ? "Criando…" : "Criar canal"}
            </button>
          </footer>
        </form>
      </div>
    </div>,
    document.body,
  );
}
