/** Synthetic metadata matching the policy v1 response, shared by UI contract tests. */
export const nativeDocumentCases = [
  {
    extension: "docx",
    contentType: "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
    kind: "pages",
  },
  {
    extension: "xlsx",
    contentType: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
    kind: "sheets",
  },
  {
    extension: "pptx",
    contentType: "application/vnd.openxmlformats-officedocument.presentationml.presentation",
    kind: "pages",
  },
  { extension: "odt", contentType: "application/vnd.oasis.opendocument.text", kind: "pages" },
  {
    extension: "ods",
    contentType: "application/vnd.oasis.opendocument.spreadsheet",
    kind: "sheets",
  },
  { extension: "csv", contentType: "text/csv; charset=utf-8", kind: "sheets" },
] as const;

/** Legacy Office payloads were accepted independently of the filename suffix. */
export const legacyContainerCase = {
  extension: "bin",
  contentType: "application/zip",
  kind: "sheets",
} as const;
