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
import {
  useEffect,
  useRef,
  useState,
  type ChangeEvent,
  type DragEvent,
  type KeyboardEvent,
} from "react";
import type { UploadProgress } from "../lib/api";
import { useAttachmentUpload, type AttachmentUploadTarget } from "./useAttachmentUpload";
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

/**
 * Whole percent sent, floored so the label never reads 100% before the last
 * byte has left. `total` is always positive here: apiUpload drops any report
 * without a computable length.
 */
function uploadPercent({ loaded, total }: UploadProgress): number {
  return Math.min(100, Math.floor((loaded / total) * 100));
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
  /**
   * Posts the composed message. `attachmentIds` (RF-32) references files that
   * are already on the server: the bytes went up when the file was chosen, and
   * pressing Enviar links them to the new message rather than sending them
   * again.
   */
  onSend: (body: string, attachmentIds?: string[]) => Promise<SendResult>;
  /**
   * Destination for attachments (RF-32, issue #458). One prop serves channels
   * and DMs — the composer is already the single place both render — so the
   * picker and the drop zone below need no per-kind branch.
   *
   * Absent means the composer has no attachment affordance at all, which is
   * what keeps the existing composer tests and any non-conversation use
   * unchanged.
   */
  uploadTarget?: AttachmentUploadTarget | null;
  /** Called after a successful upload so the caller can refresh its file list. */
  onAttachmentUploaded?: () => void;
  /**
   * Typing indicator: real, content-changing composer activity. See
   * UseChatEditorOptions.onActivity — this is a straight passthrough, so the
   * composer stays unaware of what a caller does with it (send a
   * typing.start, nothing at all).
   */
  onActivity?: (hasContent: boolean) => void;
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
  uploadTarget = null,
  onAttachmentUploaded,
  onActivity,
}: ChatComposerProps) {
  const hadContextRef = useRef(false);
  const fileInputRef = useRef<HTMLInputElement>(null);
  const [dragActive, setDragActive] = useState(false);
  const upload = useAttachmentUpload(uploadTarget, onAttachmentUploaded);
  const attachEnabled = uploadTarget !== null && !disabled;
  const uploading = upload.status === "uploading";
  const pendingAttachment = upload.uploadedAttachment;

  /**
   * The one place a send is assembled (RF-32).
   *
   * Two things happen here that the editor cannot do itself:
   *
   *  - an upload in flight blocks the send. Returning "stale" rather than
   *    throwing is deliberate: it is the result the editor already understands
   *    as "nothing happened, keep the draft", so Enter during an upload costs
   *    the user nothing;
   *  - the pending attachment is cleared only on a confirmed "sent". A "stale"
   *    result or a thrown error leaves it exactly where it was, so the same
   *    already-uploaded file can be sent again without re-uploading it.
   */
  const handleComposerSend = async (body: string): Promise<SendResult> => {
    if (uploading) return { status: "stale" };
    const result = await onSend(body, pendingAttachment ? [pendingAttachment.id] : undefined);
    if (result.status === "sent") upload.dismiss();
    return result;
  };

  const { editor, canSend, sending, handleSend } = useChatEditor({
    placeholder,
    disabled,
    channelId,
    bodyFormat,
    // An attachment is content, so a composer holding one may send an empty
    // document — but not while its own upload is still running.
    canSendEmpty: pendingAttachment !== null && !uploading,
    onSend: handleComposerSend,
    onActivity,
  });

  // Both entry points funnel here, so there is exactly one place that decides
  // whether a file may be taken and exactly one validation path behind it.
  const acceptFile = (file: File | undefined) => {
    if (!attachEnabled || !file) return;
    upload.selectFile(file);
  };

  const handlePickerChange = (event: ChangeEvent<HTMLInputElement>) => {
    acceptFile(event.target.files?.[0]);
    // Clearing the value is what lets the same file be chosen again after a
    // failure: without it the input reports no change and fires nothing.
    event.target.value = "";
  };

  // Only a drag that actually carries files is intercepted. A text drag, or a
  // drag from inside the editor, keeps its default browser behaviour.
  const dragHasFiles = (event: DragEvent<HTMLDivElement>) =>
    Array.from(event.dataTransfer?.types ?? []).includes("Files");

  const handleDragOver = (event: DragEvent<HTMLDivElement>) => {
    if (!attachEnabled || !dragHasFiles(event)) return;
    event.preventDefault();
    setDragActive(true);
  };

  // A dragleave also fires every time the pointer crosses from the box into one
  // of its children — the quote, the editor, the toolbar. Ending the drag state
  // there would flicker the outline on each internal boundary, so only a leave
  // that really exits the composer counts. A null relatedTarget (leaving the
  // window, or a drop) is outside by definition.
  const handleDragLeave = (event: DragEvent<HTMLDivElement>) => {
    const next = event.relatedTarget;
    if (next instanceof Node && event.currentTarget.contains(next)) return;
    setDragActive(false);
  };

  const handleDrop = (event: DragEvent<HTMLDivElement>) => {
    if (!attachEnabled || !dragHasFiles(event)) return;
    // Prevented only for a drop this composer handles, so the browser never
    // navigates away to the dropped file.
    event.preventDefault();
    setDragActive(false);
    // One file per request is the whole contract file-service accepts; a
    // multi-file drop takes the first rather than silently starting several.
    acceptFile(event.dataTransfer.files?.[0]);
  };

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
        className={`chat-msg-area__composer-box${disabled ? " chat-msg-area__composer-box--disabled" : ""}${dragActive ? " chat-msg-area__composer-box--drag" : ""}`}
        onKeyDownCapture={handleKeyDownCapture}
        onDragOver={handleDragOver}
        onDragLeave={handleDragLeave}
        onDrop={handleDrop}
        data-testid="chat-composer-box"
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
        {uploadTarget && (upload.status !== "idle" || dragActive) && (
          <div
            className="chat-msg-area__composer-upload"
            data-testid="chat-composer-upload-status"
            role={upload.status === "failed" ? "alert" : "status"}
          >
            {dragActive && upload.status === "idle" && <span>Solte o arquivo para enviar.</span>}
            {uploading && (
              <>
                <span>
                  Enviando arquivo…
                  {upload.progress && ` ${uploadPercent(upload.progress)}%`}
                </span>
                {/* Determinate only when the transport actually counted the
                    bytes. With nothing measured the element is omitted rather
                    than rendered value-less, so the text above stays the only
                    claim being made. */}
                {upload.progress && (
                  <progress
                    className="chat-msg-area__composer-progress"
                    data-testid="chat-composer-upload-progress"
                    aria-label="Progresso do envio"
                    value={upload.progress.loaded}
                    max={upload.progress.total}
                  />
                )}
              </>
            )}
            {/* The file is uploaded but not yet sent. Saying so is the whole
                point: the previous copy read "Arquivo enviado", which described
                a message that did not exist. */}
            {upload.status === "success" && (
              <span data-testid="chat-composer-pending-attachment">
                Anexo pronto para envio: {upload.uploadedName}
              </span>
            )}
            {upload.status === "failed" && <span>{upload.error}</span>}
            {(upload.status === "failed" || upload.status === "success") && (
              <button
                type="button"
                className="chat-msg-area__composer-quote-close"
                aria-label={
                  upload.status === "success" ? "Remover anexo" : "Dispensar aviso de anexo"
                }
                data-testid="chat-composer-remove-attachment"
                onClick={upload.dismiss}
              >
                <span className="material-symbols-outlined" aria-hidden="true">
                  close
                </span>
              </button>
            )}
          </div>
        )}
        <div className="chat-msg-area__composer-bar">
          <ComposerToolbar editor={editor ?? null} disabled={disabled || sending} />
          {uploadTarget && (
            <>
              <input
                ref={fileInputRef}
                type="file"
                className="chat-msg-area__composer-file-input"
                data-testid="chat-composer-file-input"
                aria-label="Escolher arquivo para anexar"
                hidden
                onChange={handlePickerChange}
              />
              <button
                type="button"
                className="composer-toolbar__btn"
                aria-label="Anexar arquivo"
                disabled={!attachEnabled || uploading}
                data-testid="chat-composer-attach-btn"
                onClick={() => fileInputRef.current?.click()}
              >
                <span
                  className="material-symbols-outlined"
                  aria-hidden="true"
                  style={{ fontSize: 18 }}
                >
                  attach_file
                </span>
              </button>
            </>
          )}
          <button
            type="button"
            className="chat-msg-area__send-btn"
            // Unavailable while a file is going up, whatever else the composer
            // holds: the attachment is part of the message being written, and
            // sending now would post a message without it.
            disabled={!canSend || uploading}
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
