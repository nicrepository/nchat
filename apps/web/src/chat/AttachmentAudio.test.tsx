/**
 * AttachmentAudio tests (issue #670).
 *
 * Same three properties AttachmentVideo.test.tsx asserts for video, plus the
 * one behaviour that differs on purpose: nothing is fetched until the user
 * presses Play, so a history full of audio attachments never pulls all of
 * them into memory on render.
 */

import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import AttachmentAudio from "./AttachmentAudio";
import { MAX_INLINE_AUDIO_BYTES } from "./attachmentAudioRules";
import type { ChannelAttachment } from "./chatTypes";

const mockFetchAttachmentContent = vi.hoisted(() => vi.fn());
vi.mock("./filesApi", () => ({
  fetchAttachmentContent: mockFetchAttachmentContent,
}));

const createObjectURL = vi.fn();
const revokeObjectURL = vi.fn();

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

beforeEach(() => {
  mockFetchAttachmentContent.mockReset();
  mockFetchAttachmentContent.mockResolvedValue(new Blob(["audio-bytes"]));
  createObjectURL.mockReset();
  revokeObjectURL.mockReset();
  let created = 0;
  createObjectURL.mockImplementation(() => `blob:audio-${++created}`);
  vi.stubGlobal("URL", { ...URL, createObjectURL, revokeObjectURL });
  vi.spyOn(HTMLMediaElement.prototype, "play").mockResolvedValue(undefined);
  vi.spyOn(HTMLMediaElement.prototype, "pause").mockImplementation(() => {});
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("AttachmentAudio", () => {
  it("draws nothing at all for a file that is not audio", () => {
    const { container } = render(
      <AttachmentAudio attachment={attachment({ contentType: "image/png" })} />,
    );
    expect(container).toBeEmptyDOMElement();
    expect(mockFetchAttachmentContent).not.toHaveBeenCalled();
  });

  it("says an audio file is still being scanned and requests nothing", () => {
    render(<AttachmentAudio attachment={attachment({ status: "pending_scan" })} />);
    expect(screen.getByTestId("chat-details-audio-pending")).toBeInTheDocument();
    expect(mockFetchAttachmentContent).not.toHaveBeenCalled();
  });

  it("draws nothing for a rejected file", () => {
    const { container } = render(
      <AttachmentAudio attachment={attachment({ status: "rejected" })} />,
    );
    expect(container).toBeEmptyDOMElement();
  });

  it("draws nothing for a file past the size cap", () => {
    const { container } = render(
      <AttachmentAudio attachment={attachment({ size: MAX_INLINE_AUDIO_BYTES + 1 })} />,
    );
    expect(container).toBeEmptyDOMElement();
  });

  it("fetches nothing until the user presses play", () => {
    render(<AttachmentAudio attachment={attachment()} />);
    expect(mockFetchAttachmentContent).not.toHaveBeenCalled();
    expect(screen.getByTestId("chat-audio-a-1-playpause")).toBeInTheDocument();
  });

  it("fetches and plays from an object URL only after play is pressed", async () => {
    render(<AttachmentAudio attachment={attachment()} />);

    fireEvent.click(screen.getByTestId("chat-audio-a-1-playpause"));

    await waitFor(() => expect(mockFetchAttachmentContent).toHaveBeenCalledWith("a-1", expect.any(AbortSignal)));
    const el = await screen.findByTestId("chat-audio-a-1-audio-el");
    const src = el.getAttribute("src") ?? "";
    expect(src).toBe("blob:audio-1");
    expect(src).not.toMatch(/token|bearer|authorization|access/i);
  });

  it("falls back to an error note when the content cannot be fetched", async () => {
    mockFetchAttachmentContent.mockRejectedValue(new Error("403"));
    render(<AttachmentAudio attachment={attachment()} />);

    fireEvent.click(screen.getByTestId("chat-audio-a-1-playpause"));

    expect(await screen.findByTestId("chat-audio-a-1-error")).toBeInTheDocument();
  });

  it("revokes the object URL when it unmounts", async () => {
    const { unmount } = render(<AttachmentAudio attachment={attachment()} />);
    fireEvent.click(screen.getByTestId("chat-audio-a-1-playpause"));
    await screen.findByTestId("chat-audio-a-1-audio-el");

    unmount();

    expect(revokeObjectURL).toHaveBeenCalledWith("blob:audio-1");
  });
});
