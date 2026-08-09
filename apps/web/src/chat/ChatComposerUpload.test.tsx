import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

/**
 * RF-32 (issue #458): the composer's attachment pipeline, exercised through the
 * real component rather than through filesApi alone.
 *
 * These are the tests that would have caught the reviewed defect — an upload
 * client with no production caller — so they assert the boundary calls the
 * component makes, not the internals of the hook behind it.
 */

const { mockFetchMentionCandidates, mockUploadLimit, mockUploadAttachment } = vi.hoisted(() => ({
  mockFetchMentionCandidates: vi.fn(),
  mockUploadLimit: vi.fn(),
  mockUploadAttachment: vi.fn(),
}));

vi.mock("./chatApi", () => ({
  fetchMentionCandidates: (...args: unknown[]) => mockFetchMentionCandidates(...args),
  fetchWorkspaceUploadLimit: () => mockUploadLimit(),
}));

vi.mock("./filesApi", async () => {
  // The error type and the message helper are the production ones: the point is
  // that the component renders what filesApi normalised, not a second copy.
  const actual = await vi.importActual<typeof import("./filesApi")>("./filesApi");
  return {
    ...actual,
    uploadAttachment: (...args: unknown[]) => mockUploadAttachment(...args),
  };
});

import { ApiRequestError } from "../lib/api";
import ChatComposer from "./ChatComposer";
import { AttachmentUploadError } from "./filesApi";
import type { SendResult } from "./useMessages";

const MIB = 1024 * 1024;
const LIMIT = 8 * MIB;

/** A File whose size is declared, never materialised. */
function fileOfSize(bytes: number, name = "relatorio.pdf"): File {
  const file = new File(["x"], name, { type: "application/pdf" });
  Object.defineProperty(file, "size", { value: bytes });
  return file;
}

function uploadedAttachment(filename = "relatorio.pdf") {
  return {
    id: "a-1",
    filename,
    contentType: "application/pdf",
    size: 1024,
    status: "pending_scan" as const,
    createdAt: "2026-08-03T12:00:00Z",
  };
}

function renderComposer(
  target: { kind: "channel" | "dm"; id: string } | null = { kind: "channel", id: "ch-1" },
  onAttachmentUploaded?: () => void,
) {
  const onSend = vi.fn<(body: string) => Promise<SendResult>>();
  onSend.mockResolvedValue({ status: "sent" });
  return render(
    <ChatComposer
      bodyFormat="v2"
      placeholder="Mensagem..."
      onSend={onSend}
      uploadTarget={target}
      onAttachmentUploaded={onAttachmentUploaded}
    />,
  );
}

const fileInput = () => screen.getByTestId("chat-composer-file-input") as HTMLInputElement;
const composerBox = () => screen.getByTestId("chat-composer-box");

/** A DataTransfer carrying the given files, as a real drop would. */
function dropData(files: File[]): DataTransfer {
  return {
    files,
    items: files as unknown as DataTransferItemList,
    types: files.length > 0 ? ["Files"] : ["text/plain"],
    getData: vi.fn(() => ""),
    setData: vi.fn(),
    clearData: vi.fn(),
    dropEffect: "none",
    effectAllowed: "all",
    setDragImage: vi.fn(),
  } as unknown as DataTransfer;
}

beforeEach(() => {
  mockFetchMentionCandidates.mockReset().mockResolvedValue([]);
  mockUploadLimit.mockReset().mockResolvedValue(LIMIT);
  mockUploadAttachment.mockReset().mockResolvedValue(uploadedAttachment());
});

afterEach(() => {
  vi.clearAllMocks();
});

// ── Picker ───────────────────────────────────────────────────────────────────

describe("composer file picker", () => {
  it("uploads a file below the limit", async () => {
    renderComposer();

    await userEvent.upload(fileInput(), fileOfSize(LIMIT - 1));

    await waitFor(() => expect(mockUploadAttachment).toHaveBeenCalledOnce());
    expect(mockUploadAttachment.mock.calls[0][0]).toEqual({ kind: "channel", id: "ch-1" });
    expect(mockUploadAttachment.mock.calls[0][2]).toBe(LIMIT);
  });

  it("uploads a file exactly at the limit", async () => {
    renderComposer();

    await userEvent.upload(fileInput(), fileOfSize(LIMIT));

    await waitFor(() => expect(mockUploadAttachment).toHaveBeenCalledOnce());
  });

  it("refuses a file above the limit without touching the network", async () => {
    renderComposer();

    await userEvent.upload(fileInput(), fileOfSize(LIMIT + 1));

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "O arquivo excede o limite permitido de 8 MiB.",
    );
    expect(mockUploadAttachment).not.toHaveBeenCalled();
  });

  it("accepts the same file again after a failure", async () => {
    renderComposer();
    const file = fileOfSize(LIMIT + 1);

    await userEvent.upload(fileInput(), file);
    expect(await screen.findByRole("alert")).toBeInTheDocument();
    // The input value is cleared on change, so re-choosing the same file still
    // fires — the browser would report "no change" otherwise.
    expect(fileInput().value).toBe("");

    await userEvent.upload(fileInput(), fileOfSize(LIMIT - 1));
    await waitFor(() => expect(mockUploadAttachment).toHaveBeenCalledOnce());
  });

  it("notifies the caller so the destination's file list can reconcile", async () => {
    const onUploaded = vi.fn();
    renderComposer({ kind: "channel", id: "ch-1" }, onUploaded);

    await userEvent.upload(fileInput(), fileOfSize(1024));

    await waitFor(() => expect(onUploaded).toHaveBeenCalledOnce());
  });

  it("is absent entirely when the composer has no destination", () => {
    renderComposer(null);

    expect(screen.queryByTestId("chat-composer-file-input")).not.toBeInTheDocument();
    expect(screen.queryByTestId("chat-composer-attach-btn")).not.toBeInTheDocument();
  });
});

// ── Drag and drop ────────────────────────────────────────────────────────────

describe("composer drag and drop", () => {
  it("uploads a dropped file below the limit", async () => {
    renderComposer();

    fireEvent.drop(composerBox(), { dataTransfer: dropData([fileOfSize(LIMIT - 1)]) });

    await waitFor(() => expect(mockUploadAttachment).toHaveBeenCalledOnce());
    expect(mockUploadAttachment.mock.calls[0][0]).toEqual({ kind: "channel", id: "ch-1" });
  });

  it("refuses a dropped file above the limit, exactly like the picker", async () => {
    renderComposer();

    fireEvent.drop(composerBox(), { dataTransfer: dropData([fileOfSize(LIMIT + 1)]) });

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "O arquivo excede o limite permitido de 8 MiB.",
    );
    expect(mockUploadAttachment).not.toHaveBeenCalled();
  });

  it("ignores a drop that carries no file", () => {
    renderComposer();

    fireEvent.drop(composerBox(), { dataTransfer: dropData([]) });

    expect(mockUploadAttachment).not.toHaveBeenCalled();
    expect(screen.queryByTestId("chat-composer-upload-status")).not.toBeInTheDocument();
  });

  /**
   * A dragleave naming where the pointer went.
   *
   * Built by hand because fireEvent.dragLeave cannot: jsdom has no DragEvent
   * constructor, so testing-library falls back to a plain Event, which drops
   * `relatedTarget` — the one field this behaviour is about.
   */
  function dragLeaveTo(box: HTMLElement, relatedTarget: EventTarget | null) {
    fireEvent(box, new MouseEvent("dragleave", { bubbles: true, relatedTarget }));
  }

  // A dragleave fires on every internal boundary the pointer crosses. Ending
  // the drag state there makes the outline blink its way across the composer.
  it("keeps the drop hint while the pointer moves between the composer's own children", () => {
    renderComposer();
    const box = composerBox();

    fireEvent.dragOver(box, { dataTransfer: dropData([fileOfSize(1024)]) });
    expect(screen.getByText("Solte o arquivo para enviar.")).toBeInTheDocument();

    dragLeaveTo(box, box.querySelector("button"));

    expect(screen.getByText("Solte o arquivo para enviar.")).toBeInTheDocument();
  });

  it("drops the hint once the pointer really leaves the composer", () => {
    renderComposer();
    const box = composerBox();

    fireEvent.dragOver(box, { dataTransfer: dropData([fileOfSize(1024)]) });
    dragLeaveTo(box, document.body);

    expect(screen.queryByText("Solte o arquivo para enviar.")).not.toBeInTheDocument();
  });

  // Leaving the window at all reports no destination, which is outside by
  // definition — the hint must not stick to a drag that has gone.
  it("drops the hint when the drag leaves the window entirely", () => {
    renderComposer();
    const box = composerBox();

    fireEvent.dragOver(box, { dataTransfer: dropData([fileOfSize(1024)]) });
    dragLeaveTo(box, null);

    expect(screen.queryByText("Solte o arquivo para enviar.")).not.toBeInTheDocument();
  });

  it("ignores a drop when the composer is disabled", () => {
    const onSend = vi.fn<(body: string) => Promise<SendResult>>();
    onSend.mockResolvedValue({ status: "sent" });
    render(
      <ChatComposer
        bodyFormat="v2"
        placeholder="Mensagem..."
        onSend={onSend}
        disabled
        uploadTarget={{ kind: "channel", id: "ch-1" }}
      />,
    );

    fireEvent.drop(composerBox(), { dataTransfer: dropData([fileOfSize(1024)]) });

    expect(mockUploadAttachment).not.toHaveBeenCalled();
  });
});

// ── Destinations ─────────────────────────────────────────────────────────────

describe("composer upload destinations", () => {
  it("uses the DM target for a conversation", async () => {
    renderComposer({ kind: "dm", id: "dm-9" });

    await userEvent.upload(fileInput(), fileOfSize(1024));

    await waitFor(() => expect(mockUploadAttachment).toHaveBeenCalledOnce());
    expect(mockUploadAttachment.mock.calls[0][0]).toEqual({ kind: "dm", id: "dm-9" });
  });

  it("drops to the DM target too, through the same pipeline", async () => {
    renderComposer({ kind: "dm", id: "dm-9" });

    fireEvent.drop(composerBox(), { dataTransfer: dropData([fileOfSize(1024)]) });

    await waitFor(() => expect(mockUploadAttachment).toHaveBeenCalledOnce());
    expect(mockUploadAttachment.mock.calls[0][0]).toEqual({ kind: "dm", id: "dm-9" });
  });

  it("never reuses the previous destination after a target change", async () => {
    const { rerender } = renderComposer({ kind: "channel", id: "ch-1" });
    const onSend = vi.fn<(body: string) => Promise<SendResult>>();
    onSend.mockResolvedValue({ status: "sent" });

    rerender(
      <ChatComposer
        bodyFormat="v2"
        placeholder="Mensagem..."
        onSend={onSend}
        uploadTarget={{ kind: "dm", id: "dm-2" }}
      />,
    );
    await userEvent.upload(fileInput(), fileOfSize(1024));

    await waitFor(() => expect(mockUploadAttachment).toHaveBeenCalledOnce());
    expect(mockUploadAttachment.mock.calls[0][0]).toEqual({ kind: "dm", id: "dm-2" });
  });
});

// ── States ───────────────────────────────────────────────────────────────────

describe("composer upload state", () => {
  it("shows progress while the request is pending and clears it on success", async () => {
    let resolveUpload: (value: unknown) => void = () => {};
    mockUploadAttachment.mockReturnValue(
      new Promise((resolve) => {
        resolveUpload = resolve;
      }),
    );
    renderComposer();

    await userEvent.upload(fileInput(), fileOfSize(1024));

    expect(await screen.findByText("Enviando arquivo…")).toBeInTheDocument();
    expect(screen.getByTestId("chat-composer-attach-btn")).toBeDisabled();

    resolveUpload(uploadedAttachment("relatorio.pdf"));

    expect(await screen.findByText(/Arquivo enviado: relatorio\.pdf/)).toBeInTheDocument();
    await waitFor(() => expect(screen.getByTestId("chat-composer-attach-btn")).toBeEnabled());
  });

  it("does not start a second upload while one is in flight", async () => {
    mockUploadAttachment.mockReturnValue(new Promise(() => {}));
    renderComposer();

    fireEvent.drop(composerBox(), { dataTransfer: dropData([fileOfSize(1024)]) });
    await waitFor(() => expect(mockUploadAttachment).toHaveBeenCalledOnce());
    fireEvent.drop(composerBox(), { dataTransfer: dropData([fileOfSize(2048)]) });

    expect(mockUploadAttachment).toHaveBeenCalledOnce();
  });

  it("restores the composer after a failure so a retry is possible", async () => {
    mockUploadAttachment.mockRejectedValueOnce(
      new AttachmentUploadError("unknown", "Não foi possível enviar o arquivo."),
    );
    renderComposer();

    await userEvent.upload(fileInput(), fileOfSize(1024));
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Não foi possível enviar o arquivo.",
    );
    await waitFor(() => expect(screen.getByTestId("chat-composer-attach-btn")).toBeEnabled());

    mockUploadAttachment.mockResolvedValueOnce(uploadedAttachment());
    await userEvent.upload(fileInput(), fileOfSize(1024));

    await waitFor(() => expect(mockUploadAttachment).toHaveBeenCalledTimes(2));
    expect(await screen.findByText(/Arquivo enviado/)).toBeInTheDocument();
  });
});

// ── Progress (RF-30) ─────────────────────────────────────────────────────────
//
// The bar is drawn from what the transport counted, or not drawn at all. These
// exercise the seam between the two: the composer never derives a percentage
// from anything else, so an upload nobody measured stays indeterminate.

describe("composer upload progress", () => {
  /**
   * Starts an upload that never settles and hands back the progress callback
   * filesApi was given, so the test can report bytes the way apiUpload does.
   */
  function startMeasuredUpload() {
    let report: ((progress: { loaded: number; total: number }) => void) | undefined;
    let finish: (value: unknown) => void = () => {};
    mockUploadAttachment.mockImplementation((...args: unknown[]) => {
      report = args[4] as (progress: { loaded: number; total: number }) => void;
      return new Promise((resolve) => {
        finish = resolve;
      });
    });
    return {
      report: (loaded: number, total: number) => act(() => report?.({ loaded, total })),
      finish: (value: unknown) => act(() => finish(value)),
    };
  }

  it("shows a determinate bar for the bytes the transport reported", async () => {
    const upload = startMeasuredUpload();
    renderComposer();

    await userEvent.upload(fileInput(), fileOfSize(2048));
    await waitFor(() => expect(mockUploadAttachment).toHaveBeenCalledOnce());

    upload.report(512, 2048);

    const bar = screen.getByTestId("chat-composer-upload-progress") as HTMLProgressElement;
    expect(bar).toHaveAttribute("aria-label", "Progresso do envio");
    expect(bar.value).toBe(512);
    expect(bar.max).toBe(2048);
    expect(screen.getByText(/Enviando arquivo… 25%/)).toBeInTheDocument();
  });

  it("follows the transport rather than any local estimate", async () => {
    const upload = startMeasuredUpload();
    renderComposer();

    await userEvent.upload(fileInput(), fileOfSize(2048));
    await waitFor(() => expect(mockUploadAttachment).toHaveBeenCalledOnce());

    upload.report(512, 2048);
    upload.report(1536, 2048);

    expect((screen.getByTestId("chat-composer-upload-progress") as HTMLProgressElement).value).toBe(
      1536,
    );
    expect(screen.getByText(/Enviando arquivo… 75%/)).toBeInTheDocument();
  });

  // The honest case: nothing measured, so nothing is drawn. A bar with an
  // invented denominator would be a claim this client cannot support.
  it("stays indeterminate while the transport has reported nothing", async () => {
    mockUploadAttachment.mockReturnValue(new Promise(() => {}));
    renderComposer();

    await userEvent.upload(fileInput(), fileOfSize(2048));

    expect(await screen.findByText("Enviando arquivo…")).toBeInTheDocument();
    expect(screen.queryByTestId("chat-composer-upload-progress")).not.toBeInTheDocument();
  });

  it("removes the bar once the upload settles", async () => {
    const upload = startMeasuredUpload();
    renderComposer();

    await userEvent.upload(fileInput(), fileOfSize(2048));
    await waitFor(() => expect(mockUploadAttachment).toHaveBeenCalledOnce());
    upload.report(2048, 2048);
    expect(screen.getByTestId("chat-composer-upload-progress")).toBeInTheDocument();

    upload.finish(uploadedAttachment());

    expect(await screen.findByText(/Arquivo enviado/)).toBeInTheDocument();
    expect(screen.queryByTestId("chat-composer-upload-progress")).not.toBeInTheDocument();
  });

  // A report can land after the composer is gone: the request outlives the
  // component when the user switches conversations mid-upload.
  it("survives a report that arrives after the composer unmounted", async () => {
    const upload = startMeasuredUpload();
    const { unmount } = renderComposer();

    await userEvent.upload(fileInput(), fileOfSize(2048));
    await waitFor(() => expect(mockUploadAttachment).toHaveBeenCalledOnce());
    unmount();

    expect(() => upload.report(1024, 2048)).not.toThrow();
  });
});

// ── Errors ───────────────────────────────────────────────────────────────────

describe("composer upload errors", () => {
  // Which HTTP shapes map to "too_large" — the service's structured 413 and the
  // gateway's bare one — is filesApi's job and is covered in filesApi.test.ts.
  // What matters here is that the composer renders whatever it normalised.
  it("shows a normalised size rejection from the backend", async () => {
    mockUploadAttachment.mockRejectedValueOnce(
      new AttachmentUploadError("too_large", "O arquivo excede o limite permitido de 8 MiB."),
    );
    renderComposer();

    await userEvent.upload(fileInput(), fileOfSize(1024));

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "O arquivo excede o limite permitido de 8 MiB.",
    );
  });

  it("keeps a generic failure from being reported as an oversized file", async () => {
    mockUploadAttachment.mockRejectedValueOnce(
      new AttachmentUploadError("unavailable", "O envio de arquivos está indisponível no momento."),
    );
    renderComposer();

    await userEvent.upload(fileInput(), fileOfSize(1024));

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("O envio de arquivos está indisponível no momento.");
    expect(alert).not.toHaveTextContent(/excede/);
  });

  it("never renders raw server text", async () => {
    mockUploadAttachment.mockRejectedValueOnce(
      new ApiRequestError(500, "internal_error", "pq: connection refused to db-primary.internal"),
    );
    renderComposer();

    await userEvent.upload(fileInput(), fileOfSize(1024));

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("Não foi possível enviar o arquivo.");
    expect(alert).not.toHaveTextContent("db-primary.internal");
  });
});

// ── Limit resolution ─────────────────────────────────────────────────────────

describe("composer upload limit resolution", () => {
  it("re-reads the policy on every attempt, never caching it", async () => {
    renderComposer();

    await userEvent.upload(fileInput(), fileOfSize(1024));
    await waitFor(() => expect(mockUploadAttachment).toHaveBeenCalledOnce());
    await userEvent.upload(fileInput(), fileOfSize(2048));
    await waitFor(() => expect(mockUploadAttachment).toHaveBeenCalledTimes(2));

    expect(mockUploadLimit).toHaveBeenCalledTimes(2);
  });

  // An administrator can tighten the policy while a composer is open. The
  // second attempt must be judged by the new limit, not by the one the first
  // attempt happened to read.
  it("uses a limit reduced between attempts", async () => {
    mockUploadLimit.mockResolvedValueOnce(8 * MIB).mockResolvedValueOnce(2 * MIB);
    renderComposer();

    await userEvent.upload(fileInput(), fileOfSize(4 * MIB));
    await waitFor(() => expect(mockUploadAttachment).toHaveBeenCalledOnce());
    expect(mockUploadAttachment.mock.calls[0][2]).toBe(8 * MIB);

    // The very same file is now too large.
    await userEvent.upload(fileInput(), fileOfSize(4 * MIB));

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "O arquivo excede o limite permitido de 2 MiB.",
    );
    expect(mockUploadAttachment).toHaveBeenCalledOnce();
    expect(mockUploadLimit).toHaveBeenCalledTimes(2);
  });

  // And the other direction: a file refused under the old policy must go
  // through once the policy is widened, with no reload and no remount.
  it("uses a limit raised between attempts", async () => {
    mockUploadLimit.mockResolvedValueOnce(2 * MIB).mockResolvedValueOnce(8 * MIB);
    renderComposer();

    await userEvent.upload(fileInput(), fileOfSize(4 * MIB));
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "O arquivo excede o limite permitido de 2 MiB.",
    );
    expect(mockUploadAttachment).not.toHaveBeenCalled();

    await userEvent.upload(fileInput(), fileOfSize(4 * MIB));

    await waitFor(() => expect(mockUploadAttachment).toHaveBeenCalledOnce());
    expect(mockUploadAttachment.mock.calls[0][2]).toBe(8 * MIB);
  });

  it("re-reads the policy for a newly mounted composer", async () => {
    const first = renderComposer({ kind: "channel", id: "ch-1" });
    await userEvent.upload(fileInput(), fileOfSize(1024));
    await waitFor(() => expect(mockUploadAttachment).toHaveBeenCalledOnce());
    first.unmount();

    // ChatMessageArea keys the composer by target, so a switch remounts it and
    // no limit read for the previous destination survives.
    mockUploadLimit.mockResolvedValue(2 * MIB);
    renderComposer({ kind: "dm", id: "dm-1" });
    await userEvent.upload(fileInput(), fileOfSize(1024));

    await waitFor(() => expect(mockUploadAttachment).toHaveBeenCalledTimes(2));
    expect(mockUploadAttachment.mock.calls[1][2]).toBe(2 * MIB);
  });

  it("still uploads when the policy cannot be read, leaving enforcement to the backend", async () => {
    mockUploadLimit.mockRejectedValue(new Error("network"));
    renderComposer();

    // Far larger than any real policy: with the limit unknown the client must
    // not refuse locally, because refusing would mean inventing a limit.
    await userEvent.upload(fileInput(), fileOfSize(900 * MIB));

    await waitFor(() => expect(mockUploadAttachment).toHaveBeenCalledOnce());
    expect(mockUploadAttachment.mock.calls[0][2]).toBeNull();
  });

  it("shows the backend's rejection when the policy was unknown", async () => {
    mockUploadLimit.mockResolvedValue(null);
    mockUploadAttachment.mockRejectedValueOnce(
      new AttachmentUploadError("too_large", "O arquivo excede o limite permitido."),
    );
    renderComposer();

    await userEvent.upload(fileInput(), fileOfSize(900 * MIB));

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "O arquivo excede o limite permitido.",
    );
  });
});
