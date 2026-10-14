/**
 * One row of the details panel's recent files (issues #435, #897).
 *
 * The row presents one attachment and, for a file the scan approved, offers the
 * one action the product already has for it — nothing here decides a new one:
 *
 *   pending_scan  name, metadata and "Em análise". No control at all: an
 *                 unapproved file is not offered, and the server would refuse;
 *   rejected      the same, reading "Reprovado";
 *   clean         the row is a button. A document with a rendered preview opens
 *                 the shared document viewer, exactly as its message card's
 *                 Visualizar does; anything else is the card's Baixar.
 *
 * The status is always text, never only a colour, and every fetch behind the row
 * — thumbnail, player, viewer, download — goes through the authenticated client
 * and is re-checked by file-service on every request. This decides what to
 * *offer*; the server decides what may be obtained.
 *
 * Each row is also its own lazy-hydration unit (issue #675), like a message
 * attachment: an expanded list scrolls, and a thumbnail far below the fold is
 * not fetched until the row approaches it.
 *
 * Security: the filename is a React text node and an attribute value, never
 * markup and never part of a URL. No address is built here at all.
 */

import { useId } from "react";

import AttachmentThumbnail from "./AttachmentThumbnail";
import AttachmentVideo from "./AttachmentVideo";
import { isDocumentAttachment } from "./attachmentDocumentRules";
import { useAttachmentDownload } from "./attachmentDownload";
import { useAttachmentViewer } from "./attachmentViewer";
import { isPreviewAvailable, type ChannelAttachment } from "./chatTypes";
import { formatFileSize } from "./conversationDetailsDisplay";
import { AttachmentHydrationContext, useLazyAttachment } from "./lazyAttachment";
import { formatDayLabel, formatTime } from "./messageDisplay";

/** Material symbol name for a file, chosen from the *detected* type only. */
function fileIconFor(contentType: string): string {
  if (contentType.startsWith("image/")) return "image";
  if (contentType.startsWith("video/")) return "movie";
  if (contentType.startsWith("audio/")) return "graphic_eq";
  if (contentType === "application/pdf") return "picture_as_pdf";
  if (contentType.startsWith("text/")) return "description";
  return "draft";
}

/**
 * The one place the three scan states get their words (RF-22).
 *
 * All three are drawn, including `clean`. The badge used to be suppressed for
 * an approved file on the theory that "no news is good news", which left the
 * only visible states meaning "wait" and "blocked" — so a user reading the list
 * could not tell an approved file from one whose badge they had simply missed,
 * and the approval this whole feature exists to establish was the one outcome
 * never shown.
 */
const attachmentStatusLabel: Record<ChannelAttachment["status"], string> = {
  pending_scan: "Em análise",
  clean: "Verificado",
  rejected: "Reprovado",
};

/**
 * "Hoje, 09:46 · 113 KB", built from the parts that actually exist.
 *
 * A missing or unparseable date contributes nothing — never a stray comma or a
 * leading separator — and nothing is filled in for it.
 */
function recentFileMeta(file: Pick<ChannelAttachment, "createdAt" | "size">): string {
  const day = formatDayLabel(file.createdAt);
  const when = day && `${day}, ${formatTime(file.createdAt)}`;
  return [when, formatFileSize(file.size)].filter(Boolean).join(" · ");
}

/** Icon or thumbnail, name, metadata and status: what every row shows. */
function FileSummary({ file, metaId }: { file: ChannelAttachment; metaId?: string }) {
  return (
    <>
      {/* The thumbnail owns its own fetch and object URL; the icon is what
          shows whenever there is no preview to show. */}
      <AttachmentThumbnail
        attachment={file}
        fallback={
          <span className="chat-details__file-icon" aria-hidden="true">
            <span className="material-symbols-outlined">{fileIconFor(file.contentType)}</span>
          </span>
        }
      />
      <span className="chat-details__file-text">
        {/* A filename is text. It is never a URL and never markup. */}
        <span className="chat-details__file-name">{file.filename}</span>
        <span id={metaId} className="chat-details__file-meta">
          {recentFileMeta(file)}
          <span
            className={`chat-details__file-status chat-details__file-status--${file.status}`}
            data-testid={`chat-details-file-status-${file.id}`}
          >
            {attachmentStatusLabel[file.status]}
          </span>
        </span>
      </span>
    </>
  );
}

/**
 * A clean file: the whole summary is the control.
 *
 * Named for the action and the file ("Baixar relatorio.pdf"), and described by
 * its metadata and status, so a screen reader hears what activating it does
 * first and the details after.
 *
 * `aria-disabled` rather than `disabled` while a download runs: disabling the
 * focused control would drop keyboard focus to the page, and the hook already
 * refuses a second download.
 */
function CleanFileAction({ file }: { file: ChannelAttachment }) {
  const viewer = useAttachmentViewer();
  const { state, download } = useAttachmentDownload(file);
  const metaId = useId();
  const opensViewer = isDocumentAttachment(file) && isPreviewAvailable(file.previewStatus);
  const verb = opensViewer ? "Visualizar" : "Baixar";

  return (
    <>
      <button
        type="button"
        className="chat-details__file-main chat-details__file-action"
        aria-label={`${verb} ${file.filename || "arquivo"}`}
        aria-describedby={metaId}
        aria-disabled={state === "loading" || undefined}
        data-testid={`chat-details-file-action-${file.id}`}
        onClick={(event) => {
          if (opensViewer) viewer.openDocument(file, event.currentTarget);
          else void download();
        }}
      >
        <FileSummary file={file} metaId={metaId} />
      </button>
      {state === "failed" && (
        <span className="chat-details__file-note" role="alert">
          Não foi possível baixar o arquivo.
        </span>
      )}
    </>
  );
}

export default function RecentFileRow({ file }: { file: ChannelAttachment }) {
  const { ref, gate } = useLazyAttachment();
  return (
    <li ref={ref} className="chat-details__file" data-testid={`chat-details-file-${file.id}`}>
      <AttachmentHydrationContext.Provider value={gate}>
        {file.status === "clean" ? (
          <CleanFileAction file={file} />
        ) : (
          <div className="chat-details__file-main">
            <FileSummary file={file} />
          </div>
        )}
        {/* The player is a sibling of the summary rather than part of it, so it
            wraps onto its own line, stays outside the button, and a file that is
            not a clean playable video renders nothing at all. */}
        <AttachmentVideo attachment={file} />
      </AttachmentHydrationContext.Provider>
    </li>
  );
}
