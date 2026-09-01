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

import { saveAttachmentToDisk, voiceMessageFilename } from "./attachmentDownload";
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

  it("names a recording by when it was sent, keeping the stored container extension", () => {
    // Local time by construction: the viewer reads their own clock, not UTC.
    const sentAt = new Date(SENT_AT);
    const pad = (value: number) => String(value).padStart(2, "0");
    const expected =
      `mensagem-de-voz-${sentAt.getFullYear()}-${pad(sentAt.getMonth() + 1)}` +
      `-${pad(sentAt.getDate())}-${pad(sentAt.getHours())}${pad(sentAt.getMinutes())}.webm`;

    expect(voiceMessageFilename(attachment(), SENT_AT)).toBe(expected);
  });

  it("keeps ogg and m4a recordings named as what they actually are", () => {
    expect(voiceMessageFilename(attachment({ filename: "voice-message.OGG" }), SENT_AT)).toMatch(
      /\.ogg$/,
    );
    expect(voiceMessageFilename(attachment({ filename: "voice-message.m4a" }), SENT_AT)).toMatch(
      /\.m4a$/,
    );
  });

  it("never lets a stored name reach the saved one", () => {
    const hostile = attachment({ filename: '../../etc/passwd";\r\nX-Injected: 1.webm' });
    expect(voiceMessageFilename(hostile, SENT_AT)).toMatch(/^mensagem-de-voz-[\d-]+\.webm$/);
  });

  it("omits an extension the stored name does not have", () => {
    expect(voiceMessageFilename(attachment({ filename: "voice-message" }), SENT_AT)).toMatch(
      /^mensagem-de-voz-[\d-]+$/,
    );
  });

  it("keeps the real extension when the send time is missing or unusable", () => {
    expect(voiceMessageFilename(attachment(), "")).toBe("mensagem-de-voz.webm");
    expect(voiceMessageFilename(attachment(), "ontem")).toBe("mensagem-de-voz.webm");
  });
});
