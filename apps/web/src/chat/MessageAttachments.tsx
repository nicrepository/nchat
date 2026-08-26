/**
 * Attachments rendered inside a message (RF-32).
 *
 * The three scan states are the whole component, and each one is drawn from the
 * *server's* status rather than from anything this file decides:
 *
 *   pending_scan  the file exists and has not been ruled on. Name and size, no
 *                 preview, no player, no download — an unapproved file is not
 *                 offered, and the request would be refused anyway;
 *   clean         the approved case. Thumbnail, inline video and download, all
 *                 through the same authenticated components the details panel
 *                 uses, so the eligibility rules are written once;
 *   rejected      blocked. Said plainly, with no action at all.
 *
 * Nothing here is a control. AttachmentThumbnail, AttachmentVideo and the
 * download below all fetch through the authenticated client, and file-service
 * re-checks visibility and scan state on every request. This component decides
 * what to *offer*, never what may be obtained.
 *
 * No URL is ever built from a filename, and no address outlives the click that
 * created it: the download's object URL is revoked in the same task.
 *
 * Images and GIFs (issue #491) are the one exception to "a row per
 * attachment": AttachmentImagePreview draws a large preview above the same
 * name/size/status/Baixar footer every other type shows in its row, and a
 * click on it opens AttachmentLightbox. This component owns the one lightbox
 * that can be open at a time and the focus-return-on-close that goes with it,
 * the same way ConversationDetailsPanel owns focus return for AddMembersDialog
 * — the dialog itself only ever calls onClose.
 */

import { useState } from "react";

import AttachmentDocumentPreview from "./AttachmentDocumentPreview";
import AttachmentImagePreview, { type AttachmentImageOpenPayload } from "./AttachmentImagePreview";
import AttachmentLightbox from "./AttachmentLightbox";
import AttachmentThumbnail from "./AttachmentThumbnail";
import AttachmentVideo from "./AttachmentVideo";
import DocumentPreviewViewer from "./DocumentPreviewViewer";
import { isImageAttachment } from "./attachmentImageRules";
import { fetchAttachmentContent } from "./filesApi";
import { formatFileSize } from "./conversationDetailsDisplay";
import { isPreviewAvailable, type ChannelAttachment } from "./chatTypes";

/** Same mapping the details panel uses, kept local so neither owns the other. */
function fileIconFor(contentType: string): string {
  if (contentType.startsWith("image/")) return "image";
  if (contentType.startsWith("video/")) return "movie";
  if (contentType.startsWith("audio/")) return "graphic_eq";
  if (contentType === "application/pdf") return "picture_as_pdf";
  if (contentType.startsWith("text/")) return "description";
  return "draft";
}

/**
 * Whether this attachment might carry a document preview (PDF today; CSV/
 * XLSX as of task #494's sheet phase).
 *
 * `contentType` here is file-service's *detected* type — net/http.
 * DetectContentType's own sniff, never the filename or what the browser
 * declared — and that detector has no signature for CSV or for any OOXML
 * subtype. It can only ever report `text/plain` for delimited text and
 * `application/zip` for any zip-shaped file (XLSX included, indistinguishable
 * from DOCX/PPTX/ODT/ODP or an arbitrary .zip at this layer). Matching
 * `text/csv`, `officedocument`, `msword`, `ms-excel`, `ms-powerpoint` or
 * `opendocument` — what a real upload's contentType can never be — silently
 * routed every CSV/XLSX attachment through the generic thumbnail branch
 * instead of the document one. See file-service's
 * domain.previewableMIMEs for the server-side half of this same fact.
 *
 * A `text/plain` or `application/zip` attachment is not guaranteed to have a
 * usable preview — the server may still answer "unsupported" for a DOCX, a
 * plain log file, or a generic zip — but it is always routed here so the
 * Visualizar action becomes reachable the moment previewStatus says ready.
 */
function isDocumentAttachment(attachment: ChannelAttachment): boolean {
  // The detected type can carry a parameter (e.g. "text/plain;
  // charset=utf-8" is what a real CSV upload's contentType looks like) —
  // stripped here the same way file-service's own NormalizeDetectedMIME
  // does server-side, so the two never disagree about what a bare type is.
  const type = attachment.contentType.split(";")[0].trim().toLowerCase();
  return type === "application/pdf" || type === "text/plain" || type === "application/zip";
}

/**
 * Downloads one approved attachment.
 *
 * The bytes come through the authenticated client, exactly like the preview and
 * the video, because the content route needs an Authorization header that an
 * anchor cannot send. The object URL exists only long enough for the browser to
 * take the blob, and is revoked immediately after: nothing here produces an
 * address that can be shared, bookmarked or replayed.
 *
 * Rendered only for a clean attachment. A failure says so and changes nothing
 * else — the row keeps its name, size and status.
 */
function AttachmentDownloadButton({ attachment }: { attachment: ChannelAttachment }) {
  const [state, setState] = useState<"idle" | "loading" | "failed">("idle");

  const download = async () => {
    if (state === "loading") return;
    setState("loading");
    try {
      const blob = await fetchAttachmentContent(attachment.id);
      const url = URL.createObjectURL(blob);
      const anchor = document.createElement("a");
      anchor.href = url;
      // The filename is an attribute value React never renders as markup and
      // the browser never resolves as a path.
      anchor.download = attachment.filename || "arquivo";
      anchor.click();
      URL.revokeObjectURL(url);
      setState("idle");
    } catch {
      // No server text is surfaced: it may carry detail that does not belong
      // in the UI, and every failure means the same thing here.
      setState("failed");
    }
  };

  return (
    <>
      <button
        type="button"
        className="chat-msg-area__attachment-action"
        aria-label={`Baixar ${attachment.filename}`}
        disabled={state === "loading"}
        data-testid={`chat-message-attachment-download-${attachment.id}`}
        onClick={() => void download()}
      >
        <span className="material-symbols-outlined" aria-hidden="true">
          download
        </span>
        {state === "loading" ? "Baixando…" : "Baixar"}
      </button>
      {state === "failed" && (
        <span className="chat-msg-area__attachment-note" role="alert">
          Não foi possível baixar o arquivo.
        </span>
      )}
    </>
  );
}

interface MessageAttachmentProps {
  attachment: ChannelAttachment;
  onOpenImage: (attachment: ChannelAttachment, payload: AttachmentImageOpenPayload) => void;
  onOpenDocument: (attachment: ChannelAttachment, trigger: HTMLButtonElement) => void;
}

function MessageAttachment({ attachment, onOpenImage, onOpenDocument }: MessageAttachmentProps) {
  const icon = (
    <span className="chat-msg-area__attachment-icon" aria-hidden="true">
      <span className="material-symbols-outlined">{fileIconFor(attachment.contentType)}</span>
    </span>
  );
  const meta = (
    <span className="chat-msg-area__attachment-text">
      {/* A filename is text. It is never a URL and never markup. */}
      <span className="chat-msg-area__attachment-name">{attachment.filename}</span>
      <span className="chat-msg-area__attachment-meta">
        {formatFileSize(attachment.size)}
        <span
          className={`chat-msg-area__attachment-status chat-msg-area__attachment-status--${attachment.status}`}
          data-testid={`chat-message-attachment-status-${attachment.id}`}
        >
          {attachment.status === "pending_scan" && "Verificando arquivo…"}
          {attachment.status === "clean" && "Verificado"}
          {attachment.status === "rejected" && "Bloqueado pela verificação de segurança"}
        </span>
      </span>
    </span>
  );
  const download = attachment.status === "clean" && (
    <AttachmentDownloadButton attachment={attachment} />
  );

  // Image and GIF attachments (RF-32/#491) get a large preview above the same
  // name/size/status/Baixar footer every other type shows beside its
  // thumbnail — everything else keeps the row layout untouched, PDF's own
  // server preview included.
  if (isImageAttachment(attachment)) {
    return (
      <li
        className="chat-msg-area__attachment"
        data-testid={`chat-message-attachment-${attachment.id}`}
      >
        <AttachmentImagePreview
          attachment={attachment}
          fallback={icon}
          onOpen={(payload) => onOpenImage(attachment, payload)}
        />
        <div className="chat-msg-area__attachment-row">
          {meta}
          {download}
        </div>
      </li>
    );
  }

  // Documents (PDF today; other office formats once a later phase renders
  // them) get the large WhatsApp-style card: a big first-page preview above
  // the row, an explicit Visualizar action beside Baixar. A file still being
  // scanned or rejected keeps the plain icon row below unchanged — the same
  // split AttachmentImagePreview draws for raster types, applied to
  // documents instead of replacing that branch.
  if (isDocumentAttachment(attachment)) {
    return (
      <li
        className="chat-msg-area__attachment"
        data-testid={`chat-message-attachment-${attachment.id}`}
      >
        {attachment.status === "clean" && (
          <AttachmentDocumentPreview
            attachment={attachment}
            onOpen={(trigger) => onOpenDocument(attachment, trigger)}
          />
        )}
        <div className="chat-msg-area__attachment-row">
          {icon}
          {meta}
        </div>
        {attachment.status === "clean" && (
          <div className="chat-msg-area__attachment-actions">
            {isPreviewAvailable(attachment.previewStatus) && (
              <button
                type="button"
                className="chat-msg-area__attachment-action"
                aria-label={`Visualizar ${attachment.filename}`}
                onClick={(event) => onOpenDocument(attachment, event.currentTarget)}
              >
                <span className="material-symbols-outlined" aria-hidden="true">
                  visibility
                </span>
                Visualizar
              </button>
            )}
            <AttachmentDownloadButton attachment={attachment} />
          </div>
        )}
      </li>
    );
  }

  return (
    <li
      className="chat-msg-area__attachment"
      data-testid={`chat-message-attachment-${attachment.id}`}
    >
      <div className="chat-msg-area__attachment-row">
        <AttachmentThumbnail attachment={attachment} fallback={icon} />
        {meta}
        {download}
      </div>
      {/* Draws a player only for a clean, playable video, and nothing at all
          otherwise — including for a file still being scanned. */}
      <AttachmentVideo attachment={attachment} />
    </li>
  );
}

interface OpenLightbox {
  attachment: ChannelAttachment;
  trigger: HTMLButtonElement;
  url: string;
  isOriginal: boolean;
}

export default function MessageAttachments({
  attachments,
}: {
  attachments: ChannelAttachment[] | undefined;
}) {
  const [lightbox, setLightbox] = useState<OpenLightbox | null>(null);
  const [documentViewer, setDocumentViewer] = useState<{
    attachment: ChannelAttachment;
    trigger: HTMLButtonElement;
  } | null>(null);
  const [expandedRuns, setExpandedRuns] = useState<Set<number>>(() => new Set());

  if (!attachments || attachments.length === 0) return null;

  function closeLightbox() {
    // The control that opened it gets focus back on close — this component
    // owns that button, the same way ConversationDetailsPanel owns it for
    // AddMembersDialog, so AttachmentLightbox itself never needs to know it.
    lightbox?.trigger.focus();
    setLightbox(null);
  }

  const segments: Array<
    | { kind: "images"; attachments: ChannelAttachment[] }
    | { kind: "file"; attachment: ChannelAttachment }
  > = [];
  for (const attachment of attachments) {
    if (isImageAttachment(attachment)) {
      const last = segments.at(-1);
      if (last?.kind === "images") last.attachments.push(attachment);
      else segments.push({ kind: "images", attachments: [attachment] });
    } else {
      segments.push({ kind: "file", attachment });
    }
  }

  const openImage = (openedAttachment: ChannelAttachment, payload: AttachmentImageOpenPayload) =>
    setLightbox({
      attachment: openedAttachment,
      trigger: payload.trigger,
      url: payload.url,
      isOriginal: payload.isOriginal,
    });

  return (
    <>
      <ul className="chat-msg-area__attachments" aria-label="Anexos da mensagem">
        {segments.map((segment, index) => {
          if (segment.kind === "file") {
            return (
              <MessageAttachment
                key={segment.attachment.id}
                attachment={segment.attachment}
                onOpenImage={openImage}
                onOpenDocument={(attachment, trigger) => setDocumentViewer({ attachment, trigger })}
              />
            );
          }
          const expanded = expandedRuns.has(index);
          const visible = expanded ? segment.attachments : segment.attachments.slice(0, 4);
          const hidden = segment.attachments.length - visible.length;
          return (
            <li key={`images-${segment.attachments[0].id}`} className="chat-msg-area__image-run">
              <ul
                className={`chat-msg-area__image-grid chat-msg-area__image-grid--${Math.min(segment.attachments.length, 4)}`}
                data-testid="chat-message-image-grid"
                data-count={segment.attachments.length}
                aria-label={`${segment.attachments.length} imagens anexadas`}
              >
                {visible.map((attachment) => (
                  <MessageAttachment
                    key={attachment.id}
                    attachment={attachment}
                    onOpenImage={openImage}
                    onOpenDocument={(attachment, trigger) =>
                      setDocumentViewer({ attachment, trigger })
                    }
                  />
                ))}
              </ul>
              {hidden > 0 && (
                <button
                  type="button"
                  className="chat-msg-area__image-grid-more"
                  aria-label={`Mostrar mais ${hidden} ${hidden === 1 ? "imagem" : "imagens"}`}
                  onClick={() =>
                    setExpandedRuns((current) => {
                      const next = new Set(current);
                      next.add(index);
                      return next;
                    })
                  }
                >
                  +{hidden}
                </button>
              )}
            </li>
          );
        })}
      </ul>
      {lightbox && (
        <AttachmentLightbox
          attachment={lightbox.attachment}
          inlineUrl={lightbox.url}
          inlineIsOriginal={lightbox.isOriginal}
          onClose={closeLightbox}
        />
      )}
      {documentViewer && (
        <DocumentPreviewViewer
          attachment={documentViewer.attachment}
          onClose={() => {
            documentViewer.trigger.focus();
            setDocumentViewer(null);
          }}
        />
      )}
    </>
  );
}
