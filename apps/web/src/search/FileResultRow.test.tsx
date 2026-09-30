import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes, useLocation } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

const { mockFetchPreview } = vi.hoisted(() => ({ mockFetchPreview: vi.fn() }));

vi.mock("../chat/filesApi", () => ({
  fetchAttachmentPreview: (...args: unknown[]) => mockFetchPreview(...args),
}));

// The host is the real one; only what it draws is stubbed, so the test proves
// the search reaches the timeline's viewers rather than a copy of them.
vi.mock("../chat/DocumentPreviewViewer", () => ({
  default: ({ attachment, onClose }: { attachment: { filename: string }; onClose: () => void }) => (
    <div role="dialog" aria-label={`Documento ${attachment.filename}`}>
      <button type="button" onClick={onClose}>
        Fechar documento
      </button>
    </div>
  ),
}));
vi.mock("../chat/AttachmentLightbox", () => ({
  default: ({
    attachment,
    inlineBlob,
    inlineIsOriginal,
  }: {
    attachment: { filename: string };
    inlineBlob: Blob;
    inlineIsOriginal: boolean;
  }) => (
    <div
      role="dialog"
      aria-label={`Imagem ${attachment.filename}`}
      data-blob-size={inlineBlob.size}
      data-original={String(inlineIsOriginal)}
    />
  ),
}));

import AttachmentViewerHost from "../chat/AttachmentViewerHost";
import FileResultRow from "./FileResultRow";
import { fileResult } from "./searchFixtures";
import type { FileSearchResult } from "./searchTypes";

function Landing() {
  const location = useLocation();
  return (
    <output data-testid="landing">
      {location.pathname}
      {location.search}
    </output>
  );
}

function renderFile(result: FileSearchResult) {
  return render(
    <MemoryRouter initialEntries={["/chat/search"]}>
      <Routes>
        <Route
          path="/chat/search"
          element={
            <AttachmentViewerHost>
              <FileResultRow result={result} query="backup" />
            </AttachmentViewerHost>
          }
        />
        <Route path="/chat/:kind/:id" element={<Landing />} />
      </Routes>
    </MemoryRouter>,
  );
}

const card = () =>
  screen.getByRole("button", { name: /relatorio-backup|foto-backup|video-backup/ });

beforeEach(() => {
  mockFetchPreview.mockReset();
});

describe("FileResultRow", () => {
  it("shows name, type, conversation, size and scan state", () => {
    renderFile(fileResult());
    expect(card()).toHaveTextContent("PDF · #infraestrutura · 2,4 MB");
    expect(screen.getByTestId("global-search-file-status-f1")).toHaveTextContent("Verificado");
    expect(screen.getByText("backup", { selector: "mark" })).toBeInTheDocument();
  });

  it("opens a previewable document in the existing viewer and returns focus on close", async () => {
    renderFile(fileResult());
    await userEvent.click(card());
    expect(
      screen.getByRole("dialog", { name: "Documento relatorio-backup.pdf" }),
    ).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Fechar documento" }));
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(card()).toHaveFocus();
  });

  it("opens an image in the lightbox on its preview, which then upgrades itself", async () => {
    mockFetchPreview.mockResolvedValue(new Blob(["abc"]));
    renderFile(fileResult({ id: "img", filename: "foto-backup.png", contentType: "image/png" }));
    await userEvent.click(card());

    expect(mockFetchPreview).toHaveBeenCalledWith("img");
    const lightbox = await screen.findByRole("dialog", { name: "Imagem foto-backup.png" });
    expect(lightbox).toHaveAttribute("data-blob-size", "3");
    expect(lightbox).toHaveAttribute("data-original", "false");
  });

  it("reports an image it could not fetch without leaving the search", async () => {
    mockFetchPreview.mockRejectedValue(new Error("403"));
    renderFile(fileResult({ filename: "foto-backup.png", contentType: "image/png" }));
    await userEvent.click(card());
    expect(await screen.findByRole("alert")).toHaveTextContent("Não foi possível abrir o arquivo.");
    expect(screen.queryByTestId("landing")).toBeNull();
  });

  it("offers Ir para mensagem beside the viewer", async () => {
    renderFile(fileResult());
    await userEvent.click(screen.getByRole("button", { name: "Ir para mensagem" }));
    expect(screen.getByTestId("landing")).toHaveTextContent("/chat/channel/c1?message=m9");
  });

  it.each([
    ["still scanned", { status: "pending_scan" as const }, "Verificando"],
    ["blocked", { status: "rejected" as const }, "Bloqueado"],
    ["without a preview", { previewStatus: "blocked" as const }, "Verificado"],
    ["a video", { filename: "video-backup.mp4", contentType: "video/mp4" }, "Verificado"],
  ])("opens the message when no viewer can show a file %s", async (_name, overrides, label) => {
    renderFile(
      fileResult({
        conversation: { kind: "dm", id: "d1", type: "direct", name: "Ana" },
        ...overrides,
      }),
    );
    expect(screen.getByTestId("global-search-file-status-f1")).toHaveTextContent(label);
    expect(screen.queryByRole("button", { name: "Ir para mensagem" })).toBeNull();
    await userEvent.click(card());
    expect(screen.getByTestId("landing")).toHaveTextContent("/chat/dm/d1?message=m9");
  });
});
