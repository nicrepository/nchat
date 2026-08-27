import { type KeyboardEvent, useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";

import type { ChannelAttachment } from "./chatTypes";
import {
  fetchAttachmentContent,
  fetchDocumentPreviewManifest,
  fetchDocumentPreviewPage,
  fetchDocumentPreviewSheet,
  type DocumentPreviewManifest,
  type DocumentPreviewSheet,
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
  const [loadedSheet, setLoadedSheet] = useState<{
    page: number;
    sheet: DocumentPreviewSheet;
  } | null>(null);
  const [zoom, setZoom] = useState(1);
  const [failed, setFailed] = useState(false);
  const [downloadState, setDownloadState] = useState<"idle" | "loading" | "failed">("idle");

  useEffect(() => closeRef.current?.focus(), []);
  useEffect(() => {
    const controller = new AbortController();
    void fetchDocumentPreviewManifest(attachment.id, controller.signal)
      .then(setManifest)
      .catch(() => setFailed(true));
    return () => controller.abort();
  }, [attachment.id]);

  // A sheet-kind preview (task #494's spreadsheet/CSV phase) is bounded table
  // data, not an image: it needs its own fetcher and no object URL to revoke.
  // manifest.kind decides which of the two this effect runs, deterministically
  // — the client never has to guess or attempt both.
  const isSheet = manifest?.kind === "sheets";

  useEffect(() => {
    if (!manifest || isSheet) return;
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
  }, [attachment.id, manifest, isSheet, page]);

  useEffect(() => {
    if (!manifest || !isSheet) return;
    const controller = new AbortController();
    void fetchDocumentPreviewSheet(attachment.id, page, controller.signal)
      .then((sheet) => {
        if (controller.signal.aborted) return;
        setLoadedSheet({ page, sheet });
        setFailed(false);
      })
      .catch(() => {
        if (!controller.signal.aborted) setFailed(true);
      });
    return () => controller.abort();
  }, [attachment.id, manifest, isSheet, page]);

  const pageUrl = !isSheet && loadedPage?.page === page ? loadedPage.url : null;
  const sheet = isSheet && loadedSheet?.page === page ? loadedSheet.sheet : null;

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

  // Same idle/loading/failed shape as MessageAttachments' own
  // AttachmentDownloadButton — a failed fetch here used to be a silent
  // unhandled rejection; it now degrades to a short inline note instead of
  // leaving the button looking clicked-and-forgotten.
  async function download() {
    if (downloadState === "loading") return;
    setDownloadState("loading");
    try {
      const blob = await fetchAttachmentContent(attachment.id);
      const url = URL.createObjectURL(blob);
      const anchor = document.createElement("a");
      anchor.href = url;
      anchor.download = attachment.filename || "arquivo";
      anchor.click();
      URL.revokeObjectURL(url);
      setDownloadState("idle");
    } catch {
      setDownloadState("failed");
    }
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
            <button
              type="button"
              className="document-preview__action-button"
              aria-label={`Baixar ${attachment.filename}`}
              disabled={downloadState === "loading"}
              onClick={() => void download()}
            >
              <span className="material-symbols-outlined" aria-hidden="true">
                download
              </span>
              {downloadState === "loading" ? "Baixando…" : "Baixar"}
            </button>
            <button
              ref={closeRef}
              type="button"
              className="document-preview__icon-button document-preview__close"
              aria-label="Fechar visualização"
              onClick={onClose}
            >
              <span className="material-symbols-outlined" aria-hidden="true">
                close
              </span>
            </button>
          </div>
        </header>
        {downloadState === "failed" && (
          <p className="document-preview__download-error" role="alert">
            Não foi possível baixar o arquivo.
          </p>
        )}
        <div className="document-preview__toolbar">
          {!isSheet && (
            <>
              <button
                type="button"
                className="document-preview__icon-button"
                aria-label="Página anterior"
                disabled={page <= 1}
                onClick={() => setPage((value) => value - 1)}
              >
                <span className="material-symbols-outlined" aria-hidden="true">
                  chevron_left
                </span>
              </button>
              <span>{manifest ? `Página ${page} de ${manifest.pageCount}` : "Carregando…"}</span>
              <button
                type="button"
                className="document-preview__icon-button"
                aria-label="Próxima página"
                disabled={!manifest || page >= manifest.pageCount}
                onClick={() => setPage((value) => value + 1)}
              >
                <span className="material-symbols-outlined" aria-hidden="true">
                  chevron_right
                </span>
              </button>
            </>
          )}
          {isSheet && <span>{manifest ? "Planilha" : "Carregando…"}</span>}
          <button
            type="button"
            className="document-preview__icon-button"
            aria-label="Reduzir zoom"
            disabled={zoom <= 0.5}
            onClick={() => setZoom((value) => value - 0.25)}
          >
            <span className="material-symbols-outlined" aria-hidden="true">
              zoom_out
            </span>
          </button>
          <span>{Math.round(zoom * 100)}%</span>
          <button
            type="button"
            className="document-preview__icon-button"
            aria-label="Aumentar zoom"
            disabled={zoom >= 2}
            onClick={() => setZoom((value) => value + 0.25)}
          >
            <span className="material-symbols-outlined" aria-hidden="true">
              zoom_in
            </span>
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
          {sheet && (
            <div className="document-preview__sheet" style={{ zoom }}>
              {(sheet.truncatedRows || sheet.truncatedColumns) && (
                <p className="document-preview__sheet-note" role="status">
                  Mostrando {sheet.totalRowsRead} {sheet.truncatedRows ? "das primeiras" : ""}{" "}
                  linhas
                  {sheet.truncatedColumns ? ` e as primeiras ${sheet.columns.length} colunas` : ""}.
                </p>
              )}
              <table className="document-preview__sheet-table">
                <thead>
                  <tr>
                    {sheet.columns.map((column) => (
                      <th key={column} scope="col">
                        {column}
                      </th>
                    ))}
                  </tr>
                </thead>
                <tbody>
                  {sheet.rows.map((row, rowIndex) => (
                    // Rows are opaque, positional data with no stable id of
                    // their own — the index is the only identity there is.
                    <tr key={rowIndex}>
                      {row.map((cell, cellIndex) => (
                        <td key={cellIndex}>{cell}</td>
                      ))}
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
          {failed && <p role="alert">Não foi possível carregar a pré-visualização.</p>}
        </div>
      </div>
    </div>,
    document.body,
  );
}
