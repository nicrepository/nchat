import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

/**
 * RF-32: sending an already-uploaded attachment.
 *
 * The defect these tests exist for is the whole feature's original bug: the
 * upload succeeded, the attachment was persisted, and the composer then threw
 * the reference away — so Enviar stayed disabled and no message ever carried
 * the file. Every assertion below is about the reference surviving from the
 * upload to the send, and about the bytes never travelling twice.
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
import type { ChannelAttachment } from "./chatTypes";
import type { SendResult } from "./useMessages";

const attachment: ChannelAttachment = {
  id: "att-1",
  filename: "relatorio.pdf",
  contentType: "application/pdf",
  size: 1024,
  status: "pending_scan",
  previewStatus: "pending",
  createdAt: "",
};

function pdf(name = "relatorio.pdf"): File {
  return new File(["x"], name, { type: "application/pdf" });
}

type SendFn = (body: string, attachmentIds?: string[]) => Promise<SendResult>;

function renderComposer(
  onSend: ReturnType<typeof vi.fn<SendFn>>,
  target: { kind: "channel" | "dm"; id: string } | null = { kind: "channel", id: "ch-1" },
) {
  return render(
    <ChatComposer
      bodyFormat="v2"
      placeholder="Mensagem..."
      onSend={onSend}
      uploadTarget={target}
      attachmentLimits={{
        maxUploadBytes: 8 * 1024 * 1024,
        maxFiles: 10,
        maxBytes: 512 * 1024 * 1024,
      }}
    />,
  );
}

function makeOnSend(result: SendResult = { status: "sent" }) {
  const onSend = vi.fn<SendFn>();
  onSend.mockResolvedValue(result);
  return onSend;
}

async function uploadAFile(user: ReturnType<typeof userEvent.setup>) {
  await user.upload(screen.getByTestId("chat-composer-file-input"), pdf());
  await screen.findByTestId("chat-composer-pending-attachment");
}

const sendButton = () => screen.getByTestId("chat-send-btn");

/**
 * Types into the TipTap editor by pasting: a click into ProseMirror needs
 * layout jsdom does not have, and the text is all these tests care about.
 */
async function fillEditor(text: string) {
  const element = screen.getByTestId("chat-composer-input");
  fireEvent.paste(element, {
    clipboardData: {
      getData: (type: string) => (type === "text/plain" ? text : ""),
      types: ["text/plain"],
      files: [],
    },
  });
  await waitFor(() => expect(element).toHaveTextContent(text));
}

beforeEach(() => {
  mockFetchMentionCandidates.mockReset().mockResolvedValue([]);
  mockUploadAttachment.mockReset().mockResolvedValue(attachment);
  mockDeleteAttachment.mockReset().mockResolvedValue(undefined);
});

afterEach(() => {
  vi.clearAllMocks();
});

describe("composer attachment send", () => {
  it("returns focus to the composer after selecting multiple documents", async () => {
    const user = userEvent.setup();
    renderComposer(makeOnSend());
    const picker = screen.getByTestId("chat-composer-file-input");
    picker.focus();

    await user.upload(picker, [pdf("um.pdf"), pdf("dois.pdf")]);
    await waitFor(() =>
      expect(screen.getAllByTestId("chat-composer-pending-attachment")).toHaveLength(2),
    );

    await waitFor(() => expect(screen.getByTestId("chat-composer-input")).toHaveFocus());
  });

  it("keeps the persisted attachment id after the upload", async () => {
    const user = userEvent.setup();
    renderComposer(makeOnSend());

    await uploadAFile(user);

    // The name is what the user sees; the id is what the send will carry, and
    // the next test is what proves it survived.
    expect(screen.getByTestId("chat-composer-pending-attachment")).toHaveTextContent(
      "relatorio.pdf",
    );
  });

  it("enables Enviar with an empty editor once an attachment is pending", async () => {
    const user = userEvent.setup();
    renderComposer(makeOnSend());

    expect(sendButton()).toBeDisabled();
    await uploadAFile(user);

    await waitFor(() => expect(sendButton()).toBeEnabled());
  });

  it("sends an attachment-only message with no body", async () => {
    const user = userEvent.setup();
    const onSend = makeOnSend();
    renderComposer(onSend);

    await uploadAFile(user);
    await waitFor(() => expect(sendButton()).toBeEnabled());
    await user.click(sendButton());

    await waitFor(() => expect(onSend).toHaveBeenCalledTimes(1));
    expect(onSend).toHaveBeenCalledWith("", ["att-1"]);
    expect(mockDeleteAttachment).not.toHaveBeenCalled();
    // The bytes went up when the file was chosen. Pressing Enviar links a
    // reference; it must never upload anything again.
    expect(mockUploadAttachment).toHaveBeenCalledTimes(1);
  });

  it("sends text and attachment together", async () => {
    const user = userEvent.setup();
    const onSend = makeOnSend();
    renderComposer(onSend);

    await uploadAFile(user);
    await fillEditor("veja isto");
    await user.click(sendButton());

    await waitFor(() => expect(onSend).toHaveBeenCalledTimes(1));
    expect(onSend).toHaveBeenCalledWith("veja isto", ["att-1"]);
    expect(mockUploadAttachment).toHaveBeenCalledTimes(1);
  });

  it("keeps Enviar unavailable while the upload is still running", async () => {
    const user = userEvent.setup();
    let finishUpload: (value: ChannelAttachment) => void = () => undefined;
    mockUploadAttachment.mockReturnValue(
      new Promise<ChannelAttachment>((resolve) => {
        finishUpload = resolve;
      }),
    );
    renderComposer(makeOnSend());

    await user.upload(screen.getByTestId("chat-composer-file-input"), pdf());
    await screen.findByTestId("chat-composer-upload-status");

    expect(sendButton()).toBeDisabled();

    finishUpload(attachment);
    await screen.findByTestId("chat-composer-pending-attachment");
    await waitFor(() => expect(sendButton()).toBeEnabled());
  });

  it("returns to the empty state when the pending attachment is removed", async () => {
    const user = userEvent.setup();
    const onSend = makeOnSend();
    renderComposer(onSend);

    await uploadAFile(user);
    await user.click(screen.getByTestId("chat-composer-remove-attachment"));

    await waitFor(() =>
      expect(screen.queryByTestId("chat-composer-pending-attachment")).not.toBeInTheDocument(),
    );
    expect(sendButton()).toBeDisabled();
    expect(onSend).not.toHaveBeenCalled();
  });

  it("clears the pending attachment after a confirmed send", async () => {
    const user = userEvent.setup();
    renderComposer(makeOnSend({ status: "sent" }));

    await uploadAFile(user);
    await user.click(sendButton());

    await waitFor(() =>
      expect(screen.queryByTestId("chat-composer-pending-attachment")).not.toBeInTheDocument(),
    );
    expect(sendButton()).toBeDisabled();
  });

  it("preserves the pending attachment when the send goes stale", async () => {
    const user = userEvent.setup();
    renderComposer(makeOnSend({ status: "stale" }));

    await uploadAFile(user);
    await user.click(sendButton());

    // Still there, still sendable: the file is on the server and re-uploading
    // it to retry would be the one thing this feature must never do.
    expect(await screen.findByTestId("chat-composer-pending-attachment")).toBeInTheDocument();
    await waitFor(() => expect(sendButton()).toBeEnabled());
    expect(mockUploadAttachment).toHaveBeenCalledTimes(1);
  });

  it("preserves the pending attachment when the send fails", async () => {
    const user = userEvent.setup();
    const onSend = vi.fn<SendFn>().mockRejectedValue(new Error("network"));
    renderComposer(onSend);

    await uploadAFile(user);
    await user.click(sendButton());

    await waitFor(() => expect(onSend).toHaveBeenCalledTimes(1));
    expect(screen.getByTestId("chat-composer-pending-attachment")).toBeInTheDocument();
    await waitFor(() => expect(sendButton()).toBeEnabled());
  });

  it("does not carry an attachment from a previous destination", async () => {
    const user = userEvent.setup();
    const onSend = makeOnSend();
    const { rerender } = renderComposer(onSend);

    await uploadAFile(user);

    rerender(
      <ChatComposer
        bodyFormat="v2"
        placeholder="Mensagem..."
        onSend={onSend}
        uploadTarget={{ kind: "dm", id: "dm-9" }}
      />,
    );

    await waitFor(() =>
      expect(screen.queryByTestId("chat-composer-pending-attachment")).not.toBeInTheDocument(),
    );
    expect(sendButton()).toBeDisabled();
    await fillEditor("nova conversa");
    await user.click(sendButton());

    await waitFor(() => expect(onSend).toHaveBeenCalledTimes(1));
    expect(onSend).toHaveBeenCalledWith("nova conversa", undefined);
  });
});
