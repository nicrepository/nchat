/**
 * Attachment presentation helpers used by the global search (issue #900).
 *
 * Document viewer eligibility is owned by attachmentDocumentRules.ts so the
 * search, message timeline and recent-files surfaces share the same rule.
 */

/** Same mapping the message card and the details panel use. */
export function fileIconFor(contentType: string): string {
  if (contentType.startsWith("image/")) return "image";
  if (contentType.startsWith("video/")) return "movie";
  if (contentType.startsWith("audio/")) return "graphic_eq";
  if (contentType === "application/pdf") return "picture_as_pdf";
  if (contentType.startsWith("text/")) return "description";
  return "draft";
}
