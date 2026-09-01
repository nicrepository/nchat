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
  type ClipboardEvent,
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
import { useVoiceRecorder } from "./useVoiceRecorder";
import VoiceRecorderPanel from "./VoiceRecorderPanel";
import type { WorkspaceAttachmentLimits } from "./chatApi";
import type { SendResult } from "./useMessages";
import ComposerToolbar, { type ComposerEmojiOptions } from "./ComposerToolbar";
import { useChatEditor } from "./useChatEditor";
import type { CodecFormat } from "./tiptapSerializer";
import type { Message, MessageBodyFormat } from "./chatTypes";
import { formatFileSize } from "./conversationDetailsDisplay";
import { senderLabel } from "./messageDisplay";
import RichTextRenderer from "./RichTextRenderer";
import { NAV_DRAWER_QUERY } from "./useNavDrawer";
import { useMediaQuery } from "./useMediaQuery";

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
 * The bytes a paste really carries (issue #516).
 *
 * `files` is what modern browsers populate for a screenshot or a copied file;
 * `items` is only read when it is empty, which is both the older-browser path
 * and what keeps a clipboard that exposes the same object twice from becoming
 * two attachments. Text, HTML and URLs are not files and never appear here —
 * nothing is ever fetched to turn a link into one.
 *
 * The Set is the second half of that: one item listed twice is one *object*
 * listed twice, so identity — not metadata — is what separates a repeated
 * representation from two screenshots that merely look alike. Deduplicating
 * here, before any name is generated, is what lets the naming below tell
 * distinct files apart without ever splitting a repeated one in two.
 */
function clipboardFiles(data: DataTransfer | null): File[] {
  const files = Array.from(data?.files ?? []);
  if (files.length > 0) return Array.from(new Set(files));
  const fromItems = Array.from(data?.items ?? [])
    .filter((item) => item.kind === "file")
    .map((item) => item.getAsFile())
    .filter((file): file is File => file !== null);
  return Array.from(new Set(fromItems));
}

/** A clipboard name that says nothing: "", "image", "image.png", ".png". */
const genericPasteName = /^(?:image)?(\.[a-z0-9+]+)?$/i;

/**
 * A pasted screenshot, under a name a reader can tell apart (issue #516).
 *
 * Browsers hand out a bare "image.png" placeholder — or nothing at all — for a
 * bitmap taken from the clipboard, so every paste would otherwise queue under
 * the same label. A file copied from the file manager arrives with its real
 * name and is returned untouched.
 *
 * `ordinal` numbers the generated names within one paste, and only from the
 * second onwards. The stamp alone resolves to the second, which two shots
 * taken in the same second share — together with the placeholder name and,
 * for two frames of the same screen, the very same byte count. The upload
 * queue identifies a file by name, size and mtime, so without the ordinal the
 * second of those would be swallowed as a duplicate of the first.
 *
 * The name is presentation only: the extension follows the declared MIME
 * rather than the other way round, and neither is trusted for anything —
 * file-service still inspects the bytes it receives.
 */
function pastedFile(file: File, ordinal: number): File {
  const generic = genericPasteName.exec(file.name);
  if (!generic) return file;
  const extension = generic[1]?.slice(1) || file.type.split("/").pop() || "bin";
  const stamp = new Date(file.lastModified)
    .toISOString()
    .slice(0, 19)
    .replace("T", "-")
    .replace(/:/g, "");
  const suffix = ordinal > 1 ? `-${ordinal}` : "";
  return new File([file], `Screenshot-${stamp}${suffix}.${extension}`, {
    type: file.type,
    lastModified: file.lastModified,
  });
}

/**
 * One paste's files, with every screenshot named apart from the others.
 *
 * The ordinal counts only the names this composer generated, so a paste
 * carrying a real file and a screenshot still yields an unsuffixed
 * "Screenshot-…", and a file the browser named keeps its own name whatever
 * position it arrived in.
 */
function pastedFiles(files: readonly File[]): File[] {
  let generated = 0;
  return files.map((file) => {
    const named = pastedFile(file, generated + 1);
    if (named !== file) generated += 1;
    return named;
  });
}

/**
 * Taking files into the composer — from the file picker, from a drag, and from
 * the clipboard.
 *
 * One place decides whether a file may be taken at all, so the picker, the
 * drop zone and the paste can never disagree, and ChatComposer is left with
 * the editor and the send rather than with four drag handlers.
 *
 * `interceptEnabled` and `acceptEnabled` are deliberately separate (issue
 * #670 code review): a voice recording must not accept a dropped file, but a
 * drag over the composer still has to be prevented from turning into the
 * browser's own file-open navigation. So while recording, a drag is still
 * intercepted — `preventDefault()` still runs — but never shown as an active
 * drop target and never handed to `selectFiles`. Outside of a recording the
 * two always agree, which is what keeps every other caller of this hook
 * unaffected.
 */
function useComposerDropZone(
  interceptEnabled: boolean,
  acceptEnabled: boolean,
  selectFiles: (files: Iterable<File>) => void,
) {
  const inputRef = useRef<HTMLInputElement>(null);
  const [active, setActive] = useState(false);

  const accept = (files: Iterable<File> | undefined) => {
    if (acceptEnabled && files) selectFiles(files);
  };

  // Only a drag that actually carries files is intercepted. A text drag, or a
  // drag from inside the editor, keeps its default browser behaviour.
  const hasFiles = (event: DragEvent<HTMLDivElement>) =>
    interceptEnabled && Array.from(event.dataTransfer?.types ?? []).includes("Files");

  return {
    inputRef,
    active,
    onPick: (event: ChangeEvent<HTMLInputElement>) => {
      accept(event.target.files ?? undefined);
      // Clearing the value is what lets the same file be chosen again after a
      // failure: without it the input reports no change and fires nothing.
      event.target.value = "";
    },
    // A paste is only ever intercepted when it really carries bytes and this
    // composer could take them. Text — plain, HTML or a URL — falls through
    // untouched to the editor, which is what keeps an ordinary Ctrl+V native.
    onPaste: (event: ClipboardEvent<HTMLDivElement>) => {
      if (!acceptEnabled) return;
      const files = clipboardFiles(event.clipboardData);
      if (files.length === 0) return;
      event.preventDefault();
      selectFiles(pastedFiles(files));
    },
    onDragOver: (event: DragEvent<HTMLDivElement>) => {
      if (!hasFiles(event)) return;
      event.preventDefault();
      // Only the *visual* affordance is conditional on acceptance: a user must
      // never be shown a drop target that a recording is about to ignore.
      if (!acceptEnabled) return;
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
      // Prevented whenever this composer could in principle handle the drop —
      // recording or not — so the browser never navigates away to the dropped
      // file. `setActive(false)` is unconditional for the same reason:
      // nothing must be left signalling an accepted drop that a recording
      // silently ignored.
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

/**
 * The voice-record affordance (issue #670). `disabled` already folds in
 * every reason recording may not start right now — no destination, the
 * composer disabled, an upload in flight, or an attachment already sitting
 * in the composer — so ComposerBar itself never has to know any of those
 * rules.
 */
interface ComposerVoiceOptions {
  disabled: boolean;
  /** Explains a disabled state beyond the button's own label, e.g. "remove the attachment first". */
  title?: string;
  onStart: () => void;
}

/** The row under the editor: formatting, emoji, attachment, voice, send. */
function ComposerBar({
  editor,
  disabled,
  emoji,
  pickerOpen,
  onPickerOpenChange,
  attach,
  voice,
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
  /** Absent when this composer has no destination, or the browser cannot record. */
  voice: ComposerVoiceOptions | null;
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
      {voice && (
        <button
          type="button"
          className="composer-toolbar__btn"
          aria-label="Gravar mensagem de voz"
          title={voice.title}
          disabled={voice.disabled}
          data-testid="chat-composer-record-btn"
          onClick={voice.onStart}
        >
          <span className="material-symbols-outlined" aria-hidden="true" style={{ fontSize: 18 }}>
            mic
          </span>
        </button>
      )}
      <button
        type="button"
        className="chat-msg-area__send-btn"
        disabled={!canSend}
        aria-label="Enviar mensagem"
        onClick={() => {
          editor?.view.dom.focus({ preventScroll: true });
          void onSend();
        }}
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

/**
 * The voice-record affordance, or nothing when this composer has no
 * destination or the browser offers no MediaRecorder format the backend
 * accepts (issue #670).
 *
 * A pending/ready attachment disables it rather than hiding it: the reader
 * should see *why* recording is unavailable (the button's own `title`) and
 * not wonder where it went, and it must never be combined implicitly with
 * what is already in the composer.
 */
function voiceOptions(
  hasDestination: boolean,
  supported: boolean,
  attachEnabled: boolean,
  uploading: boolean,
  hasComposerAttachments: boolean,
  onStart: () => void,
): ComposerVoiceOptions | null {
  if (!hasDestination || !supported) return null;
  return {
    disabled: !attachEnabled || uploading || hasComposerAttachments,
    title: hasComposerAttachments ? "Remova os anexos para gravar uma mensagem de voz." : undefined,
    onStart,
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
  const initialFocusOwnerRef = useRef(document.activeElement);
  const initialFocusHandledRef = useRef(false);
  const suppressInitialFocus = useMediaQuery(`${NAV_DRAWER_QUERY}, (pointer: coarse)`);
  // A picker left hanging over a sent message is noise. Closing on a confirmed
  // send is the only case the toolbar cannot see for itself; a change of
  // conversation needs no code at all, because ChatMessageArea keys this
  // composer by target and the whole subtree — picker included — is remounted.
  const [emojiPickerOpen, setEmojiPickerOpen] = useState(false);
  const upload = useAttachmentUpload(uploadTarget, attachmentLimits, onAttachmentUploaded);
  const attachEnabled = Boolean(uploadTarget) && !disabled;
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
    // Both props are optional on ChatComposerProps and neither is defaulted
    // in the destructuring above (issue #682 removed the old defaults), so
    // nullish coalescing here is load-bearing, not defensive style.
    target: uploadTarget ?? null,
    maxUploadBytes: attachmentLimits?.maxUploadBytes ?? null,
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

  // Whether a new attachment may be taken at all right now. A voice
  // recording is deliberately never combined with an attachment (issue
  // #670): the picker and the attach button already disappear while
  // `recording` (the whole bar is hidden — see the render below), and this
  // is the same rule extended to the one entry point that stays reachable
  // regardless of that: the composer box's own drag-and-drop surface, bound
  // below whether or not a recording is in progress. `interceptEnabled`
  // (passed as `attachEnabled` below) stays unconditional so a drag over the
  // composer during a recording is still neutralised rather than falling
  // through to the browser's own file-open navigation.
  const canAcceptAttachments = attachEnabled && !recording;
  const drop = useComposerDropZone(attachEnabled, canAcceptAttachments, upload.selectFiles);
  const activeEditor = editor ?? null;

  useEffect(() => {
    if (initialFocusHandledRef.current || disabled || !editor) return;
    if (suppressInitialFocus) {
      initialFocusHandledRef.current = true;
      return;
    }
    const frame = requestAnimationFrame(() => {
      if (!editor.isEditable) return;
      if (document.activeElement !== initialFocusOwnerRef.current) {
        initialFocusHandledRef.current = true;
        return;
      }
      initialFocusHandledRef.current = true;
      editor.view.dom.focus({ preventScroll: true });
    });
    return () => cancelAnimationFrame(frame);
  }, [disabled, editor, suppressInitialFocus]);

  const startRecording = () => {
    // A picker left open over a recording panel is the same noise a picker
    // left open over a sent message would be, and recording replaces the bar
    // the picker lives in regardless.
    setEmojiPickerOpen(false);
    recorder.start();
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
        className={`chat-msg-area__composer-box${disabled ? " chat-msg-area__composer-box--disabled" : ""}${drop.active ? " chat-msg-area__composer-box--drag" : ""}`}
        onKeyDownCapture={handleKeyDownCapture}
        onPaste={drop.onPaste}
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
        {recording ? (
          <VoiceRecorderPanel recorder={recorder} />
        ) : (
          <>
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
              voice={voiceOptions(
                Boolean(uploadTarget),
                recorder.supported,
                attachEnabled,
                uploading,
                hasComposerAttachments,
                startRecording,
              )}
              // Unavailable while a file is going up, whatever else the composer
              // holds: the attachment is part of the message being written, and
              // sending now would post a message without it.
              canSend={canSend && !uploading}
              onSend={handleSend}
            />
          </>
        )}
      </div>
    </div>
  );
}
