/**
 * ComposerToolbar — formatting toolbar for the TipTap-based message composer.
 *
 * All format buttons call TipTap editor chain commands; no string insertion.
 * Emoji picker inserts content via editor.chain().insertContent().
 * ponytail: emoji picker has no search (add when requested); GIF/upload are RF-12+.
 *
 * RF-11: bold/italic/ul exposed as direct buttons (Material Symbols icons).
 *        code/codeblock/ol remain in the "more formats" dropdown.
 *        link/attach/mic: removed — add back when the backing RF lands.
 */

import { useEffect, useLayoutEffect, useRef, useState } from "react";
import type { Editor } from "@tiptap/core";

// ── Toolbar item descriptors ──────────────────────────────────────────────────

interface ToolbarItem {
  run: (editor: Editor) => void;
  label: string;
  testId: string;
}

interface DirectItem extends ToolbarItem {
  icon: string; // Material Symbols ligature name
}

const DIRECT_ITEMS: DirectItem[] = [
  {
    run: (e) => e.chain().focus().toggleBold().run(),
    label: "Negrito",
    testId: "fmt-bold",
    icon: "format_bold",
  },
  {
    run: (e) => e.chain().focus().toggleItalic().run(),
    label: "Itálico",
    testId: "fmt-italic",
    icon: "format_italic",
  },
  {
    run: (e) => e.chain().focus().toggleBulletList().run(),
    label: "Lista",
    testId: "fmt-ul",
    icon: "format_list_bulleted",
  },
];

const DROPDOWN_ITEMS: ToolbarItem[] = [
  {
    run: (e) => e.chain().focus().toggleCode().run(),
    label: "Código",
    testId: "fmt-code",
  },
  {
    run: (e) => e.chain().focus().toggleCodeBlock().run(),
    label: "Bloco de código",
    testId: "fmt-codeblock",
  },
  {
    run: (e) => e.chain().focus().toggleOrderedList().run(),
    label: "Lista ordenada",
    testId: "fmt-ol",
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

const IconMoreFormat = () => (
  <span className="material-symbols-outlined" aria-hidden="true" style={{ fontSize: 18 }}>
    text_format
  </span>
);
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
  const [formatOpen, setFormatOpen] = useState(false);
  const [emojiOpen, setEmojiOpen] = useState(false);
  const containerRef = useRef<HTMLDivElement>(null);
  const formatBtnRef = useRef<HTMLButtonElement>(null);
  const emojiBtnRef = useRef<HTMLButtonElement>(null);
  const firstFmtRef = useRef<HTMLButtonElement>(null);
  const firstEmojiRef = useRef<HTMLButtonElement>(null);

  // Close panels on outside click.
  useEffect(() => {
    const onDown = (ev: MouseEvent) => {
      if (containerRef.current && !containerRef.current.contains(ev.target as Node)) {
        setFormatOpen(false);
        setEmojiOpen(false);
      }
    };
    document.addEventListener("mousedown", onDown);
    return () => document.removeEventListener("mousedown", onDown);
  }, []);

  useLayoutEffect(() => {
    if (formatOpen) firstFmtRef.current?.focus();
  }, [formatOpen]);
  useLayoutEffect(() => {
    if (emojiOpen) firstEmojiRef.current?.focus();
  }, [emojiOpen]);

  function closeAll() {
    setFormatOpen(false);
    setEmojiOpen(false);
  }

  function handleFormat(item: ToolbarItem) {
    if (editor) item.run(editor);
    closeAll();
  }

  function handleEmoji(emoji: string) {
    editor?.chain().focus().insertContent(emoji).run();
    setEmojiOpen(false);
  }

  const closeFormat = () => {
    setFormatOpen(false);
    formatBtnRef.current?.focus();
  };
  const closeEmoji = () => {
    setEmojiOpen(false);
    emojiBtnRef.current?.focus();
  };

  return (
    <div className="composer-toolbar" ref={containerRef}>
      {/* ── "More formats" dropdown: code, codeblock, ordered list ── */}
      <div className="composer-toolbar__wrap">
        <button
          ref={formatBtnRef}
          type="button"
          className="composer-toolbar__btn"
          aria-label="Mais formatações"
          aria-haspopup="menu"
          aria-expanded={formatOpen}
          disabled={disabled}
          data-testid="toolbar-format-btn"
          onClick={() => {
            setFormatOpen((o) => !o);
            setEmojiOpen(false);
          }}
        >
          <IconMoreFormat />
        </button>

        {formatOpen && (
          <div
            role="menu"
            className="composer-toolbar__dropdown"
            data-testid="toolbar-format-menu"
            onKeyDown={(ev) => {
              if (ev.key === "Escape") closeFormat();
            }}
          >
            {DROPDOWN_ITEMS.map((item, i) => (
              <button
                key={item.testId}
                ref={i === 0 ? firstFmtRef : undefined}
                type="button"
                role="menuitem"
                className="composer-toolbar__format-item"
                onClick={() => handleFormat(item)}
                data-testid={item.testId}
              >
                {item.label}
              </button>
            ))}
          </div>
        )}
      </div>

      {/* ── Direct RF-11 buttons: bold, italic, list ── */}
      {DIRECT_ITEMS.map((item) => (
        <button
          key={item.testId}
          type="button"
          className="composer-toolbar__btn"
          aria-label={item.label}
          disabled={disabled}
          data-testid={item.testId}
          onClick={() => handleFormat(item)}
        >
          <span className="material-symbols-outlined" aria-hidden="true" style={{ fontSize: 18 }}>
            {item.icon}
          </span>
        </button>
      ))}

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
            setFormatOpen(false);
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
