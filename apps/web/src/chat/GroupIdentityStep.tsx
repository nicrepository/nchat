/**
 * GroupIdentityStep — the second step of "Nova conversa" → Grupo (#1026):
 * the group's name, its identity and the final confirmation.
 *
 * It owns no state: the draft lives in GroupConversationFlow, so going back to
 * Participantes and returning here keeps the name and the chosen emoji. Nothing
 * is sent until "Criar grupo".
 */

import { useEffect, useRef } from "react";

import { limitGroupTitleInput } from "./dmGroupForm";
import GroupAvatarPicker from "./GroupAvatarPicker";
import { type GroupIdentity, identityIncomplete } from "./groupIdentity";

/** chat-service names an untitled group "Grupo DM"; the preview says the same. */
const UNTITLED_GROUP_NAME = "Grupo DM";

interface GroupIdentityStepProps {
  title: string;
  identity: GroupIdentity;
  currentUserId: string;
  pending: boolean;
  submitError: string;
  onTitleChange: (title: string) => void;
  onIdentityChange: (identity: GroupIdentity) => void;
  onBack: () => void;
  onSubmit: () => void;
}

export default function GroupIdentityStep({
  title,
  identity,
  currentUserId,
  pending,
  submitError,
  onTitleChange,
  onIdentityChange,
  onBack,
  onSubmit,
}: GroupIdentityStepProps) {
  const nameRef = useRef<HTMLInputElement>(null);

  // This step mounts only when the person chose "Continuar", so arriving here
  // is the one moment focus should move: to the first field of the step.
  useEffect(() => {
    nameRef.current?.focus();
  }, []);

  return (
    <>
      <div className="new-dm-dialog__identity">
        <label className="new-dm-dialog__group-name" htmlFor="new-dm-group-name">
          Nome do grupo (opcional)
        </label>
        {/* Truncation is by Unicode code point, the unit the server counts;
        the maxLength attribute would count UTF-16 units and cut an
        emoji-heavy name in half of its allowance. */}
        <input
          ref={nameRef}
          id="new-dm-group-name"
          type="text"
          autoComplete="off"
          placeholder="Ex.: Infraestrutura"
          value={title}
          disabled={pending}
          onChange={(event) => onTitleChange(limitGroupTitleInput(event.target.value))}
        />
        <GroupAvatarPicker
          name={title.trim() || UNTITLED_GROUP_NAME}
          identity={identity}
          currentUserId={currentUserId}
          disabled={pending}
          onChange={onIdentityChange}
        />
        {submitError && (
          <p className="new-dm-dialog__error new-dm-dialog__error--open" role="alert">
            {submitError}
          </p>
        )}
      </div>
      <footer className="new-dm-dialog__footer">
        <button type="button" className="new-dm-dialog__back" disabled={pending} onClick={onBack}>
          Voltar
        </button>
        <button
          type="button"
          className="new-dm-dialog__submit"
          disabled={pending || identityIncomplete(identity)}
          aria-busy={pending}
          onClick={onSubmit}
        >
          {pending ? "Criando…" : "Criar grupo"}
        </button>
      </footer>
    </>
  );
}
