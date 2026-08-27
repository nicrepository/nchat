import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

/**
 * Code review regression (issue #670): the voice recorder must never combine
 * with pending attachments. Recording a voice message sends through
 * `onSend("", [attachmentId])` directly — it does not go through
 * handleComposerSend and does not know about `pendingAttachments` — so
 * starting a recording while an attachment is queued, uploading or already
 * uploaded would either silently drop that attachment from the next send or
 * require merging two independent media types with no UX for it. Neither is
 * acceptable, so recording must not even start while one exists.
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
import type { SendResult } from "./useMessages";

const MIB = 1024 * 1024;

function fileOfSize(bytes: number, name = "relatorio.pdf"): File {
  const file = new File(["x"], name, { type: "application/pdf" });
  Object.defineProperty(file, "size", { value: bytes });
  return file;
}

function uploadedAttachment() {
  return {
    id: "a-1",
    filename: "relatorio.pdf",
    contentType: "application/pdf",
    size: 1024,
    status: "pending_scan" as const,
    previewStatus: "unsupported" as const,
    createdAt: "2026-08-03T12:00:00Z",
  };
}

const limits: WorkspaceAttachmentLimits = {
  maxUploadBytes: 8 * MIB,
  maxFiles: 10,
  maxBytes: 512 * MIB,
};

function renderComposer() {
  const onSend = vi.fn<(body: string) => Promise<SendResult>>();
  onSend.mockResolvedValue({ status: "sent" });
  const view = render(
    <ChatComposer
      bodyFormat="v2"
      placeholder="Mensagem..."
      onSend={onSend}
      uploadTarget={{ kind: "channel", id: "ch-1" }}
      attachmentLimits={limits}
    />,
  );
  return { ...view, onSend };
}

const fileInput = () => screen.getByTestId("chat-composer-file-input") as HTMLInputElement;
const composerBox = () => screen.getByTestId("chat-composer-box");
const recordButton = () => screen.getByTestId("chat-composer-record-btn") as HTMLButtonElement;

/** A DataTransfer carrying the given files, as a real drag/drop would. */
function dropData(files: File[]): DataTransfer {
  return {
    files,
    items: files as unknown as DataTransferItemList,
    types: files.length > 0 ? ["Files"] : ["text/plain"],
    getData: vi.fn(() => ""),
    setData: vi.fn(),
    clearData: vi.fn(),
    dropEffect: "none",
    setDragImage: vi.fn(),
  } as unknown as DataTransfer;
}

// A minimal MediaRecorder fake, just enough to drive the state machine into
// an actual `recording` phase — the disabled-button tests above never reach
// this far, but the drag-and-drop regression below needs a real recording in
// progress, not just the mic button's own disabled state.
class FakeMediaRecorder {
  static isTypeSupported(type: string): boolean {
    return type === "audio/webm;codecs=opus";
  }
  state: "inactive" | "recording" | "paused" = "inactive";
  ondataavailable: ((event: { data: Blob }) => void) | null = null;
  onstop: (() => void) | null = null;
  onerror: (() => void) | null = null;

  start(): void {
    this.state = "recording";
  }
  stop(): void {
    this.state = "inactive";
    this.ondataavailable?.({ data: new Blob(["chunk"]) });
    this.onstop?.();
  }
}

function fakeMicStream(): MediaStream {
  const tracks = [{ stop: vi.fn() }];
  return { getTracks: () => tracks } as unknown as MediaStream;
}

// The mic button is only rendered when the recorder reports itself
// `supported`, which requires both getUserMedia and an accepted
// MediaRecorder format — neither exists in jsdom by default.
let getUserMedia: ReturnType<typeof vi.fn>;
const originalMediaDevices = navigator.mediaDevices;

beforeEach(() => {
  mockFetchMentionCandidates.mockReset().mockResolvedValue([]);
  mockUploadAttachment.mockReset().mockResolvedValue(uploadedAttachment());
  mockDeleteAttachment.mockReset().mockResolvedValue(undefined);
  getUserMedia = vi.fn(async () => fakeMicStream());
  Object.defineProperty(navigator, "mediaDevices", {
    value: { getUserMedia },
    configurable: true,
  });
  vi.stubGlobal("MediaRecorder", FakeMediaRecorder);
});

afterEach(() => {
  Object.defineProperty(navigator, "mediaDevices", {
    value: originalMediaDevices,
    configurable: true,
  });
  vi.unstubAllGlobals();
  vi.clearAllMocks();
});

describe("voice recording vs pending attachments", () => {
  it("enables the record button when the composer holds no attachment", () => {
    renderComposer();

    expect(recordButton()).toBeEnabled();
  });

  it("disables the record button once an attachment reaches success, and never starts a recording", async () => {
    renderComposer();

    await userEvent.upload(fileInput(), fileOfSize(1024));
    // "pronto para enviar" — the same success state handleComposerSend reads
    // pendingAttachments from.
    await screen.findByTestId("chat-composer-pending-attachment");

    expect(recordButton()).toBeDisabled();

    // Defensive: even a click reaching a disabled button must not start a
    // recording. fireEvent, unlike userEvent, does not itself refuse to
    // dispatch on a disabled element, so this also exercises the click
    // handler's own guard.
    fireEvent.click(recordButton());
    expect(getUserMedia).not.toHaveBeenCalled();

    // The attachment itself is untouched: still listed, still sendable.
    expect(screen.getByTestId("chat-composer-pending-attachment")).toBeInTheDocument();
  });

  it("disables the record button while an attachment is still uploading, before it reaches success", async () => {
    let resolveUpload: (value: unknown) => void = () => undefined;
    mockUploadAttachment.mockReset().mockImplementation(
      () =>
        new Promise((resolve) => {
          resolveUpload = resolve;
        }),
    );
    renderComposer();

    await userEvent.upload(fileInput(), fileOfSize(1024));
    await waitFor(() => expect(mockUploadAttachment).toHaveBeenCalledOnce());

    expect(recordButton()).toBeDisabled();

    resolveUpload(uploadedAttachment());
    await screen.findByTestId("chat-composer-pending-attachment");
    expect(recordButton()).toBeDisabled();
  });

  it("re-enables the record button once the attachment is removed", async () => {
    renderComposer();

    await userEvent.upload(fileInput(), fileOfSize(1024));
    await screen.findByTestId("chat-composer-pending-attachment");
    expect(recordButton()).toBeDisabled();

    await userEvent.click(screen.getByTestId("chat-composer-remove-attachment"));

    await waitFor(() => expect(recordButton()).toBeEnabled());
  });

  it("gives an accessible reason for the disabled state without relying on color", async () => {
    renderComposer();

    await userEvent.upload(fileInput(), fileOfSize(1024));
    await screen.findByTestId("chat-composer-pending-attachment");

    expect(recordButton()).toHaveAttribute("disabled");
    expect(recordButton().title).toMatch(/anexo/i);
    // The action name itself stays stable — a screen reader still announces
    // what the control is for, not just that it is unavailable.
    expect(recordButton()).toHaveAccessibleName("Gravar mensagem de voz");
  });
});

// The inverse of the block above: the picker and the attach button already
// disappear while recording (the whole toolbar row is hidden), but the
// composer box's own drag-and-drop handlers stay bound regardless — they are
// what this regression is about. A voice recording must stay just as
// isolated from a dropped file as it is from an already-selected one.
describe("voice recording vs drag-and-drop attachments", () => {
  async function startRecording() {
    renderComposer();
    await userEvent.click(recordButton());
    // "recording", not merely "requesting_permission": the pause/stop
    // controls only render once MediaRecorder has actually started.
    await screen.findByTestId("chat-voice-pauseresume");
  }

  it("dragover during recording still suppresses native browser handling but shows no drop target", async () => {
    await startRecording();

    // fireEvent returns false when a cancelable event's preventDefault() was
    // called — this is what stops the browser from taking over the drag
    // (e.g. opening the file) even though the drop itself will be a no-op.
    const notCancelled = fireEvent.dragOver(composerBox(), {
      dataTransfer: dropData([fileOfSize(1024)]),
    });

    expect(notCancelled).toBe(false);
    expect(composerBox()).not.toHaveClass("chat-msg-area__composer-box--drag");
  });

  it("drop during recording is fully ignored: no upload, no attachment, recording keeps running", async () => {
    await startRecording();

    fireEvent.dragOver(composerBox(), { dataTransfer: dropData([fileOfSize(1024)]) });
    expect(composerBox()).not.toHaveClass("chat-msg-area__composer-box--drag");

    const notCancelled = fireEvent.drop(composerBox(), {
      dataTransfer: dropData([fileOfSize(1024)]),
    });
    expect(notCancelled).toBe(false);

    // Nothing was accepted: no upload started, no attachment item rendered,
    // no leftover "drop accepted" visual state.
    expect(mockUploadAttachment).not.toHaveBeenCalled();
    expect(screen.queryByTestId("chat-composer-upload-status")).not.toBeInTheDocument();
    expect(screen.queryByTestId("chat-composer-pending-attachment")).not.toBeInTheDocument();
    expect(composerBox()).not.toHaveClass("chat-msg-area__composer-box--drag");

    // The recording itself was completely undisturbed by the drop.
    expect(screen.getByTestId("chat-voice-recorder")).toBeInTheDocument();
    expect(screen.getByTestId("chat-voice-pauseresume")).toBeInTheDocument();
  });
});
