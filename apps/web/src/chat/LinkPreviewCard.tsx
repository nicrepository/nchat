/**
 * The rich preview card (issue #807 §24-28).
 *
 * Drawn under a message for a link the server cleared and described. Every
 * string on it is remote text carried as data and rendered as text; the image
 * is a derived asset fetched from chat-service with the reader's credentials
 * and wrapped in a blob URL — nothing on this card ever loads from the remote
 * host, so viewing a conversation tells that host nothing.
 *
 * Hierarchy: the real hostname first, because it is the one thing the reader
 * can judge the destination by, then title, then description, then image. The
 * page's own site_name is shown beside the host, never instead of it.
 *
 * The whole card is one anchor to the server's href, with an accessible name of
 * title + hostname. The menu beside it offers exactly what the backend supports:
 * open, copy, hide. "Reportar link" has no backend yet and is deliberately not
 * offered rather than wired to nothing.
 */

import { useCallback, useEffect, useId, useRef, useState } from "react";

import { fetchLinkPreviewImage } from "./chatApi";
import { hideLinkPreview, isLinkPreviewHidden } from "./linkPreviewPreferences";
import type { LinkPreview, MessageLink } from "./messageLinks";
import { useAttachmentBlobUrl } from "./useAttachmentBlobUrl";

export interface LinkPreviewCardProps {
  messageId: string;
  link: MessageLink;
}

function CardImage({ imageId, title }: { imageId: string; title: string }) {
  const { url, onLoadError } = useAttachmentBlobUrl(imageId, imageId !== "", fetchLinkPreviewImage);
  if (!url) return null;
  return (
    <div className="link-card__media">
      <img
        className="link-card__image"
        src={url}
        alt={title ? `Imagem de ${title}` : ""}
        loading="lazy"
        onError={onLoadError}
      />
    </div>
  );
}

function CardMenu({ link, onHide }: { link: MessageLink; onHide: () => void }) {
  const [open, setOpen] = useState(false);
  const [copied, setCopied] = useState(false);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const menuRef = useRef<HTMLDivElement>(null);
  const menuId = useId();

  const close = useCallback(() => {
    setOpen(false);
    triggerRef.current?.focus();
  }, []);

  useEffect(() => {
    if (!open) return;
    menuRef.current?.querySelector<HTMLElement>("[role=menuitem]")?.focus();
    const onPointerDown = (event: PointerEvent) => {
      if (!menuRef.current?.contains(event.target as Node) && event.target !== triggerRef.current) {
        setOpen(false);
      }
    };
    document.addEventListener("pointerdown", onPointerDown);
    return () => document.removeEventListener("pointerdown", onPointerDown);
  }, [open]);

  const copy = useCallback(async () => {
    try {
      await navigator.clipboard.writeText(link.href);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1500);
    } catch {
      // Clipboard refused: nothing to recover; the URL is visible on the card.
    }
    close();
  }, [close, link.href]);

  const onKeyDown = (event: React.KeyboardEvent<HTMLDivElement>) => {
    if (event.key === "Escape") {
      event.preventDefault();
      close();
      return;
    }
    if (event.key !== "ArrowDown" && event.key !== "ArrowUp") return;
    event.preventDefault();
    const items = Array.from(
      menuRef.current?.querySelectorAll<HTMLElement>("[role=menuitem]") ?? [],
    );
    const index = items.indexOf(document.activeElement as HTMLElement);
    const next =
      event.key === "ArrowDown"
        ? (index + 1) % items.length
        : (index - 1 + items.length) % items.length;
    items[next]?.focus();
  };

  return (
    <div className="link-card__menu-host">
      <button
        ref={triggerRef}
        type="button"
        className="link-card__menu-trigger"
        aria-label={`Opções da visualização de ${link.hostname}`}
        aria-haspopup="menu"
        aria-expanded={open}
        aria-controls={open ? menuId : undefined}
        onClick={() => setOpen((value) => !value)}
      >
        <span className="material-symbols-outlined" aria-hidden="true">
          more_horiz
        </span>
      </button>
      {open && (
        <div
          ref={menuRef}
          id={menuId}
          className="link-card__menu"
          role="menu"
          aria-label="Ações da visualização"
          onKeyDown={onKeyDown}
        >
          <a
            role="menuitem"
            className="link-card__menu-item"
            href={link.href}
            target="_blank"
            rel="noopener noreferrer"
            onClick={close}
          >
            Abrir link
          </a>
          <button
            type="button"
            role="menuitem"
            className="link-card__menu-item"
            onClick={() => void copy()}
          >
            {copied ? "Copiado" : "Copiar link"}
          </button>
          <button
            type="button"
            role="menuitem"
            className="link-card__menu-item"
            onClick={() => {
              setOpen(false);
              onHide();
            }}
          >
            Ocultar visualização
          </button>
        </div>
      )}
    </div>
  );
}

function PlaceholderCard({ hostname }: { hostname: string }) {
  return (
    <div
      className="link-card link-card--placeholder"
      data-testid="chat-link-card-placeholder"
      role="status"
    >
      <span className="link-card__host">{hostname}</span>
      <span className="link-card__placeholder-text">Preparando visualização…</span>
    </div>
  );
}

export default function LinkPreviewCard({ messageId, link }: LinkPreviewCardProps) {
  const [hidden, setHidden] = useState(() => isLinkPreviewHidden(messageId, link.url));
  const preview = link.preview;
  const hide = useCallback(() => {
    hideLinkPreview(messageId, link.url);
    setHidden(true);
  }, [link.url, messageId]);

  if (hidden || !preview || link.safety !== "safe" || link.href === "") return null;
  if (preview.state !== "ready")
    return <PlaceholderCard hostname={preview.hostname || link.hostname} />;

  const hostname = preview.hostname || link.hostname;
  const accessibleName = preview.title ? `${preview.title} — ${hostname}` : hostname;
  return (
    <div className="link-card" data-testid="chat-link-card">
      <a
        className="link-card__body"
        href={link.href}
        target="_blank"
        rel="noopener noreferrer"
        aria-label={accessibleName}
      >
        <CardText preview={preview} hostname={hostname} />
      </a>
      <CardMenu link={link} onHide={hide} />
    </div>
  );
}

/** The card's text and image: what the page said about itself, as data. */
function CardText({ preview, hostname }: { preview: LinkPreview; hostname: string }) {
  const siteName = preview.siteName !== hostname ? preview.siteName : "";
  return (
    <>
      <span className="link-card__host">
        {hostname}
        {siteName && <span className="link-card__site"> · {siteName}</span>}
      </span>
      {preview.title && <span className="link-card__title">{preview.title}</span>}
      {preview.description && <span className="link-card__description">{preview.description}</span>}
      {preview.imageId && <CardImage imageId={preview.imageId} title={preview.title} />}
    </>
  );
}
