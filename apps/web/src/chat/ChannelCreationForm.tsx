import { type FormEvent, useState } from "react";

import { maxAddMembersPerRequest } from "./addMembersLimits";
import { searchDMCandidates } from "./chatApi";
import type { ChannelCategory } from "./chatTypes";
import {
  MAX_CHANNEL_SLUG_LENGTH,
  NEW_CATEGORY_OPTION,
  slugifyChannelName,
  validateChannelDisplayName,
  validateChannelForm,
  type ChannelFormType,
} from "./channelForm";
import { PrivateChannelMembersField } from "./PrivateChannelMembersField";
import { useChannelCreation } from "./useChannelCreation";
import { useMemberPicker } from "./useMemberPicker";

interface ChannelCreationFormProps {
  categories: ChannelCategory[];
  /** The signed-in creator: shown as a fixed member and kept out of the search. */
  currentUserId: string;
  workspaceId: string;
  /** Called with the new channel's ID once the server has created it. */
  onCreated: (channelId: string) => void;
  /**
   * Reports whether a creation is in flight, so the dialog around this form can
   * hold the door shut and freeze the mode switch. The form keeps ownership of
   * the state; the parent only mirrors it.
   */
  onPendingChange: (pending: boolean) => void;
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
          <option value={NEW_CATEGORY_OPTION}>+ Criar nova categoria...</option>
        </select>
      </div>

      {selectedCategoryId === NEW_CATEGORY_OPTION && (
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

interface ChannelNameFieldsProps {
  displayName: string;
  slug: string;
  nameError: string | null;
  disabled: boolean;
  onNameChange: (value: string) => void;
  onSlugChange: (value: string) => void;
}

/** The channel's name and its identifier, with the name's inline error. */
function ChannelNameFields({
  displayName,
  slug,
  nameError,
  disabled,
  onNameChange,
  onSlugChange,
}: ChannelNameFieldsProps) {
  return (
    <>
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
          disabled={disabled}
          aria-invalid={nameError !== null}
          aria-describedby={nameError ? "new-channel-name-error" : undefined}
          onChange={(event) => onNameChange(event.target.value)}
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
          value={slug}
          disabled={disabled}
          onChange={(event) => onSlugChange(event.target.value)}
        />
      </div>
      <p id="new-channel-slug-hint" className="new-dm-dialog__footer-hint">
        Letras minúsculas, números e hifens internos. Aparece como #{slug || "canal"}.
      </p>
    </>
  );
}

interface ChannelSubmitFooterProps {
  type: ChannelFormType;
  inviteeCount: number;
  pending: boolean;
  disabled: boolean;
}

/**
 * The summary the user confirms with the submit (issue #1025): who will be able
 * to see the channel, in the same words the server's decision will bear out.
 */
function channelAccessSummary(type: ChannelFormType, inviteeCount: number): string {
  if (type === "public") return "Canal público: todo o workspace poderá entrar.";
  if (inviteeCount === 0) return "Canal privado: somente você terá acesso.";
  const invitees = inviteeCount === 1 ? "1 convidado" : `${inviteeCount} convidados`;
  return `Canal privado: somente você e ${invitees} terão acesso.`;
}

function ChannelSubmitFooter({ type, inviteeCount, pending, disabled }: ChannelSubmitFooterProps) {
  return (
    <footer className="new-dm-dialog__footer">
      <p className="new-dm-dialog__footer-hint" aria-live="polite">
        {channelAccessSummary(type, inviteeCount)}
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
 *
 * A private channel adds a members step (issue #1025). Its selection belongs to
 * this draft, so Privado → Público → Privado keeps it; Público just does not
 * send it. The write — with its idempotency and retry rules — is
 * useChannelCreation's.
 *
 * The people search is the workspace one (searchDMCandidates). Its target rule
 * — active workspace, active membership, active undeleted account, guests
 * included — is exactly channelmembership.EligibleTargetsCTE's for a channel
 * that has no members yet, and the channel-scoped search needs a channel that
 * does not exist. The server re-checks every invitee in the creation itself.
 */
export default function ChannelCreationForm({
  categories = [],
  currentUserId,
  workspaceId,
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
  const creation = useChannelCreation(onCreated, onPendingChange);
  const { pending, error, setError } = creation;
  const picker = useMemberPicker({
    search: searchDMCandidates,
    excludedUserIds: [currentUserId],
    maxSelection: maxAddMembersPerRequest,
  });
  const inviteeCount = type === "private" ? picker.selected.length : 0;

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
    if (selectedCategoryId === NEW_CATEGORY_OPTION && !newCategoryName.trim()) {
      return "Digite o nome da nova categoria.";
    }
    return null;
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
    creation.submit({
      displayName,
      slug: effectiveSlug,
      type,
      categoryId: selectedCategoryId,
      newCategoryName,
      memberIds: picker.selected.map((member) => member.userId),
    });
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
              onChange={() => {
                setType(value);
                setError("");
              }}
            />
            {label}
          </label>
        ))}
      </fieldset>

      <div className="new-dm-dialog__search">
        <ChannelNameFields
          displayName={displayName}
          slug={effectiveSlug}
          nameError={nameError}
          disabled={pending}
          onNameChange={(value) => {
            setDisplayName(value);
            setError("");
          }}
          onSlugChange={(value) => {
            setSlugEdited(true);
            setSlug(value);
            setError("");
          }}
        />

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
      </div>

      {type === "private" && (
        <PrivateChannelMembersField
          picker={picker}
          workspaceId={workspaceId}
          disabled={pending}
          onEdit={() => setError("")}
        />
      )}

      {error && (
        <p className="new-dm-dialog__error new-dm-dialog__error--open" role="alert">
          {error}
        </p>
      )}

      <ChannelSubmitFooter
        type={type}
        inviteeCount={inviteeCount}
        pending={pending}
        disabled={pending || trimmedName === "" || nameError !== null}
      />
    </form>
  );
}
