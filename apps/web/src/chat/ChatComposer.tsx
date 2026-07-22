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

import { EditorContent } from "@tiptap/react";
import { useEffect, useRef, type KeyboardEvent } from "react";
import type { SendResult } from "./useMessages";
import ComposerToolbar from "./ComposerToolbar";
import { useChatEditor } from "./useChatEditor";
import type { CodecFormat } from "./tiptapSerializer";
import type { Message, MessageBodyFormat } from "./chatTypes";
import { senderLabel } from "./messageDisplay";
import RichTextRenderer from "./RichTextRenderer";

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
  channelId?: string;
  bodyFormat: CodecFormat;
  disabled?: boolean;
  replyPreview?: ComposerReplyPreview | null;
  onCancelReply?: () => void;
  referencePreview?: PendingReferencePreview;
  referenceTargetLabel?: string;
  onCancelReference?: () => void;
  onSend: (body: string) => Promise<SendResult>;
}

export interface ComposerReplyPreview {
  authorLabel: string;
  bodyText: string;
  bodyFormat: MessageBodyFormat;
  isRemoved: boolean;
}

export type PendingReferencePreview =
  | { status: "idle" }
  | { status: "loading"; messageId: string }
  | { status: "available"; messageId: string; message: Message }
  | { status: "unavailable"; messageId: string };

function ComposerReference({
  preview,
  targetLabel,
  onCancel,
}: {
  preview: PendingReferencePreview;
  targetLabel: string;
  onCancel?: () => void;
}) {
  if (preview.status === "idle") return null;

  return (
    <div className="chat-msg-area__composer-quote" data-testid="chat-composer-reference">
      <div className="chat-msg-area__composer-quote-body">
        {preview.status === "loading" && (
          <div className="chat-msg-area__quote-excerpt" role="status">
            Carregando citação…
          </div>
        )}
        {preview.status === "unavailable" && (
          <div className="chat-msg-area__quote-excerpt">citação indisponível</div>
        )}
        {preview.status === "available" && (
          <>
            <div className="chat-msg-area__quote-author">
              {senderLabel(preview.message)} · {targetLabel}
            </div>
            <div className="chat-msg-area__quote-excerpt">
              <RichTextRenderer
                text={preview.message.bodyText}
                bodyFormat={preview.message.bodyFormat}
              />
            </div>
          </>
        )}
      </div>
      <button
        type="button"
        className="chat-msg-area__composer-quote-close"
        aria-label="Cancelar citação"
        onClick={onCancel}
      >
        <span className="material-symbols-outlined" aria-hidden="true">
          close
        </span>
      </button>
    </div>
  );
}

// ── Component ─────────────────────────────────────────────────────────────────

export default function ChatComposer({
  placeholder,
  channelId,
  bodyFormat,
  disabled = false,
  replyPreview = null,
  onCancelReply,
  referencePreview = { status: "idle" },
  referenceTargetLabel = "Conversa",
  onCancelReference,
  onSend,
}: ChatComposerProps) {
  const { editor, canSend, sending, handleSend } = useChatEditor({
    placeholder,
    disabled,
    channelId,
    bodyFormat,
    onSend,
  });
  const hadContextRef = useRef(false);

  useEffect(() => {
    const hasContext = Boolean(replyPreview) || referencePreview.status !== "idle";
    if (hasContext && !hadContextRef.current && editor && !disabled) {
      editor.commands.focus("end");
    }
    hadContextRef.current = hasContext;
  }, [disabled, editor, referencePreview.status, replyPreview]);

  const handleKeyDownCapture = (event: KeyboardEvent<HTMLDivElement>) => {
    if (event.key !== "Escape") return;
    if (referencePreview.status !== "idle") onCancelReference?.();
    else if (replyPreview) onCancelReply?.();
  };

  return (
    <div className="chat-msg-area__composer">
      <div
        className={`chat-msg-area__composer-box${disabled ? " chat-msg-area__composer-box--disabled" : ""}`}
        onKeyDownCapture={handleKeyDownCapture}
      >
        {replyPreview && (
          <div className="chat-msg-area__composer-quote" data-testid="chat-composer-quote">
            <div className="chat-msg-area__composer-quote-body">
              <div className="chat-msg-area__quote-author">{replyPreview.authorLabel}</div>
              <div className="chat-msg-area__quote-excerpt">
                {replyPreview.isRemoved ? (
                  "Mensagem original indisponível."
                ) : (
                  <RichTextRenderer
                    text={replyPreview.bodyText}
                    bodyFormat={replyPreview.bodyFormat}
                  />
                )}
              </div>
            </div>
            <button
              type="button"
              className="chat-msg-area__composer-quote-close"
              aria-label="Cancelar resposta"
              onClick={onCancelReply}
            >
              <span className="material-symbols-outlined" aria-hidden="true">
                close
              </span>
            </button>
          </div>
        )}
        <ComposerReference
          preview={referencePreview}
          targetLabel={referenceTargetLabel}
          onCancel={onCancelReference}
        />
        <div className="chat-msg-area__composer-editor-wrap">
          {editor?.isEmpty && !editor.isActive("listItem") && (
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
