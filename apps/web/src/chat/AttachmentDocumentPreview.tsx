/**
 * Large inline document preview (task #494 — WhatsApp-style PDF card).
 *
 * The counterpart of AttachmentImagePreview for documents: the same large box
 * above the row, the same object URL discipline, the same skeleton — reused
 * classes and reused hook, not a parallel implementation. It draws only page
 * one, exactly like WhatsApp's own PDF card: a cover, never the whole
 * document, and never more than the one request the box needs.
 *
 * # What decides which of the four states this draws
 *
 * The server already computed the answer — `attachment.previewStatus` — and
 * this component only translates it, the same discipline MessageAttachments'
 * own header comment states for the scan status:
 *
 *   pending    the render has not finished. A skeleton, not an error: the
 *              task's own requirement is "avoid showing an error while
 *              processing is still happening", and previewStatus is exactly
 *              how this component tells "still working" from "gave up".
 *   ready      fetch page one and show it. A fetch failure past this point
 *              (truncated bytes, a decode error) is the one case this
 *              component discovers on its own rather than being told, and it
 *              degrades to the same unavailable note as failed/unsupported.
 *   failed /
 *   unsupported  an expected or operational absence with nothing left to wait
 *              for. A short note, not a blank box — the row below still has
 *              the filename and, when the file itself is downloadable, the
 *              Baixar button.
 *
 * Nothing is fetched for a document whose own `status` is not "clean": a
 * still-scanning or rejected file gets no preview request at all, which is
 * both the performance requirement (no duplicate or premature calls) and the
 * security one (never touch content nobody has approved).
 */

import type { ReactNode } from "react";

import { useAttachmentBlobUrl } from "./useAttachmentBlobUrl";
import { fetchDocumentPreviewPage } from "./filesApi";
import type { ChannelAttachment } from "./chatTypes";

/**
 * Module-scoped so its identity is stable across renders — useAttachmentBlobUrl
 * refetches whenever its fetcher reference changes, and an inline arrow here
 * would change on every render of every message in the list.
 */
function firstDocumentPreviewPage(attachmentId: string, signal?: AbortSignal): Promise<Blob> {
  return fetchDocumentPreviewPage(attachmentId, 1, signal);
}

interface AttachmentDocumentPreviewProps {
  attachment: ChannelAttachment;
  onOpen: (trigger: HTMLButtonElement) => void;
}

/**
 * Renders the large first-page preview, or the reason there is none.
 *
 * The caller places this only for a document attachment whose own `status` is
 * "clean" — see MessageAttachments — so this component never has to repeat
 * that gate; it starts from previewStatus.
 */
export default function AttachmentDocumentPreview({
  attachment,
  onOpen,
}: AttachmentDocumentPreviewProps) {
  const eligible = attachment.previewStatus === "ready";
  const { url, failed, onLoadError } = useAttachmentBlobUrl(
    attachment.id,
    eligible,
    firstDocumentPreviewPage,
  );

  if (attachment.previewStatus === "pending") {
    return <Skeleton attachmentId={attachment.id} />;
  }

  if (!eligible) {
    // failed or unsupported: a permanent, expected absence. Said plainly, not
    // as an alert — the row's own Baixar button, drawn by the caller, is
    // still there for whoever may reach it.
    return (
      <p
        className="chat-msg-area__attachment-note"
        data-testid={`chat-message-attachment-document-unavailable-${attachment.id}`}
      >
        Pré-visualização indisponível.
      </p>
    );
  }

  if (failed) {
    return (
      <p
        className="chat-msg-area__attachment-note"
        data-testid={`chat-message-attachment-document-unavailable-${attachment.id}`}
      >
        Pré-visualização indisponível.
      </p>
    );
  }

  if (url === null) {
    return <Skeleton attachmentId={attachment.id} />;
  }

  return (
    <div className="chat-msg-area__attachment-preview">
      <button
        type="button"
        className="chat-msg-area__attachment-preview-trigger"
        aria-label={`Visualizar ${attachment.filename}`}
        data-testid={`chat-message-attachment-document-${attachment.id}`}
        onClick={(event) => onOpen(event.currentTarget)}
      >
        <img
          className="chat-msg-area__attachment-preview-img"
          src={url}
          alt=""
          loading="lazy"
          decoding="async"
          onError={onLoadError}
        />
      </button>
    </div>
  );
}

function Skeleton({ attachmentId }: { attachmentId: string }): ReactNode {
  return (
    <div className="chat-msg-area__attachment-preview">
      <div
        className="chat-msg-area__attachment-preview-skeleton"
        role="status"
        aria-label="Carregando pré-visualização…"
        data-testid={`chat-message-attachment-document-loading-${attachmentId}`}
      />
    </div>
  );
}
