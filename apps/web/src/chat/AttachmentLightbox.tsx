/**
 * AttachmentLightbox — enlarged image/GIF viewer (issue #491).
 *
 * The shell (portal, backdrop, role="dialog", Tab cycle, Escape) mirrors
 * AddMembersDialog rather than introducing a second modal system, the same
 * choice that dialog itself documents against NewConversationDialog. What is
 * new here, because none of the three existing dialogs needed it, is
 * returning focus to the control that opened it — that is the caller's job
 * (MessageAttachments owns the trigger button, the same way
 * ConversationDetailsPanel owns the "Adicionar membros" button for
 * AddMembersDialog), so this component only ever calls `onClose`.
 *
 * There is deliberately no footer, no metadata and no Baixar button: the
 * issue is explicit that the enlarged view "deve priorizar a mídia e não
 * incluir controles desnecessários nesta primeira versão". The one control
 * beyond Fechar is the same "Reproduzir animação" toggle AttachmentImagePreview
 * offers, and only in the same situation — a GIF, reduced motion active, not
 * already playing — because reduced motion applies here exactly as it does in
 * the card; the animation must not autoplay just because the view got bigger.
 */

import { type KeyboardEvent, useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";

import "./AttachmentLightbox.css";
import { isGifAttachment } from "./attachmentImageRules";
import { useAttachmentBlobUrl } from "./useAttachmentBlobUrl";
import { usePrefersReducedMotion } from "./useReducedMotion";
import { fetchAttachmentContent } from "./filesApi";
import type { ChannelAttachment } from "./chatTypes";

interface AttachmentLightboxProps {
  attachment: ChannelAttachment;
  /** The URL the card was already showing — reused so opening never blanks. */
  inlineUrl: string;
  /** True when `inlineUrl` is already the original (animated GIF, or WebP). */
  inlineIsOriginal: boolean;
  onClose: () => void;
}

export default function AttachmentLightbox({
  attachment,
  inlineUrl,
  inlineIsOriginal,
  onClose,
}: AttachmentLightboxProps) {
  const dialogRef = useRef<HTMLDivElement>(null);
  const closeButtonRef = useRef<HTMLButtonElement>(null);
  const [userPlayedGif, setUserPlayedGif] = useState(false);

  // Focus opens on Fechar: there is no primary input here, and it is the one
  // control guaranteed to exist regardless of GIF/reduced-motion state.
  useEffect(() => {
    closeButtonRef.current?.focus();
  }, []);

  const gif = isGifAttachment(attachment);
  const reducedMotion = usePrefersReducedMotion();
  // Withheld exactly when the card would also withhold it: a GIF, reduced
  // motion active, not already the original and not yet explicitly started.
  const withheldForMotion = gif && reducedMotion && !inlineIsOriginal && !userPlayedGif;
  const needsOriginal = !inlineIsOriginal && !withheldForMotion;

  // No size cap here, unlike the card: opening the enlarged view is precisely
  // "needed for the viewer", and AttachmentDownloadButton already fetches the
  // same original uncapped for the same reason — decode cost for a still or a
  // single GIF the user asked to enlarge is not the video-sized concern that
  // cap exists for.
  const original = useAttachmentBlobUrl(attachment.id, needsOriginal, fetchAttachmentContent);
  // A fetch failure keeps showing what was already on screen rather than
  // erroring: inlineUrl is a real, already-loaded image, and losing it over a
  // failed *upgrade* would be worse than staying at the lower resolution.
  const displayUrl = inlineIsOriginal ? inlineUrl : (original.url ?? inlineUrl);

  function handleKeyDown(event: KeyboardEvent<HTMLDivElement>) {
    if (event.key === "Escape") {
      event.preventDefault();
      onClose();
      return;
    }
    if (event.key !== "Tab") return;

    const focusable = dialogRef.current?.querySelectorAll<HTMLElement>("button:not(:disabled)");
    if (!focusable?.length) return;
    const first = focusable[0];
    const last = focusable[focusable.length - 1];
    if (event.shiftKey && document.activeElement === first) {
      event.preventDefault();
      last.focus();
    } else if (!event.shiftKey && document.activeElement === last) {
      event.preventDefault();
      first.focus();
    }
  }

  const titleId = `attachment-lightbox-title-${attachment.id}`;

  return createPortal(
    <div className="attachment-lightbox__backdrop" onMouseDown={onClose}>
      <div
        ref={dialogRef}
        className="attachment-lightbox"
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        onKeyDown={handleKeyDown}
        onMouseDown={(event) => event.stopPropagation()}
      >
        <header className="attachment-lightbox__header">
          <h2 id={titleId} className="attachment-lightbox__title">
            {attachment.filename}
          </h2>
          <button
            ref={closeButtonRef}
            type="button"
            className="attachment-lightbox__close"
            aria-label="Fechar visualização ampliada"
            onClick={onClose}
          >
            <span className="material-symbols-outlined" aria-hidden="true">
              close
            </span>
          </button>
        </header>

        <div className="attachment-lightbox__body">
          <img className="attachment-lightbox__img" src={displayUrl} alt="" />
          {withheldForMotion && (
            <button
              type="button"
              className="attachment-lightbox__play"
              onClick={() => setUserPlayedGif(true)}
            >
              Reproduzir animação
            </button>
          )}
        </div>
      </div>
    </div>,
    document.body,
  );
}
