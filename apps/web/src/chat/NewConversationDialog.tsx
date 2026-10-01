import { type KeyboardEvent, type ReactNode, useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";

import ChannelCreationForm from "./ChannelCreationForm";
import type { ChannelCategory } from "./chatTypes";
import GroupConversationFlow from "./GroupConversationFlow";
import PersonConversationFlow from "./PersonConversationFlow";

/**
 * The three things "Nova conversa" can start (BUG #393). Channel creation lives
 * here rather than behind its own sidebar control so the workspace has a single
 * entry point; the mode picks which flow the dialog body runs.
 */
type ConversationMode = "direct" | "group" | "channel";

const MODES = [
  {
    value: "direct",
    label: "Pessoa",
    description: "Encontre uma pessoa do workspace para conversar.",
  },
  {
    value: "group",
    label: "Grupo",
    description: "Selecione pelo menos 2 pessoas do workspace para criar um grupo.",
  },
  {
    value: "channel",
    label: "Canal",
    description:
      "Canais públicos ficam visíveis para todo o workspace. Canais privados só aparecem para quem for adicionado.",
  },
] as const satisfies readonly { value: ConversationMode; label: string; description: string }[];

const FOCUSABLE = "button:not(:disabled), input:not(:disabled)";

interface NewConversationDialogProps {
  currentUserId: string;
  workspaceId?: string;
  categories: ChannelCategory[];
  onClose: () => void;
  onOpened: (conversationId: string) => void;
  onChannelCreated: (channelId: string) => void;
}

interface ConversationModeChoiceProps {
  mode: ConversationMode;
  disabled: boolean;
  onChange: (mode: ConversationMode) => void;
}

/**
 * Native radios: arrow keys move between modes and focus stays on the group, so
 * a keyboard user can walk Pessoa → Grupo → Canal without being pulled into a
 * form on the way.
 */
function ConversationModeChoice({ mode, disabled, onChange }: ConversationModeChoiceProps) {
  return (
    <fieldset className="new-dm-dialog__mode">
      <legend>Tipo de conversa</legend>
      {MODES.map(({ value, label }) => (
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

/**
 * Mounts a flow the first time its mode is chosen and then only hides it, so
 * its draft survives a visit to another mode (issue #1023). Never visited means
 * never mounted: no hidden duplicate fields, no work for a mode not opened.
 */
function KeptAlivePanel({ active, children }: { active: boolean; children: ReactNode }) {
  const [visited, setVisited] = useState(active);
  if (active && !visited) setVisited(true);
  if (!visited) return null;
  return (
    <div className="new-dm-dialog__panel" hidden={!active}>
      {children}
    </div>
  );
}

/**
 * The "Nova conversa" shell: chrome, mode, focus trap, Escape and the
 * in-flight lock. Each flow owns its fields, its search and its single write;
 * the shell only learns *whether* one is running, through onPendingChange, and
 * then holds the door shut and freezes the mode switch. Focus goes back to the
 * trigger from ChatSidebar, which owns the trigger.
 */
export default function NewConversationDialog({
  currentUserId,
  workspaceId = "",
  categories = [],
  onClose,
  onOpened,
  onChannelCreated,
}: NewConversationDialogProps) {
  const [mode, setMode] = useState<ConversationMode>("direct");
  const [busy, setBusy] = useState(false);
  const searchInputRef = useRef<HTMLInputElement>(null);
  const dialogRef = useRef<HTMLDivElement>(null);
  const description = MODES.find((option) => option.value === mode)?.description;

  useEffect(() => {
    searchInputRef.current?.focus();
  }, []);

  // Leaving Pessoa unmounts its search box. If focus was there (a mouse click
  // on a label does not move focus in every browser), it would fall to <body>
  // and out of the trap; the chosen mode is where it belongs instead.
  useEffect(() => {
    const dialog = dialogRef.current;
    if (!dialog || dialog.contains(document.activeElement)) return;
    dialog.querySelector<HTMLInputElement>('input[name="new-dm-mode"]:checked')?.focus();
  }, [mode]);

  function requestClose() {
    if (!busy) onClose();
  }

  function handleKeyDown(event: KeyboardEvent<HTMLDivElement>) {
    if (event.key === "Escape") {
      event.preventDefault();
      requestClose();
      return;
    }
    if (event.key !== "Tab") return;

    // Hidden flows keep their fields in the DOM; they are not tab stops.
    const focusable = Array.from(
      dialogRef.current?.querySelectorAll<HTMLElement>(FOCUSABLE) ?? [],
    ).filter((element) => !element.closest("[hidden]"));
    if (!focusable.length) return;
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
            <h2 id="new-dm-title">Nova conversa</h2>
            <p id="new-dm-description">{description}</p>
          </div>
          <button
            type="button"
            className="new-dm-dialog__close"
            aria-label="Fechar nova conversa"
            disabled={busy}
            onClick={requestClose}
          >
            <span className="material-symbols-outlined" aria-hidden="true">
              close
            </span>
          </button>
        </header>

        <ConversationModeChoice mode={mode} disabled={busy} onChange={setMode} />

        {mode === "direct" && (
          <div className="new-dm-dialog__panel">
            <PersonConversationFlow
              currentUserId={currentUserId}
              workspaceId={workspaceId}
              inputRef={searchInputRef}
              onOpened={onOpened}
              onPendingChange={setBusy}
            />
          </div>
        )}
        <KeptAlivePanel active={mode === "group"}>
          <GroupConversationFlow
            active={mode === "group"}
            currentUserId={currentUserId}
            workspaceId={workspaceId}
            onOpened={onOpened}
            onPendingChange={setBusy}
          />
        </KeptAlivePanel>
        <KeptAlivePanel active={mode === "channel"}>
          <ChannelCreationForm
            categories={categories}
            onCreated={onChannelCreated}
            onPendingChange={setBusy}
          />
        </KeptAlivePanel>
      </div>
    </div>,
    document.body,
  );
}
