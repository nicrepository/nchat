/**
 * ChatComposer — self-contained message composer with TipTap editor.
 *
 * Owns: editor lifecycle (via useChatEditor), formatting toolbar, send button,
 * and EditorContent rendering. ChatMessageArea uses this component and does not
 * import any TipTap primitives directly.
 *
 * Security: editor serializes to Markdown via tiptapDocToMarkdown before sending;
 * no HTML ever reaches the server or RichTextRenderer.
 */

import { useCallback } from "react";
import { EditorContent } from "@tiptap/react";
import type { SendResult } from "./useMessages";
import ComposerToolbar from "./ComposerToolbar";
import { useChatEditor } from "./useChatEditor";

// ── Icons ─────────────────────────────────────────────────────────────────────

function IconSend() {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
      width="16"
      height="16"
    >
      <line x1="22" y1="2" x2="11" y2="13" />
      <polygon points="22 2 15 22 11 13 2 9 22 2" />
    </svg>
  );
}

// ── Types ─────────────────────────────────────────────────────────────────────

export interface ChatComposerProps {
  placeholder: string;
  disabled?: boolean;
  onSend: (body: string) => Promise<SendResult>;
}

// ── Component ─────────────────────────────────────────────────────────────────

export default function ChatComposer({ placeholder, disabled = false, onSend }: ChatComposerProps) {
  const { editor, canSend, sending, handleSend } = useChatEditor({
    placeholder,
    disabled,
    onSend,
  });

  // Test-only: expose the TipTap editor on its DOM element so JSDOM tests can
  // insert content programmatically. Tree-shaken in production builds
  // (import.meta.env.PROD is a build-time constant in Vite).
  const editorWrapRef = useCallback(
    (el: HTMLDivElement | null) => {
      if (import.meta.env.PROD || !el || !editor) return;
      const pm = el.querySelector('[data-testid="chat-composer-input"]');
      if (pm) (pm as unknown as Record<string, unknown>).__tiptapEditor = editor;
    },
    [editor],
  );

  return (
    <div className="chat-msg-area__composer">
      <div
        className={`chat-msg-area__composer-box${disabled ? " chat-msg-area__composer-box--disabled" : ""}`}
      >
        <div className="chat-msg-area__composer-editor-wrap" ref={editorWrapRef}>
          {editor?.isEmpty && (
            <div className="chat-msg-area__composer-placeholder" aria-hidden="true">
              {placeholder}
            </div>
          )}
          <EditorContent editor={editor} />
        </div>
        <div className="chat-msg-area__composer-bar">
          <ComposerToolbar editor={editor ?? null} disabled={disabled || sending} />
          <button
            type="button"
            className="chat-msg-area__send-btn"
            disabled={!canSend}
            aria-label="Enviar mensagem"
            onClick={() => void handleSend()}
            data-testid="chat-send-btn"
          >
            <IconSend />
          </button>
        </div>
      </div>
    </div>
  );
}
