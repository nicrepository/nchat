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
import Italic from "@tiptap/extension-italic";
import ListItem from "@tiptap/extension-list-item";
import OrderedList from "@tiptap/extension-ordered-list";
import Paragraph from "@tiptap/extension-paragraph";
import Text from "@tiptap/extension-text";
import { createMentionExtension } from "./mentionExtension";
import { tiptapDocToMarkdown } from "./tiptapSerializer";
import type { CodecFormat, TTNode } from "./tiptapSerializer";
import type { SendResult } from "./useMessages";

declare module "@tiptap/core" {
  interface Commands<ReturnType> {
    mentionChannelContext: {
      setMentionChannel: (channelId?: string) => ReturnType;
    };
  }
}

const ChatListItem = ListItem.extend({
  content: "paragraph (paragraph|bulletList|orderedList)*",
});

const MentionChannelContext = Extension.create({
  name: "mentionChannelContext",
  addStorage: () => ({ channelId: undefined as string | undefined }),
  addCommands() {
    return {
      setMentionChannel: (channelId) => () => {
        this.storage.channelId = channelId;
        return true;
      },
    };
  },
});

// ── Types ─────────────────────────────────────────────────────────────────────

export interface UseChatEditorOptions {
  placeholder: string;
  disabled?: boolean;
  channelId?: string;
  bodyFormat: CodecFormat;
  initialContent?: TTNode;
  clearOnSend?: boolean;
  testId?: string;
  /**
   * Whether the message has content this editor cannot see — today, a pending
   * attachment (RF-32).
   *
   * The editor owns the text and nothing else, so "is there anything to send"
   * is not a question it can answer alone. When true, an empty document is a
   * valid send; when false (the default) the long-standing rule is unchanged
   * and an empty composer sends nothing.
   */
  canSendEmpty?: boolean;
  onSend: (body: string) => Promise<SendResult>;
  /**
   * Called on real, content-changing editor activity — the typing indicator's
   * one hook into the composer. Fires from TipTap's onUpdate, which only
   * fires on an actual document mutation (never cursor movement, selection,
   * or focus), so this is never a raw keydown listener. Not called from
   * onCreate: opening a conversation with an existing draft must not read as
   * "started typing".
   */
  onActivity?: (hasContent: boolean) => void;
}

// ── Shared extension factory ──────────────────────────────────────────────────

/**
 * Returns the base TipTap extensions for the chat editor (RF-11 feature set).
 * Exported so integration tests can use the same configuration as production.
 *
 * RF-11: bold, italic, inline code, code block, bullet list, ordered list.
 * HardBreak: Shift+Enter inserts a line break within a message.
 */
export function createChatEditorExtensions(enableMentions = true) {
  const extensions = [
    Document,
    Paragraph,
    Text,
    Bold,
    Italic,
    Code,
    CodeBlock,
    BulletList,
    OrderedList,
    ChatListItem,
    HardBreak,
  ];
  return enableMentions
    ? [...extensions, MentionChannelContext, createMentionExtension()]
    : extensions;
}

// ── Hook ──────────────────────────────────────────────────────────────────────

export function useChatEditor({
  placeholder,
  disabled,
  channelId,
  bodyFormat,
  initialContent,
  clearOnSend = true,
  testId = "chat-composer-input",
  canSendEmpty = false,
  onSend,
  onActivity,
}: UseChatEditorOptions) {
  const [sending, setSending] = useState(false);
  const [hasContent, setHasContent] = useState(false);
  const restoreFocusAfterSendRef = useRef(false);

  // Ref, like handleSendRef below: onUpdate is captured once per extensions
  // instance, so a new onActivity identity each render must not require
  // recreating the editor.
  const onActivityRef = useRef(onActivity);
  useEffect(() => {
    onActivityRef.current = onActivity;
  });

  // Ref keeps the latest handleSend accessible to the submitOnEnter extension
  // without causing the extension (useMemo'd) to be recreated on every render.
  const handleSendRef = useRef<(() => Promise<void>) | undefined>(undefined);

  // ponytail: extensions created once; captures ref by reference, not value.
  const extensions = useMemo(
    () => [
      ...createChatEditorExtensions(bodyFormat === "v3"),
      Extension.create({
        name: "submitOnEnter",
        addKeyboardShortcuts() {
          return {
            // Shift-Enter defers to HardBreak; list Enter defers to ListItem.
            "Shift-Enter": () => false,
            Enter: () => {
              if (this.editor.isActive("listItem")) return false;
              void handleSendRef.current?.();
              return true;
            },
          };
        },
      }),
    ],
    [bodyFormat],
  );

  const editor = useEditor(
    {
      extensions,
      editorProps: {
        attributes: {
          class: "chat-msg-area__composer-input",
          "aria-label": placeholder,
          "aria-multiline": "true",
          role: "textbox",
          "data-testid": testId,
        },
      },
      onUpdate: ({ editor: e }) => {
        const hasContentNow = !e.isEmpty;
        setHasContent(hasContentNow);
        onActivityRef.current?.(hasContentNow);
      },
      content: initialContent,
      onCreate: ({ editor: e }) => setHasContent(!e.isEmpty),
    },
    [bodyFormat],
  );

  // Mentions (and therefore setMentionChannel) exist only on the v3 editor.
  // The capability is read from the editor instance, never inferred from the
  // bodyFormat prop: useEditor swaps instances from an effect, so on a
  // v2 → v3 switch (DM → channel, same mounted composer) this effect runs once
  // while `editor` is still the v2 instance. Calling the command there threw and
  // tore down the whole React root, blanking the app until a reload.
  useEffect(() => {
    editor?.commands.setMentionChannel?.(channelId);
  }, [editor, channelId]);

  // Sync editable state and aria attributes when props change.
  useEffect(() => {
    if (!editor) return;
    const isEditable = !disabled && !sending;
    editor.setEditable(isEditable, false);
    const dom = editor.view.dom;
    dom.setAttribute("aria-disabled", String(!isEditable));
    dom.setAttribute("aria-label", placeholder);
    if (isEditable && restoreFocusAfterSendRef.current) {
      dom.focus({ preventScroll: true });
    }
    if (!sending) restoreFocusAfterSendRef.current = false;
  }, [editor, disabled, sending, placeholder]);

  useEffect(() => {
    if (!editor || !sending || !restoreFocusAfterSendRef.current) return;
    const dom = editor.view.dom;
    const cancelRestore = (event: FocusEvent) => {
      if (event.target !== dom && !dom.contains(event.target as Node)) {
        restoreFocusAfterSendRef.current = false;
      }
    };
    document.addEventListener("focusin", cancelRestore);
    return () => document.removeEventListener("focusin", cancelRestore);
  }, [editor, sending]);

  const canSend = (hasContent || canSendEmpty) && !sending && !disabled;

  async function handleSend() {
    if (!canSend || !editor) return;
    const body = tiptapDocToMarkdown(editor.getJSON(), bodyFormat).trim();
    if (!body && !canSendEmpty) return;
    restoreFocusAfterSendRef.current = editor.isFocused;
    setSending(true);
    try {
      const result = await onSend(body);
      if (result.status === "sent" && clearOnSend) {
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
