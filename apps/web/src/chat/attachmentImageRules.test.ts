import { describe, expect, it } from "vitest";

import {
  MAX_INLINE_ORIGINAL_IMAGE_BYTES,
  canShowOriginalInline,
  isGifAttachment,
  isImageAttachment,
  needsOriginalForInline,
  withinInlineOriginalCap,
} from "./attachmentImageRules";
import type { ChannelAttachment } from "./chatTypes";

function attachment(overrides: Partial<ChannelAttachment> = {}): ChannelAttachment {
  return {
    id: "a-1",
    filename: "foto.png",
    contentType: "image/png",
    size: 1024,
    status: "clean",
    previewStatus: "ready",
    createdAt: "2026-07-15T12:00:00.000Z",
    ...overrides,
  };
}

describe("isImageAttachment", () => {
  it("accepts the four raster types this feature previews", () => {
    for (const contentType of ["image/png", "image/jpeg", "image/webp", "image/gif"]) {
      expect(isImageAttachment(attachment({ contentType }))).toBe(true);
    }
  });

  it("reads the type case-insensitively", () => {
    expect(isImageAttachment(attachment({ contentType: "IMAGE/PNG" }))).toBe(true);
  });

  it("refuses SVG even though it is an image MIME type", () => {
    expect(isImageAttachment(attachment({ contentType: "image/svg+xml" }))).toBe(false);
  });

  it("refuses non-image types", () => {
    expect(isImageAttachment(attachment({ contentType: "application/pdf" }))).toBe(false);
    expect(isImageAttachment(attachment({ contentType: "video/mp4" }))).toBe(false);
    expect(isImageAttachment(attachment({ contentType: "" }))).toBe(false);
  });
});

describe("isGifAttachment", () => {
  it("matches only image/gif", () => {
    expect(isGifAttachment(attachment({ contentType: "image/gif" }))).toBe(true);
    expect(isGifAttachment(attachment({ contentType: "IMAGE/GIF" }))).toBe(true);
    expect(isGifAttachment(attachment({ contentType: "image/png" }))).toBe(false);
    expect(isGifAttachment(attachment({ contentType: "image/webp" }))).toBe(false);
  });
});

describe("needsOriginalForInline", () => {
  it("is true only for GIF and WebP", () => {
    expect(needsOriginalForInline(attachment({ contentType: "image/gif" }))).toBe(true);
    expect(needsOriginalForInline(attachment({ contentType: "image/webp" }))).toBe(true);
    expect(needsOriginalForInline(attachment({ contentType: "image/png" }))).toBe(false);
    expect(needsOriginalForInline(attachment({ contentType: "image/jpeg" }))).toBe(false);
  });
});

describe("canShowOriginalInline", () => {
  it("accepts a cleared GIF or WebP within the size cap", () => {
    expect(canShowOriginalInline(attachment({ contentType: "image/gif" }))).toBe(true);
    expect(
      canShowOriginalInline(
        attachment({ contentType: "image/webp", size: MAX_INLINE_ORIGINAL_IMAGE_BYTES }),
      ),
    ).toBe(true);
  });

  it("refuses anything the scan has not cleared", () => {
    expect(
      canShowOriginalInline(attachment({ contentType: "image/gif", status: "pending_scan" })),
    ).toBe(false);
    expect(
      canShowOriginalInline(attachment({ contentType: "image/gif", status: "rejected" })),
    ).toBe(false);
  });

  it("refuses PNG/JPEG, which never need the original for the default preview", () => {
    expect(canShowOriginalInline(attachment({ contentType: "image/png" }))).toBe(false);
    expect(canShowOriginalInline(attachment({ contentType: "image/jpeg" }))).toBe(false);
  });

  it("refuses a file past the size cap", () => {
    expect(
      canShowOriginalInline(
        attachment({ contentType: "image/gif", size: MAX_INLINE_ORIGINAL_IMAGE_BYTES + 1 }),
      ),
    ).toBe(false);
  });

  it("refuses an empty file", () => {
    expect(canShowOriginalInline(attachment({ contentType: "image/gif", size: 0 }))).toBe(false);
  });
});

describe("withinInlineOriginalCap", () => {
  it("accepts a clean file of any raster type within the cap, unlike canShowOriginalInline", () => {
    expect(withinInlineOriginalCap(attachment({ contentType: "image/png" }))).toBe(true);
    expect(withinInlineOriginalCap(attachment({ contentType: "image/jpeg" }))).toBe(true);
  });

  it("refuses anything the scan has not cleared", () => {
    expect(withinInlineOriginalCap(attachment({ status: "pending_scan" }))).toBe(false);
    expect(withinInlineOriginalCap(attachment({ status: "rejected" }))).toBe(false);
  });

  it("refuses a file past the size cap or an empty one", () => {
    expect(withinInlineOriginalCap(attachment({ size: MAX_INLINE_ORIGINAL_IMAGE_BYTES + 1 }))).toBe(
      false,
    );
    expect(withinInlineOriginalCap(attachment({ size: 0 }))).toBe(false);
  });
});
