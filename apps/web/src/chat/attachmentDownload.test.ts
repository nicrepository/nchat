/**
 * attachmentDownload tests (issue #740).
 *
 * Two obligations: the bytes come from the authenticated content route and the
 * object URL never outlives the click, and a saved voice message gets a name a
 * person can read without any part of a server- or client-supplied string
 * reaching it.
 */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const mockContent = vi.hoisted(() => vi.fn());
vi.mock("./filesApi", () => ({ fetchAttachmentContent: mockContent }));

import {
  attachmentDownloadFilename,
  saveAttachmentToDisk,
  voiceMessageFilename,
} from "./attachmentDownload";
import type { ChannelAttachment } from "./chatTypes";

const createObjectURL = vi.fn();
const revokeObjectURL = vi.fn();

function attachment(overrides: Partial<ChannelAttachment> = {}): ChannelAttachment {
  return {
    id: "att-voice",
    filename: "voice-message.webm",
    contentType: "video/webm",
    size: 4096,
    status: "clean",
    previewStatus: "unsupported",
    createdAt: "2026-07-15T12:04:00.000Z",
    audioKind: "voice",
    ...overrides,
  };
}

beforeEach(() => {
  mockContent.mockReset().mockResolvedValue(new Blob(["voice-bytes"]));
  createObjectURL.mockReset().mockReturnValue("blob:saved");
  revokeObjectURL.mockReset();
  vi.stubGlobal("URL", { ...URL, createObjectURL, revokeObjectURL });
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("saveAttachmentToDisk", () => {
  it("asks the authenticated content route for the bytes and hands them to the browser", async () => {
    const click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {});

    await saveAttachmentToDisk("att-voice", "mensagem-de-voz-2026-07-15-1204.webm");

    expect(mockContent).toHaveBeenCalledWith("att-voice");
    const anchor = click.mock.instances[0] as HTMLAnchorElement;
    expect(anchor.href).toBe("blob:saved");
    expect(anchor.download).toBe("mensagem-de-voz-2026-07-15-1204.webm");
  });

  it("revokes the object URL in the same task, so no address survives the click", async () => {
    vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {});

    await saveAttachmentToDisk("att-voice", "arquivo.webm");

    expect(revokeObjectURL).toHaveBeenCalledWith("blob:saved");
  });

  it("revokes the object URL even when the browser refuses the click", async () => {
    vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {
      throw new Error("blocked");
    });

    await expect(saveAttachmentToDisk("att-voice", "arquivo.webm")).rejects.toThrow("blocked");
    expect(revokeObjectURL).toHaveBeenCalledWith("blob:saved");
  });

  it("rejects, and creates no URL at all, when the server refuses the request", async () => {
    mockContent.mockRejectedValue(new Error("403"));

    await expect(saveAttachmentToDisk("att-voice", "arquivo.webm")).rejects.toThrow();
    expect(createObjectURL).not.toHaveBeenCalled();
    expect(revokeObjectURL).not.toHaveBeenCalled();
  });
});

describe("voiceMessageFilename", () => {
  // A message's attachment carries no date of its own (chat-service dates it by
  // the message), so the name is built from the message's timestamp.
  const SENT_AT = "2026-07-15T12:04:00.000Z";

  it("names a recording by when it was sent, with a .mp3 extension regardless of the recorded container", () => {
    // Local time by construction: the viewer reads their own clock, not UTC.
    const sentAt = new Date(SENT_AT);
    const pad = (value: number) => String(value).padStart(2, "0");
    const expected =
      `mensagem-de-voz-${sentAt.getFullYear()}-${pad(sentAt.getMonth() + 1)}` +
      `-${pad(sentAt.getDate())}-${pad(sentAt.getHours())}${pad(sentAt.getMinutes())}.mp3`;

    expect(voiceMessageFilename(SENT_AT)).toBe(expected);
  });

  it("keeps the .mp3 extension no matter what the recorded container's own extension is", () => {
    // file-service always re-encodes voice-message downloads to real MP3 (see
    // Download's audioTranscodeFormat) — this file never had to know the
    // recorded container's extension to begin with, so there is nothing left
    // to vary here.
    expect(voiceMessageFilename(SENT_AT)).toMatch(/\.mp3$/);
  });

  it("uses .mp3 when the send time is missing or unusable", () => {
    expect(voiceMessageFilename("")).toBe("mensagem-de-voz.mp3");
    expect(voiceMessageFilename("ontem")).toBe("mensagem-de-voz.mp3");
  });
});

describe("attachmentDownloadFilename", () => {
  it("swaps a non-voice audio file's extension to .mp3", () => {
    const audioFile = attachment({
      filename: "gravação.ogg",
      contentType: "audio/ogg",
      audioKind: undefined,
    });
    expect(attachmentDownloadFilename(audioFile)).toBe("gravação.mp3");
  });

  it("leaves a non-audio attachment's filename untouched", () => {
    const pdf = attachment({
      filename: "relatório.pdf",
      contentType: "application/pdf",
      audioKind: undefined,
    });
    expect(attachmentDownloadFilename(pdf)).toBe("relatório.pdf");
  });

  it("falls back to a fixed name when a non-audio attachment has no filename", () => {
    const noName = attachment({
      filename: "",
      contentType: "application/pdf",
      audioKind: undefined,
    });
    expect(attachmentDownloadFilename(noName)).toBe("arquivo");
  });

  it("is case-insensitive about the extension it replaces", () => {
    const audioFile = attachment({
      filename: "gravação.OGG",
      contentType: "audio/ogg",
      audioKind: undefined,
    });
    expect(attachmentDownloadFilename(audioFile)).toBe("gravação.mp3");
  });
});
