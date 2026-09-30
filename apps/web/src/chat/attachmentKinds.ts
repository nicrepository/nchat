/**
 * What kind of card an attachment gets, by its detected type. Shared by the
 * message timeline and the global search's file results (issue #900), so the
 * two never disagree about which files open in the document viewer.
 */

import type { ChannelAttachment } from "./chatTypes";

/** Same mapping the details panel uses, kept local so neither owns the other. */
export function fileIconFor(contentType: string): string {
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
export function isDocumentAttachment(attachment: ChannelAttachment): boolean {
  // The detected type can carry a parameter (e.g. "text/plain;
  // charset=utf-8" is what a real CSV upload's contentType looks like) —
  // stripped here the same way file-service's own NormalizeDetectedMIME
  // does server-side, so the two never disagree about what a bare type is.
  const type = attachment.contentType.split(";")[0].trim().toLowerCase();
  return type === "application/pdf" || type === "text/plain" || type === "application/zip";
}
