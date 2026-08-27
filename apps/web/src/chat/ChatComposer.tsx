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
import { useVoiceRecorder } from "./useVoiceRecorder";
import VoiceRecorderPanel from "./VoiceRecorderPanel";
import type { WorkspaceAttachmentLimits } from "./chatApi";
import type { SendResult } from "./useMessages";
import ComposerToolbar from "./ComposerToolbar";
import { useChatEditor } from "./useChatEditor";
import type { CodecFormat } from "./tiptapSerializer";
import type { Message, MessageBodyFormat } from "./chatTypes";
import { formatFileSize } from "./conversationDetailsDisplay";
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
  attachmentLimits?: WorkspaceAttachmentLimits;
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
  attachmentLimits = {
    maxUploadBytes: null,
    maxFiles: 1,
    maxBytes: Number.MAX_SAFE_INTEGER,
  },
  onAttachmentUploaded,
  onActivity,
}: ChatComposerProps) {
  const hadContextRef = useRef(false);
  const fileInputRef = useRef<HTMLInputElement>(null);
  const [dragActive, setDragActive] = useState(false);
  const upload = useAttachmentUpload(uploadTarget, attachmentLimits, onAttachmentUploaded);
  const attachEnabled = uploadTarget !== null && !disabled;
  const uploading = upload.busy;
  // Whether any attachment — queued, uploading, ready or even failed-but-not-
  // dismissed — is currently sitting in the composer. `upload.items` is the
  // single source of truth useAttachmentUpload already keeps for exactly
  // this; a voice recording does not merge with it (issue #670 code review),
  // so recording must not even start while it is non-empty.
  const hasComposerAttachments = upload.items.length > 0;
  // A voice recording is sent through the same onSend as any other message:
  // an empty body plus the one attachment that upload just produced. See
  // handleComposerSend below for why an attachment-only send is already the
  // composer's normal shape.
  const recorder = useVoiceRecorder({
    target: uploadTarget,
    maxUploadBytes: attachmentLimits.maxUploadBytes,
    onUploaded: async (attachmentId) => {
      const result = await onSend("", [attachmentId]);
      return result.status === "sent";
    },
  });
  const recording = recorder.phase !== "idle";
  const pendingAttachments = upload.items
    .map((item) => item.attachment)
    .filter((attachment): attachment is NonNullable<typeof attachment> => attachment !== null);

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
    const result = await onSend(
      body,
      pendingAttachments.length ? pendingAttachments.map((attachment) => attachment.id) : undefined,
    );
    if (result.status === "sent") upload.resetAfterPublish();
    return result;
  };

  const { editor, canSend, sending, handleSend } = useChatEditor({
    placeholder,
    disabled,
    channelId,
    bodyFormat,
    // An attachment is content, so a composer holding one may send an empty
    // document — but not while its own upload is still running.
    canSendEmpty: pendingAttachments.length > 0 && !uploading,
    onSend: handleComposerSend,
    onActivity,
  });

  // Whether a new attachment may be taken at all right now. A voice
  // recording is deliberately never combined with an attachment (issue
  // #670): the picker and the attach button already disappear while
  // `recording` (the whole toolbar row is hidden), and this is the same
  // rule extended to the one entry point that stays reachable regardless —
  // the composer box's own drag-and-drop surface, bound below whether or
  // not a recording is in progress.
  const canAcceptAttachments = attachEnabled && !recording;

  // Both entry points funnel here, so there is exactly one place that decides
  // whether a file may be taken and exactly one validation path behind it.
  const acceptFiles = (files: Iterable<File> | undefined) => {
    if (!canAcceptAttachments || !files) return;
    upload.selectFiles(files);
  };

  const handlePickerChange = (event: ChangeEvent<HTMLInputElement>) => {
    acceptFiles(event.target.files ?? undefined);
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
    // Prevented whenever this composer could in principle handle the drag —
    // recording or not — so the browser never takes over navigation to the
    // dragged file. Only the *visual* affordance below is conditional on
    // recording: a user must never be shown a drop target that a recording
    // is about to make ignore the drop.
    event.preventDefault();
    if (!canAcceptAttachments) return;
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
    // Prevented whenever this composer could in principle handle the drop —
    // recording or not — so the browser never navigates away to the dropped
    // file. `setDragActive(false)` is unconditional for the same reason:
    // nothing must be left signalling an accepted drop that a recording
    // silently ignored.
    event.preventDefault();
    setDragActive(false);
    if (!canAcceptAttachments) return;
    acceptFiles(event.dataTransfer.files ?? undefined);
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
        {recording && <VoiceRecorderPanel recorder={recorder} />}
        <div
          className="chat-msg-area__composer-editor-wrap"
          hidden={recording}
        >
          {editor?.isEmpty && !editor.isActive("listItem") && (
            <div className="chat-msg-area__composer-placeholder" aria-hidden="true">
              {placeholder}
            </div>
          )}
          <EditorContent editor={editor} />
        </div>
        {!recording && uploadTarget && (upload.items.length > 0 || dragActive || upload.notice) && (
          <div
            className="chat-msg-area__composer-upload"
            data-testid="chat-composer-upload-status"
            role={upload.status === "failed" ? "alert" : "status"}
          >
            <div className="chat-msg-area__composer-upload-header">
              <span className="material-symbols-outlined" aria-hidden="true">
                attach_file
              </span>
              <strong>
                {upload.items.length === 1
                  ? "1 arquivo anexado"
                  : `${upload.items.length} arquivos anexados`}
              </strong>
              {upload.aggregateProgress && upload.items.length > 1 && (
                <span className="chat-msg-area__composer-upload-total">
                  {uploadPercent(upload.aggregateProgress)}% no total
                </span>
              )}
            </div>
            {dragActive && upload.status === "idle" && (
              <span className="chat-msg-area__composer-upload-notice">Solte os arquivos aqui.</span>
            )}
            {upload.notice && (
              <span className="chat-msg-area__composer-upload-notice" role="status">
                {upload.notice}
              </span>
            )}
            <div className="chat-msg-area__composer-upload-list">
              {upload.items.map((item) => (
                <div
                  key={item.localId}
                  className="chat-msg-area__composer-upload-item"
                  data-testid={
                    item.status === "success" ? "chat-composer-pending-attachment" : undefined
                  }
                >
                  <span className="chat-msg-area__composer-upload-icon" aria-hidden="true">
                    <span className="material-symbols-outlined">draft</span>
                  </span>
                  <div className="chat-msg-area__composer-upload-body">
                    <span className="chat-msg-area__composer-upload-name" title={item.file.name}>
                      {item.file.name}
                    </span>
                    <span
                      className={`chat-msg-area__composer-upload-state chat-msg-area__composer-upload-state--${item.status}`}
                    >
                      {item.status === "queued" && "Aguardando envio"}
                      {item.status === "uploading" &&
                        `Enviando arquivo…${item.progress ? ` ${uploadPercent(item.progress)}%` : ""}`}
                      {item.status === "success" && "Pronto para enviar"}
                      {item.status === "failed" && item.error}
                    </span>
                    <span className="chat-msg-area__composer-upload-size">
                      {formatFileSize(item.file.size)}
                    </span>
                    {item.status === "uploading" && item.progress && (
                      <progress
                        className="chat-msg-area__composer-progress"
                        data-testid="chat-composer-upload-progress"
                        aria-label={
                          upload.items.length === 1
                            ? "Progresso do envio"
                            : `Progresso do envio de ${item.file.name}`
                        }
                        value={item.progress.loaded}
                        max={item.progress.total}
                      />
                    )}
                    {item.status === "failed" && (
                      <button
                        type="button"
                        className="chat-msg-area__composer-upload-retry"
                        onClick={() => upload.retry(item.localId)}
                      >
                        Tentar novamente
                      </button>
                    )}
                  </div>
                  <button
                    type="button"
                    className="chat-msg-area__composer-quote-close"
                    aria-label={
                      upload.items.length === 1
                        ? "Remover anexo"
                        : `Remover anexo ${item.file.name}`
                    }
                    data-testid="chat-composer-remove-attachment"
                    onClick={() => upload.remove(item.localId)}
                  >
                    <span className="material-symbols-outlined" aria-hidden="true">
                      close
                    </span>
                  </button>
                </div>
              ))}
            </div>
            {upload.aggregateProgress && upload.items.length > 1 && (
              <progress
                className="chat-msg-area__composer-progress chat-msg-area__composer-progress--total"
                aria-label="Progresso total dos anexos"
                value={upload.aggregateProgress.loaded}
                max={upload.aggregateProgress.total}
              />
            )}
          </div>
        )}
        {!recording && (
          <div className="chat-msg-area__composer-bar">
            <ComposerToolbar editor={editor ?? null} disabled={disabled || sending} />
            {uploadTarget && (
              <>
                <input
                  ref={fileInputRef}
                  type="file"
                  multiple
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
                  disabled={!attachEnabled || upload.items.length >= 10}
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
            {uploadTarget && recorder.supported && (
              <button
                type="button"
                className="composer-toolbar__btn"
                aria-label="Gravar mensagem de voz"
                // A voice recording is never merged with pending attachments
                // (issue #670 code review) — recording must not even start
                // while one exists, so this only ever disables, never
                // silently drops or combines the two.
                title={
                  hasComposerAttachments
                    ? "Remova os anexos para gravar uma mensagem de voz."
                    : undefined
                }
                disabled={!attachEnabled || uploading || hasComposerAttachments}
                data-testid="chat-composer-record-btn"
                onClick={recorder.start}
              >
                <span
                  className="material-symbols-outlined"
                  aria-hidden="true"
                  style={{ fontSize: 18 }}
                >
                  mic
                </span>
              </button>
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
        )}
      </div>
    </div>
  );
}
