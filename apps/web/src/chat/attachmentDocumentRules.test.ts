import { describe, expect, it } from "vitest";
import { nativeDocumentCases } from "./attachmentDocumentFixtures";
import {
  canOpenDocumentPreview,
  documentPreviewKind,
  isDocumentAttachment,
  isImagePreviewableDocument,
} from "./attachmentDocumentRules";
import type { ChannelAttachment } from "./chatTypes";

function file(contentType: string, filename = "costs.bin"): ChannelAttachment {
  return {
    id: "document",
    filename,
    contentType,
    size: 12,
    status: "clean",
    previewStatus: "ready",
    createdAt: "",
  };
}

describe("document family classification", () => {
  it.each(nativeDocumentCases)("recognizes canonical $extension", ({ contentType, extension }) => {
    expect(isDocumentAttachment(file(contentType, `costs.${extension}`))).toBe(true);
  });
  it.each(["text/plain; charset=utf-8", "application/zip", "Application/PDF; charset=binary"])(
    "preserves legacy %s",
    (mime) => {
      expect(isDocumentAttachment(file(mime))).toBe(true);
    },
  );
  it("does not infer support from an arbitrary vendor MIME or filename", () => {
    expect(isDocumentAttachment(file("application/vnd.untrusted.export", "costs.docx"))).toBe(
      false,
    );
    expect(isDocumentAttachment(file("application/octet-stream"))).toBe(false);
  });
});

it.each(nativeDocumentCases)(
  "selects the renderer for $extension regardless of name",
  ({ contentType, kind }) => {
    const attachment = file(contentType);
    expect(documentPreviewKind(attachment)).toBe(kind);
    expect(isImagePreviewableDocument(attachment)).toBe(kind === "pages");
  },
);
it.each([
  "text/markdown",
  "text/tab-separated-values",
  "application/vnd.oasis.opendocument.presentation",
])("recognizes %s without promising a renderer", (mime) => {
  expect(isDocumentAttachment(file(mime))).toBe(true);
  expect(documentPreviewKind(file(mime))).toBeNull();
});
it.each(["docx", "odt", "ppt", "pptx"])("preserves legacy raster %s", (ext) => {
  expect(isImagePreviewableDocument(file("application/zip", `costs.${ext}`))).toBe(true);
  expect(isDocumentAttachment(file("application/octet-stream", `costs.${ext}`))).toBe(true);
});
it.each(["xlsx", "ods"])("preserves legacy sheet %s", (ext) => {
  expect(documentPreviewKind(file("application/zip", `costs.${ext}`))).toBe("sheets");
});
it("does not give an arbitrary archive a renderer", () => {
  expect(documentPreviewKind(file("application/zip", "bundle.zip"))).toBeNull();
});

it("does not treat inherited object names as renderers", () => {
  expect(documentPreviewKind(file("constructor"))).toBeNull();
  expect(documentPreviewKind(file("application/zip", "costs.constructor"))).toBeNull();
});

it("opens ready legacy containers by manifest without guessing an image renderer", () => {
  const legacy = file("application/zip", "opaque.bin");
  expect(documentPreviewKind(legacy)).toBeNull();
  expect(isImagePreviewableDocument(legacy)).toBe(false);
  expect(canOpenDocumentPreview(legacy)).toBe(true);
  expect(canOpenDocumentPreview({ ...legacy, previewStatus: "unsupported" })).toBe(false);
  expect(canOpenDocumentPreview({ ...legacy, status: "pending_scan" })).toBe(false);
  expect(canOpenDocumentPreview(file("application/vnd.oasis.opendocument.presentation"))).toBe(
    false,
  );
});
