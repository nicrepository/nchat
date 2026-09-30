import { useState, type MouseEvent } from "react";

import { isImageAttachment } from "../chat/attachmentImageRules";
import { fileIconFor, isDocumentAttachment } from "../chat/attachmentKinds";
import { useAttachmentViewer } from "../chat/attachmentViewer";
import { isPreviewAvailable, type ChannelAttachment } from "../chat/chatTypes";
import { formatFileSize } from "../chat/conversationDetailsDisplay";
import { fetchAttachmentPreview } from "../chat/filesApi";
import HighlightedText from "./HighlightedText";
import { conversationLabel } from "./searchLabels";
import { openMessage, useOpenSearchResult } from "./searchNavigation";
import type { FileSearchResult } from "./searchTypes";

interface FileResultRowProps {
  result: FileSearchResult;
  query: string;
}

/** The product's scan vocabulary, shortened to fit a result card. */
const SCAN_LABELS: Record<FileSearchResult["status"], string> = {
  pending_scan: "Verificando",
  clean: "Verificado",
  rejected: "Bloqueado",
};

function asAttachment(result: FileSearchResult): ChannelAttachment {
  return {
    id: result.id,
    filename: result.filename,
    contentType: result.contentType,
    size: result.size,
    status: result.status,
    previewStatus: result.previewStatus,
    createdAt: result.createdAt,
  };
}

/** Which of the existing viewers can show this file, if any. */
function viewerFor(attachment: ChannelAttachment): "image" | "document" | null {
  if (attachment.status !== "clean" || !isPreviewAvailable(attachment.previewStatus)) return null;
  if (isImageAttachment(attachment)) return "image";
  if (isDocumentAttachment(attachment)) return "document";
  return null;
}

function typeLabel(result: FileSearchResult): string {
  const extension = /\.([a-z0-9]{1,5})$/i.exec(result.filename)?.[1];
  return extension ? extension.toUpperCase() : "Arquivo";
}

/**
 * A file opens in the Attachment Viewer the timeline already uses (#806) —
 * the same host, lightbox and document viewer, never one of its own. The
 * viewer fetches through file-service, which re-checks membership and the scan
 * gate on every request, so a result is only ever a pointer. A file no viewer
 * can show (still being scanned, blocked, audio, video, a type with no preview)
 * opens its message instead, where the inline player or the download lives.
 */
export default function FileResultRow({ result, query }: FileResultRowProps) {
  const viewer = useAttachmentViewer();
  const open = useOpenSearchResult();
  const [opening, setOpening] = useState(false);
  const [error, setError] = useState("");
  const attachment = asAttachment(result);
  const kind = viewerFor(attachment);
  const goToMessage = () => openMessage(open, result.conversation, result.messageId);

  async function openViewer(event: MouseEvent<HTMLButtonElement>) {
    const trigger = event.currentTarget;
    if (kind === "document") {
      viewer.openDocument(attachment, trigger);
      return;
    }
    if (kind !== "image") {
      goToMessage();
      return;
    }
    if (opening) return;
    setOpening(true);
    setError("");
    try {
      // The lightbox opens on the preview and upgrades to the original itself,
      // exactly as it does from a message card.
      const blob = await fetchAttachmentPreview(attachment.id);
      viewer.openImage(attachment, { trigger, blob, isOriginal: false });
    } catch {
      setError("Não foi possível abrir o arquivo.");
    } finally {
      setOpening(false);
    }
  }

  return (
    <div className="global-search__result-group">
      <button
        type="button"
        className="global-search__result"
        onClick={(event) => void openViewer(event)}
        disabled={opening}
      >
        <span className="global-search__icon" aria-hidden="true">
          <span className="material-symbols-outlined">{fileIconFor(result.contentType)}</span>
        </span>
        <span className="global-search__result-body">
          <span className="global-search__result-title">
            <HighlightedText text={result.filename} query={query} />
          </span>{" "}
          <span className="global-search__result-sub">
            {typeLabel(result)} · {conversationLabel(result.conversation)} ·{" "}
            {formatFileSize(result.size)}
          </span>
        </span>{" "}
        <span
          className={`global-search__scan global-search__scan--${result.status}`}
          data-testid={`global-search-file-status-${result.id}`}
        >
          {SCAN_LABELS[result.status]}
        </span>
      </button>
      {kind && (
        <button type="button" className="global-search__secondary" onClick={goToMessage}>
          Ir para mensagem
        </button>
      )}
      {error && (
        <span className="global-search__result-error" role="alert">
          {error}
        </span>
      )}
    </div>
  );
}
