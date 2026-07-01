/**
 * ComposerToolbar — formatting toolbar for the TipTap-based message composer.
 *
 * All format buttons call TipTap editor chain commands; no string insertion.
 * Emoji picker inserts content via editor.chain().insertContent().
 * ponytail: emoji picker has no search (add when requested); GIF/upload are RF-12+.
 *
 * RF-11: all formatting commands are direct Material Symbols buttons.
 *        link/attach/mic: removed — add back when the backing RF lands.
 */

import { useEffect, useLayoutEffect, useRef, useState } from "react";
import type { Editor } from "@tiptap/core";

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

// ── Emoji list ─────────────────────────────────────────────────────────────────

// ponytail: 20 frequent emojis; expand to full picker when users request search
const EMOJIS = [
  "😀",
  "😂",
  "🙏",
  "👍",
  "👎",
  "❤️",
  "🔥",
  "✅",
  "⚠️",
  "🎉",
  "💡",
  "📝",
  "🚀",
  "🐛",
  "💬",
  "📌",
  "🔒",
  "⚡",
  "🎯",
  "🌟",
];

// ── Icons ─────────────────────────────────────────────────────────────────────

const IconEmoji = () => (
  <span className="material-symbols-outlined" aria-hidden="true" style={{ fontSize: 20 }}>
    mood
  </span>
);

// ── Component ─────────────────────────────────────────────────────────────────

export interface ComposerToolbarProps {
  editor: Editor | null;
  disabled?: boolean;
}

export default function ComposerToolbar({ editor, disabled = false }: ComposerToolbarProps) {
  const [emojiOpen, setEmojiOpen] = useState(false);
  const containerRef = useRef<HTMLDivElement>(null);
  const emojiBtnRef = useRef<HTMLButtonElement>(null);
  const firstEmojiRef = useRef<HTMLButtonElement>(null);

  // Close panels on outside click.
  useEffect(() => {
    const onDown = (ev: MouseEvent) => {
      if (containerRef.current && !containerRef.current.contains(ev.target as Node)) {
        setEmojiOpen(false);
      }
    };
    document.addEventListener("mousedown", onDown);
    return () => document.removeEventListener("mousedown", onDown);
  }, []);

  useLayoutEffect(() => {
    if (emojiOpen) firstEmojiRef.current?.focus();
  }, [emojiOpen]);

  function handleFormat(item: ToolbarItem) {
    if (editor) item.run(editor);
    setEmojiOpen(false);
  }

  function handleEmoji(emoji: string) {
    editor?.chain().focus().insertContent(emoji).run();
    setEmojiOpen(false);
  }

  const closeEmoji = () => {
    setEmojiOpen(false);
    emojiBtnRef.current?.focus();
  };

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
          aria-expanded={emojiOpen}
          disabled={disabled}
          data-testid="toolbar-emoji-btn"
          onClick={() => {
            setEmojiOpen((o) => !o);
          }}
        >
          <IconEmoji />
        </button>

        {emojiOpen && (
          <div
            role="dialog"
            aria-label="Seletor de emoji"
            className="composer-toolbar__emoji-picker"
            data-testid="toolbar-emoji-picker"
            onKeyDown={(ev) => {
              if (ev.key === "Escape") closeEmoji();
            }}
          >
            {EMOJIS.map((emoji, i) => (
              <button
                key={emoji}
                ref={i === 0 ? firstEmojiRef : undefined}
                type="button"
                className="composer-toolbar__emoji-btn"
                aria-label={emoji}
                onClick={() => handleEmoji(emoji)}
              >
                {emoji}
              </button>
            ))}
          </div>
        )}
      </div>
    </div>
  );
}
