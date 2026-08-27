import { describe, expect, it } from "vitest";

import {
  MAX_INLINE_AUDIO_BYTES,
  canPlayAudioInline,
  isAudioAttachment,
  isVoiceMessage,
} from "./attachmentAudioRules";
import type { ChannelAttachment } from "./chatTypes";

function attachment(overrides: Partial<ChannelAttachment> = {}): ChannelAttachment {
  return {
    id: "a-1",
    filename: "nota.ogg",
    contentType: "application/ogg",
    size: 4096,
    status: "clean",
    previewStatus: "unsupported",
    createdAt: "2026-07-15T12:00:00.000Z",
    ...overrides,
  };
}

describe("isAudioAttachment", () => {
  it("accepts the recognised audio container types", () => {
    for (const contentType of [
      "audio/mpeg",
      "audio/ogg",
      "audio/wav",
      "audio/wave",
      "audio/x-wav",
      "application/ogg",
      "AUDIO/MPEG",
    ]) {
      expect(isAudioAttachment(attachment({ contentType }))).toBe(true);
    }
  });

  it("accepts a voice message whatever its sniffed container is", () => {
    // MediaRecorder's audio-only output sniffs as the video container it is
    // wrapped in — this is the whole reason audioKind exists.
    expect(isAudioAttachment(attachment({ contentType: "video/webm", audioKind: "voice" }))).toBe(
      true,
    );
    expect(isAudioAttachment(attachment({ contentType: "video/mp4", audioKind: "voice" }))).toBe(
      true,
    );
  });

  it("refuses a video that was not explicitly tagged as voice", () => {
    expect(isAudioAttachment(attachment({ contentType: "video/webm" }))).toBe(false);
  });

  it("refuses an unrelated file type", () => {
    expect(isAudioAttachment(attachment({ contentType: "application/pdf" }))).toBe(false);
  });
});

describe("isVoiceMessage", () => {
  it("is true only for the explicit server tag, never inferred from content type or filename", () => {
    expect(isVoiceMessage(attachment({ audioKind: "voice" }))).toBe(true);
    expect(
      isVoiceMessage(
        attachment({ filename: "recording-1699999999.webm", contentType: "audio/mpeg" }),
      ),
    ).toBe(false);
  });
});

describe("canPlayAudioInline", () => {
  it("accepts a cleared audio attachment within the size cap", () => {
    expect(canPlayAudioInline(attachment())).toBe(true);
    expect(canPlayAudioInline(attachment({ size: MAX_INLINE_AUDIO_BYTES }))).toBe(true);
  });

  it("refuses anything the scan has not cleared", () => {
    expect(canPlayAudioInline(attachment({ status: "pending_scan" }))).toBe(false);
    expect(canPlayAudioInline(attachment({ status: "rejected" }))).toBe(false);
  });

  it("refuses a file that is not audio, whatever it is called", () => {
    expect(
      canPlayAudioInline(attachment({ contentType: "application/pdf", filename: "nota.ogg" })),
    ).toBe(false);
  });

  it("refuses a file too large to hold in memory", () => {
    expect(canPlayAudioInline(attachment({ size: MAX_INLINE_AUDIO_BYTES + 1 }))).toBe(false);
  });

  it("refuses an empty file", () => {
    expect(canPlayAudioInline(attachment({ size: 0 }))).toBe(false);
  });
});
