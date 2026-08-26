import { render, screen, waitFor, within } from "@testing-library/react";
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

const {
  mockPreview,
  mockContent,
  mockDocumentManifest,
  mockDocumentPage,
  mockDocumentSheet,
  mockRegenerate,
} = vi.hoisted(() => ({
  mockPreview: vi.fn(),
  mockContent: vi.fn(),
  mockDocumentManifest: vi.fn(),
  mockDocumentPage: vi.fn(),
  mockDocumentSheet: vi.fn(),
  mockRegenerate: vi.fn(),
}));

vi.mock("./filesApi", () => ({
  fetchAttachmentPreview: (...args: unknown[]) => mockPreview(...args),
  fetchAttachmentContent: (...args: unknown[]) => mockContent(...args),
  fetchDocumentPreviewManifest: (...args: unknown[]) => mockDocumentManifest(...args),
  fetchDocumentPreviewPage: (...args: unknown[]) => mockDocumentPage(...args),
  fetchDocumentPreviewSheet: (...args: unknown[]) => mockDocumentSheet(...args),
  regenerateDocumentPreview: (...args: unknown[]) => mockRegenerate(...args),
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
  mockDocumentManifest.mockReset().mockResolvedValue({
    attachmentId: "att-1",
    kind: "pages",
    pageCount: 2,
    labels: ["Página 1", "Página 2"],
  });
  mockDocumentPage.mockReset().mockResolvedValue(new Blob(["jpeg-page"]));
  mockDocumentSheet.mockReset().mockResolvedValue({
    columns: ["A", "B"],
    rows: [["1", "2"]],
    truncatedRows: false,
    truncatedColumns: false,
    totalRowsRead: 1,
  });
  mockRegenerate.mockReset().mockResolvedValue(undefined);
});

describe("message attachments — document viewer", () => {
  beforeEach(() => {
    let objectIndex = 0;
    vi.stubGlobal("URL", {
      ...URL,
      createObjectURL: vi.fn(() => `blob:document-${++objectIndex}`),
      revokeObjectURL: vi.fn(),
    });
  });

  afterEach(() => vi.unstubAllGlobals());

  it("opens an approved document preview without downloading and navigates its raster pages", async () => {
    const user = userEvent.setup();
    mockPreview.mockResolvedValue(new Blob(["thumbnail"]));
    render(
      <MessageAttachments
        attachments={[attachment({ status: "clean", previewStatus: "ready" })]}
      />,
    );

    const trigger = await screen.findByRole("button", { name: "Visualizar relatorio.pdf" });
    await user.click(trigger);

    const dialog = await screen.findByRole("dialog", { name: "relatorio.pdf" });
    expect(within(dialog).getByText("Página 1 de 2")).toBeInTheDocument();
    expect(mockDocumentPage).toHaveBeenCalledWith("att-1", 1, expect.any(AbortSignal));
    expect(mockContent).not.toHaveBeenCalled();

    await user.click(within(dialog).getByRole("button", { name: "Próxima página" }));
    await waitFor(() =>
      expect(mockDocumentPage).toHaveBeenCalledWith("att-1", 2, expect.any(AbortSignal)),
    );
    expect(within(dialog).getByText("Página 2 de 2")).toBeInTheDocument();
  });

  it("closes with Escape and restores focus to the document preview trigger", async () => {
    const user = userEvent.setup();
    mockPreview.mockResolvedValue(new Blob(["thumbnail"]));
    render(
      <MessageAttachments
        attachments={[attachment({ status: "clean", previewStatus: "ready" })]}
      />,
    );

    const trigger = await screen.findByRole("button", { name: "Visualizar relatorio.pdf" });
    await user.click(trigger);
    await screen.findByRole("dialog", { name: "relatorio.pdf" });
    await user.keyboard("{Escape}");

    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(trigger).toHaveFocus();
  });

  it("renders a CSV/XLSX preview as a table, with no pagination and a truncation note when the server sent one", async () => {
    const user = userEvent.setup();
    mockDocumentManifest.mockResolvedValue({
      attachmentId: "att-1",
      kind: "sheets",
      pageCount: 1,
      labels: ["Planilha"],
    });
    mockDocumentSheet.mockResolvedValue({
      columns: ["A", "B"],
      rows: [["1", "2"]],
      truncatedRows: true,
      truncatedColumns: false,
      totalRowsRead: 500,
    });
    render(
      <MessageAttachments
        attachments={[
          attachment({
            filename: "planilha.csv",
            contentType: "text/plain",
            status: "clean",
            previewStatus: "ready",
          }),
        ]}
      />,
    );

    const trigger = await screen.findByRole("button", { name: "Visualizar planilha.csv" });
    await user.click(trigger);

    const dialog = await screen.findByRole("dialog", { name: "planilha.csv" });
    await waitFor(() =>
      expect(mockDocumentSheet).toHaveBeenCalledWith("att-1", 1, expect.any(AbortSignal)),
    );
    expect(mockDocumentPage).not.toHaveBeenCalled();

    expect(within(dialog).getByRole("cell", { name: "1" })).toBeInTheDocument();
    expect(within(dialog).getByRole("columnheader", { name: "A" })).toBeInTheDocument();
    expect(within(dialog).getByText(/500/)).toBeInTheDocument();
    expect(
      within(dialog).queryByRole("button", { name: "Página anterior" }),
    ).not.toBeInTheDocument();
    expect(
      within(dialog).queryByRole("button", { name: "Próxima página" }),
    ).not.toBeInTheDocument();
  });
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
    // A blocked file's content is never fetched, preview included — nothing
    // authorized to show and nothing requested.
    expect(mockDocumentPage).not.toHaveBeenCalled();
    expect(screen.queryByTestId("chat-message-attachment-document-att-1")).not.toBeInTheDocument();
    expect(
      screen.queryByTestId("chat-message-attachment-document-loading-att-1"),
    ).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Visualizar relatorio.pdf" })).toBeNull();
  });

  it("shows a loading placeholder for a pdf whose preview is still being generated, never an error", () => {
    render(
      <MessageAttachments
        attachments={[attachment({ status: "clean", previewStatus: "pending" })]}
      />,
    );

    expect(
      screen.getByTestId("chat-message-attachment-document-loading-att-1"),
    ).toBeInTheDocument();
    expect(screen.queryByText("Pré-visualização indisponível.")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Visualizar relatorio.pdf" })).toBeNull();
    // Nothing is fetched until the server says the page is ready.
    expect(mockDocumentPage).not.toHaveBeenCalled();
  });

  it("offers retry when a pdf preview failed", () => {
    render(
      <MessageAttachments
        attachments={[attachment({ status: "clean", previewStatus: "failed" })]}
      />,
    );

    expect(screen.getByRole("button", { name: "Tentar novamente" })).toBeInTheDocument();
    expect(mockDocumentPage).not.toHaveBeenCalled();
    // The file itself is still downloadable — only its preview gave up.
    expect(downloadButton()).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Visualizar relatorio.pdf" })).toBeNull();
  });

  it("regenerates an expired preview exactly once while keeping the skeleton", async () => {
    const { rerender } = render(
      <MessageAttachments
        attachments={[attachment({ status: "clean", previewStatus: "expired" })]}
      />,
    );
    await waitFor(() => expect(mockRegenerate).toHaveBeenCalledTimes(1));
    expect(
      screen.getByRole("status", { name: "Carregando pré-visualização…" }),
    ).toBeInTheDocument();
    rerender(
      <MessageAttachments
        attachments={[attachment({ status: "clean", previewStatus: "expired" })]}
      />,
    );
    expect(mockRegenerate).toHaveBeenCalledTimes(1);
  });

  it("shows the pdf's first-page preview, filename, size and both actions once ready", async () => {
    render(
      <MessageAttachments
        attachments={[attachment({ status: "clean", previewStatus: "ready" })]}
      />,
    );

    await waitFor(() =>
      expect(screen.getByTestId("chat-message-attachment-document-att-1")).toBeInTheDocument(),
    );
    expect(screen.getByText("relatorio.pdf")).toBeInTheDocument();
    expect(screen.getByText("KB", { exact: false })).toBeInTheDocument();
    // The large preview image is itself a click target, and the explicit
    // Visualizar action beside Baixar is a second, separately reachable one.
    expect(screen.getAllByRole("button", { name: "Visualizar relatorio.pdf" })).toHaveLength(2);
    expect(downloadButton()).toBeInTheDocument();
  });

  it.each([
    ["documento.docx", "application/zip"],
    ["documento.odt", "application/zip"],
    ["apresentacao.pptx", "application/zip"],
  ])("shows the raster preview generated for %s", async (filename, contentType) => {
    render(
      <MessageAttachments
        attachments={[
          attachment({ filename, contentType, status: "clean", previewStatus: "available" }),
        ]}
      />,
    );

    await waitFor(() =>
      expect(screen.getByTestId("chat-message-attachment-document-att-1")).toBeInTheDocument(),
    );
    expect(mockDocumentPage).toHaveBeenCalledWith("att-1", 1, expect.anything());
    expect(screen.getAllByRole("button", { name: `Visualizar ${filename}` })).toHaveLength(2);
  });

  it("keeps a ready CSV/XLSX attachment on the plain icon row, with no large preview box", async () => {
    render(
      <MessageAttachments
        attachments={[
          attachment({
            filename: "planilha.csv",
            contentType: "text/plain",
            status: "clean",
            previewStatus: "ready",
          }),
        ]}
      />,
    );

    expect(await screen.findByText("planilha.csv")).toBeInTheDocument();
    // A spreadsheet never gets the large first-page card — see
    // AttachmentDocumentPreview's own module comment — only the row's
    // existing icon, name, size and the Visualizar/Baixar actions.
    expect(screen.queryByTestId("chat-message-attachment-document-att-1")).not.toBeInTheDocument();
    expect(
      screen.queryByTestId("chat-message-attachment-document-loading-att-1"),
    ).not.toBeInTheDocument();
    expect(mockDocumentPage).not.toHaveBeenCalled();
    expect(screen.getByRole("button", { name: "Visualizar planilha.csv" })).toBeInTheDocument();
    expect(downloadButton()).toBeInTheDocument();
  });

  it("requests a preview only for an approved file that has one", () => {
    // A non-document, non-image type still goes through AttachmentThumbnail's
    // small preview fetch — PDFs get their own large first-page preview,
    // covered separately below. application/zip and text/plain are excluded
    // here on purpose: those are exactly the coarse detected types CSV/XLSX
    // attachments carry (see isDocumentAttachment's own comment), so an MP3
    // stands in for "a type that is genuinely never a document".
    const audio = (overrides: Partial<ChannelAttachment> = {}) =>
      attachment({ filename: "audio.mp3", contentType: "audio/mpeg", ...overrides });
    const { rerender } = render(
      <MessageAttachments attachments={[audio({ previewStatus: "ready" })]} />,
    );
    // pending_scan + ready preview: still nothing, because the scan decides.
    expect(mockPreview).not.toHaveBeenCalled();

    rerender(
      <MessageAttachments attachments={[audio({ status: "clean", previewStatus: "ready" })]} />,
    );
    expect(mockPreview).toHaveBeenCalledWith("att-1", expect.anything());
  });

  it("requests a PDF's first-page preview only once it is approved and ready", () => {
    const { rerender } = render(
      <MessageAttachments attachments={[attachment({ previewStatus: "ready" })]} />,
    );
    // pending_scan + ready preview: still nothing, because the scan decides.
    expect(mockDocumentPage).not.toHaveBeenCalled();

    rerender(
      <MessageAttachments
        attachments={[attachment({ status: "clean", previewStatus: "ready" })]}
      />,
    );
    expect(mockDocumentPage).toHaveBeenCalledWith("att-1", 1, expect.anything());
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

  it("does not turn a ready video preview into a document viewer", () => {
    render(
      <MessageAttachments
        attachments={[
          attachment({
            filename: "clip.mp4",
            contentType: "video/mp4",
            status: "clean",
            previewStatus: "ready",
          }),
        ]}
      />,
    );
    expect(screen.queryByRole("button", { name: "Visualizar clip.mp4" })).not.toBeInTheDocument();
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

  it("groups contiguous images, keeps mixed document order and expands the +N remainder", async () => {
    const images = Array.from({ length: 5 }, (_, index) =>
      imageAttachment({ id: `img-${index + 1}`, filename: `${index + 1}.png` }),
    );
    render(
      <MessageAttachments
        attachments={[...images, attachment({ id: "doc-1", filename: "fim.pdf" })]}
      />,
    );

    const grid = screen.getByTestId("chat-message-image-grid");
    expect(grid).toHaveAttribute("data-count", "5");
    for (let index = 1; index <= 4; index += 1) {
      const card = screen.getByTestId(`chat-message-attachment-img-${index}`);
      expect(card.querySelector(".chat-msg-area__attachment-row")).not.toBeNull();
      expect(within(card).getByText(`${index}.png`)).toBeInTheDocument();
      expect(within(card).getByText("Verificado")).toBeInTheDocument();
      expect(within(card).getByRole("button", { name: `Baixar ${index}.png` })).toBeInTheDocument();
    }
    expect(screen.queryByTestId("chat-message-attachment-img-5")).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Mostrar mais 1 imagem" }));
    expect(screen.getByTestId("chat-message-attachment-img-5")).toBeInTheDocument();
    expect(
      screen.getByTestId("chat-message-attachment-doc-1").compareDocumentPosition(grid) &
        Node.DOCUMENT_POSITION_PRECEDING,
    ).toBeTruthy();
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
