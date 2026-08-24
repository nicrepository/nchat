import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

/**
 * RF-32: what a message's attachment offers in each scan state.
 *
 * The rule under test is one sentence: only an approved file gets an action.
 * The gate itself is file-service's and is re-applied to every byte it serves —
 * these tests assert the UI does not *offer* what the server would refuse, which
 * is a separate obligation and the one a user actually sees.
 */

const { mockPreview, mockContent } = vi.hoisted(() => ({
  mockPreview: vi.fn(),
  mockContent: vi.fn(),
}));

vi.mock("./filesApi", () => ({
  fetchAttachmentPreview: (...args: unknown[]) => mockPreview(...args),
  fetchAttachmentContent: (...args: unknown[]) => mockContent(...args),
}));

import MessageAttachments from "./MessageAttachments";
import type { ChannelAttachment } from "./chatTypes";

function attachment(overrides: Partial<ChannelAttachment> = {}): ChannelAttachment {
  return {
    id: "att-1",
    filename: "relatorio.pdf",
    contentType: "application/pdf",
    size: 2048,
    status: "pending_scan",
    previewStatus: "pending",
    createdAt: "",
    ...overrides,
  };
}

const downloadButton = () => screen.queryByTestId("chat-message-attachment-download-att-1");

beforeEach(() => {
  mockPreview.mockReset().mockRejectedValue(new Error("no preview"));
  mockContent.mockReset().mockResolvedValue(new Blob(["bytes"]));
});

afterEach(() => {
  vi.clearAllMocks();
});

describe("message attachments", () => {
  it("renders nothing when the message carries none", () => {
    const { container } = render(<MessageAttachments attachments={undefined} />);
    expect(container).toBeEmptyDOMElement();
  });

  it("shows a file still being scanned without any way to obtain it", () => {
    render(<MessageAttachments attachments={[attachment()]} />);

    expect(screen.getByText("relatorio.pdf")).toBeInTheDocument();
    expect(screen.getByTestId("chat-message-attachment-status-att-1")).toHaveTextContent(
      "Verificando arquivo",
    );
    expect(downloadButton()).not.toBeInTheDocument();
    // Not even a request: an unapproved file's bytes are never asked for.
    expect(mockContent).not.toHaveBeenCalled();
    expect(mockPreview).not.toHaveBeenCalled();
  });

  it("offers a download once the scan approves the file", async () => {
    const user = userEvent.setup();
    // jsdom implements neither, and both are part of the download path.
    const createObjectURL = vi.fn(() => "blob:att-1");
    const revokeObjectURL = vi.fn();
    vi.stubGlobal("URL", { ...URL, createObjectURL, revokeObjectURL });

    render(<MessageAttachments attachments={[attachment({ status: "clean" })]} />);

    const button = downloadButton();
    expect(button).toBeInTheDocument();
    await user.click(button as HTMLElement);

    await waitFor(() => expect(mockContent).toHaveBeenCalledWith("att-1"));
    // The object URL does not outlive the click that created it.
    await waitFor(() => expect(revokeObjectURL).toHaveBeenCalledWith("blob:att-1"));
    vi.unstubAllGlobals();
  });

  it("says a rejected file is blocked and offers nothing", () => {
    render(<MessageAttachments attachments={[attachment({ status: "rejected" })]} />);

    expect(screen.getByTestId("chat-message-attachment-status-att-1")).toHaveTextContent(
      "Bloqueado",
    );
    expect(downloadButton()).not.toBeInTheDocument();
    expect(mockContent).not.toHaveBeenCalled();
  });

  it("requests a preview only for an approved file that has one", () => {
    const { rerender } = render(
      <MessageAttachments attachments={[attachment({ previewStatus: "ready" })]} />,
    );
    // pending_scan + ready preview: still nothing, because the scan decides.
    expect(mockPreview).not.toHaveBeenCalled();

    rerender(
      <MessageAttachments
        attachments={[attachment({ status: "clean", previewStatus: "ready" })]}
      />,
    );
    expect(mockPreview).toHaveBeenCalledWith("att-1", expect.anything());
  });

  it("never plays a video the scan has not cleared", () => {
    render(
      <MessageAttachments
        attachments={[attachment({ id: "att-1", filename: "clipe.mp4", contentType: "video/mp4" })]}
      />,
    );

    expect(screen.queryByTestId("chat-details-file-video")).not.toBeInTheDocument();
    expect(mockContent).not.toHaveBeenCalled();
  });
});

/**
 * The image branch (issue #491): a large preview instead of the 32px
 * thumbnail, a lightbox that opens without downloading, and focus returning
 * to the real trigger button on close — the one piece AttachmentLightbox
 * itself deliberately does not own (see MessageAttachments.tsx's own
 * comment). Per-state and per-format behaviour is AttachmentImagePreview's
 * and AttachmentLightbox's own suites; this only checks the wiring.
 */
describe("message attachments — image preview and lightbox", () => {
  function imageAttachment(overrides: Partial<ChannelAttachment> = {}): ChannelAttachment {
    return {
      id: "img-1",
      filename: "foto.png",
      contentType: "image/png",
      size: 4096,
      status: "clean",
      previewStatus: "ready",
      createdAt: "",
      ...overrides,
    };
  }

  beforeEach(() => {
    mockPreview.mockResolvedValue(new Blob(["preview-bytes"]));
    const createObjectURL = vi.fn(() => "blob:img-1");
    const revokeObjectURL = vi.fn();
    vi.stubGlobal("URL", { ...URL, createObjectURL, revokeObjectURL });
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("shows the large preview instead of the 32px thumbnail", async () => {
    render(<MessageAttachments attachments={[imageAttachment()]} />);

    expect(await screen.findByTestId("chat-message-attachment-image-img-1")).toBeInTheDocument();
    expect(screen.queryByTestId("chat-details-file-thumb")).not.toBeInTheDocument();
  });

  it("opens the lightbox on click without downloading, and returns focus to the trigger on close", async () => {
    const user = userEvent.setup();
    render(<MessageAttachments attachments={[imageAttachment()]} />);

    const trigger = await screen.findByRole("button", { name: "Ampliar foto.png" });
    await user.click(trigger);

    const dialog = await screen.findByRole("dialog");
    expect(dialog).toBeInTheDocument();
    // The lightbox fetching the full-resolution original for the enlarged
    // view is expected — what must not happen is the *download* flow, whose
    // own visible signal is the Baixar button's loading state.
    expect(screen.getByTestId("chat-message-attachment-download-img-1")).toHaveTextContent(
      "Baixar",
    );

    await user.keyboard("{Escape}");

    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(trigger).toHaveFocus();
  });

  it("wires a GIF attachment through to the animated inline preview", async () => {
    render(
      <MessageAttachments
        attachments={[
          imageAttachment({ id: "gif-1", filename: "reacao.gif", contentType: "image/gif" }),
        ]}
      />,
    );

    await screen.findByTestId("chat-message-attachment-image-gif-1");
    expect(mockContent).toHaveBeenCalledWith("gif-1", expect.any(AbortSignal));
    expect(mockPreview).not.toHaveBeenCalled();
  });

  it("wires a WebP attachment through to the original — there is no server preview for it", async () => {
    render(
      <MessageAttachments
        attachments={[
          imageAttachment({ id: "webp-1", filename: "banner.webp", contentType: "image/webp" }),
        ]}
      />,
    );

    await screen.findByTestId("chat-message-attachment-image-webp-1");
    expect(mockContent).toHaveBeenCalledWith("webp-1", expect.any(AbortSignal));
  });

  it("respects prefers-reduced-motion end to end: a GIF shows the static preview, not the animated original", async () => {
    mockPreview.mockResolvedValue(new Blob(["preview-bytes"]));
    window.matchMedia = ((query: string) =>
      ({
        matches: query.includes("reduce"),
        media: query,
        addEventListener: () => {},
        removeEventListener: () => {},
      }) as unknown as MediaQueryList) as typeof window.matchMedia;

    render(
      <MessageAttachments
        attachments={[
          imageAttachment({ id: "gif-2", filename: "reacao.gif", contentType: "image/gif" }),
        ]}
      />,
    );

    await screen.findByTestId("chat-message-attachment-image-gif-2");
    expect(mockPreview).toHaveBeenCalledWith("gif-2", expect.any(AbortSignal));
    expect(mockContent).not.toHaveBeenCalled();
    expect(screen.getByRole("button", { name: "Reproduzir animação" })).toBeInTheDocument();

    // @ts-expect-error -- restore jsdom's absence of matchMedia for other tests.
    delete window.matchMedia;
  });
});
