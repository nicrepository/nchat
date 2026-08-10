import { type FormEvent, useEffect, useRef, useState } from "react";

import { ApiRequestError } from "../lib/api";
import { createChannel } from "./chatApi";
import {
  MAX_CHANNEL_SLUG_LENGTH,
  slugifyChannelName,
  validateChannelDisplayName,
  validateChannelForm,
  type ChannelFormType,
} from "./channelForm";
import type { ChannelCategoryGroup } from "./channelGrouping";
import { useChannelCategories } from "./useChannelCategories";

interface ChannelCreationFormProps {
  /** Called with the new channel's ID once the server has created it. */
  onCreated: (channelId: string) => void;
  /**
   * Reports whether a creation is in flight, so the dialog around this form can
   * hold the door shut and freeze the mode switch. The form keeps ownership of
   * the state; the parent only mirrors it.
   */
  onPendingChange: (pending: boolean) => void;
}

/**
 * Turns a failed creation into something the user can act on.
 *
 * 401 and 403 keep their own wording: being signed out and being refused by the
 * workspace are different problems with different fixes, and collapsing either
 * into "tente novamente" would send the user in circles. Server detail is never
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

/**
 * The canonical channel-creation form (RF-01), rendered inside the single
 * "Nova conversa" dialog (BUG #393). It owns the fields and the one write it
 * can make; the dialog shell around it owns focus, Escape and the backdrop.
 *
 * Nothing here decides whether the user may create a channel: the endpoint
 * derives the actor, the workspace and the membership from the session on every
 * call, and a denial arrives as a status this form translates.
 */
export default function ChannelCreationForm({
  onCreated,
  onPendingChange,
}: ChannelCreationFormProps) {
  const [displayName, setDisplayName] = useState("");
  const [slug, setSlug] = useState("");
  // Once the slug is edited by hand it stops following the name: silently
  // overwriting a deliberate identifier on the next keystroke would be worse
  // than asking the user to keep it in sync themselves.
  const [slugEdited, setSlugEdited] = useState(false);
  const [type, setType] = useState<ChannelFormType>("public");
  // Empty means "Geral" (uncategorized) — the default, and the only option a
  // caller who cannot manage categories ever gets (the selector below is
  // hidden for them; the server would reject a non-empty value anyway).
  const [categoryId, setCategoryId] = useState("");
  const [error, setError] = useState("");
  const [pending, setPending] = useState(false);
  const channelCategories = useChannelCategories();
  const manageableCategories: Array<ChannelCategoryGroup & { id: string }> =
    channelCategories.state.status === "ready" && channelCategories.state.canManage
      ? channelCategories.state.groups.filter(
          (group): group is ChannelCategoryGroup & { id: string } =>
            group.kind === "category" && Boolean(group.id),
        )
      : [];
  const nameInputRef = useRef<HTMLInputElement>(null);
  // State updates are asynchronous, so `pending` cannot stop a second click
  // fired in the same tick; this ref is what actually makes submission single.
  const submittingRef = useRef(false);
  const abortRef = useRef<AbortController>(null);
  const mountedRef = useRef(true);

  const effectiveSlug = slugEdited ? slug : slugifyChannelName(displayName);
  const trimmedName = displayName.trim();
  // Reported while the user types rather than only on submit, because the way
  // past this is to shorten the name and they need to see that before losing a
  // click. Guarding on the empty case leaves only the over-length message here:
  // telling someone their name is empty before they have typed is noise. The
  // field is never truncated, so a pasted name stays intact and editable.
  const nameError = trimmedName === "" ? null : validateChannelDisplayName(trimmedName);

  useEffect(() => {
    mountedRef.current = true;
    nameInputRef.current?.focus();
    return () => {
      mountedRef.current = false;
      abortRef.current?.abort();
    };
  }, []);

  function markPending(next: boolean) {
    setPending(next);
    onPendingChange(next);
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
   * rules and the authorization check no matter what is sent. On failure the form
   * stays put with the fields intact so a retry costs one click, and the mounted
   * guard keeps a late response from touching state after the dialog unmounts.
   */
  async function submit() {
    if (submittingRef.current) return;
    const message = validateChannelForm({ displayName, slug: effectiveSlug });
    if (message) {
      setError(message);
      return;
    }
    submittingRef.current = true;
    markPending(true);
    setError("");
    const controller = new AbortController();
    abortRef.current = controller;

    try {
      const channel = await createChannel(
        { slug: effectiveSlug, displayName, type, categoryId: categoryId || undefined },
        controller.signal,
      );
      if (mountedRef.current) onCreated(channel.id);
    } catch (err) {
      if (mountedRef.current) setError(createErrorMessage(err));
    } finally {
      submittingRef.current = false;
      if (mountedRef.current) markPending(false);
    }
  }

  return (
    // A real <form> is what makes Enter submit from any field. The submit button
    // is the form's, so the keyboard and the mouse take the exact same path
    // through handleSubmit → submit(), including every guard.
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
          {/* No maxLength: the browser counts UTF-16 units, so it would cut a
          pasted name of emoji at half its real allowance and silently discard
          what the user meant to keep. The count that decides is the server's,
          mirrored here in code points. */}
          <input
            ref={nameInputRef}
            id="new-channel-name"
            type="text"
            autoComplete="off"
            placeholder="Ex.: Infraestrutura"
            value={displayName}
            disabled={pending}
            aria-invalid={nameError !== null}
            aria-describedby={nameError ? "new-channel-name-error" : undefined}
            onChange={(event) => {
              setDisplayName(event.target.value);
              setError("");
            }}
          />
        </div>
        {nameError && (
          <p
            id="new-channel-name-error"
            className="new-dm-dialog__error new-dm-dialog__error--open"
            role="alert"
          >
            {nameError}
          </p>
        )}

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
          Letras minúsculas, números e hifens internos. Aparece como #{effectiveSlug || "canal"}.
        </p>

        {/* RF-17: only owner/admin (server-confirmed via `canManage`, never
            the client's own guess) get to place the new channel into an
            existing category. Anyone else creates an uncategorized channel,
            same as before this control existed — there is nothing to pick
            from when there are no manageable categories either. */}
        {manageableCategories.length > 0 && (
          <>
            <label className="new-dm-dialog__group-name" htmlFor="new-channel-category">
              Categoria
            </label>
            <div className="new-dm-dialog__search-field">
              <select
                id="new-channel-category"
                disabled={pending}
                value={categoryId}
                onChange={(event) => setCategoryId(event.target.value)}
              >
                <option value="">Geral (sem categoria)</option>
                {manageableCategories.map((category) => (
                  <option key={category.id} value={category.id}>
                    {category.name}
                  </option>
                ))}
              </select>
            </div>
          </>
        )}

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
          disabled={pending || trimmedName === "" || nameError !== null}
          aria-busy={pending}
        >
          {pending ? "Criando…" : "Criar canal"}
        </button>
      </footer>
    </form>
  );
}
