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
import type { Editor } from "@tiptap/core";
import {
  useEffect,
  useRef,
  useState,
  type ChangeEvent,
  type DragEvent,
  type KeyboardEvent,
  type RefObject,
} from "react";
import type { UploadProgress } from "../lib/api";
import {
  useAttachmentUpload,
  type AttachmentUploadItem,
  type AttachmentUploadState,
  type AttachmentUploadTarget,
} from "./useAttachmentUpload";
import type { WorkspaceAttachmentLimits } from "./chatApi";
import type { SendResult } from "./useMessages";
import ComposerToolbar, { type ComposerEmojiOptions } from "./ComposerToolbar";
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
  /**
   * The reader's shared emoji history, lent to the toolbar's picker (#496).
   * Absent, the picker still opens — it simply offers no "Recentes".
   */
  emoji?: ComposerEmojiOptions;
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
  targetLabel = "Conversa",
  onCancel,
}: {
  preview: PendingReferencePreview;
  targetLabel?: string;
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

/** The upload panel's cap on how many files one message may carry. */
const maxComposerFiles = 10;

/**
 * The message being replied to, above the editor. Extracted from ChatComposer
 * with the upload panel and the attach button: the composer's own logic is the
 * editor, the send and the drop zone, and it was carrying three unrelated
 * presentational trees that made it unreadable and pushed it far past the
 * complexity gate.
 */
function ComposerReplyQuote({
  preview,
  onCancel,
}: {
  preview: ComposerReplyPreview | null | undefined;
  onCancel?: () => void;
}) {
  if (!preview) return null;
  return (
    <div className="chat-msg-area__composer-quote" data-testid="chat-composer-quote">
      <div className="chat-msg-area__composer-quote-body">
        <div className="chat-msg-area__quote-author">{preview.authorLabel}</div>
        <div className="chat-msg-area__quote-excerpt">
          {preview.isRemoved ? (
            "Mensagem original indisponível."
          ) : (
            <RichTextRenderer text={preview.bodyText} bodyFormat={preview.bodyFormat} />
          )}
        </div>
      </div>
      <button
        type="button"
        className="chat-msg-area__composer-quote-close"
        aria-label="Cancelar resposta"
        onClick={onCancel}
      >
        <span className="material-symbols-outlined" aria-hidden="true">
          close
        </span>
      </button>
    </div>
  );
}

/** What one queued file is doing, in the reader's words. */
function uploadStateLabel(item: AttachmentUploadItem): string {
  if (item.status === "queued") return "Aguardando envio";
  if (item.status === "uploading") {
    return `Enviando arquivo…${item.progress ? ` ${uploadPercent(item.progress)}%` : ""}`;
  }
  if (item.status === "success") return "Pronto para enviar";
  return item.error ?? "";
}

function ComposerUploadItem({
  item,
  upload,
  alone,
}: {
  item: AttachmentUploadItem;
  upload: AttachmentUploadState;
  /** The only file in the panel; its labels then need no file name to tell it apart. */
  alone: boolean;
}) {
  return (
    <div
      className="chat-msg-area__composer-upload-item"
      data-testid={item.status === "success" ? "chat-composer-pending-attachment" : undefined}
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
          {uploadStateLabel(item)}
        </span>
        <span className="chat-msg-area__composer-upload-size">
          {formatFileSize(item.file.size)}
        </span>
        {item.status === "uploading" && item.progress && (
          <progress
            className="chat-msg-area__composer-progress"
            data-testid="chat-composer-upload-progress"
            aria-label={alone ? "Progresso do envio" : `Progresso do envio de ${item.file.name}`}
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
        aria-label={alone ? "Remover anexo" : `Remover anexo ${item.file.name}`}
        data-testid="chat-composer-remove-attachment"
        onClick={() => upload.remove(item.localId)}
      >
        <span className="material-symbols-outlined" aria-hidden="true">
          close
        </span>
      </button>
    </div>
  );
}

function ComposerUploadHeader({
  count,
  total,
}: {
  count: number;
  /** Aggregate progress, shown only when there is more than one file to total. */
  total: UploadProgress | null;
}) {
  return (
    <div className="chat-msg-area__composer-upload-header">
      <span className="material-symbols-outlined" aria-hidden="true">
        attach_file
      </span>
      <strong>{count === 1 ? "1 arquivo anexado" : `${count} arquivos anexados`}</strong>
      {total && count > 1 && (
        <span className="chat-msg-area__composer-upload-total">
          {uploadPercent(total)}% no total
        </span>
      )}
    </div>
  );
}

function ComposerUploadPanel({
  upload,
  dragActive,
}: {
  upload: AttachmentUploadState;
  dragActive: boolean;
}) {
  // Nothing chosen, nothing being dragged, nothing to say: no panel at all.
  if (upload.items.length === 0 && !dragActive && !upload.notice) return null;
  const alone = upload.items.length === 1;
  const total = upload.aggregateProgress;
  return (
    <div
      className="chat-msg-area__composer-upload"
      data-testid="chat-composer-upload-status"
      role={upload.status === "failed" ? "alert" : "status"}
    >
      <ComposerUploadHeader count={upload.items.length} total={total} />
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
          <ComposerUploadItem key={item.localId} item={item} upload={upload} alone={alone} />
        ))}
      </div>
      {total && !alone && (
        <progress
          className="chat-msg-area__composer-progress chat-msg-area__composer-progress--total"
          aria-label="Progresso total dos anexos"
          value={total.loaded}
          max={total.total}
        />
      )}
    </div>
  );
}

function ComposerAttachButton({
  inputRef,
  enabled,
  onPick,
}: {
  inputRef: RefObject<HTMLInputElement | null>;
  enabled: boolean;
  onPick: (event: ChangeEvent<HTMLInputElement>) => void;
}) {
  return (
    <>
      <input
        ref={inputRef}
        type="file"
        multiple
        className="chat-msg-area__composer-file-input"
        data-testid="chat-composer-file-input"
        aria-label="Escolher arquivo para anexar"
        hidden
        onChange={onPick}
      />
      <button
        type="button"
        className="composer-toolbar__btn"
        aria-label="Anexar arquivo"
        disabled={!enabled}
        data-testid="chat-composer-attach-btn"
        onClick={() => inputRef.current?.click()}
      >
        <span className="material-symbols-outlined" aria-hidden="true" style={{ fontSize: 18 }}>
          attach_file
        </span>
      </button>
    </>
  );
}

/**
 * Taking files into the composer — from the file picker, and from a drag.
 *
 * One place decides whether a file may be taken at all, so the picker and the
 * drop zone can never disagree, and ChatComposer is left with the editor and
 * the send rather than with four drag handlers.
 */
function useComposerDropZone(enabled: boolean, selectFiles: (files: Iterable<File>) => void) {
  const inputRef = useRef<HTMLInputElement>(null);
  const [active, setActive] = useState(false);

  const accept = (files: Iterable<File> | undefined) => {
    if (enabled && files) selectFiles(files);
  };

  // Only a drag that actually carries files is intercepted. A text drag, or a
  // drag from inside the editor, keeps its default browser behaviour.
  const hasFiles = (event: DragEvent<HTMLDivElement>) =>
    enabled && Array.from(event.dataTransfer?.types ?? []).includes("Files");

  return {
    inputRef,
    active,
    onPick: (event: ChangeEvent<HTMLInputElement>) => {
      accept(event.target.files ?? undefined);
      // Clearing the value is what lets the same file be chosen again after a
      // failure: without it the input reports no change and fires nothing.
      event.target.value = "";
    },
    onDragOver: (event: DragEvent<HTMLDivElement>) => {
      if (!hasFiles(event)) return;
      event.preventDefault();
      setActive(true);
    },
    // A dragleave also fires every time the pointer crosses from the box into
    // one of its children — the quote, the editor, the toolbar. Ending the drag
    // state there would flicker the outline on each internal boundary, so only
    // a leave that really exits the composer counts. A null relatedTarget
    // (leaving the window, or a drop) is outside by definition.
    onDragLeave: (event: DragEvent<HTMLDivElement>) => {
      const next = event.relatedTarget;
      if (next instanceof Node && event.currentTarget.contains(next)) return;
      setActive(false);
    },
    onDrop: (event: DragEvent<HTMLDivElement>) => {
      if (!hasFiles(event)) return;
      // Prevented only for a drop this composer handles, so the browser never
      // navigates away to the dropped file.
      event.preventDefault();
      setActive(false);
      accept(event.dataTransfer.files ?? undefined);
    },
  };
}

/**
 * The editor and the placeholder drawn behind it.
 *
 * TipTap's own placeholder extension is not used: an empty list item is still
 * an empty document to it, and the hint would sit on top of the bullet the
 * reader just created.
 */
function ComposerEditor({ editor, placeholder }: { editor: Editor | null; placeholder: string }) {
  const empty = editor?.isEmpty && !editor.isActive("listItem");
  return (
    <div className="chat-msg-area__composer-editor-wrap">
      {empty && (
        <div className="chat-msg-area__composer-placeholder" aria-hidden="true">
          {placeholder}
        </div>
      )}
      <EditorContent editor={editor} />
    </div>
  );
}

interface ComposerAttachOptions {
  inputRef: RefObject<HTMLInputElement | null>;
  enabled: boolean;
  onPick: (event: ChangeEvent<HTMLInputElement>) => void;
}

/** The row under the editor: formatting, emoji, attachment, send. */
function ComposerBar({
  editor,
  disabled,
  emoji,
  pickerOpen,
  onPickerOpenChange,
  attach,
  canSend,
  onSend,
}: {
  editor: Editor | null;
  disabled: boolean;
  emoji?: ComposerEmojiOptions;
  pickerOpen: boolean;
  onPickerOpenChange: (open: boolean) => void;
  /** Absent when this composer has nowhere to put a file. */
  attach: ComposerAttachOptions | null;
  canSend: boolean;
  onSend: () => Promise<unknown>;
}) {
  return (
    <div className="chat-msg-area__composer-bar">
      <ComposerToolbar
        editor={editor}
        disabled={disabled}
        emoji={emoji}
        pickerOpen={pickerOpen}
        onPickerOpenChange={onPickerOpenChange}
      />
      {attach && <ComposerAttachButton {...attach} />}
      <button
        type="button"
        className="chat-msg-area__send-btn"
        disabled={!canSend}
        aria-label="Enviar mensagem"
        onClick={() => void onSend()}
        data-testid="chat-send-btn"
      >
        <IconSend />
      </button>
    </div>
  );
}

/**
 * An attachment is content, so a composer holding one may send an empty
 * document — but not while its own upload is still running.
 */
function hasSendableAttachment(upload: AttachmentUploadState): boolean {
  return !upload.busy && upload.items.some((item) => item.attachment !== null);
}

/** The attach affordance, or nothing when this composer has nowhere to put a file. */
function attachOptions(
  enabled: boolean,
  upload: AttachmentUploadState,
  drop: { inputRef: RefObject<HTMLInputElement | null>; onPick: ComposerAttachOptions["onPick"] },
): ComposerAttachOptions | null {
  if (!enabled) return null;
  return {
    inputRef: drop.inputRef,
    onPick: drop.onPick,
    enabled: upload.items.length < maxComposerFiles,
  };
}

export default function ChatComposer({
  placeholder,
  channelId,
  bodyFormat,
  disabled,
  replyPreview,
  onCancelReply,
  referencePreview = { status: "idle" },
  referenceTargetLabel,
  onCancelReference,
  onSend,
  uploadTarget,
  attachmentLimits,
  onAttachmentUploaded,
  onActivity,
  emoji,
}: ChatComposerProps) {
  const hadContextRef = useRef(false);
  // A picker left hanging over a sent message is noise. Closing on a confirmed
  // send is the only case the toolbar cannot see for itself; a change of
  // conversation needs no code at all, because ChatMessageArea keys this
  // composer by target and the whole subtree — picker included — is remounted.
  const [emojiPickerOpen, setEmojiPickerOpen] = useState(false);
  const upload = useAttachmentUpload(uploadTarget, attachmentLimits, onAttachmentUploaded);
  const attachEnabled = Boolean(uploadTarget) && !disabled;
  const uploading = upload.busy;
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
    if (result.status === "sent") {
      upload.resetAfterPublish();
      setEmojiPickerOpen(false);
    }
    return result;
  };

  const { editor, canSend, sending, handleSend } = useChatEditor({
    placeholder,
    disabled,
    channelId,
    bodyFormat,
    // An attachment is content, so a composer holding one may send an empty
    // document — but not while its own upload is still running.
    canSendEmpty: hasSendableAttachment(upload),
    onSend: handleComposerSend,
    onActivity,
  });

  const drop = useComposerDropZone(attachEnabled, upload.selectFiles);
  const activeEditor = editor ?? null;

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
        className={`chat-msg-area__composer-box${disabled ? " chat-msg-area__composer-box--disabled" : ""}${drop.active ? " chat-msg-area__composer-box--drag" : ""}`}
        onKeyDownCapture={handleKeyDownCapture}
        onDragOver={drop.onDragOver}
        onDragLeave={drop.onDragLeave}
        onDrop={drop.onDrop}
        data-testid="chat-composer-box"
      >
        <ComposerReplyQuote preview={replyPreview} onCancel={onCancelReply} />
        <ComposerReference
          preview={referencePreview}
          targetLabel={referenceTargetLabel}
          onCancel={onCancelReference}
        />
        <ComposerEditor editor={activeEditor} placeholder={placeholder} />
        {/* No target, no items and no drag to report: the panel draws nothing. */}
        <ComposerUploadPanel upload={upload} dragActive={drop.active} />
        <ComposerBar
          editor={activeEditor}
          disabled={disabled || sending}
          emoji={emoji}
          pickerOpen={emojiPickerOpen}
          onPickerOpenChange={setEmojiPickerOpen}
          attach={attachOptions(attachEnabled, upload, drop)}
          // Unavailable while a file is going up, whatever else the composer
          // holds: the attachment is part of the message being written, and
          // sending now would post a message without it.
          canSend={canSend && !uploading}
          onSend={handleSend}
        />
      </div>
    </div>
  );
}
