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
import { AttachmentHydrationContext, type AttachmentGate } from "./lazyAttachment";
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
  it("waits on the shell, never the original, while the preview is still being rendered", () => {
    // Issue #675: the card is not worth a full-resolution photograph. The
    // skeleton stays until the preview lands, which is a deliberate trade —
    // see the module comment on the preview-ready notification gap.
    render(
      <AttachmentImagePreview
        attachment={attachment({ previewStatus: "pending" })}
        fallback={fallback}
        onOpen={onOpen}
      />,
    );

    expect(screen.getByTestId("chat-message-attachment-image-loading-img-1")).toBeInTheDocument();
    expect(mockContent).not.toHaveBeenCalled();
  });

  it("shows the file-type icon, and fetches nothing, when there will never be a preview", () => {
    render(
      <AttachmentImagePreview
        attachment={attachment({ previewStatus: "unsupported" })}
        fallback={fallback}
        onOpen={onOpen}
      />,
    );

    expect(screen.getByTestId("fallback-icon")).toBeInTheDocument();
    expect(mockContent).not.toHaveBeenCalled();
    expect(mockPreview).not.toHaveBeenCalled();
  });

  it("falls back to the icon, never a broken-image glyph, once the preview itself fails to load", async () => {
    mockPreview.mockRejectedValue(new Error("403"));
    render(
      <AttachmentImagePreview attachment={attachment()} fallback={fallback} onOpen={onOpen} />,
    );

    expect(await screen.findByTestId("fallback-icon")).toBeInTheDocument();
    expect(screen.queryByRole("img")).not.toBeInTheDocument();
    expect(mockContent).not.toHaveBeenCalled();
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

    // The bytes, not the card's object URL (issue #675): the viewer outlives
    // this card, so it mints an address of its own.
    expect(onOpen).toHaveBeenCalledWith({
      trigger,
      blob: expect.any(Blob),
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
  it("draws the file-type icon and fetches nothing, having no derived preview to show", () => {
    // Issue #675: WebP has no server preview, and the timeline is not where an
    // original gets downloaded. Opening the card still shows the real image —
    // the viewer is what fetches it.
    render(
      <AttachmentImagePreview
        attachment={attachment({ contentType: "image/webp", previewStatus: "unsupported" })}
        fallback={fallback}
        onOpen={onOpen}
      />,
    );

    expect(screen.getByTestId("fallback-icon")).toBeInTheDocument();
    expect(mockContent).not.toHaveBeenCalled();
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
      expect.objectContaining({ isOriginal: true, blob: expect.any(Blob) }),
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

/**
 * Proximity, which is what decides whether the one heavy original this
 * component may still fetch — a GIF's animated form — is worth fetching at all
 * (issue #675).
 */
describe("proximity", () => {
  const FAR: AttachmentGate = { proximity: "far", active: false, priority: 1 };
  const NEAR: AttachmentGate = { proximity: "near", active: true, priority: 1 };
  const VISIBLE: AttachmentGate = { proximity: "visible", active: true, priority: 0 };

  function renderAt(gate: AttachmentGate, attachmentOverrides: Partial<ChannelAttachment> = {}) {
    const node = (currentGate: AttachmentGate) => (
      <AttachmentHydrationContext.Provider value={currentGate}>
        <AttachmentImagePreview
          attachment={attachment(attachmentOverrides)}
          fallback={fallback}
          onOpen={onOpen}
        />
      </AttachmentHydrationContext.Provider>
    );
    const result = render(node(gate));
    return { ...result, moveTo: (next: AttachmentGate) => result.rerender(node(next)) };
  }

  const gifType = { contentType: "image/gif" } as const;

  it("asks for nothing at all while the row is far from the scrollport", () => {
    renderAt(FAR);

    expect(mockPreview).not.toHaveBeenCalled();
    expect(mockContent).not.toHaveBeenCalled();
  });

  it("asks for nothing for a far GIF either — not even its static frame", () => {
    renderAt(FAR, gifType);

    expect(mockPreview).not.toHaveBeenCalled();
    expect(mockContent).not.toHaveBeenCalled();
  });

  it("shows a near GIF its static frame, never the animated original", async () => {
    renderAt(NEAR, gifType);

    await screen.findByTestId("chat-message-attachment-image-img-1");
    expect(mockPreview).toHaveBeenCalledTimes(1);
    expect(mockContent).not.toHaveBeenCalled();
  });

  it("animates a GIF only once it is genuinely on screen", async () => {
    renderAt(VISIBLE, gifType);

    await screen.findByTestId("chat-message-attachment-image-img-1");
    expect(mockContent).toHaveBeenCalledTimes(1);
  });

  it("returns a GIF to its static frame when it scrolls out of the viewport", async () => {
    const { moveTo } = renderAt(VISIBLE, gifType);
    const trigger = await screen.findByTestId("chat-message-attachment-image-img-1");
    expect(trigger.querySelector("img")).toHaveAttribute("src", "blob:img-1");
    expect(mockContent).toHaveBeenCalledTimes(1);

    moveTo(NEAR);

    // The animated original is dropped and its address revoked: a timeline of
    // GIFs must not keep decoding the ones nobody is looking at.
    await waitFor(() => expect(mockPreview).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(revokeObjectURL).toHaveBeenCalledWith("blob:img-1"));
  });

  it("still refuses to animate a visible GIF under reduced motion until asked", async () => {
    stubMatchMedia(true);
    renderAt(VISIBLE, gifType);

    await screen.findByTestId("chat-message-attachment-image-img-1");
    expect(mockPreview).toHaveBeenCalledTimes(1);
    expect(mockContent).not.toHaveBeenCalled();
    expect(screen.getByRole("button", { name: "Reproduzir animação" })).toBeInTheDocument();
  });

  it("keeps a static preview that already arrived when the row drifts far away", async () => {
    const { moveTo } = renderAt(VISIBLE);
    const trigger = await screen.findByTestId("chat-message-attachment-image-img-1");
    expect(trigger.querySelector("img")).toHaveAttribute("src", "blob:img-1");

    moveTo(FAR);

    // Bytes already on screen stay: revoking them would buy a flicker and a
    // second request for a row that is one scroll tick from being useful.
    expect(screen.getByTestId("chat-message-attachment-image-img-1")).toBeInTheDocument();
    expect(revokeObjectURL).not.toHaveBeenCalledWith("blob:img-1");
    expect(mockPreview).toHaveBeenCalledTimes(1);
  });

  it("abandons a preview still in flight when the row goes far, and asks again on return", async () => {
    let inFlightSignal: AbortSignal | undefined;
    mockPreview.mockImplementation((_id: string, signal: AbortSignal) => {
      inFlightSignal = signal;
      return new Promise<Blob>(() => {});
    });
    const { moveTo } = renderAt(VISIBLE);
    await waitFor(() => expect(mockPreview).toHaveBeenCalledTimes(1));

    moveTo(FAR);
    await waitFor(() => expect(inFlightSignal?.aborted).toBe(true));

    mockPreview.mockResolvedValue(new Blob(["preview-bytes"]));
    moveTo(VISIBLE);

    await waitFor(() => expect(mockPreview).toHaveBeenCalledTimes(2));
  });
});
