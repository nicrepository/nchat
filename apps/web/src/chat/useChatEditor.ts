/**
 * useChatEditor — encapsulates TipTap editor setup for the chat composer.
 *
 * Responsibilities:
 * - Create and configure the TipTap Editor (RF-11 extensions + submitOnEnter).
 * - Track content presence and sending state.
 * - Provide handleSend that serializes → calls onSend → clears on success.
 * - Sync editable / aria attributes when disabled/sending changes.
 *
 * Security: if @tiptap/extension-link is added in the future, configure it with
 * validate/allowedProtocols that explicitly rejects "javascript:" and "data:"
 * URIs to prevent XSS via anchor links. Never rely on the default link handling
 * for user-submitted content.
 */

import { useEffect, useMemo, useRef, useState } from "react";
import { useEditor } from "@tiptap/react";
import { Extension } from "@tiptap/core";
import Bold from "@tiptap/extension-bold";
import BulletList from "@tiptap/extension-bullet-list";
import Code from "@tiptap/extension-code";
import CodeBlock from "@tiptap/extension-code-block";
import Document from "@tiptap/extension-document";
import HardBreak from "@tiptap/extension-hard-break";
import History from "@tiptap/extension-history";
import Italic from "@tiptap/extension-italic";
import ListItem from "@tiptap/extension-list-item";
import OrderedList from "@tiptap/extension-ordered-list";
import Paragraph from "@tiptap/extension-paragraph";
import Text from "@tiptap/extension-text";
import { tiptapDocToMarkdown } from "./tiptapSerializer";
import type { SendResult } from "./useMessages";

// ── Types ─────────────────────────────────────────────────────────────────────

export interface UseChatEditorOptions {
  placeholder: string;
  disabled: boolean;
  onSend: (body: string) => Promise<SendResult>;
}

// ── Shared extension factory ──────────────────────────────────────────────────

/**
 * Returns the base TipTap extensions for the chat editor (RF-11 feature set).
 * Exported so integration tests can use the same configuration as production.
 *
 * RF-11: bold, italic, inline code, code block, bullet list, ordered list.
 * HardBreak: Shift+Enter inserts a line break within a message.
 * History: undo/redo support.
 */
export function createChatEditorExtensions() {
  return [
    Document,
    Paragraph,
    Text,
    Bold,
    Italic,
    Code,
    CodeBlock,
    BulletList,
    OrderedList,
    ListItem,
    HardBreak,
    History,
  ];
}

// ── Hook ──────────────────────────────────────────────────────────────────────

export function useChatEditor({ placeholder, disabled, onSend }: UseChatEditorOptions) {
  const [sending, setSending] = useState(false);
  const [hasContent, setHasContent] = useState(false);

  // Ref keeps the latest handleSend accessible to the submitOnEnter extension
  // without causing the extension (useMemo'd) to be recreated on every render.
  const handleSendRef = useRef<(() => Promise<void>) | undefined>(undefined);

  // ponytail: extensions created once; captures ref by reference, not value.
  const extensions = useMemo(
    () => [
      ...createChatEditorExtensions(),
      Extension.create({
        name: "submitOnEnter",
        addKeyboardShortcuts() {
          return {
            // Declarative: Shift-Enter defers to HardBreak; Enter sends.
            "Shift-Enter": () => false,
            Enter: () => {
              void handleSendRef.current?.();
              return true;
            },
          };
        },
      }),
    ],
    [],
  );

  const editor = useEditor({
    extensions,
    editorProps: {
      attributes: {
        class: "chat-msg-area__composer-input",
        "aria-label": placeholder,
        "aria-multiline": "true",
        role: "textbox",
        "data-testid": "chat-composer-input",
      },
    },
    onUpdate: ({ editor: e }) => {
      setHasContent(!e.isEmpty);
    },
  });

  // Sync editable state and aria attributes when props change.
  useEffect(() => {
    if (!editor) return;
    const isEditable = !disabled && !sending;
    editor.setEditable(isEditable, false);
    const dom = editor.view.dom;
    dom.setAttribute("aria-disabled", String(!isEditable));
    dom.setAttribute("aria-label", placeholder);
  }, [editor, disabled, sending, placeholder]);

  const canSend = hasContent && !sending && !disabled;

  async function handleSend() {
    if (!canSend || !editor) return;
    const body = tiptapDocToMarkdown(editor.getJSON()).trim();
    if (!body) return;
    setSending(true);
    try {
      const result = await onSend(body);
      if (result.status === "sent") {
        editor.commands.clearContent(true);
      }
      // result.status === "stale": target changed — editor content preserved
    } catch {
      // failure — content preserved for retry, error shown by parent
    } finally {
      setSending(false);
    }
  }

  // Keep ref in sync with latest handleSend after every render.
  useEffect(() => {
    handleSendRef.current = handleSend;
  });

  return { editor, canSend, sending, handleSend };
}
