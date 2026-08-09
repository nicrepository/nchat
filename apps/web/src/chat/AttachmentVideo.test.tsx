/**
 * AttachmentVideo tests (RF-31).
 *
 * Three properties are under test and they matter for different reasons:
 *
 *  - which attachments are allowed to spend a request at all. A file the scan
 *    has not cleared must not be asked for, and a file that is not a video must
 *    not become one on the strength of its name;
 *  - that nothing a player needs ever appears in a URL. The credential stays in
 *    the Authorization header the api client sets, and the element's src is an
 *    object URL scoped to this document;
 *  - that every object URL this component creates is revoked. A missed revoke is
 *    a leak the tab keeps until it is closed, and a video is the largest thing
 *    this application ever holds in memory.
 */

import { render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import AttachmentVideo from "./AttachmentVideo";
import { MAX_INLINE_VIDEO_BYTES, canPlayInline } from "./attachmentVideo";
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
    filename: "reunião.mp4",
    contentType: "video/mp4",
    size: 4 * 1024 * 1024,
    status: "clean",
    previewStatus: "unsupported",
    createdAt: "2026-07-15T12:00:00.000Z",
    ...overrides,
  };
}

beforeEach(() => {
  mockFetchAttachmentContent.mockReset();
  mockFetchAttachmentContent.mockResolvedValue(new Blob(["video-bytes"]));
  createObjectURL.mockReset();
  revokeObjectURL.mockReset();
  let created = 0;
  createObjectURL.mockImplementation(() => `blob:video-${++created}`);
  // jsdom implements neither, and they are precisely what has to be asserted.
  vi.stubGlobal("URL", { ...URL, createObjectURL, revokeObjectURL });
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("canPlayInline", () => {
  it("accepts a cleared video within the size cap", () => {
    expect(canPlayInline(attachment())).toBe(true);
    expect(canPlayInline(attachment({ size: MAX_INLINE_VIDEO_BYTES }))).toBe(true);
  });

  it("refuses anything the scan has not cleared", () => {
    expect(canPlayInline(attachment({ status: "pending_scan" }))).toBe(false);
    expect(canPlayInline(attachment({ status: "rejected" }))).toBe(false);
  });

  it("refuses a file that is not a video, whatever it is called", () => {
    expect(canPlayInline(attachment({ contentType: "text/html", filename: "clip.mp4" }))).toBe(
      false,
    );
    expect(canPlayInline(attachment({ contentType: "application/pdf" }))).toBe(false);
    expect(canPlayInline(attachment({ contentType: "" }))).toBe(false);
  });

  it("refuses a file too large to hold in memory", () => {
    expect(canPlayInline(attachment({ size: MAX_INLINE_VIDEO_BYTES + 1 }))).toBe(false);
  });

  it("refuses an empty file", () => {
    expect(canPlayInline(attachment({ size: 0 }))).toBe(false);
  });

  it("reads the type case-insensitively", () => {
    expect(canPlayInline(attachment({ contentType: "VIDEO/MP4" }))).toBe(true);
  });
});

describe("AttachmentVideo", () => {
  it("renders a native player with controls for a cleared video", async () => {
    render(<AttachmentVideo attachment={attachment()} />);

    const player = await screen.findByTestId("chat-details-file-video");
    expect(player.tagName).toBe("VIDEO");
    expect(player).toHaveAttribute("controls");
    expect(player).not.toHaveAttribute("autoplay");
    expect(player).toHaveAttribute("aria-label", "Vídeo: reunião.mp4");
  });

  it("plays from an object URL and never from one carrying a credential", async () => {
    render(<AttachmentVideo attachment={attachment()} />);

    const player = await screen.findByTestId("chat-details-file-video");
    const src = player.getAttribute("src") ?? "";
    expect(src).toBe("blob:video-1");
    expect(src).not.toMatch(/token|bearer|authorization|access/i);
    expect(src).not.toContain("?");
    // The bytes came from the api client, which is what sets the header.
    expect(mockFetchAttachmentContent).toHaveBeenCalledWith("a-1", expect.any(AbortSignal));
  });

  it("says a video is still being scanned and requests nothing", async () => {
    render(<AttachmentVideo attachment={attachment({ status: "pending_scan" })} />);

    expect(await screen.findByTestId("chat-details-video-pending")).toBeInTheDocument();
    expect(screen.queryByTestId("chat-details-file-video")).not.toBeInTheDocument();
    expect(mockFetchAttachmentContent).not.toHaveBeenCalled();
  });

  it("draws no player for a rejected video and requests nothing", () => {
    render(<AttachmentVideo attachment={attachment({ status: "rejected" })} />);

    expect(screen.queryByTestId("chat-details-file-video")).not.toBeInTheDocument();
    expect(screen.queryByTestId("chat-details-video-pending")).not.toBeInTheDocument();
    expect(mockFetchAttachmentContent).not.toHaveBeenCalled();
  });

  it("draws nothing at all for a file that is not a video", () => {
    const { container } = render(
      <AttachmentVideo attachment={attachment({ contentType: "image/png" })} />,
    );

    expect(container).toBeEmptyDOMElement();
    expect(mockFetchAttachmentContent).not.toHaveBeenCalled();
  });

  it("draws no player for a video past the size cap and requests nothing", () => {
    render(<AttachmentVideo attachment={attachment({ size: MAX_INLINE_VIDEO_BYTES + 1 })} />);

    expect(screen.queryByTestId("chat-details-file-video")).not.toBeInTheDocument();
    expect(mockFetchAttachmentContent).not.toHaveBeenCalled();
  });

  it("shows a loading state before the bytes arrive", () => {
    mockFetchAttachmentContent.mockReturnValue(new Promise(() => {}));
    render(<AttachmentVideo attachment={attachment()} />);

    expect(screen.getByTestId("chat-details-video-loading")).toBeInTheDocument();
  });

  it("falls back to a message when the content cannot be fetched", async () => {
    mockFetchAttachmentContent.mockRejectedValue(new Error("403"));
    render(<AttachmentVideo attachment={attachment()} />);

    expect(await screen.findByTestId("chat-details-video-error")).toBeInTheDocument();
    expect(screen.queryByTestId("chat-details-file-video")).not.toBeInTheDocument();
  });

  it("revokes the object URL when it unmounts", async () => {
    const { unmount } = render(<AttachmentVideo attachment={attachment()} />);
    await screen.findByTestId("chat-details-file-video");

    unmount();

    expect(revokeObjectURL).toHaveBeenCalledWith("blob:video-1");
  });

  it("revokes the previous URL when the attachment changes", async () => {
    const { rerender } = render(<AttachmentVideo attachment={attachment()} />);
    await screen.findByTestId("chat-details-file-video");

    rerender(<AttachmentVideo attachment={attachment({ id: "a-2", filename: "outro.mp4" })} />);

    await waitFor(() => expect(revokeObjectURL).toHaveBeenCalledWith("blob:video-1"));
    expect(await screen.findByTestId("chat-details-file-video")).toHaveAttribute(
      "src",
      "blob:video-2",
    );
  });

  it("revokes the URL when a cleared video is later rejected", async () => {
    const { rerender } = render(<AttachmentVideo attachment={attachment()} />);
    await screen.findByTestId("chat-details-file-video");

    rerender(<AttachmentVideo attachment={attachment({ status: "rejected" })} />);

    await waitFor(() => expect(revokeObjectURL).toHaveBeenCalledWith("blob:video-1"));
    expect(screen.queryByTestId("chat-details-file-video")).not.toBeInTheDocument();
  });

  it("does not refetch when an unrelated rerender happens", async () => {
    const { rerender } = render(<AttachmentVideo attachment={attachment()} />);
    await screen.findByTestId("chat-details-file-video");

    rerender(<AttachmentVideo attachment={attachment()} />);

    await waitFor(() => expect(mockFetchAttachmentContent).toHaveBeenCalledTimes(1));
  });

  it("aborts a request still in flight when it unmounts", () => {
    let signal: AbortSignal | undefined;
    mockFetchAttachmentContent.mockImplementation((_id: string, s: AbortSignal) => {
      signal = s;
      return new Promise(() => {});
    });
    const { unmount } = render(<AttachmentVideo attachment={attachment()} />);

    unmount();

    expect(signal?.aborted).toBe(true);
  });
});
