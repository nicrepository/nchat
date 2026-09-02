import { createEvent, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

/**
 * Issue #516: attaching what the clipboard carries, with Ctrl+V / Cmd+V.
 *
 * Everything here goes through the composer, because the point of the feature
 * is that a pasted screenshot is not a second pipeline: it lands in the same
 * queue, under the same limits, as a file chosen with the picker or dropped on
 * the box. A paste that carries no bytes must stay the browser's own.
 */

const { mockFetchMentionCandidates, mockUploadAttachment, mockDeleteAttachment } = vi.hoisted(
  () => ({
    mockFetchMentionCandidates: vi.fn(),
    mockUploadAttachment: vi.fn(),
    mockDeleteAttachment: vi.fn(),
  }),
);

vi.mock("./chatApi", () => ({
  fetchMentionCandidates: (...args: unknown[]) => mockFetchMentionCandidates(...args),
}));

vi.mock("./filesApi", async () => {
  const actual = await vi.importActual<typeof import("./filesApi")>("./filesApi");
  return {
    ...actual,
    uploadAttachment: (...args: unknown[]) => mockUploadAttachment(...args),
    deleteAttachmentDraft: (...args: unknown[]) => mockDeleteAttachment(...args),
  };
});

import ChatComposer from "./ChatComposer";
import type { WorkspaceAttachmentLimits } from "./chatApi";
import { AttachmentUploadError } from "./filesApi";
import type { SendResult } from "./useMessages";

const MIB = 1024 * 1024;
const LIMIT = 8 * MIB;

/** The instant every screenshot in this file was "taken": 2026-08-31 14:03:05 UTC. */
const PASTED_AT = Date.UTC(2026, 7, 31, 14, 3, 5);
const PASTED_NAME = "Screenshot-2026-08-31-140305";

/**
 * A File of a real size. The bytes are materialised rather than declared —
 * unlike the picker's tests — because a pasted screenshot is re-wrapped to
 * carry its generated name, and a `size` merely defined on the old object
 * would not survive into the new one the way real bytes do.
 */
function fileOf(bytes: number, name: string, type: string, lastModified = PASTED_AT): File {
  return new File([new Uint8Array(bytes)], name, { type, lastModified });
}

/**
 * Two different screenshots that a browser reports identically: the same
 * placeholder name, the same MIME, the same instant, and — two frames of the
 * same screen — the same number of bytes. Only the content differs, which is
 * the one thing the upload queue's identity does not look at.
 */
function indistinguishableScreenshots(): [File, File] {
  const make = (fill: number) =>
    new File([new Uint8Array(2048).fill(fill)], "image.png", {
      type: "image/png",
      lastModified: PASTED_AT,
    });
  return [make(0x61), make(0x62)];
}

/** What a browser hands over for a screenshot: bytes under a placeholder name. */
const screenshot = (bytes = 2048, type = "image/png", name = "image.png") =>
  fileOf(bytes, name, type);

function uploadedAttachment(filename = `${PASTED_NAME}.png`) {
  return {
    id: "a-1",
    filename,
    contentType: "image/png",
    size: 2048,
    status: "pending_scan" as const,
    createdAt: "2026-08-31T14:03:05Z",
  };
}

/**
 * A clipboard, as ClipboardEvent exposes it.
 *
 * `items` defaults to the file-kind entries the browser reports alongside
 * `files` for the very same objects — the duplication the composer has to
 * neutralise — so a test only names them when it wants a different shape.
 */
function clipboard(
  files: File[],
  options: { text?: string; items?: Partial<DataTransferItem>[] } = {},
): DataTransfer {
  const fileItems = files.map((file) => ({ kind: "file", type: file.type, getAsFile: () => file }));
  const items = options.items ?? fileItems;
  const types = [
    ...(options.text === undefined ? [] : ["text/plain"]),
    ...files.map((f) => f.type),
  ];
  return {
    files,
    items: items as unknown as DataTransferItemList,
    types,
    // Only text/plain carries anything: the editor asks for several types
    // (text/html among them), and a clipboard that answered them all with the
    // same string would not be one any browser produces.
    getData: vi.fn((type: string) => (type === "text/plain" ? (options.text ?? "") : "")),
  } as unknown as DataTransfer;
}

function renderComposer(
  target: { kind: "channel" | "dm"; id: string } | null = { kind: "channel", id: "ch-1" },
  attachmentLimits: WorkspaceAttachmentLimits = {
    maxUploadBytes: LIMIT,
    maxFiles: 10,
    maxBytes: 512 * MIB,
  },
) {
  const onSend = vi.fn<(body: string) => Promise<SendResult>>();
  onSend.mockResolvedValue({ status: "sent" });
  const view = render(
    <ChatComposer
      bodyFormat="v2"
      placeholder="Mensagem..."
      onSend={onSend}
      uploadTarget={target}
      attachmentLimits={attachmentLimits}
    />,
  );
  return { ...view, onSend };
}

const editor = () => screen.getByTestId("chat-composer-input");
const editorText = () => editor().textContent ?? "";
const fileInput = () => screen.getByTestId("chat-composer-file-input") as HTMLInputElement;

/**
 * A paste, as the browser delivers it: on the editor, bubbling out to the
 * composer box that decides whether any of it is an attachment.
 *
 * `defaultPrevented` is deliberately not asserted anywhere: the editor
 * prevents a text paste itself, having inserted the text on its own terms, so
 * the flag says nothing about who handled what. What the editor ended up
 * holding, and what reached the upload queue, do.
 */
function paste(data?: DataTransfer): void {
  fireEvent(editor(), createEvent.paste(editor(), data ? { clipboardData: data } : undefined));
}

const uploadedFile = (call = 0) => mockUploadAttachment.mock.calls[call][1] as File;

beforeEach(() => {
  mockFetchMentionCandidates.mockReset().mockResolvedValue([]);
  mockUploadAttachment.mockReset().mockResolvedValue(uploadedAttachment());
  mockDeleteAttachment.mockReset().mockResolvedValue(undefined);
});

afterEach(() => {
  vi.clearAllMocks();
});

// ── Images ───────────────────────────────────────────────────────────────────

describe("pasting an image", () => {
  it("uploads a pasted PNG through the picker's own pipeline", async () => {
    renderComposer();

    paste(clipboard([screenshot()]));

    await waitFor(() => expect(mockUploadAttachment).toHaveBeenCalledOnce());
    // Nothing of the image was written into the message body: it is an
    // attachment, and the editor is left exactly as the user had it.
    expect(editorText()).toBe("");
    expect(mockUploadAttachment.mock.calls[0][0]).toEqual({ kind: "channel", id: "ch-1" });
    expect(mockUploadAttachment.mock.calls[0][2]).toBe(LIMIT);
    expect(uploadedFile().type).toBe("image/png");
  });

  it("uploads a pasted JPEG the browser named nothing at all", async () => {
    renderComposer();

    paste(clipboard([fileOf(2048, "", "image/jpeg")]));

    await waitFor(() => expect(mockUploadAttachment).toHaveBeenCalledOnce());
    expect(uploadedFile().name).toBe(`${PASTED_NAME}.jpeg`);
  });

  it("names a screenshot after the moment it reached the clipboard", async () => {
    renderComposer();

    paste(clipboard([screenshot()]));

    expect(await screen.findByText(`${PASTED_NAME}.png`)).toBeInTheDocument();
    expect(uploadedFile().name).toBe(`${PASTED_NAME}.png`);
  });

  it("still names a nameless, typeless blob predictably", async () => {
    renderComposer();

    paste(clipboard([fileOf(512, "", "")]));

    await waitFor(() => expect(mockUploadAttachment).toHaveBeenCalledOnce());
    expect(uploadedFile().name).toBe(`${PASTED_NAME}.bin`);
  });

  it("keeps the real name of a file copied from the file manager", async () => {
    renderComposer();

    paste(clipboard([fileOf(1024, "relatorio.pdf", "application/pdf")]));

    await waitFor(() => expect(mockUploadAttachment).toHaveBeenCalledOnce());
    expect(uploadedFile().name).toBe("relatorio.pdf");
  });

  it("deduplicates the same clipboard item exposed through files and items", async () => {
    renderComposer();
    const file = screenshot();

    // One screenshot, listed twice — the shape a clipboard takes when the same
    // item is reachable through both `files` and `items`.
    paste(clipboard([file, file]));

    await waitFor(() => expect(mockUploadAttachment).toHaveBeenCalledOnce());
    expect(screen.getByText("1 arquivo anexado")).toBeInTheDocument();
    expect(uploadedFile().name).toBe(`${PASTED_NAME}.png`);
  });

  it("keeps distinct screenshots with identical clipboard metadata in the same paste", async () => {
    renderComposer();
    const [first, second] = indistinguishableScreenshots();
    expect(second.size).toBe(first.size);
    expect(second.lastModified).toBe(first.lastModified);

    paste(clipboard([first, second]));

    await waitFor(() => expect(mockUploadAttachment).toHaveBeenCalledTimes(2));
    expect(screen.getByText("2 arquivos anexados")).toBeInTheDocument();
    expect(screen.queryByText(/duplicados foram ignorados/i)).not.toBeInTheDocument();
    // The first keeps the plain name; only what would have collided is numbered.
    expect(uploadedFile(0).name).toBe(`${PASTED_NAME}.png`);
    expect(uploadedFile(1).name).toBe(`${PASTED_NAME}-2.png`);
  });

  it("numbers only the names it generated, never a file the browser named", async () => {
    renderComposer();

    paste(
      clipboard([
        fileOf(1024, "relatorio.pdf", "application/pdf"),
        screenshot(2048),
        screenshot(4096),
      ]),
    );

    await waitFor(() => expect(mockUploadAttachment).toHaveBeenCalledTimes(3));
    expect(uploadedFile(0).name).toBe("relatorio.pdf");
    expect(uploadedFile(1).name).toBe(`${PASTED_NAME}.png`);
    expect(uploadedFile(2).name).toBe(`${PASTED_NAME}-2.png`);
  });

  it("reads items only when the browser exposed no files", async () => {
    renderComposer();
    const file = screenshot();

    paste(clipboard([], { items: [{ kind: "file", getAsFile: () => file }] }));

    await waitFor(() => expect(mockUploadAttachment).toHaveBeenCalledOnce());
    expect(uploadedFile().name).toBe(`${PASTED_NAME}.png`);
  });

  it("queues each screenshot of a sequence under its own name", async () => {
    renderComposer();

    paste(clipboard([screenshot(2048)]));
    paste(clipboard([fileOf(4096, "image.png", "image/png", PASTED_AT + 60_000)]));

    await waitFor(() => expect(mockUploadAttachment).toHaveBeenCalledTimes(2));
    expect(screen.getByText("2 arquivos anexados")).toBeInTheDocument();
    expect(uploadedFile(0).name).toBe(`${PASTED_NAME}.png`);
    expect(uploadedFile(1).name).toBe("Screenshot-2026-08-31-140405.png");
  });

  it("shares one batch with the file picker", async () => {
    renderComposer();

    paste(clipboard([screenshot()]));
    await userEvent.upload(fileInput(), fileOf(1024, "relatorio.pdf", "application/pdf"));

    await waitFor(() => expect(mockUploadAttachment).toHaveBeenCalledTimes(2));
    expect(screen.getByText("2 arquivos anexados")).toBeInTheDocument();
    expect(screen.getByText(`${PASTED_NAME}.png`)).toBeInTheDocument();
    expect(screen.getByText("relatorio.pdf")).toBeInTheDocument();
  });

  it("removes a pasted screenshot like any other attachment", async () => {
    renderComposer();
    paste(clipboard([screenshot()]));
    expect(await screen.findByText("Pronto para enviar")).toBeInTheDocument();

    await userEvent.click(screen.getByTestId("chat-composer-remove-attachment"));

    expect(screen.queryByText(`${PASTED_NAME}.png`)).not.toBeInTheDocument();
    expect(mockDeleteAttachment).toHaveBeenCalledWith("a-1");
  });

  it("reports a failed upload of a pasted screenshot, with a retry", async () => {
    mockUploadAttachment.mockRejectedValueOnce(
      new AttachmentUploadError("unknown", "Não foi possível enviar o arquivo."),
    );
    renderComposer();

    paste(clipboard([screenshot()]));

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Não foi possível enviar o arquivo.",
    );
    expect(screen.getByText("Tentar novamente")).toBeInTheDocument();
  });

  it("leaves the caret in the editor so typing continues uninterrupted", async () => {
    renderComposer();
    editor().focus();

    paste(clipboard([screenshot()]));

    await waitFor(() => expect(mockUploadAttachment).toHaveBeenCalledOnce());
    expect(document.activeElement).toBe(editor());
  });
});

// ── Text stays native ────────────────────────────────────────────────────────

describe("pasting without bytes", () => {
  it("leaves a text-only paste entirely to the editor", () => {
    renderComposer();
    editor().focus();

    paste(clipboard([], { text: "olá" }));

    expect(editorText()).toContain("olá");
    expect(mockUploadAttachment).not.toHaveBeenCalled();
    expect(screen.queryByTestId("chat-composer-upload-status")).not.toBeInTheDocument();
  });

  it("does not treat pasted HTML as content of its own", () => {
    renderComposer();

    editor().focus();

    paste(
      clipboard([], {
        text: "<img src=x onerror=alert(1)>",
        items: [{ kind: "string", type: "text/html" }],
      }),
    );

    // Text, and only text: the markup is the message the user pasted, never a
    // node the composer built out of it.
    expect(screen.getByTestId("chat-composer-box").querySelector("img")).toBeNull();
    expect(editorText()).toContain("<img src=x onerror=alert(1)>");
    expect(mockUploadAttachment).not.toHaveBeenCalled();
  });

  it("never fetches an image a paste only referenced by URL", () => {
    const fetchSpy = vi.spyOn(globalThis, "fetch");
    renderComposer();

    editor().focus();

    paste(
      clipboard([], {
        text: "https://exemplo.invalido/foto.png",
        items: [{ kind: "string", type: "text/plain" }],
      }),
    );

    expect(editorText()).toContain("https://exemplo.invalido/foto.png");
    expect(fetchSpy).not.toHaveBeenCalled();
    expect(mockUploadAttachment).not.toHaveBeenCalled();
  });

  it("ignores an item the browser refuses to materialise", () => {
    renderComposer();

    paste(clipboard([], { items: [{ kind: "file", getAsFile: () => null }] }));

    expect(mockUploadAttachment).not.toHaveBeenCalled();
    expect(screen.queryByTestId("chat-composer-upload-status")).not.toBeInTheDocument();
  });

  it("ignores a paste with no clipboard data at all", () => {
    renderComposer();

    paste();

    expect(mockUploadAttachment).not.toHaveBeenCalled();
    expect(screen.queryByTestId("chat-composer-upload-status")).not.toBeInTheDocument();
  });

  it("stays native when the composer has nowhere to put a file", () => {
    renderComposer(null);

    paste(clipboard([screenshot()]));

    expect(mockUploadAttachment).not.toHaveBeenCalled();
    expect(screen.queryByTestId("chat-composer-upload-status")).not.toBeInTheDocument();
  });
});

// ── The limits the picker already enforces ───────────────────────────────────

describe("pasted files under the workspace limits", () => {
  it("refuses active content, exactly like the picker", async () => {
    renderComposer();

    paste(clipboard([fileOf(64, "page.html", "text/html")]));

    expect(
      await screen.findByText("HTML, SVG e executáveis não podem ser anexados."),
    ).toBeInTheDocument();
    expect(mockUploadAttachment).not.toHaveBeenCalled();
  });

  it("refuses a screenshot above the per-file limit", async () => {
    renderComposer();

    paste(clipboard([screenshot(LIMIT + 1)]));

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "O arquivo excede o limite permitido de 8 MiB.",
    );
    expect(mockUploadAttachment).not.toHaveBeenCalled();
  });

  it("stops at the conversation's maximum number of attachments", async () => {
    renderComposer(
      { kind: "channel", id: "ch-1" },
      {
        maxUploadBytes: LIMIT,
        maxFiles: 1,
        maxBytes: 512 * MIB,
      },
    );

    paste(clipboard([screenshot(2048)]));
    paste(clipboard([fileOf(4096, "image.png", "image/png", PASTED_AT + 60_000)]));

    expect(
      await screen.findByText("Esta conversa permite até 1 anexos por mensagem."),
    ).toBeInTheDocument();
    await waitFor(() => expect(mockUploadAttachment).toHaveBeenCalledOnce());
  });

  it("stops at the conversation's aggregate size", async () => {
    renderComposer(
      { kind: "channel", id: "ch-1" },
      {
        maxUploadBytes: LIMIT,
        maxFiles: 10,
        maxBytes: 3072,
      },
    );

    paste(clipboard([screenshot(2048)]));
    paste(clipboard([fileOf(2048, "image.png", "image/png", PASTED_AT + 60_000)]));

    expect(
      await screen.findByText("O tamanho total dos anexos excede o limite da conversa."),
    ).toBeInTheDocument();
    await waitFor(() => expect(mockUploadAttachment).toHaveBeenCalledOnce());
  });
});

// ── Mixed clipboards ─────────────────────────────────────────────────────────

describe("pasting text alongside an image", () => {
  it("attaches the image and still lets the editor keep the text", async () => {
    renderComposer();
    editor().focus();

    paste(clipboard([screenshot()], { text: "legenda" }));

    await waitFor(() => expect(mockUploadAttachment).toHaveBeenCalledOnce());
    expect(editorText()).toContain("legenda");
  });

  it("coexists with text the user typed, and sends both", async () => {
    const view = renderComposer();
    // Focused rather than clicked: ProseMirror answers a mousedown by asking
    // the document what is at those coordinates, and jsdom has no layout.
    editor().focus();
    await userEvent.keyboard("legenda");

    paste(clipboard([screenshot()]));
    expect(await screen.findByText("Pronto para enviar")).toBeInTheDocument();
    await userEvent.click(screen.getByTestId("chat-send-btn"));

    expect(view.onSend).toHaveBeenCalledWith("legenda", ["a-1"]);
  });
});
