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
 *
 * Since issue #675 a fourth property joins them, and it is the reason the
 * others moved: nothing is fetched until someone presses Play. A timeline of
 * clips costs a poster each, and bytes are spent only on the one asked for.
 */

import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { ReactElement } from "react";

import AttachmentVideo from "./AttachmentVideo";
import { AttachmentHydrationContext, type AttachmentGate } from "./lazyAttachment";
import { MAX_INLINE_VIDEO_BYTES, canPlayInline } from "./attachmentVideoRules";
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

/**
 * Renders and presses Play, which is what every assertion about an actual
 * player now needs: the card starts as a poster and spends nothing.
 */
async function renderPlaying(node: ReactElement) {
  const user = userEvent.setup();
  const result = render(node);
  await user.click(screen.getByTestId("chat-message-attachment-video-play-a-1"));
  return result;
}

describe("AttachmentVideo before Play (issue #675)", () => {
  it("draws a poster with a Play control and fetches nothing", () => {
    render(<AttachmentVideo attachment={attachment()} />);

    expect(screen.getByRole("button", { name: /Reproduzir v/ })).toBeInTheDocument();
    expect(screen.queryByTestId("chat-details-file-video")).not.toBeInTheDocument();
    expect(mockFetchAttachmentContent).not.toHaveBeenCalled();
  });

  it("keeps fetching nothing for a whole history of clips until one is asked for", () => {
    render(
      <>
        <AttachmentVideo attachment={attachment({ id: "a-1" })} />
        <AttachmentVideo attachment={attachment({ id: "a-2" })} />
        <AttachmentVideo attachment={attachment({ id: "a-3" })} />
      </>,
    );

    expect(screen.getAllByRole("button", { name: /Reproduzir v/ })).toHaveLength(3);
    expect(mockFetchAttachmentContent).not.toHaveBeenCalled();
  });
});

describe("AttachmentVideo", () => {
  it("renders a native player with controls once Play is pressed", async () => {
    await renderPlaying(<AttachmentVideo attachment={attachment()} />);

    const player = await screen.findByTestId("chat-details-file-video");
    expect(player.tagName).toBe("VIDEO");
    expect(player).toHaveAttribute("controls");
    // autoPlay is safe here and only here: the click above is the user gesture
    // browsers require, and it is exactly what the reader just asked for.
    expect(player).toHaveAttribute("autoplay");
    expect(player).toHaveAttribute("aria-label", "Vídeo: reunião.mp4");
  });

  it("plays from an object URL and never from one carrying a credential", async () => {
    await renderPlaying(<AttachmentVideo attachment={attachment()} />);

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

  it("shows a loading state before the bytes arrive", async () => {
    mockFetchAttachmentContent.mockReturnValue(new Promise(() => {}));
    await renderPlaying(<AttachmentVideo attachment={attachment()} />);

    expect(screen.getByTestId("chat-details-video-loading")).toBeInTheDocument();
  });

  it("falls back to a message when the content cannot be fetched", async () => {
    mockFetchAttachmentContent.mockRejectedValue(new Error("403"));
    await renderPlaying(<AttachmentVideo attachment={attachment()} />);

    expect(await screen.findByTestId("chat-details-video-error")).toBeInTheDocument();
    expect(screen.queryByTestId("chat-details-file-video")).not.toBeInTheDocument();
  });

  it("revokes the object URL when it unmounts", async () => {
    const { unmount } = await renderPlaying(<AttachmentVideo attachment={attachment()} />);
    await screen.findByTestId("chat-details-file-video");

    unmount();

    expect(revokeObjectURL).toHaveBeenCalledWith("blob:video-1");
  });

  it("revokes the previous URL when the attachment changes", async () => {
    const { rerender } = await renderPlaying(<AttachmentVideo attachment={attachment()} />);
    await screen.findByTestId("chat-details-file-video");

    rerender(<AttachmentVideo attachment={attachment({ id: "a-2", filename: "outro.mp4" })} />);

    await waitFor(() => expect(revokeObjectURL).toHaveBeenCalledWith("blob:video-1"));
    expect(await screen.findByTestId("chat-details-file-video")).toHaveAttribute(
      "src",
      "blob:video-2",
    );
  });

  it("revokes the URL when a cleared video is later rejected", async () => {
    const { rerender } = await renderPlaying(<AttachmentVideo attachment={attachment()} />);
    await screen.findByTestId("chat-details-file-video");

    rerender(<AttachmentVideo attachment={attachment({ status: "rejected" })} />);

    await waitFor(() => expect(revokeObjectURL).toHaveBeenCalledWith("blob:video-1"));
    expect(screen.queryByTestId("chat-details-file-video")).not.toBeInTheDocument();
  });

  it("does not refetch when an unrelated rerender happens", async () => {
    const { rerender } = await renderPlaying(<AttachmentVideo attachment={attachment()} />);
    await screen.findByTestId("chat-details-file-video");

    rerender(<AttachmentVideo attachment={attachment()} />);

    await waitFor(() => expect(mockFetchAttachmentContent).toHaveBeenCalledTimes(1));
  });

  it("aborts a request still in flight when it unmounts", async () => {
    let signal: AbortSignal | undefined;
    mockFetchAttachmentContent.mockImplementation((_id: string, s: AbortSignal) => {
      signal = s;
      return new Promise(() => {});
    });
    const { unmount } = await renderPlaying(<AttachmentVideo attachment={attachment()} />);

    unmount();

    expect(signal?.aborted).toBe(true);
  });
});

/**
 * Playback against proximity (issue #675).
 *
 * The bytes arriving is not the end of the cost: a clip left playing behind a
 * reader who scrolled away keeps decoding, and keeps making noise. Pausing is
 * the whole of the contract — it is never resumed on its own, and proximity
 * never spends a second request for bytes that already arrived.
 *
 * jsdom implements none of the media element, so the parts that decide this —
 * `paused`, `play`, `pause`, `currentTime` — are played here explicitly. Every
 * assertion below reads one of them.
 */
describe("AttachmentVideo playback against proximity (issue #675)", () => {
  const VISIBLE: AttachmentGate = { proximity: "visible", active: true, priority: 0 };
  const NEAR: AttachmentGate = { proximity: "near", active: true, priority: 1 };
  const FAR: AttachmentGate = { proximity: "far", active: false, priority: 1 };

  let paused = true;
  let currentTime = 0;
  const play = vi.fn();
  const pause = vi.fn();
  const originalPaused = Object.getOwnPropertyDescriptor(HTMLMediaElement.prototype, "paused");
  const originalTime = Object.getOwnPropertyDescriptor(HTMLMediaElement.prototype, "currentTime");

  beforeEach(() => {
    paused = true;
    currentTime = 0;
    play.mockReset().mockImplementation(() => {
      paused = false;
      return Promise.resolve();
    });
    pause.mockReset().mockImplementation(() => {
      // The real element keeps currentTime across a pause, which is the whole
      // reason this is a pause and not a teardown.
      paused = true;
    });
    Object.defineProperty(HTMLMediaElement.prototype, "paused", {
      configurable: true,
      get: () => paused,
    });
    Object.defineProperty(HTMLMediaElement.prototype, "currentTime", {
      configurable: true,
      get: () => currentTime,
      set: (value: number) => {
        currentTime = value;
      },
    });
    HTMLMediaElement.prototype.play = play as unknown as HTMLMediaElement["play"];
    HTMLMediaElement.prototype.pause = pause as unknown as HTMLMediaElement["pause"];
  });

  afterEach(() => {
    if (originalPaused) Object.defineProperty(HTMLMediaElement.prototype, "paused", originalPaused);
    if (originalTime)
      Object.defineProperty(HTMLMediaElement.prototype, "currentTime", originalTime);
  });

  const card = (gate: AttachmentGate) => (
    <AttachmentHydrationContext.Provider value={gate}>
      <AttachmentVideo attachment={attachment()} />
    </AttachmentHydrationContext.Provider>
  );

  /** Presses Play, waits for the bytes, and starts playback at 12 seconds in. */
  async function playingAt12Seconds() {
    const user = userEvent.setup();
    const view = render(card(VISIBLE));
    await user.click(screen.getByTestId("chat-message-attachment-video-play-a-1"));
    const player = (await screen.findByTestId("chat-details-file-video")) as HTMLVideoElement;
    // The element's own autoplay, played by hand: jsdom honours no attribute.
    await player.play();
    player.currentTime = 12;
    expect(player.paused).toBe(false);
    return { view, player };
  }

  it("pauses when the card leaves the viewport, keeping its position", async () => {
    const { view, player } = await playingAt12Seconds();
    const fetchesWhilePlaying = mockFetchAttachmentContent.mock.calls.length;

    view.rerender(card(NEAR));

    expect(pause).toHaveBeenCalledTimes(1);
    expect(player.paused).toBe(true);
    expect(player.currentTime).toBe(12);
    // Still the same bytes, still on screen: proximity stops work, never results.
    expect(player).toHaveAttribute("src", "blob:video-1");
    expect(mockFetchAttachmentContent).toHaveBeenCalledTimes(fetchesWhilePlaying);
  });

  it("does not start playing again when the card comes back into view", async () => {
    const { view, player } = await playingAt12Seconds();
    view.rerender(card(NEAR));
    play.mockClear();

    view.rerender(card(VISIBLE));

    // The reader pressed Play several screens ago; sound resuming on its own
    // as a row scrolls past would be a surprise, and the controls are there.
    expect(play).not.toHaveBeenCalled();
    expect(player.paused).toBe(true);
    expect(player.currentTime).toBe(12);
    expect(mockFetchAttachmentContent).toHaveBeenCalledTimes(1);
  });

  it("pauses for a card that goes far away, and keeps the bytes it has", async () => {
    const { view, player } = await playingAt12Seconds();

    view.rerender(card(FAR));

    expect(pause).toHaveBeenCalledTimes(1);
    expect(player.paused).toBe(true);
    expect(player.currentTime).toBe(12);
    // Far is not a reason to throw away eight megabytes that already arrived —
    // the row is one scroll tick from being useful again.
    expect(player).toHaveAttribute("src", "blob:video-1");
    expect(revokeObjectURL).not.toHaveBeenCalled();
    expect(mockFetchAttachmentContent).toHaveBeenCalledTimes(1);
  });

  it("leaves an already-paused card alone", async () => {
    const { view } = await playingAt12Seconds();
    const player = screen.getByTestId("chat-details-file-video") as HTMLVideoElement;
    player.pause();
    pause.mockClear();

    view.rerender(card(FAR));

    expect(pause).not.toHaveBeenCalled();
  });

  it("still revokes the object URL when the card unmounts", async () => {
    const { view } = await playingAt12Seconds();
    view.rerender(card(FAR));

    view.unmount();

    expect(revokeObjectURL).toHaveBeenCalledWith("blob:video-1");
  });
});
