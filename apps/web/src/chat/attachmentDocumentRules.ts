import { isPreviewAvailable, type ChannelAttachment } from "./chatTypes";

type DocumentPreviewKind = "pages" | "sheets";

// Explicit renderer contracts for canonical MIME values emitted by policy v1.
const documentRenderers = new Map<string, DocumentPreviewKind>([
  ["application/pdf", "pages"],
  ["text/csv", "sheets"],
  ["application/vnd.openxmlformats-officedocument.wordprocessingml.document", "pages"],
  ["application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", "sheets"],
  ["application/vnd.openxmlformats-officedocument.presentationml.presentation", "pages"],
  ["application/vnd.oasis.opendocument.text", "pages"],
  ["application/vnd.oasis.opendocument.spreadsheet", "sheets"],
]);
const documentMIMEsWithoutRenderer = new Set([
  "text/markdown",
  "text/tab-separated-values",
  "application/vnd.oasis.opendocument.presentation",
]);
const legacyContainerRenderers = new Map<string, DocumentPreviewKind>([
  ["docx", "pages"],
  ["odt", "pages"],
  ["ppt", "pages"],
  ["pptx", "pages"],
  ["xlsx", "sheets"],
  ["ods", "sheets"],
]);

function normalizedMIME(attachment: ChannelAttachment): string {
  return attachment.contentType.split(";")[0].trim().toLowerCase();
}

/** Renderer compatibility is presentation only; status/availability remain server decisions. */
export function documentPreviewKind(attachment: ChannelAttachment): DocumentPreviewKind | null {
  const type = normalizedMIME(attachment);
  const renderer = documentRenderers.get(type);
  if (renderer) return renderer;
  // Legacy delimited text was persisted as text/plain. New TXT has unsupported status.
  if (type === "text/plain") return "sheets";
  if (type !== "application/zip" && type !== "application/octet-stream") return null;
  const extension = attachment.filename.toLowerCase().match(/\.([a-z0-9]+)$/)?.[1] ?? "";
  return legacyContainerRenderers.get(extension) ?? null;
}

/** A document may belong to this family without having a renderer or a ready preview. */
export function isDocumentAttachment(attachment: ChannelAttachment): boolean {
  return (
    documentPreviewKind(attachment) !== null ||
    documentMIMEsWithoutRenderer.has(normalizedMIME(attachment)) ||
    normalizedMIME(attachment) === "application/zip"
  );
}

/** Only raster pages may be assigned to an image; sheets are bounded JSON. */
export function isImagePreviewableDocument(attachment: ChannelAttachment): boolean {
  return documentPreviewKind(attachment) === "pages";
}

/** A ready legacy ZIP is resolved by its manifest, even with an opaque filename. */
export function canOpenDocumentPreview(attachment: ChannelAttachment): boolean {
  return (
    attachment.status === "clean" &&
    isPreviewAvailable(attachment.previewStatus) &&
    (documentPreviewKind(attachment) !== null || normalizedMIME(attachment) === "application/zip")
  );
}
