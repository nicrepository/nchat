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

import AttachmentImagePreview, { type AttachmentImageOpenPayload } from "./AttachmentImagePreview";
import AttachmentLightbox from "./AttachmentLightbox";
import AttachmentThumbnail from "./AttachmentThumbnail";
import AttachmentVideo from "./AttachmentVideo";
import { isImageAttachment } from "./attachmentImageRules";
import { fetchAttachmentContent } from "./filesApi";
import { formatFileSize } from "./conversationDetailsDisplay";
import type { ChannelAttachment } from "./chatTypes";

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
}

function MessageAttachment({ attachment, onOpenImage }: MessageAttachmentProps) {
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

  return (
    <li
      className="chat-msg-area__attachment"
      data-testid={`chat-message-attachment-${attachment.id}`}
    >
      <div className="chat-msg-area__attachment-row">
        {/* The thumbnail draws itself only for an approved file with a ready
            preview; everything else falls back to the type icon. */}
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

  if (!attachments || attachments.length === 0) return null;

  function closeLightbox() {
    // The control that opened it gets focus back on close — this component
    // owns that button, the same way ConversationDetailsPanel owns it for
    // AddMembersDialog, so AttachmentLightbox itself never needs to know it.
    lightbox?.trigger.focus();
    setLightbox(null);
  }

  return (
    <>
      <ul className="chat-msg-area__attachments" aria-label="Anexos da mensagem">
        {attachments.map((attachment) => (
          <MessageAttachment
            key={attachment.id}
            attachment={attachment}
            onOpenImage={(openedAttachment, payload) =>
              setLightbox({
                attachment: openedAttachment,
                trigger: payload.trigger,
                url: payload.url,
                isOriginal: payload.isOriginal,
              })
            }
          />
        ))}
      </ul>
      {lightbox && (
        <AttachmentLightbox
          attachment={lightbox.attachment}
          inlineUrl={lightbox.url}
          inlineIsOriginal={lightbox.isOriginal}
          onClose={closeLightbox}
        />
      )}
    </>
  );
}
