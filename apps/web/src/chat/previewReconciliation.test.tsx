/**
 * Preview reconciliation, end to end on the client (RF-31, issue #464).
 *
 * The two hooks own different halves of the behaviour — useConversationDetails
 * re-reads the list while something is pending, useAttachmentPreview fetches the
 * bytes once a preview is ready — and the bug this covers lived exactly in the
 * seam between them: the panel stayed on `pending` forever because nobody
 * re-read the list.
 *
 * So this renders both together against mocked transports and asserts what the
 * user sees: an icon, then a thumbnail, with one request for the image.
 */

import { act, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import AttachmentThumbnail from "./AttachmentThumbnail";
import {
  previewReconcileDelayMs,
  previewReconcileIntervalMs,
  previewReconcileMaxAttempts,
  previewRevalidateIntervalMs,
  useConversationDetails,
  type ConversationDetailsTarget,
} from "./useConversationDetails";
import type { ChannelAttachment, ChannelDetails } from "./chatTypes";

const { mockFetchChannelDetails, mockFetchConversationAttachments, mockFetchAttachmentPreview } =
  vi.hoisted(() => ({
    mockFetchChannelDetails: vi.fn(),
    mockFetchConversationAttachments: vi.fn(),
    mockFetchAttachmentPreview: vi.fn(),
  }));

vi.mock("./chatApi", () => ({
  fetchChannelDetails: (id: string, signal?: AbortSignal) => mockFetchChannelDetails(id, signal),
  fetchGroupDetails: vi.fn(),
  fetchDirectProfile: vi.fn(),
}));

vi.mock("./filesApi", () => ({
  fetchConversationAttachments: (
    target: { kind: "channel" | "dm"; id: string },
    limit: number,
    signal?: AbortSignal,
  ) => mockFetchConversationAttachments(target, limit, signal),
  fetchAttachmentPreview: (id: string, signal?: AbortSignal) =>
    mockFetchAttachmentPreview(id, signal),
}));

const revokeObjectURL = vi.fn();

function attachment(overrides: Partial<ChannelAttachment> = {}): ChannelAttachment {
  return {
    id: "a-1",
    filename: "foto.png",
    contentType: "image/png",
    size: 2048,
    status: "clean",
    previewStatus: "pending",
    createdAt: "2026-07-15T12:00:00.000Z",
    ...overrides,
  };
}

/** Renders the list the way the panel does: a thumbnail with the icon behind it. */
function Files({ target }: { target: ConversationDetailsTarget }) {
  const state = useConversationDetails(target);
  if (state.files.status !== "ready") {
    return <p>carregando</p>;
  }
  return (
    <ul>
      {state.files.data.map((file) => (
        <li key={file.id}>
          <AttachmentThumbnail
            attachment={file}
            fallback={<span data-testid={`icon-${file.id}`} />}
          />
        </li>
      ))}
    </ul>
  );
}

beforeEach(() => {
  vi.useFakeTimers({ shouldAdvanceTime: true });
  mockFetchChannelDetails.mockResolvedValue({ id: "ch-1", name: "Infra" } as ChannelDetails);
  mockFetchAttachmentPreview.mockResolvedValue(new Blob(["jpeg-bytes"]));
  let created = 0;
  vi.stubGlobal("URL", {
    ...URL,
    createObjectURL: vi.fn(() => `blob:preview-${++created}`),
    revokeObjectURL,
  });
});

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
  vi.clearAllMocks();
});

async function advancePastInterval() {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(previewReconcileIntervalMs);
  });
}

/** Advances past the delay that follows `attempt` polls which saw no change. */
async function advancePastDelay(attempt: number) {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(previewReconcileDelayMs(attempt));
  });
}

/** Advances past the slow cadence that watches a displayed thumbnail. */
async function advancePastRevalidate() {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(previewRevalidateIntervalMs);
  });
}

// ── Revocation after the bytes are already on screen (SEC-001) ───────────────
//
// The panel fetched the preview while the attachment was clean and ready, and
// the user is looking at it. Then a rescan condemns the file. The server does
// the right thing for every *future* request, but the bytes it already answered
// with are in the page, and no amount of server-side enforcement takes them
// off. These tests are the client's half of that: notice, then remove.

describe("preview revocation", () => {
  it("removes a displayed thumbnail once the attachment is rejected", async () => {
    mockFetchConversationAttachments.mockResolvedValueOnce([
      attachment({ previewStatus: "ready" }),
    ]);
    // The backend keeps a ready preview under a rejected attachment on purpose.
    // `ready` alone must not be enough to keep it on screen.
    mockFetchConversationAttachments.mockResolvedValue([
      attachment({ status: "rejected", previewStatus: "ready" }),
    ]);

    render(<Files target={{ kind: "channel", id: "ch-1" }} />);

    const image = await screen.findByTestId("chat-details-file-thumb");
    expect(image).toHaveAttribute("src", "blob:preview-1");
    expect(mockFetchAttachmentPreview).toHaveBeenCalledTimes(1);

    await advancePastRevalidate();

    // Gone from the DOM, and the URL that was behind it released.
    await waitFor(() => expect(screen.getByTestId("icon-a-1")).toBeInTheDocument());
    expect(screen.queryByTestId("chat-details-file-thumb")).not.toBeInTheDocument();
    expect(revokeObjectURL).toHaveBeenCalledWith("blob:preview-1");
    // And the endpoint is not asked again: the attachment is not servable.
    expect(mockFetchAttachmentPreview).toHaveBeenCalledTimes(1);
  });

  // A rescan can also send a file back to the queue rather than condemn it.
  it("removes a displayed thumbnail when the attachment returns to pending_scan", async () => {
    mockFetchConversationAttachments.mockResolvedValueOnce([
      attachment({ previewStatus: "ready" }),
    ]);
    mockFetchConversationAttachments.mockResolvedValue([
      attachment({ status: "pending_scan", previewStatus: "ready" }),
    ]);

    render(<Files target={{ kind: "channel", id: "ch-1" }} />);
    await screen.findByTestId("chat-details-file-thumb");

    await advancePastRevalidate();

    await waitFor(() => expect(screen.getByTestId("icon-a-1")).toBeInTheDocument());
    expect(revokeObjectURL).toHaveBeenCalledWith("blob:preview-1");
    expect(mockFetchAttachmentPreview).toHaveBeenCalledTimes(1);
  });

  // Soft-delete, as the client sees it: the row stops being listed.
  it("removes a displayed thumbnail when the attachment leaves the listing", async () => {
    mockFetchConversationAttachments.mockResolvedValueOnce([
      attachment({ previewStatus: "ready" }),
    ]);
    mockFetchConversationAttachments.mockResolvedValue([]);

    render(<Files target={{ kind: "channel", id: "ch-1" }} />);
    await screen.findByTestId("chat-details-file-thumb");

    await advancePastRevalidate();

    await waitFor(() =>
      expect(screen.queryByTestId("chat-details-file-thumb")).not.toBeInTheDocument(),
    );
    expect(screen.queryByTestId("icon-a-1")).not.toBeInTheDocument();
    expect(revokeObjectURL).toHaveBeenCalledWith("blob:preview-1");
  });

  it.each(["unsupported", "failed"] as const)(
    "removes a displayed thumbnail when the preview becomes %s",
    async (previewStatus) => {
      mockFetchConversationAttachments.mockResolvedValueOnce([
        attachment({ previewStatus: "ready" }),
      ]);
      mockFetchConversationAttachments.mockResolvedValue([attachment({ previewStatus })]);

      render(<Files target={{ kind: "channel", id: "ch-1" }} />);
      await screen.findByTestId("chat-details-file-thumb");

      await advancePastRevalidate();

      await waitFor(() => expect(screen.getByTestId("icon-a-1")).toBeInTheDocument());
      expect(revokeObjectURL).toHaveBeenCalledWith("blob:preview-1");
      expect(mockFetchAttachmentPreview).toHaveBeenCalledTimes(1);
    },
  );

  // The steady state has to be cheap, or the watch would be worse than the bug:
  // one listing request per cadence, and no repeat of the bytes.
  it("does not refetch the bytes while the attachment stays clean and ready", async () => {
    mockFetchConversationAttachments.mockResolvedValue([attachment({ previewStatus: "ready" })]);

    render(<Files target={{ kind: "channel", id: "ch-1" }} />);
    const image = await screen.findByTestId("chat-details-file-thumb");

    await advancePastRevalidate();
    await advancePastRevalidate();
    await advancePastRevalidate();

    // Three rounds of watching, three listing reads, and the same object URL
    // throughout — no new Blob, no new URL, nothing revoked.
    expect(mockFetchConversationAttachments).toHaveBeenCalledTimes(4);
    expect(mockFetchAttachmentPreview).toHaveBeenCalledTimes(1);
    expect(image).toHaveAttribute("src", "blob:preview-1");
    expect(revokeObjectURL).not.toHaveBeenCalled();
  });

  // The race: the verdict lands while the bytes are still in flight. The answer
  // was authorised when it left and is not when it arrives.
  it("discards bytes that arrive after the rejection was observed", async () => {
    let resolvePreview: (blob: Blob) => void = () => {};
    mockFetchAttachmentPreview.mockImplementation(
      () => new Promise<Blob>((resolve) => (resolvePreview = resolve)),
    );
    mockFetchConversationAttachments.mockResolvedValueOnce([
      attachment({ previewStatus: "ready" }),
    ]);
    mockFetchConversationAttachments.mockResolvedValue([
      attachment({ status: "rejected", previewStatus: "ready" }),
    ]);

    render(<Files target={{ kind: "channel", id: "ch-1" }} />);
    await waitFor(() => expect(mockFetchAttachmentPreview).toHaveBeenCalledTimes(1));

    // The rejection is observed while the request is still open.
    await advancePastRevalidate();
    await waitFor(() => expect(screen.getByTestId("icon-a-1")).toBeInTheDocument());

    await act(async () => {
      resolvePreview(new Blob(["jpeg-bytes"]));
    });

    // The late answer never becomes an image, and never becomes a URL either —
    // so there is nothing left over to revoke or to render on a later commit.
    expect(screen.queryByTestId("chat-details-file-thumb")).not.toBeInTheDocument();
    expect(screen.getByTestId("icon-a-1")).toBeInTheDocument();
    expect(mockFetchAttachmentPreview).toHaveBeenCalledTimes(1);
  });

  it("stops watching and revokes when the panel unmounts", async () => {
    mockFetchConversationAttachments.mockResolvedValue([attachment({ previewStatus: "ready" })]);

    const { unmount } = render(<Files target={{ kind: "channel", id: "ch-1" }} />);
    await screen.findByTestId("chat-details-file-thumb");

    unmount();
    expect(revokeObjectURL).toHaveBeenCalledWith("blob:preview-1");

    const listCalls = mockFetchConversationAttachments.mock.calls.length;
    await advancePastRevalidate();
    await advancePastRevalidate();
    expect(mockFetchConversationAttachments).toHaveBeenCalledTimes(listCalls);
  });
});

describe("preview reconciliation", () => {
  // The default path, from the user's side: they send a file, the scan clears
  // it, the worker renders it, and the icon becomes a thumbnail — with the
  // panel never closed and no request for the bytes until both halves are done.
  it("turns the icon into a thumbnail across the scan and the render", async () => {
    mockFetchConversationAttachments.mockResolvedValueOnce([
      attachment({ status: "pending_scan" }),
    ]);
    mockFetchConversationAttachments.mockResolvedValueOnce([attachment()]);
    mockFetchConversationAttachments.mockResolvedValue([attachment({ previewStatus: "ready" })]);

    render(<Files target={{ kind: "channel", id: "ch-1" }} />);

    // Awaiting the scan: the icon, and nothing asked of the preview endpoint.
    expect(await screen.findByTestId("icon-a-1")).toBeInTheDocument();
    expect(mockFetchAttachmentPreview).not.toHaveBeenCalled();

    // Cleared, still not rendered: same icon, still no request. This is the
    // state the panel used to get stuck in.
    await advancePastInterval();
    expect(await screen.findByTestId("icon-a-1")).toBeInTheDocument();
    expect(mockFetchAttachmentPreview).not.toHaveBeenCalled();

    await advancePastInterval();

    const image = await screen.findByTestId("chat-details-file-thumb");
    expect(image).toHaveAttribute("src", "blob:preview-1");
    expect(screen.queryByTestId("icon-a-1")).not.toBeInTheDocument();
    expect(mockFetchAttachmentPreview).toHaveBeenCalledTimes(1);
    expect(mockFetchAttachmentPreview).toHaveBeenCalledWith("a-1", expect.any(AbortSignal));

    // Both waits are over, so the fast cycle is. The slow revocation watch
    // continues on its own cadence — covered by its own tests below.
    const listCalls = mockFetchConversationAttachments.mock.calls.length;
    await advancePastDelay(1);
    expect(mockFetchConversationAttachments).toHaveBeenCalledTimes(listCalls);
    expect(mockFetchAttachmentPreview).toHaveBeenCalledTimes(1);
  });

  // A scanner that never rules must not become an open-ended request loop. The
  // panel gives up on watching and keeps the file listed, downloadable and
  // rendered with its fallback.
  it("stops watching a scan that never rules, and keeps the fallback", async () => {
    mockFetchConversationAttachments.mockResolvedValue([attachment({ status: "pending_scan" })]);

    render(<Files target={{ kind: "channel", id: "ch-1" }} />);
    await screen.findByTestId("icon-a-1");

    for (let attempt = 0; attempt < previewReconcileMaxAttempts; attempt += 1) {
      await advancePastDelay(attempt);
    }
    const listCalls = mockFetchConversationAttachments.mock.calls.length;
    expect(listCalls).toBe(previewReconcileMaxAttempts + 1);

    await advancePastDelay(previewReconcileMaxAttempts);
    await advancePastDelay(previewReconcileMaxAttempts);

    expect(mockFetchConversationAttachments).toHaveBeenCalledTimes(listCalls);
    expect(screen.getByTestId("icon-a-1")).toBeInTheDocument();
    expect(mockFetchAttachmentPreview).not.toHaveBeenCalled();
  });

  // Rejection is the other end of the scan, and the preview endpoint must never
  // be asked for a file the scanner refused.
  it("never asks for the bytes of an attachment the scan rejected", async () => {
    mockFetchConversationAttachments.mockResolvedValueOnce([
      attachment({ status: "pending_scan" }),
    ]);
    mockFetchConversationAttachments.mockResolvedValue([
      attachment({ status: "rejected", previewStatus: "unsupported" }),
    ]);

    render(<Files target={{ kind: "channel", id: "ch-1" }} />);
    await screen.findByTestId("icon-a-1");

    await advancePastInterval();
    const listCalls = mockFetchConversationAttachments.mock.calls.length;
    await advancePastInterval();
    await advancePastDelay(1);

    expect(mockFetchConversationAttachments).toHaveBeenCalledTimes(listCalls);
    expect(mockFetchAttachmentPreview).not.toHaveBeenCalled();
    expect(screen.getByTestId("icon-a-1")).toBeInTheDocument();
  });

  // Leaving the conversation ends everything: no more polling, and the object
  // URL of whatever was on screen is released.
  it("stops polling and releases the object URL when the conversation changes", async () => {
    mockFetchConversationAttachments.mockResolvedValue([
      attachment({ id: "a-ready", previewStatus: "ready" }),
      attachment({ id: "a-pending", status: "pending_scan" }),
    ]);

    const { rerender } = render(<Files target={{ kind: "channel", id: "ch-1" }} />);
    await screen.findByTestId("chat-details-file-thumb");

    mockFetchConversationAttachments.mockClear();
    mockFetchConversationAttachments.mockResolvedValue([
      attachment({ id: "b-1", previewStatus: "unsupported" }),
    ]);
    rerender(<Files target={{ kind: "channel", id: "ch-2" }} />);
    await screen.findByTestId("icon-b-1");

    expect(revokeObjectURL).toHaveBeenCalledWith("blob:preview-1");

    // The previous conversation's cycle did not survive the switch, and the new
    // list has nothing pending, so no timer is running at all.
    const listCalls = mockFetchConversationAttachments.mock.calls.length;
    await advancePastInterval();
    await advancePastDelay(1);
    expect(mockFetchConversationAttachments).toHaveBeenCalledTimes(listCalls);
  });

  it("turns the icon into a thumbnail without reopening the panel", async () => {
    mockFetchConversationAttachments.mockResolvedValueOnce([attachment()]);
    mockFetchConversationAttachments.mockResolvedValue([attachment({ previewStatus: "ready" })]);

    render(<Files target={{ kind: "channel", id: "ch-1" }} />);

    // While the preview is pending the user sees the icon, and no image request
    // is made: asking would be answered 409 by the server.
    expect(await screen.findByTestId("icon-a-1")).toBeInTheDocument();
    expect(mockFetchAttachmentPreview).not.toHaveBeenCalled();

    await advancePastInterval();

    const image = await screen.findByTestId("chat-details-file-thumb");
    expect(image).toHaveAttribute("src", "blob:preview-1");
    expect(screen.queryByTestId("icon-a-1")).not.toBeInTheDocument();
    // Exactly one request for the bytes, from the one state transition.
    expect(mockFetchAttachmentPreview).toHaveBeenCalledTimes(1);
    expect(mockFetchAttachmentPreview).toHaveBeenCalledWith("a-1", expect.any(AbortSignal));

    // And the cycle is over: nothing is pending any more.
    const listCalls = mockFetchConversationAttachments.mock.calls.length;
    await advancePastInterval();
    await advancePastInterval();
    expect(mockFetchConversationAttachments).toHaveBeenCalledTimes(listCalls);
    expect(mockFetchAttachmentPreview).toHaveBeenCalledTimes(1);
  });

  it("keeps the icon and asks for nothing when the preview fails", async () => {
    mockFetchConversationAttachments.mockResolvedValueOnce([attachment()]);
    mockFetchConversationAttachments.mockResolvedValue([attachment({ previewStatus: "failed" })]);

    render(<Files target={{ kind: "channel", id: "ch-1" }} />);
    await screen.findByTestId("icon-a-1");

    await advancePastInterval();
    const listCalls = mockFetchConversationAttachments.mock.calls.length;
    await advancePastInterval();

    expect(screen.getByTestId("icon-a-1")).toBeInTheDocument();
    expect(mockFetchAttachmentPreview).not.toHaveBeenCalled();
    expect(mockFetchConversationAttachments).toHaveBeenCalledTimes(listCalls);
  });

  it("shows the fallback when the image request fails, and does not retry it", async () => {
    mockFetchConversationAttachments.mockResolvedValue([attachment({ previewStatus: "ready" })]);
    mockFetchAttachmentPreview.mockRejectedValue(new Error("409"));

    render(<Files target={{ kind: "channel", id: "ch-1" }} />);

    expect(await screen.findByTestId("icon-a-1")).toBeInTheDocument();
    await waitFor(() => expect(mockFetchAttachmentPreview).toHaveBeenCalledTimes(1));

    await advancePastInterval();

    expect(screen.getByTestId("icon-a-1")).toBeInTheDocument();
    expect(mockFetchAttachmentPreview).toHaveBeenCalledTimes(1);
  });

  it("revokes the object URL when the list is unmounted", async () => {
    mockFetchConversationAttachments.mockResolvedValue([attachment({ previewStatus: "ready" })]);

    const { unmount } = render(<Files target={{ kind: "channel", id: "ch-1" }} />);
    await screen.findByTestId("chat-details-file-thumb");

    unmount();

    expect(revokeObjectURL).toHaveBeenCalledWith("blob:preview-1");
  });

  it("fetches the image once across the reconciliation rounds of other attachments", async () => {
    const ready = attachment({ id: "a-ready", previewStatus: "ready" });
    const pending = attachment({ id: "a-pending", previewStatus: "pending" });
    mockFetchConversationAttachments.mockResolvedValue([ready, pending]);

    render(<Files target={{ kind: "channel", id: "ch-1" }} />);
    await screen.findByTestId("chat-details-file-thumb");

    // The list keeps being re-read because one attachment is still pending; the
    // one that is already ready must not be re-fetched on every round.
    await advancePastInterval();
    await advancePastDelay(1);

    expect(mockFetchAttachmentPreview).toHaveBeenCalledTimes(1);
    expect(mockFetchAttachmentPreview).toHaveBeenCalledWith("a-ready", expect.any(AbortSignal));
    expect(screen.getByTestId("icon-a-pending")).toBeInTheDocument();
  });
});
