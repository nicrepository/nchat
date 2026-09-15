/**
 * ComposerToolbar — formatting toolbar for the TipTap-based message composer.
 *
 * All format buttons call TipTap editor chain commands; no string insertion.
 *
 * The emoji button opens the same picker the reactions use (issue #496). It
 * used to open a hard-coded panel of twenty emoji, which meant one product with
 * two different emoji experiences — a searchable Unicode catalog beside a
 * message, and a fixed grid inside the composer. There is one picker now; only
 * what happens after a choice differs, and that belongs to the caller.
 *
 * RF-11: all formatting commands are direct Material Symbols buttons.
 *        link/attach/mic: removed — add back when the backing RF lands.
 */

import { lazy, Suspense, useCallback, useRef, useState } from "react";
import { createPortal } from "react-dom";
import type { Editor } from "@tiptap/core";

import { useAnchoredPicker } from "./emoji/useAnchoredPicker";
import { emptyEmojiUsage, type EmojiUsage } from "./emoji/emojiUsage";
import MessagePriorityDialog from "./MessagePriorityDialog";
import {
  isDefaultPriorityIntent,
  priorityTriggerLabel,
  standardPriorityIntent,
  type MessagePriorityIntent,
} from "./messagePriority";

/**
 * The picker and its catalog stay in the chunk the reactions already load, so
 * opening it from the composer downloads nothing the conversation has not
 * needed, and never a second copy.
 */
const EmojiPicker = lazy(() => import("./emoji/EmojiPicker"));

// ── Toolbar item descriptors ──────────────────────────────────────────────────

interface ToolbarItem {
  name: string;
  run: (editor: Editor) => void;
  label: string;
  testId: string;
  icon: string; // Material Symbols ligature name
}

const FORMAT_ITEMS: ToolbarItem[] = [
  {
    name: "bold",
    run: (e) => e.chain().focus().toggleBold().run(),
    label: "Negrito",
    testId: "fmt-bold",
    icon: "format_bold",
  },
  {
    name: "italic",
    run: (e) => e.chain().focus().toggleItalic().run(),
    label: "Itálico",
    testId: "fmt-italic",
    icon: "format_italic",
  },
  {
    name: "code",
    run: (e) => e.chain().focus().toggleCode().run(),
    label: "Código",
    testId: "fmt-code",
    icon: "code",
  },
  {
    name: "codeBlock",
    run: (e) => e.chain().focus().toggleCodeBlock().run(),
    label: "Bloco de código",
    testId: "fmt-codeblock",
    icon: "code_blocks",
  },
  {
    name: "bulletList",
    run: (e) => e.chain().focus().toggleBulletList().run(),
    label: "Lista não ordenada",
    testId: "fmt-ul",
    icon: "format_list_bulleted",
  },
  {
    name: "orderedList",
    run: (e) => e.chain().focus().toggleOrderedList().run(),
    label: "Lista ordenada",
    testId: "fmt-ol",
    icon: "format_list_numbered",
  },
];

// ── Icons ─────────────────────────────────────────────────────────────────────

/** Stable id so the button's aria-controls can point at the panel. */
const composerPickerId = "composer-emoji-picker";

const IconEmoji = () => (
  <span className="material-symbols-outlined" aria-hidden="true" style={{ fontSize: 20 }}>
    mood
  </span>
);

// ── Component ─────────────────────────────────────────────────────────────────

/**
 * What the surrounding surface lends the picker: the reader's shared emoji
 * history, and where a use is recorded. Optional because the inline message
 * editor has no conversation state to lend — its picker still works, it simply
 * has no personalised "Recentes".
 */
export interface ComposerEmojiOptions {
  usage: EmojiUsage;
  onToneChange: (tone: number) => void;
  /** Records a use, so an emoji typed here reaches the same "Recentes". */
  onUsed: (emoji: string) => void;
}

export interface ComposerToolbarProps {
  editor: Editor | null;
  disabled?: boolean;
  emoji?: ComposerEmojiOptions;
  /** Controlled open state. Omitted, the toolbar manages its own. */
  pickerOpen?: boolean;
  onPickerOpenChange?: (open: boolean) => void;
  /**
   * What this message states about its own priority, and how to restate it
   * (issue #822).
   *
   * One value rather than a control per flag: priority, the confirmation
   * request (#824) and the persistent reminders (#825) are decided together in
   * the popover this button opens, and a toolbar that could set one of them
   * behind the others' backs is how a stale flag reaches the server. This
   * absorbs the standalone acknowledgement toggle #824 left here for exactly
   * this issue to take over.
   *
   * Omitted entirely by callers that do not support it (the inline editor), so
   * the button simply is not drawn.
   */
  priority?: MessagePriorityIntent;
  onPriorityChange?: (intent: MessagePriorityIntent) => void;
}

const noEmojiUse = () => undefined;

/**
 * The picker's open state and the usage it reads, whether the caller supplies
 * them or not.
 *
 * The composer controls the state because it has to close the picker on send;
 * the inline editor has nothing to say about it and gets local state instead.
 */
function useComposerEmoji(props: ComposerToolbarProps) {
  const [localOpen, setLocalOpen] = useState(false);
  const { pickerOpen, onPickerOpenChange, emoji } = props;
  return {
    open: pickerOpen ?? localOpen,
    setOpen: onPickerOpenChange ?? setLocalOpen,
    usage: emoji?.usage ?? emptyEmojiUsage,
    onToneChange: emoji?.onToneChange ?? noEmojiUse,
    onUsed: emoji?.onUsed ?? noEmojiUse,
  };
}

/**
 * The priority button and the popover it owns (issue #822).
 *
 * Its own component, and its own open state, because the toolbar has no reason
 * to know when a popover is open: the value is what the composer above cares
 * about, and it arrives only through onChange. That also keeps the applied
 * intent and the popover's draft in different components, so there is no way
 * for an unapplied edit to leak upwards.
 *
 * Closing — Cancelar, Escape, a click outside or Aplicar — always hands focus
 * back to this button, which is where the reader was before it opened.
 */
function ComposerPriorityControl({
  intent,
  disabled,
  onChange,
}: {
  intent: MessagePriorityIntent;
  disabled: boolean;
  onChange: (intent: MessagePriorityIntent) => void;
}) {
  const [open, setOpen] = useState(false);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const stated = !isDefaultPriorityIntent(intent);

  function close() {
    setOpen(false);
    triggerRef.current?.focus();
  }

  return (
    <>
      <button
        ref={triggerRef}
        type="button"
        className={`composer-toolbar__btn${
          stated ? ` composer-toolbar__btn--priority-${intent.priority}` : ""
        }`}
        // The whole applied state, in words: a screen reader hears the priority
        // and the options that came with it, never the tint that also marks them.
        aria-label={priorityTriggerLabel(intent)}
        title={priorityTriggerLabel(intent)}
        aria-haspopup="dialog"
        aria-expanded={open}
        disabled={disabled}
        data-testid="toolbar-priority-btn"
        onClick={() => setOpen((current) => !current)}
      >
        <span className="material-symbols-outlined" aria-hidden="true" style={{ fontSize: 18 }}>
          error
        </span>
      </button>
      {open && (
        <MessagePriorityDialog
          intent={intent}
          anchorRef={triggerRef}
          onCancel={close}
          onApply={(applied) => {
            onChange(applied);
            close();
          }}
        />
      )}
    </>
  );
}

export default function ComposerToolbar(props: ComposerToolbarProps) {
  const { editor, disabled = false } = props;
  const { open, setOpen, usage, onToneChange, onUsed } = useComposerEmoji(props);
  const containerRef = useRef<HTMLDivElement>(null);
  const emojiBtnRef = useRef<HTMLButtonElement>(null);

  /**
   * Escape and the button itself hand focus back to the editor, so the reader
   * carries on typing where they left off (issue #493). A click elsewhere does
   * not: focus belongs wherever that click put it.
   */
  const closeEmoji = useCallback(
    (restoreFocus: boolean) => {
      setOpen(false);
      if (restoreFocus) editor?.chain().focus().run();
    },
    [editor, setOpen],
  );

  const pickerRef = useAnchoredPicker({
    open,
    anchorRef: emojiBtnRef,
    onDismiss: closeEmoji,
    containerRef,
    align: "start",
  });

  function handleFormat(item: ToolbarItem) {
    if (editor) item.run(editor);
    setOpen(false);
  }

  /**
   * Inserts the emoji where the cursor is, and leaves the picker open.
   *
   * TipTap keeps its own selection while the DOM focus is on the picker, so
   * insertContent lands at the caret — replacing a selection exactly as typing
   * would — without this having to save and restore anything. Focus is
   * deliberately not pulled back to the editor: the reader is in the picker, and
   * a messenger lets them pick 😂❤️🚀 without reopening it three times.
   */
  function handleEmoji(emoji: string) {
    editor?.chain().insertContent(emoji).run();
    onUsed(emoji);
  }

  return (
    <div className="composer-toolbar" ref={containerRef}>
      {FORMAT_ITEMS.map((item) => {
        const active = editor?.isActive(item.name) ?? false;
        return (
          <button
            key={item.testId}
            type="button"
            className={`composer-toolbar__btn${active ? " composer-toolbar__btn--active" : ""}`}
            aria-label={item.label}
            aria-pressed={active}
            disabled={disabled}
            data-testid={item.testId}
            onClick={() => handleFormat(item)}
          >
            <span className="material-symbols-outlined" aria-hidden="true" style={{ fontSize: 18 }}>
              {item.icon}
            </span>
          </button>
        );
      })}

      {/*
        Issue #822. One button for the whole attention axis — priority, the
        confirmation request and the persistent reminders — because they are
        decided together and the server reads them together. It replaces the
        standalone acknowledgement toggle #824 parked here until this popover
        existed; the capability is unchanged, it simply lives inside the dialog
        now instead of beside it.
      */}
      {props.onPriorityChange ? (
        <ComposerPriorityControl
          intent={props.priority ?? standardPriorityIntent}
          disabled={disabled}
          onChange={props.onPriorityChange}
        />
      ) : null}

      {/* ── Emoji button + picker ── */}
      <div className="composer-toolbar__wrap">
        <button
          ref={emojiBtnRef}
          type="button"
          className="composer-toolbar__btn"
          aria-label="Inserir emoji"
          aria-haspopup="dialog"
          aria-expanded={open}
          aria-controls={open ? composerPickerId : undefined}
          disabled={disabled}
          data-testid="toolbar-emoji-btn"
          onClick={() => (open ? closeEmoji(true) : setOpen(true))}
        >
          <IconEmoji />
        </button>

        {open &&
          createPortal(
            <div
              ref={pickerRef}
              id={composerPickerId}
              // Portalled out of the composer so the picker is never clipped by
              // it, and carrying the chat scope with it — see the reaction
              // picker's own container for why the theme class travels.
              className="chat-theme chat-emoji-surface"
              role="dialog"
              aria-label="Inserir emoji"
              data-testid="toolbar-emoji-picker"
              style={{ visibility: "hidden" }}
            >
              <Suspense
                fallback={
                  <p className="chat-emoji-picker__status" role="status">
                    Carregando emojis…
                  </p>
                }
              >
                <EmojiPicker usage={usage} onToneChange={onToneChange} onSelect={handleEmoji} />
              </Suspense>
            </div>,
            document.body,
          )}
      </div>
    </div>
  );
}
