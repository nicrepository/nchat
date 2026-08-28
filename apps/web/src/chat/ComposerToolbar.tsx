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
