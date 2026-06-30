/**
 * ComposerToolbar — formatting toolbar for the message composer.
 * Security: all insertions are plain string operations — no innerHTML injection.
 * ponytail: emoji picker has no search (add when requested); GIF/upload are RF-12+.
 *
 * RF-11: bold/italic/ul exposed as direct buttons (Material Symbols icons).
 *        code/codeblock/ol remain in the "more formats" dropdown.
 *        link/attach/mic: removed — add back when the backing RF lands.
 */

import { useEffect, useLayoutEffect, useRef, useState } from "react";
import type { RefObject } from "react";
import { FORMAT_ITEMS } from "./richTextGrammar";
import type { FormatItem } from "./richTextGrammar";

// ── Toolbar-specific presentation (labels/testIds are not grammar concerns) ────

interface ToolbarItem extends FormatItem {
  label: string;
  testId: string;
}

interface DirectItem extends ToolbarItem {
  icon: string; // Material Symbols ligature name
}

// Direct visible buttons (bold, italic, ul) — linked to RF-11 grammar.
const DIRECT_ITEMS: DirectItem[] = [
  { ...FORMAT_ITEMS[0], label: "Negrito", testId: "fmt-bold", icon: "format_bold" },
  { ...FORMAT_ITEMS[1], label: "Itálico", testId: "fmt-italic", icon: "format_italic" },
  { ...FORMAT_ITEMS[4], label: "Lista", testId: "fmt-ul", icon: "format_list_bulleted" },
];

// "More formats" dropdown: items not shown as direct buttons.
const DROPDOWN_ITEMS: ToolbarItem[] = [
  { ...FORMAT_ITEMS[2], label: "Código", testId: "fmt-code" },
  { ...FORMAT_ITEMS[3], label: "Bloco de código", testId: "fmt-codeblock" },
  { ...FORMAT_ITEMS[5], label: "Lista ordenada", testId: "fmt-ol" },
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

// ── Insertion helpers ─────────────────────────────────────────────────────────

type Sel = { s: number; e: number };

/** Expand sel to the full lines it touches (start-of-line ↔ end-of-line). */
function expandToLines(val: string, sel: Sel): Sel {
  let s = sel.s;
  while (s > 0 && val[s - 1] !== "\n") s--;
  let e = sel.e;
  while (e < val.length && val[e] !== "\n") e++;
  return { s, e };
}

function wrapInline(
  ta: HTMLTextAreaElement,
  setDraft: (v: string) => void,
  mark: string,
  sel: Sel,
) {
  const text = ta.value.slice(sel.s, sel.e);
  setDraft(ta.value.slice(0, sel.s) + mark + text + mark + ta.value.slice(sel.e));
  // Cursor stays on wrapped content (not selecting markers); collapsed when no selection.
  requestAnimationFrame(() => {
    ta.focus();
    ta.setSelectionRange(sel.s + mark.length, sel.s + mark.length + text.length);
  });
}

function wrapBlock(
  ta: HTMLTextAreaElement,
  setDraft: (v: string) => void,
  kind: "code" | "ul" | "ol",
  sel: Sel,
) {
  // Always expand to full line boundaries so mid-line cursors are handled correctly.
  const exp = expandToLines(ta.value, sel);
  const text = ta.value.slice(exp.s, exp.e);
  let insert: string;
  let cursor: number;
  if (kind === "code") {
    const body = text || "";
    insert = "```\n" + body + (body && !body.endsWith("\n") ? "\n" : "") + "```";
    cursor = exp.s + 4; // inside fence, after "```\n"
  } else {
    const lines = text.split("\n");
    insert =
      kind === "ul"
        ? lines.map((l) => "- " + l).join("\n")
        : lines.map((l, i) => `${i + 1}. ${l}`).join("\n");
    cursor = exp.s + insert.length; // after all prefixed lines
  }
  setDraft(ta.value.slice(0, exp.s) + insert + ta.value.slice(exp.e));
  // Collapsed cursor — never leave markers selected so they aren't replaced on first keystroke.
  requestAnimationFrame(() => {
    ta.focus();
    ta.setSelectionRange(cursor, cursor);
  });
}

// ── Icons ─────────────────────────────────────────────────────────────────────
// Using Material Symbols (self-hosted) to match shell.html prototype visual.

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
  textareaRef: RefObject<HTMLTextAreaElement | null>;
  setDraft: (v: string) => void;
  disabled?: boolean;
}

export default function ComposerToolbar({
  textareaRef,
  setDraft,
  disabled = false,
}: ComposerToolbarProps) {
  const [formatOpen, setFormatOpen] = useState(false);
  const [emojiOpen, setEmojiOpen] = useState(false);
  const containerRef = useRef<HTMLDivElement>(null);
  const formatBtnRef = useRef<HTMLButtonElement>(null);
  const emojiBtnRef = useRef<HTMLButtonElement>(null);
  const firstFmtRef = useRef<HTMLButtonElement>(null);
  const firstEmojiRef = useRef<HTMLButtonElement>(null);
  // Snapshot textarea selection on pointerdown (before button click steals focus).
  const savedSel = useRef<Sel | null>(null);

  // Close panels + clear savedSel on outside click.
  useEffect(() => {
    const onDown = (ev: MouseEvent) => {
      if (containerRef.current && !containerRef.current.contains(ev.target as Node)) {
        setFormatOpen(false);
        setEmojiOpen(false);
        savedSel.current = null; // stale selection is no longer valid
      }
    };
    document.addEventListener("mousedown", onDown);
    return () => document.removeEventListener("mousedown", onDown);
  }, []);

  // Synchronous focus on first item when panel opens (useLayoutEffect avoids rAF).
  useLayoutEffect(() => {
    if (formatOpen) firstFmtRef.current?.focus();
  }, [formatOpen]);
  useLayoutEffect(() => {
    if (emojiOpen) firstEmojiRef.current?.focus();
  }, [emojiOpen]);

  function snapSel() {
    const ta = textareaRef.current;
    if (ta) savedSel.current = { s: ta.selectionStart, e: ta.selectionEnd };
  }

  function getSel(): Sel {
    if (savedSel.current) return savedSel.current;
    const ta = textareaRef.current;
    return ta ? { s: ta.selectionStart, e: ta.selectionEnd } : { s: 0, e: 0 };
  }

  /** Core insertion — shared by direct buttons and the dropdown. Closes all panels. */
  function insert({ kind, marker }: FormatItem) {
    const ta = textareaRef.current;
    if (!ta) return;
    const sel = getSel();
    if (kind === "inline") wrapInline(ta, setDraft, marker, sel);
    else if (kind === "block-code") wrapBlock(ta, setDraft, "code", sel);
    else if (kind === "list-ul") wrapBlock(ta, setDraft, "ul", sel);
    else wrapBlock(ta, setDraft, "ol", sel);
    setFormatOpen(false);
    setEmojiOpen(false);
    savedSel.current = null;
  }

  function handleFormat(item: ToolbarItem) {
    insert(item); // insert() already closes formatOpen and emojiOpen
  }

  function handleEmoji(emoji: string) {
    const ta = textareaRef.current;
    if (!ta) return;
    const { s, e } = getSel();
    setDraft(ta.value.slice(0, s) + emoji + ta.value.slice(e));
    requestAnimationFrame(() => {
      ta.focus();
      ta.setSelectionRange(s + emoji.length, s + emoji.length);
    });
    setEmojiOpen(false);
    savedSel.current = null;
  }

  const closeFormat = () => {
    setFormatOpen(false);
    savedSel.current = null;
    formatBtnRef.current?.focus();
  };
  const closeEmoji = () => {
    setEmojiOpen(false);
    savedSel.current = null;
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
          onPointerDown={snapSel}
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
          onPointerDown={snapSel}
          onClick={() => insert(item)}
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
          onPointerDown={snapSel}
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
