/**
 * GroupAvatarPicker — choose a group's identity, with a live preview (#1026).
 *
 * Shared by the creation flow's Identidade step and by the "Alterar identidade"
 * dialog, so both offer the same two modes under the same rules. It only edits a
 * draft: nothing is persisted from here, and the preview is the very component
 * the sidebar renders, so what is shown is what the group will look like.
 *
 * The emoji grid is the #496 picker, reused as is and loaded on demand. Its
 * catalog is the same generated one chat-service validates against; choosing
 * here is UX, the server's check is the boundary. Picking an emoji does not
 * touch the reader's reaction history.
 */

import { lazy, Suspense, useId } from "react";

import GroupAvatar from "./GroupAvatar";
import {
  type GroupIdentity,
  type GroupIdentityMode,
  persistedEmoji,
  withIdentityMode,
} from "./groupIdentity";
import { initialsFrom } from "./messageDisplay";
import { useEmojiUsage } from "./emoji/useEmojiUsage";

// The picker's styles live with the message area, which is itself lazy; the
// group dialogs can open before it ever loaded, so its stylesheet comes along.
const EmojiPicker = lazy(() =>
  Promise.all([import("./emoji/EmojiPicker"), import("./ChatMessageArea.css")]).then(
    ([module]) => module,
  ),
);

const MODES: readonly { value: GroupIdentityMode; label: string }[] = [
  { value: "auto", label: "Automático" },
  { value: "emoji", label: "Emoji" },
];

function previewLabel(name: string, emoji: string | undefined): string {
  return emoji
    ? `Prévia da identidade: emoji ${emoji}`
    : `Prévia da identidade: iniciais ${initialsFrom(name)}`;
}

interface GroupAvatarPickerProps {
  /** The name the initials are derived from — the draft's, live. */
  name: string;
  identity: GroupIdentity;
  currentUserId: string;
  disabled?: boolean;
  onChange: (identity: GroupIdentity) => void;
}

export default function GroupAvatarPicker({
  name,
  identity,
  currentUserId,
  disabled = false,
  onChange,
}: GroupAvatarPickerProps) {
  const radioName = useId();
  const { usage, changeTone } = useEmojiUsage(currentUserId);
  const emoji = persistedEmoji(identity);

  return (
    <div className="group-identity">
      <div className="group-identity__preview" role="img" aria-label={previewLabel(name, emoji)}>
        <GroupAvatar name={name} emoji={emoji} size="lg" />
      </div>
      <fieldset className="group-identity__mode" disabled={disabled}>
        <legend>Identidade do grupo</legend>
        {MODES.map(({ value, label }) => (
          <label
            key={value}
            className={identity.mode === value ? "group-identity__mode-option--on" : undefined}
          >
            <input
              type="radio"
              name={radioName}
              value={value}
              checked={identity.mode === value}
              onChange={() => onChange(withIdentityMode(identity, value))}
            />
            {label}
          </label>
        ))}
      </fieldset>
      {identity.mode === "auto" ? (
        <p className="group-identity__hint">Iniciais do nome do grupo sobre fundo neutro.</p>
      ) : (
        <>
          <p className="group-identity__hint">
            {emoji ? `Emoji escolhido: ${emoji}` : "Escolha um emoji para o grupo."}
          </p>
          {!disabled && (
            <div className="chat-theme group-identity__picker">
              <Suspense
                fallback={
                  <p className="chat-emoji-picker__status" role="status">
                    Carregando emojis…
                  </p>
                }
              >
                <EmojiPicker
                  usage={usage}
                  onToneChange={changeTone}
                  onSelect={(chosen) => onChange({ mode: "emoji", emoji: chosen })}
                />
              </Suspense>
            </div>
          )}
        </>
      )}
    </div>
  );
}
