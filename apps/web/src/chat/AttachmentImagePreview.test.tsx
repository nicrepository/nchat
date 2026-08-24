/**
 * AttachmentImagePreview tests (issue #491).
 *
 * Three things are under test:
 *
 *  - which bytes get fetched for which raster type, and why never both at
 *    once for the same render (server preview vs. original — see
 *    attachmentImageRules and the component's own module comment);
 *  - that reduced motion never lets a GIF animate without an explicit,
 *    user-triggered "Reproduzir animação";
 *  - that the trigger opens the caller's lightbox and never the download
 *    path, with an accessible name naming the file.
 */

import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import AttachmentImagePreview from "./AttachmentImagePreview";
import { MAX_INLINE_ORIGINAL_IMAGE_BYTES } from "./attachmentImageRules";
import type { ChannelAttachment } from "./chatTypes";

const { mockPreview, mockContent } = vi.hoisted(() => ({
  mockPreview: vi.fn(),
  mockContent: vi.fn(),
}));
vi.mock("./filesApi", () => ({
  fetchAttachmentPreview: (...args: unknown[]) => mockPreview(...args),
  fetchAttachmentContent: (...args: unknown[]) => mockContent(...args),
}));

const createObjectURL = vi.fn();
const revokeObjectURL = vi.fn();

function attachment(overrides: Partial<ChannelAttachment> = {}): ChannelAttachment {
  return {
    id: "img-1",
    filename: "paisagem.png",
    contentType: "image/png",
    size: 2048,
    status: "clean",
    previewStatus: "ready",
    createdAt: "2026-07-15T12:00:00.000Z",
    ...overrides,
  };
}

function stubMatchMedia(reduced: boolean) {
  window.matchMedia = ((query: string) =>
    ({
      matches: query.includes("reduce") ? reduced : false,
      media: query,
      addEventListener: () => {},
      removeEventListener: () => {},
    }) as unknown as MediaQueryList) as typeof window.matchMedia;
}

const onOpen = vi.fn();
const fallback = <span data-testid="fallback-icon">icon</span>;

beforeEach(() => {
  mockPreview.mockReset().mockResolvedValue(new Blob(["preview-bytes"]));
  mockContent.mockReset().mockResolvedValue(new Blob(["original-bytes"]));
  onOpen.mockReset();
  createObjectURL.mockReset();
  revokeObjectURL.mockReset();
  let created = 0;
  createObjectURL.mockImplementation(() => `blob:img-${++created}`);
  vi.stubGlobal("URL", { ...URL, createObjectURL, revokeObjectURL });
  stubMatchMedia(false);
});

afterEach(() => {
  vi.unstubAllGlobals();
  // @ts-expect-error -- restore jsdom's absence of matchMedia between tests.
  delete window.matchMedia;
});

describe("scan gating", () => {
  it("shows an analysis note and fetches nothing for a file still being scanned", () => {
    render(
      <AttachmentImagePreview
        attachment={attachment({ status: "pending_scan" })}
        fallback={fallback}
        onOpen={onOpen}
      />,
    );

    expect(screen.getByTestId("chat-message-attachment-image-pending-img-1")).toBeInTheDocument();
    expect(mockPreview).not.toHaveBeenCalled();
    expect(mockContent).not.toHaveBeenCalled();
  });

  it("draws nothing and fetches nothing for a rejected file", () => {
    const { container } = render(
      <AttachmentImagePreview
        attachment={attachment({ status: "rejected" })}
        fallback={fallback}
        onOpen={onOpen}
      />,
    );

    expect(container).toBeEmptyDOMElement();
    expect(mockPreview).not.toHaveBeenCalled();
    expect(mockContent).not.toHaveBeenCalled();
  });
});

describe("PNG/JPEG", () => {
  it("fetches only the server preview, never the original, once ready", async () => {
    render(
      <AttachmentImagePreview attachment={attachment()} fallback={fallback} onOpen={onOpen} />,
    );

    const trigger = await screen.findByTestId("chat-message-attachment-image-img-1");
    expect(trigger.querySelector("img")).toHaveAttribute("src", "blob:img-1");
    expect(mockPreview).toHaveBeenCalledTimes(1);
    expect(mockContent).not.toHaveBeenCalled();
  });

  /**
   * The fix for the reported bug: nothing pushes a preview-worker completion
   * into an already-rendered message the way a scan verdict is pushed (see
   * AttachmentImagePreview's own module comment), so a PNG/JPEG whose
   * `previewStatus` is stuck at "pending" — the normal state for a message
   * just sent in this session — must not wait on it forever. It falls back to
   * the original instead, the same mechanism GIF/WebP already use.
   */
  it("falls back to the original, not an indefinite skeleton, when the preview never becomes ready", async () => {
    render(
      <AttachmentImagePreview
        attachment={attachment({ previewStatus: "pending" })}
        fallback={fallback}
        onOpen={onOpen}
      />,
    );

    const trigger = await screen.findByTestId("chat-message-attachment-image-img-1");
    expect(trigger.querySelector("img")).toHaveAttribute("src", "blob:img-1");
    expect(mockContent).toHaveBeenCalledTimes(1);
    expect(mockPreview).not.toHaveBeenCalled();
  });

  it("shows a skeleton, never the fallback icon, while that fallback original is in flight", () => {
    mockContent.mockReturnValue(new Promise(() => {}));
    render(
      <AttachmentImagePreview
        attachment={attachment({ previewStatus: "pending" })}
        fallback={fallback}
        onOpen={onOpen}
      />,
    );

    expect(screen.getByTestId("chat-message-attachment-image-loading-img-1")).toBeInTheDocument();
    expect(screen.queryByTestId("fallback-icon")).not.toBeInTheDocument();
  });

  it("falls back to the original, the same way, when the server marks the preview unsupported or failed", async () => {
    render(
      <AttachmentImagePreview
        attachment={attachment({ previewStatus: "unsupported" })}
        fallback={fallback}
        onOpen={onOpen}
      />,
    );

    const trigger = await screen.findByTestId("chat-message-attachment-image-img-1");
    expect(trigger.querySelector("img")).toHaveAttribute("src", "blob:img-1");
    expect(mockContent).toHaveBeenCalledTimes(1);
    expect(mockPreview).not.toHaveBeenCalled();
  });

  it("falls back to the icon, never a broken-image glyph, only once the fallback original itself fails", async () => {
    mockContent.mockRejectedValue(new Error("403"));
    render(
      <AttachmentImagePreview
        attachment={attachment({ previewStatus: "unsupported" })}
        fallback={fallback}
        onOpen={onOpen}
      />,
    );

    expect(await screen.findByTestId("fallback-icon")).toBeInTheDocument();
    expect(screen.queryByRole("img")).not.toBeInTheDocument();
  });

  it("keeps waiting on the skeleton, not the original, past the size cap with no ready preview", () => {
    render(
      <AttachmentImagePreview
        attachment={attachment({
          previewStatus: "pending",
          size: MAX_INLINE_ORIGINAL_IMAGE_BYTES + 1,
        })}
        fallback={fallback}
        onOpen={onOpen}
      />,
    );

    expect(screen.getByTestId("chat-message-attachment-image-loading-img-1")).toBeInTheDocument();
    expect(mockContent).not.toHaveBeenCalled();
  });

  it("falls back to the icon when the preview fails to decode", async () => {
    mockPreview.mockResolvedValue(new Blob(["not-an-image"]));
    render(
      <AttachmentImagePreview attachment={attachment()} fallback={fallback} onOpen={onOpen} />,
    );

    const trigger = await screen.findByTestId("chat-message-attachment-image-img-1");
    const img = trigger.querySelector("img") as HTMLImageElement;
    img.dispatchEvent(new Event("error"));

    expect(await screen.findByTestId("fallback-icon")).toBeInTheDocument();
  });

  it("has an accessible name naming the file and opens the lightbox without downloading", async () => {
    const user = userEvent.setup();
    render(
      <AttachmentImagePreview attachment={attachment()} fallback={fallback} onOpen={onOpen} />,
    );

    const trigger = await screen.findByRole("button", { name: "Ampliar paisagem.png" });
    await user.click(trigger);

    expect(onOpen).toHaveBeenCalledWith({
      trigger,
      url: "blob:img-1",
      isOriginal: false,
    });
    expect(mockContent).not.toHaveBeenCalled();
  });

  it("opens on Enter and on Space, exactly like any native button", async () => {
    const user = userEvent.setup();
    render(
      <AttachmentImagePreview attachment={attachment()} fallback={fallback} onOpen={onOpen} />,
    );

    const trigger = await screen.findByRole("button", { name: "Ampliar paisagem.png" });
    trigger.focus();
    await user.keyboard("{Enter}");
    expect(onOpen).toHaveBeenCalledTimes(1);

    await user.keyboard(" ");
    expect(onOpen).toHaveBeenCalledTimes(2);
  });
});

describe("WebP", () => {
  it("fetches the original directly, since there is no server preview for WebP", async () => {
    render(
      <AttachmentImagePreview
        attachment={attachment({ contentType: "image/webp", previewStatus: "unsupported" })}
        fallback={fallback}
        onOpen={onOpen}
      />,
    );

    const trigger = await screen.findByTestId("chat-message-attachment-image-img-1");
    expect(trigger.querySelector("img")).toHaveAttribute("src", "blob:img-1");
    expect(mockContent).toHaveBeenCalledTimes(1);
    expect(mockPreview).not.toHaveBeenCalled();
  });

  it("falls back to the icon, with no fetch at all, past the size cap", () => {
    render(
      <AttachmentImagePreview
        attachment={attachment({
          contentType: "image/webp",
          previewStatus: "unsupported",
          size: MAX_INLINE_ORIGINAL_IMAGE_BYTES + 1,
        })}
        fallback={fallback}
        onOpen={onOpen}
      />,
    );

    expect(screen.getByTestId("fallback-icon")).toBeInTheDocument();
    expect(mockContent).not.toHaveBeenCalled();
    expect(mockPreview).not.toHaveBeenCalled();
  });
});

describe("GIF, motion allowed", () => {
  function gif(overrides: Partial<ChannelAttachment> = {}) {
    return attachment({ contentType: "image/gif", ...overrides });
  }

  it("fetches the original to animate it, never the static server preview", async () => {
    render(<AttachmentImagePreview attachment={gif()} fallback={fallback} onOpen={onOpen} />);

    const trigger = await screen.findByTestId("chat-message-attachment-image-img-1");
    expect(trigger.querySelector("img")).toHaveAttribute("src", "blob:img-1");
    expect(mockContent).toHaveBeenCalledTimes(1);
    expect(mockPreview).not.toHaveBeenCalled();
  });

  it("falls back to the static preview when the GIF is too large to animate", async () => {
    render(
      <AttachmentImagePreview
        attachment={gif({ size: MAX_INLINE_ORIGINAL_IMAGE_BYTES + 1 })}
        fallback={fallback}
        onOpen={onOpen}
      />,
    );

    await waitFor(() => expect(mockPreview).toHaveBeenCalledTimes(1));
    expect(mockContent).not.toHaveBeenCalled();
    // No "play" toggle: this is the size cap, not a reduced-motion choice.
    expect(
      screen.queryByTestId("chat-message-attachment-gif-toggle-img-1"),
    ).not.toBeInTheDocument();
  });
});

describe("GIF, reduced motion", () => {
  function gif(overrides: Partial<ChannelAttachment> = {}) {
    return attachment({ contentType: "image/gif", ...overrides });
  }

  beforeEach(() => stubMatchMedia(true));

  it("shows the static server preview and offers an explicit play control", async () => {
    render(<AttachmentImagePreview attachment={gif()} fallback={fallback} onOpen={onOpen} />);

    await waitFor(() => expect(mockPreview).toHaveBeenCalledTimes(1));
    expect(mockContent).not.toHaveBeenCalled();
    expect(await screen.findByRole("button", { name: "Reproduzir animação" })).toBeInTheDocument();
  });

  it("fetches the original only once the user presses play, and opens as the original after", async () => {
    const user = userEvent.setup();
    render(<AttachmentImagePreview attachment={gif()} fallback={fallback} onOpen={onOpen} />);

    const play = await screen.findByRole("button", { name: "Reproduzir animação" });
    await user.click(play);

    await waitFor(() => expect(mockContent).toHaveBeenCalledTimes(1));
    expect(screen.queryByRole("button", { name: "Reproduzir animação" })).not.toBeInTheDocument();

    const trigger = await screen.findByTestId("chat-message-attachment-image-img-1");
    await user.click(trigger);
    expect(onOpen).toHaveBeenLastCalledWith(
      expect.objectContaining({ isOriginal: true, url: "blob:img-2" }),
    );
  });

  it("falls back to the icon with no play control when the server preview is not ready", () => {
    render(
      <AttachmentImagePreview
        attachment={gif({ previewStatus: "failed" })}
        fallback={fallback}
        onOpen={onOpen}
      />,
    );

    expect(screen.getByTestId("fallback-icon")).toBeInTheDocument();
    expect(mockContent).not.toHaveBeenCalled();
    expect(screen.queryByRole("button", { name: "Reproduzir animação" })).not.toBeInTheDocument();
  });
});
