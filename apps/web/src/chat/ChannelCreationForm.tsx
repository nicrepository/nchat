import { type FormEvent, useState } from "react";

import { ApiRequestError } from "../lib/api";
import { createChannel, createChannelCategory } from "./chatApi";
import type { ChannelCategory } from "./chatTypes";
import {
  MAX_CHANNEL_SLUG_LENGTH,
  slugifyChannelName,
  validateChannelDisplayName,
  validateChannelForm,
  type ChannelFormType,
} from "./channelForm";
import { useSingleSubmission } from "./useSingleSubmission";

interface ChannelCreationFormProps {
  categories: ChannelCategory[];
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

interface ChannelCategoryFieldProps {
  categories: ChannelCategory[];
  selectedCategoryId: string;
  newCategoryName: string;
  disabled: boolean;
  onSelect: (categoryId: string) => void;
  onNewCategoryNameChange: (name: string) => void;
}

/** Category picker, plus the name of a new one when that option is chosen. */
function ChannelCategoryField({
  categories,
  selectedCategoryId,
  newCategoryName,
  disabled,
  onSelect,
  onNewCategoryNameChange,
}: ChannelCategoryFieldProps) {
  return (
    <>
      <label className="new-dm-dialog__group-name" htmlFor="new-channel-category-select">
        Categoria
      </label>
      <div className="new-dm-dialog__search-field">
        <select
          id="new-channel-category-select"
          value={selectedCategoryId}
          disabled={disabled}
          onChange={(event) => onSelect(event.target.value)}
          style={{
            width: "100%",
            background: "transparent",
            border: "none",
            outline: "none",
            color: "inherit",
            font: "inherit",
            fontSize: "14px",
            height: "42px",
            cursor: "pointer",
          }}
        >
          <option value="">Nenhuma (Geral)</option>
          {categories
            .filter((cat) => cat.kind === "category" && cat.id)
            .map((cat) => (
              <option key={cat.id} value={cat.id}>
                {cat.name}
              </option>
            ))}
          <option value="__new__">+ Criar nova categoria...</option>
        </select>
      </div>

      {selectedCategoryId === "__new__" && (
        <>
          <label className="new-dm-dialog__group-name" htmlFor="new-channel-new-category">
            Nome da nova categoria
          </label>
          <div className="new-dm-dialog__search-field">
            <input
              id="new-channel-new-category"
              type="text"
              autoComplete="off"
              placeholder="Ex.: Projetos Especiais"
              value={newCategoryName}
              disabled={disabled}
              onChange={(event) => onNewCategoryNameChange(event.target.value)}
            />
          </div>
        </>
      )}
    </>
  );
}

interface ChannelSubmitFooterProps {
  type: ChannelFormType;
  pending: boolean;
  disabled: boolean;
}

function ChannelSubmitFooter({ type, pending, disabled }: ChannelSubmitFooterProps) {
  return (
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
        disabled={disabled}
        aria-busy={pending}
      >
        {pending ? "Criando…" : "Criar canal"}
      </button>
    </footer>
  );
}

/**
 * The canonical channel-creation form (RF-01), rendered inside the single
 * "Nova conversa" dialog (BUG #393). It owns the fields and the one write it
 * can make; the dialog shell around it owns focus, Escape and the backdrop.
 *
 * The shell keeps it mounted (hidden) while another mode is visited, so the
 * draft survives a detour (issue #1023). For the same reason it does not grab
 * focus on mount: focus stays on the mode the user just chose.
 *
 * Nothing here decides whether the user may create a channel: the endpoint
 * derives the actor, the workspace and the membership from the session on every
 * call, and a denial arrives as a status this form translates.
 */
export default function ChannelCreationForm({
  categories = [],
  onCreated,
  onPendingChange,
}: ChannelCreationFormProps) {
  const [displayName, setDisplayName] = useState("");
  const [selectedCategoryId, setSelectedCategoryId] = useState("");
  const [newCategoryName, setNewCategoryName] = useState("");
  const [slug, setSlug] = useState("");
  // Once the slug is edited by hand it stops following the name: silently
  // overwriting a deliberate identifier on the next keystroke would be worse
  // than asking the user to keep it in sync themselves.
  const [slugEdited, setSlugEdited] = useState(false);
  const [type, setType] = useState<ChannelFormType>("public");
  const submission = useSingleSubmission(onPendingChange);
  const { pending, error, setError } = submission;

  const effectiveSlug = slugEdited ? slug : slugifyChannelName(displayName);
  const trimmedName = displayName.trim();
  // Reported while the user types rather than only on submit, because the way
  // past this is to shorten the name and they need to see that before losing a
  // click. Guarding on the empty case leaves only the over-length message here:
  // telling someone their name is empty before they have typed is noise. The
  // field is never truncated, so a pasted name stays intact and editable.
  const nameError = trimmedName === "" ? null : validateChannelDisplayName(trimmedName);

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
    submit();
  }

  function validationError(): string | null {
    const message = validateChannelForm({ displayName, slug: effectiveSlug });
    if (message) return message;
    if (selectedCategoryId === "__new__" && !newCategoryName.trim()) {
      return "Digite o nome da nova categoria.";
    }
    return null;
  }

  async function createWithCategory(signal: AbortSignal) {
    let finalCategoryId = selectedCategoryId;
    if (selectedCategoryId === "__new__") {
      const newCat = await createChannelCategory(newCategoryName, signal);
      finalCategoryId = newCat.id || "";
    }
    return createChannel(
      {
        slug: effectiveSlug,
        displayName,
        type,
        categoryId: finalCategoryId || undefined,
      },
      signal,
    );
  }

  /**
   * Creates the channel, at most once per submission (useSingleSubmission).
   *
   * The local validation only spares a round trip; chat-service applies the same
   * rules and the authorization check no matter what is sent. On failure the form
   * stays put with the fields intact so a retry costs one click.
   */
  function submit() {
    const message = validationError();
    if (message) {
      setError(message);
      return;
    }
    submission.run(createWithCategory, (channel) => onCreated(channel.id), createErrorMessage);
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

        <ChannelCategoryField
          categories={categories}
          selectedCategoryId={selectedCategoryId}
          newCategoryName={newCategoryName}
          disabled={pending}
          onSelect={(value) => {
            setSelectedCategoryId(value);
            setError("");
          }}
          onNewCategoryNameChange={(value) => {
            setNewCategoryName(value);
            setError("");
          }}
        />

        {error && (
          <p className="new-dm-dialog__error new-dm-dialog__error--open" role="alert">
            {error}
          </p>
        )}
      </div>

      <ChannelSubmitFooter
        type={type}
        pending={pending}
        disabled={pending || trimmedName === "" || nameError !== null}
      />
    </form>
  );
}
