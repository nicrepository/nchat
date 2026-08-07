/**
 * AttachmentThumbnail tests (RF-31, issue #464).
 *
 * Two things are under test and they matter for different reasons: which
 * attachments are allowed to spend a request at all, and whether every object
 * URL this component creates is revoked. The first is a privacy and
 * correctness property — an attachment the scan has not cleared must not even
 * be asked for — and the second is a leak that would survive every rerender for
 * as long as the tab is open.
 */

import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import AttachmentThumbnail from "./AttachmentThumbnail";
import type { AttachmentPreviewStatus, AttachmentStatus, ChannelAttachment } from "./chatTypes";

const mockFetchAttachmentPreview = vi.hoisted(() => vi.fn());
vi.mock("./filesApi", () => ({
  fetchAttachmentPreview: mockFetchAttachmentPreview,
}));

const createObjectURL = vi.fn();
const revokeObjectURL = vi.fn();

function attachment(overrides: Partial<ChannelAttachment> = {}): ChannelAttachment {
  return {
    id: "a-1",
    filename: "diagrama.png",
    contentType: "image/png",
    size: 2048,
    status: "clean",
    previewStatus: "ready",
    createdAt: "2026-07-15T12:00:00.000Z",
    ...overrides,
  };
}

function icon() {
  return <span data-testid="file-icon">icon</span>;
}

beforeEach(() => {
  mockFetchAttachmentPreview.mockReset();
  createObjectURL.mockReset();
  revokeObjectURL.mockReset();
  let created = 0;
  createObjectURL.mockImplementation(() => `blob:preview-${++created}`);
  // jsdom implements neither, and they are precisely what has to be asserted.
  vi.stubGlobal("URL", {
    ...URL,
    createObjectURL,
    revokeObjectURL,
  });
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("AttachmentThumbnail", () => {
  it("fetches and renders the thumbnail of a clean, ready attachment", async () => {
    mockFetchAttachmentPreview.mockResolvedValue(new Blob(["jpeg-bytes"]));

    render(<AttachmentThumbnail attachment={attachment()} fallback={icon()} />);

    const image = await screen.findByTestId("chat-details-file-thumb");
    expect(image).toHaveAttribute("src", "blob:preview-1");
    // The filename reaches the DOM as text, never as markup or as a URL.
    expect(image).toHaveAttribute("alt", "Pré-visualização de diagrama.png");
    expect(mockFetchAttachmentPreview).toHaveBeenCalledTimes(1);
    expect(mockFetchAttachmentPreview).toHaveBeenCalledWith("a-1", expect.any(AbortSignal));
    expect(screen.queryByTestId("file-icon")).not.toBeInTheDocument();
  });

  // The preview route answers 409 for all three of these, so asking would spend
  // a request to be told what the metadata already said.
  it.each<AttachmentPreviewStatus>(["pending", "failed", "unsupported"])(
    "does not request a preview while previewStatus is %s",
    async (previewStatus) => {
      render(<AttachmentThumbnail attachment={attachment({ previewStatus })} fallback={icon()} />);

      expect(await screen.findByTestId("file-icon")).toBeInTheDocument();
      expect(mockFetchAttachmentPreview).not.toHaveBeenCalled();
    },
  );

  // The scan gate is the server's, and it applies to the preview exactly as it
  // applies to the download. The client must not probe around it.
  it.each<AttachmentStatus>(["pending_scan", "rejected"])(
    "does not request a preview while the scan status is %s",
    async (status) => {
      render(<AttachmentThumbnail attachment={attachment({ status })} fallback={icon()} />);

      expect(await screen.findByTestId("file-icon")).toBeInTheDocument();
      expect(mockFetchAttachmentPreview).not.toHaveBeenCalled();
    },
  );

  it("keeps the icon when the request fails, without retrying", async () => {
    mockFetchAttachmentPreview.mockRejectedValue(new Error("409"));

    const { rerender } = render(
      <AttachmentThumbnail attachment={attachment()} fallback={icon()} />,
    );

    expect(await screen.findByTestId("file-icon")).toBeInTheDocument();
    expect(createObjectURL).not.toHaveBeenCalled();

    // A rerender is not a reason to ask again: a 409 answers the same way every
    // time, and retrying on render would be an unbounded loop.
    rerender(<AttachmentThumbnail attachment={attachment()} fallback={icon()} />);
    await waitFor(() => expect(mockFetchAttachmentPreview).toHaveBeenCalledTimes(1));
  });

  it("falls back to the icon and revokes the URL when the image cannot be decoded", async () => {
    mockFetchAttachmentPreview.mockResolvedValue(new Blob(["not an image"]));

    render(<AttachmentThumbnail attachment={attachment()} fallback={icon()} />);
    const image = await screen.findByTestId("chat-details-file-thumb");

    fireEvent.error(image);

    expect(await screen.findByTestId("file-icon")).toBeInTheDocument();
    expect(revokeObjectURL).toHaveBeenCalledWith("blob:preview-1");
  });

  it("revokes the previous URL when the attachment changes", async () => {
    mockFetchAttachmentPreview.mockResolvedValue(new Blob(["jpeg-bytes"]));

    const { rerender } = render(
      <AttachmentThumbnail attachment={attachment()} fallback={icon()} />,
    );
    await screen.findByTestId("chat-details-file-thumb");

    rerender(<AttachmentThumbnail attachment={attachment({ id: "a-2" })} fallback={icon()} />);

    await waitFor(() => expect(revokeObjectURL).toHaveBeenCalledWith("blob:preview-1"));
    await waitFor(() =>
      expect(mockFetchAttachmentPreview).toHaveBeenLastCalledWith("a-2", expect.any(AbortSignal)),
    );
  });

  it("revokes the URL on unmount", async () => {
    mockFetchAttachmentPreview.mockResolvedValue(new Blob(["jpeg-bytes"]));

    const { unmount } = render(<AttachmentThumbnail attachment={attachment()} fallback={icon()} />);
    await screen.findByTestId("chat-details-file-thumb");

    unmount();

    expect(revokeObjectURL).toHaveBeenCalledWith("blob:preview-1");
  });

  // The response of an attachment nobody is looking at any more must never
  // become the image on screen, however late it arrives.
  it("ignores a response that arrives after the attachment changed", async () => {
    let resolveFirst: (blob: Blob) => void = () => {};
    mockFetchAttachmentPreview.mockImplementationOnce(
      () => new Promise<Blob>((resolve) => (resolveFirst = resolve)),
    );
    mockFetchAttachmentPreview.mockResolvedValue(new Blob(["second"]));

    const { rerender } = render(
      <AttachmentThumbnail attachment={attachment()} fallback={icon()} />,
    );
    rerender(<AttachmentThumbnail attachment={attachment({ id: "a-2" })} fallback={icon()} />);

    const image = await screen.findByTestId("chat-details-file-thumb");
    const shownAfterSwitch = image.getAttribute("src");

    // The first request now answers, for an attachment that is no longer shown.
    resolveFirst(new Blob(["first"]));

    await waitFor(() =>
      expect(screen.getByTestId("chat-details-file-thumb")).toHaveAttribute(
        "src",
        shownAfterSwitch,
      ),
    );
    // The stale blob never became a URL at all, so there is nothing to revoke.
    expect(createObjectURL).toHaveBeenCalledTimes(1);
  });

  // ── Losing eligibility after the bytes are already on screen ──────────────
  //
  // A rescan can condemn a file that already has a preview. The bytes are in
  // the page by then and the server can no longer take them back — revoking is
  // the client's half of that, and it has to happen the moment the metadata
  // says the attachment is no longer servable.

  it.each<Partial<ChannelAttachment>>([
    { status: "rejected" },
    { status: "rejected", previewStatus: "unsupported" },
    { status: "pending_scan" },
    { previewStatus: "unsupported" },
    { previewStatus: "failed" },
    { previewStatus: "pending" },
  ])("drops a loaded thumbnail when the attachment becomes %o", async (revoked) => {
    mockFetchAttachmentPreview.mockResolvedValue(new Blob(["jpeg-bytes"]));

    const { rerender } = render(
      <AttachmentThumbnail attachment={attachment()} fallback={icon()} />,
    );
    await screen.findByTestId("chat-details-file-thumb");
    expect(createObjectURL).toHaveBeenCalledTimes(1);

    rerender(<AttachmentThumbnail attachment={attachment(revoked)} fallback={icon()} />);

    // Gone from the DOM, revoked, and replaced by the fallback.
    expect(screen.queryByTestId("chat-details-file-thumb")).not.toBeInTheDocument();
    expect(await screen.findByTestId("file-icon")).toBeInTheDocument();
    expect(revokeObjectURL).toHaveBeenCalledWith("blob:preview-1");
    // No second reading of the endpoint, and no new URL: losing eligibility is
    // not a reason to ask again.
    expect(mockFetchAttachmentPreview).toHaveBeenCalledTimes(1);
    expect(createObjectURL).toHaveBeenCalledTimes(1);
  });

  // `previewStatus: "ready"` alone never authorises anything. The backend keeps
  // a ready preview under a rejected attachment on purpose (retention), and the
  // client must read the pair, not the half it likes.
  it("never requests the bytes of a rejected attachment that still reads ready", async () => {
    render(
      <AttachmentThumbnail
        attachment={attachment({ status: "rejected", previewStatus: "ready" })}
        fallback={icon()}
      />,
    );

    expect(await screen.findByTestId("file-icon")).toBeInTheDocument();
    expect(mockFetchAttachmentPreview).not.toHaveBeenCalled();
    expect(createObjectURL).not.toHaveBeenCalled();
  });

  // The race the rescan opens: the request was authorised when it left, and the
  // verdict landed while it was in flight. The answer describes a file the user
  // may no longer see, so it must not become an image — and must not leave a
  // URL behind either.
  it("discards a response that arrives after the attachment was rejected", async () => {
    let resolvePreview: (blob: Blob) => void = () => {};
    mockFetchAttachmentPreview.mockImplementation(
      () => new Promise<Blob>((resolve) => (resolvePreview = resolve)),
    );

    const { rerender } = render(
      <AttachmentThumbnail attachment={attachment()} fallback={icon()} />,
    );
    await waitFor(() => expect(mockFetchAttachmentPreview).toHaveBeenCalledTimes(1));

    // The rescan verdict reaches the panel while the bytes are still in flight.
    rerender(
      <AttachmentThumbnail
        attachment={attachment({ status: "rejected", previewStatus: "unsupported" })}
        fallback={icon()}
      />,
    );

    resolvePreview(new Blob(["jpeg-bytes"]));
    await waitFor(() => expect(screen.getByTestId("file-icon")).toBeInTheDocument());

    expect(screen.queryByTestId("chat-details-file-thumb")).not.toBeInTheDocument();
    // The blob never became a URL, so there is nothing left to revoke and
    // nothing that could be rendered by a later commit.
    expect(createObjectURL).not.toHaveBeenCalled();
    expect(mockFetchAttachmentPreview).toHaveBeenCalledTimes(1);
  });

  // Regaining eligibility is a real transition too — a rescan can clear a file
  // again — and it must produce a fresh reading, not a resurrected URL.
  it("re-requests the bytes only when eligibility is regained", async () => {
    mockFetchAttachmentPreview.mockResolvedValue(new Blob(["jpeg-bytes"]));

    const { rerender } = render(
      <AttachmentThumbnail attachment={attachment()} fallback={icon()} />,
    );
    await screen.findByTestId("chat-details-file-thumb");

    rerender(
      <AttachmentThumbnail attachment={attachment({ status: "pending_scan" })} fallback={icon()} />,
    );
    expect(await screen.findByTestId("file-icon")).toBeInTheDocument();
    expect(mockFetchAttachmentPreview).toHaveBeenCalledTimes(1);

    rerender(<AttachmentThumbnail attachment={attachment()} fallback={icon()} />);

    const image = await screen.findByTestId("chat-details-file-thumb");
    expect(mockFetchAttachmentPreview).toHaveBeenCalledTimes(2);
    // A new URL, not the revoked one.
    expect(image).toHaveAttribute("src", "blob:preview-2");
  });

  it("does not repeat the request across ordinary rerenders", async () => {
    mockFetchAttachmentPreview.mockResolvedValue(new Blob(["jpeg-bytes"]));

    const file = attachment();
    const { rerender } = render(<AttachmentThumbnail attachment={file} fallback={icon()} />);
    await screen.findByTestId("chat-details-file-thumb");

    // A new object with the same values is what a parent rerender produces.
    rerender(<AttachmentThumbnail attachment={{ ...file }} fallback={icon()} />);
    rerender(<AttachmentThumbnail attachment={{ ...file }} fallback={icon()} />);

    await waitFor(() => expect(mockFetchAttachmentPreview).toHaveBeenCalledTimes(1));
    expect(createObjectURL).toHaveBeenCalledTimes(1);
    expect(revokeObjectURL).not.toHaveBeenCalled();
  });
});
