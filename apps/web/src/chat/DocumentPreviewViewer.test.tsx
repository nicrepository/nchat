/**
 * DocumentPreviewViewer tests (task #494).
 *
 * The shell (portal, dialog semantics, focus trap, Escape, backdrop click) is
 * the same pattern AttachmentLightbox already covers; these tests focus on
 * what is specific here: the manifest-driven pages/sheets branch, pagination,
 * zoom, and the download action's idle/loading/failed states.
 */

import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import DocumentPreviewViewer from "./DocumentPreviewViewer";
import type { ChannelAttachment } from "./chatTypes";
import type { DocumentPreviewManifest, DocumentPreviewSheet } from "./filesApi";

const mockManifest = vi.hoisted(() => vi.fn());
const mockPage = vi.hoisted(() => vi.fn());
const mockSheet = vi.hoisted(() => vi.fn());
const mockContent = vi.hoisted(() => vi.fn());
vi.mock("./filesApi", () => ({
  fetchDocumentPreviewManifest: (...args: unknown[]) => mockManifest(...args),
  fetchDocumentPreviewPage: (...args: unknown[]) => mockPage(...args),
  fetchDocumentPreviewSheet: (...args: unknown[]) => mockSheet(...args),
  fetchAttachmentContent: (...args: unknown[]) => mockContent(...args),
}));

const createObjectURL = vi.fn();
const revokeObjectURL = vi.fn();
const onClose = vi.fn();

function attachment(overrides: Partial<ChannelAttachment> = {}): ChannelAttachment {
  return {
    id: "att-1",
    filename: "relatorio.pdf",
    contentType: "application/pdf",
    size: 4096,
    status: "clean",
    previewStatus: "ready",
    createdAt: "2026-07-15T12:00:00.000Z",
    ...overrides,
  };
}

function pagesManifest(overrides: Partial<DocumentPreviewManifest> = {}): DocumentPreviewManifest {
  return {
    attachmentId: "att-1",
    kind: "pages",
    pageCount: 3,
    labels: ["1", "2", "3"],
    ...overrides,
  };
}

function sheetsManifest(overrides: Partial<DocumentPreviewManifest> = {}): DocumentPreviewManifest {
  return {
    attachmentId: "att-1",
    kind: "sheets",
    pageCount: 1,
    labels: ["Planilha"],
    ...overrides,
  };
}

function sheet(overrides: Partial<DocumentPreviewSheet> = {}): DocumentPreviewSheet {
  return {
    columns: ["A", "B"],
    rows: [["1", "2"]],
    truncatedRows: false,
    truncatedColumns: false,
    totalRowsRead: 1,
    ...overrides,
  };
}

beforeEach(() => {
  mockManifest.mockReset();
  mockPage.mockReset().mockResolvedValue(new Blob(["page-bytes"]));
  mockSheet.mockReset().mockResolvedValue(sheet());
  mockContent.mockReset().mockResolvedValue(new Blob(["file-bytes"]));
  onClose.mockReset();
  createObjectURL.mockReset();
  revokeObjectURL.mockReset();
  let created = 0;
  createObjectURL.mockImplementation(() => `blob:doc-${++created}`);
  vi.stubGlobal("URL", { ...URL, createObjectURL, revokeObjectURL });
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("shell", () => {
  it("renders into document.body as a labelled dialog and focuses Fechar", async () => {
    mockManifest.mockResolvedValue(pagesManifest());
    render(<DocumentPreviewViewer attachment={attachment()} onClose={onClose} />);

    const dialog = screen.getByRole("dialog");
    expect(dialog).toHaveAttribute("aria-modal", "true");
    expect(dialog.parentElement?.parentElement).toBe(document.body);
    expect(screen.getByText("relatorio.pdf")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Fechar visualização" })).toHaveFocus();
  });

  it("closes on Escape, on a backdrop click, and on the Fechar button", async () => {
    mockManifest.mockResolvedValue(pagesManifest());
    const user = userEvent.setup();
    render(<DocumentPreviewViewer attachment={attachment()} onClose={onClose} />);

    await user.keyboard("{Escape}");
    expect(onClose).toHaveBeenCalledTimes(1);

    onClose.mockClear();
    await user.click(screen.getByRole("dialog").parentElement as HTMLElement);
    expect(onClose).toHaveBeenCalledTimes(1);

    onClose.mockClear();
    await user.click(screen.getByRole("button", { name: "Fechar visualização" }));
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("does not close on a click inside the dialog", async () => {
    mockManifest.mockResolvedValue(pagesManifest());
    const user = userEvent.setup();
    render(<DocumentPreviewViewer attachment={attachment()} onClose={onClose} />);

    await user.click(screen.getByRole("dialog"));
    expect(onClose).not.toHaveBeenCalled();
  });

  it("cycles focus with Tab between the first and last control", async () => {
    mockManifest.mockResolvedValue(pagesManifest());
    const user = userEvent.setup();
    render(<DocumentPreviewViewer attachment={attachment()} onClose={onClose} />);
    await waitFor(() => expect(mockPage).toHaveBeenCalled());

    const buttons = screen.getAllByRole("button");
    const first = buttons[0];
    const last = buttons[buttons.length - 1];
    last.focus();
    await user.tab();
    expect(first).toHaveFocus();
    await user.tab({ shift: true });
    expect(last).toHaveFocus();
  });
});

describe("pages", () => {
  it("fetches and shows the first page, with pagination enabled at the bounds", async () => {
    mockManifest.mockResolvedValue(pagesManifest());
    render(<DocumentPreviewViewer attachment={attachment()} onClose={onClose} />);

    await waitFor(() => expect(mockPage).toHaveBeenCalledWith("att-1", 1, expect.any(AbortSignal)));
    expect(screen.getByText("Página 1 de 3")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Página anterior" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Próxima página" })).toBeEnabled();
  });

  it("navigates to the next and previous page on click", async () => {
    mockManifest.mockResolvedValue(pagesManifest());
    const user = userEvent.setup();
    render(<DocumentPreviewViewer attachment={attachment()} onClose={onClose} />);
    await waitFor(() => expect(mockPage).toHaveBeenCalledWith("att-1", 1, expect.any(AbortSignal)));

    await user.click(screen.getByRole("button", { name: "Próxima página" }));
    await waitFor(() => expect(mockPage).toHaveBeenCalledWith("att-1", 2, expect.any(AbortSignal)));
    expect(screen.getByText("Página 2 de 3")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Página anterior" }));
    await waitFor(() => expect(mockPage).toHaveBeenCalledWith("att-1", 1, expect.any(AbortSignal)));
  });

  it("disables Próxima página on the last page", async () => {
    mockManifest.mockResolvedValue(pagesManifest({ pageCount: 1 }));
    render(<DocumentPreviewViewer attachment={attachment()} onClose={onClose} />);
    await waitFor(() => expect(mockPage).toHaveBeenCalled());

    expect(screen.getByRole("button", { name: "Próxima página" })).toBeDisabled();
  });

  it("shows an alert when the page fetch fails", async () => {
    mockManifest.mockResolvedValue(pagesManifest());
    mockPage.mockRejectedValue(new Error("boom"));
    render(<DocumentPreviewViewer attachment={attachment()} onClose={onClose} />);

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Não foi possível carregar a pré-visualização.",
    );
  });

  it("zooms in and out within bounds", async () => {
    mockManifest.mockResolvedValue(pagesManifest());
    const user = userEvent.setup();
    render(<DocumentPreviewViewer attachment={attachment()} onClose={onClose} />);
    await waitFor(() => expect(mockPage).toHaveBeenCalled());

    expect(screen.getByText("100%")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Aumentar zoom" }));
    expect(screen.getByText("125%")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Reduzir zoom" }));
    expect(screen.getByText("100%")).toBeInTheDocument();
  });

  it("shows a manifest fetch failure as an alert", async () => {
    mockManifest.mockRejectedValue(new Error("boom"));
    render(<DocumentPreviewViewer attachment={attachment()} onClose={onClose} />);

    expect(await screen.findByRole("alert")).toBeInTheDocument();
  });
});

describe("sheets", () => {
  it("fetches and renders the table instead of pagination controls", async () => {
    mockManifest.mockResolvedValue(sheetsManifest());
    render(<DocumentPreviewViewer attachment={attachment()} onClose={onClose} />);

    await waitFor(() =>
      expect(mockSheet).toHaveBeenCalledWith("att-1", 1, expect.any(AbortSignal)),
    );
    expect(screen.getByText("Planilha")).toBeInTheDocument();
    expect(screen.getByRole("columnheader", { name: "A" })).toBeInTheDocument();
    expect(screen.getByRole("cell", { name: "1" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Próxima página" })).not.toBeInTheDocument();
  });

  it("shows a truncation note when the sheet was cut", async () => {
    mockManifest.mockResolvedValue(sheetsManifest());
    mockSheet.mockResolvedValue(sheet({ truncatedRows: true, truncatedColumns: true }));
    render(<DocumentPreviewViewer attachment={attachment()} onClose={onClose} />);

    expect(await screen.findByRole("status")).toHaveTextContent(/linhas/);
  });

  it("shows an alert when the sheet fetch fails", async () => {
    mockManifest.mockResolvedValue(sheetsManifest());
    mockSheet.mockRejectedValue(new Error("boom"));
    render(<DocumentPreviewViewer attachment={attachment()} onClose={onClose} />);

    expect(await screen.findByRole("alert")).toBeInTheDocument();
  });
});

describe("download", () => {
  it("downloads the file and revokes the object URL", async () => {
    mockManifest.mockResolvedValue(pagesManifest());
    const user = userEvent.setup();
    render(<DocumentPreviewViewer attachment={attachment()} onClose={onClose} />);
    await waitFor(() => expect(mockPage).toHaveBeenCalled());

    await user.click(screen.getByRole("button", { name: "Baixar relatorio.pdf" }));
    await waitFor(() => expect(mockContent).toHaveBeenCalledWith("att-1"));
    await waitFor(() => expect(revokeObjectURL).toHaveBeenCalled());
  });

  it("shows Baixando… while the download is in flight and disables the button", async () => {
    mockManifest.mockResolvedValue(pagesManifest());
    mockContent.mockReturnValue(new Promise(() => {}));
    const user = userEvent.setup();
    render(<DocumentPreviewViewer attachment={attachment()} onClose={onClose} />);
    await waitFor(() => expect(mockPage).toHaveBeenCalled());

    await user.click(screen.getByRole("button", { name: "Baixar relatorio.pdf" }));
    expect(await screen.findByRole("button", { name: "Baixar relatorio.pdf" })).toBeDisabled();
    expect(screen.getByText("Baixando…")).toBeInTheDocument();
  });

  it("shows an inline error note when the download fails, without closing the viewer", async () => {
    mockManifest.mockResolvedValue(pagesManifest());
    mockContent.mockRejectedValue(new Error("boom"));
    const user = userEvent.setup();
    render(<DocumentPreviewViewer attachment={attachment()} onClose={onClose} />);
    await waitFor(() => expect(mockPage).toHaveBeenCalled());

    await user.click(screen.getByRole("button", { name: "Baixar relatorio.pdf" }));
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Não foi possível baixar o arquivo.",
    );
    expect(onClose).not.toHaveBeenCalled();
  });
});
