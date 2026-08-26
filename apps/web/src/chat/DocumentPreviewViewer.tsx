import { type KeyboardEvent, useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";

import type { ChannelAttachment } from "./chatTypes";
import {
  fetchAttachmentContent,
  fetchDocumentPreviewManifest,
  fetchDocumentPreviewPage,
  type DocumentPreviewManifest,
} from "./filesApi";
import "./DocumentPreviewViewer.css";

interface Props {
  attachment: ChannelAttachment;
  onClose: () => void;
}

export default function DocumentPreviewViewer({ attachment, onClose }: Props) {
  const dialogRef = useRef<HTMLDivElement>(null);
  const closeRef = useRef<HTMLButtonElement>(null);
  const [manifest, setManifest] = useState<DocumentPreviewManifest | null>(null);
  const [page, setPage] = useState(1);
  const [loadedPage, setLoadedPage] = useState<{ page: number; url: string } | null>(null);
  const [zoom, setZoom] = useState(1);
  const [failed, setFailed] = useState(false);

  useEffect(() => closeRef.current?.focus(), []);
  useEffect(() => {
    const controller = new AbortController();
    void fetchDocumentPreviewManifest(attachment.id, controller.signal)
      .then(setManifest)
      .catch(() => setFailed(true));
    return () => controller.abort();
  }, [attachment.id]);

  useEffect(() => {
    if (!manifest) return;
    const controller = new AbortController();
    let url: string | null = null;
    void fetchDocumentPreviewPage(attachment.id, page, controller.signal)
      .then((blob) => {
        if (controller.signal.aborted) return;
        url = URL.createObjectURL(blob);
        setLoadedPage({ page, url });
        setFailed(false);
      })
      .catch(() => {
        if (!controller.signal.aborted) setFailed(true);
      });
    return () => {
      controller.abort();
      if (url) URL.revokeObjectURL(url);
    };
  }, [attachment.id, manifest, page]);

  const pageUrl = loadedPage?.page === page ? loadedPage.url : null;

  function keyDown(event: KeyboardEvent<HTMLDivElement>) {
    if (event.key === "Escape") {
      event.preventDefault();
      onClose();
      return;
    }
    if (event.key !== "Tab") return;
    const controls = dialogRef.current?.querySelectorAll<HTMLElement>("button:not(:disabled)");
    if (!controls?.length) return;
    const first = controls[0];
    const last = controls[controls.length - 1];
    if (event.shiftKey && document.activeElement === first) {
      event.preventDefault();
      last.focus();
    } else if (!event.shiftKey && document.activeElement === last) {
      event.preventDefault();
      first.focus();
    }
  }

  async function download() {
    const blob = await fetchAttachmentContent(attachment.id);
    const url = URL.createObjectURL(blob);
    const anchor = document.createElement("a");
    anchor.href = url;
    anchor.download = attachment.filename || "arquivo";
    anchor.click();
    URL.revokeObjectURL(url);
  }

  const titleId = `document-preview-title-${attachment.id}`;
  return createPortal(
    <div className="document-preview__backdrop" onMouseDown={onClose}>
      <div
        ref={dialogRef}
        className="document-preview"
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        onKeyDown={keyDown}
        onMouseDown={(event) => event.stopPropagation()}
      >
        <header className="document-preview__header">
          <h2 id={titleId}>{attachment.filename}</h2>
          <div className="document-preview__actions">
            <button type="button" onClick={() => void download()}>
              Baixar
            </button>
            <button ref={closeRef} type="button" aria-label="Fechar visualização" onClick={onClose}>
              Fechar
            </button>
          </div>
        </header>
        <div className="document-preview__toolbar">
          <button
            type="button"
            aria-label="Página anterior"
            disabled={page <= 1}
            onClick={() => setPage((value) => value - 1)}
          >
            ‹
          </button>
          <span>{manifest ? `Página ${page} de ${manifest.pageCount}` : "Carregando…"}</span>
          <button
            type="button"
            aria-label="Próxima página"
            disabled={!manifest || page >= manifest.pageCount}
            onClick={() => setPage((value) => value + 1)}
          >
            ›
          </button>
          <button
            type="button"
            aria-label="Reduzir zoom"
            disabled={zoom <= 0.5}
            onClick={() => setZoom((value) => value - 0.25)}
          >
            −
          </button>
          <span>{Math.round(zoom * 100)}%</span>
          <button
            type="button"
            aria-label="Aumentar zoom"
            disabled={zoom >= 2}
            onClick={() => setZoom((value) => value + 0.25)}
          >
            +
          </button>
        </div>
        <div className="document-preview__canvas">
          {pageUrl && (
            <img
              src={pageUrl}
              alt={manifest?.labels[page - 1] ?? `Página ${page}`}
              style={{ width: `${zoom * 100}%` }}
            />
          )}
          {failed && <p role="alert">Não foi possível carregar a pré-visualização.</p>}
        </div>
      </div>
    </div>,
    document.body,
  );
}
