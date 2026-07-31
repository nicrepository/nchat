import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { channelFilesPreviewLimit, useChannelDetails } from "./useChannelDetails";
import type { ChannelAttachment, ChannelDetails } from "./chatTypes";

const { mockFetchChannelDetails, mockFetchChannelAttachments } = vi.hoisted(() => ({
  mockFetchChannelDetails:
    vi.fn<(channelId: string, signal?: AbortSignal) => Promise<ChannelDetails>>(),
  mockFetchChannelAttachments:
    vi.fn<
      (channelId: string, limit: number, signal?: AbortSignal) => Promise<ChannelAttachment[]>
    >(),
}));

vi.mock("./chatApi", () => ({
  fetchChannelDetails: (channelId: string, signal?: AbortSignal) =>
    mockFetchChannelDetails(channelId, signal),
}));

vi.mock("./filesApi", () => ({
  fetchChannelAttachments: (channelId: string, limit: number, signal?: AbortSignal) =>
    mockFetchChannelAttachments(channelId, limit, signal),
}));

function details(overrides: Partial<ChannelDetails> = {}): ChannelDetails {
  return {
    id: "ch-1",
    slug: "infra",
    name: "Infraestrutura",
    type: "public",
    createdAt: "2024-01-12T09:30:00Z",
    memberCount: 3,
    onlineCount: 0,
    onlineMembers: [],
    ...overrides,
  };
}

function attachment(id: string): ChannelAttachment {
  return {
    id,
    filename: `${id}.pdf`,
    contentType: "application/pdf",
    size: 1024,
    status: "clean",
    createdAt: "2026-07-15T12:00:00.000Z",
  };
}

beforeEach(() => {
  mockFetchChannelDetails.mockResolvedValue(details());
  mockFetchChannelAttachments.mockResolvedValue([]);
});

afterEach(() => {
  vi.clearAllMocks();
});

describe("useChannelDetails", () => {
  it("issues no request while idle", () => {
    const { result } = renderHook(() => useChannelDetails(null));

    expect(mockFetchChannelDetails).not.toHaveBeenCalled();
    expect(mockFetchChannelAttachments).not.toHaveBeenCalled();
    expect(result.current.details.status).toBe("loading");
  });

  it("loads both sections for a channel", async () => {
    mockFetchChannelDetails.mockResolvedValue(details({ name: "Infra" }));
    mockFetchChannelAttachments.mockResolvedValue([attachment("a-1")]);

    const { result } = renderHook(() => useChannelDetails("ch-1"));

    await waitFor(() => expect(result.current.details.status).toBe("ready"));
    await waitFor(() => expect(result.current.files.status).toBe("ready"));

    expect(result.current.details).toEqual({ status: "ready", data: details({ name: "Infra" }) });
    expect(mockFetchChannelAttachments).toHaveBeenCalledWith(
      "ch-1",
      channelFilesPreviewLimit,
      expect.any(AbortSignal),
    );
  });

  it("keeps one failed section from destroying the other", async () => {
    mockFetchChannelAttachments.mockRejectedValue(new Error("file-service down"));

    const { result } = renderHook(() => useChannelDetails("ch-1"));

    await waitFor(() => expect(result.current.files.status).toBe("error"));
    expect(result.current.details.status).toBe("ready");
  });

  it("resets to loading and refetches when the channel changes", async () => {
    mockFetchChannelDetails.mockResolvedValueOnce(details({ id: "ch-1", name: "Primeiro" }));
    const { result, rerender } = renderHook(({ id }) => useChannelDetails(id), {
      initialProps: { id: "ch-1" as string | null },
    });
    await waitFor(() => expect(result.current.details.status).toBe("ready"));

    // A never-resolving second channel: the panel must show loading, not the
    // previous channel's data under the new channel's name.
    mockFetchChannelDetails.mockReturnValueOnce(new Promise<ChannelDetails>(() => {}));
    mockFetchChannelAttachments.mockReturnValueOnce(new Promise<ChannelAttachment[]>(() => {}));
    rerender({ id: "ch-2" });

    expect(result.current.details.status).toBe("loading");
    expect(result.current.files.status).toBe("loading");
    expect(mockFetchChannelDetails).toHaveBeenLastCalledWith("ch-2", expect.any(AbortSignal));
  });

  it("aborts the previous channel's request and ignores its late answer", async () => {
    let resolveFirst: (value: ChannelDetails) => void = () => {};
    let firstSignal: AbortSignal | undefined;
    mockFetchChannelDetails.mockImplementationOnce((_id, signal) => {
      firstSignal = signal;
      return new Promise<ChannelDetails>((resolve) => {
        resolveFirst = resolve;
      });
    });

    const { result, rerender } = renderHook(({ id }) => useChannelDetails(id), {
      initialProps: { id: "ch-1" as string | null },
    });

    mockFetchChannelDetails.mockResolvedValueOnce(details({ id: "ch-2", name: "Segundo" }));
    rerender({ id: "ch-2" });
    await waitFor(() => expect(result.current.details.status).toBe("ready"));

    expect(firstSignal?.aborted).toBe(true);
    // The first channel finally answers — long after the switch. It must not
    // overwrite the channel the user is actually looking at.
    await act(async () => {
      resolveFirst(details({ id: "ch-1", name: "Primeiro" }));
    });

    expect(result.current.details).toEqual({
      status: "ready",
      data: details({ id: "ch-2", name: "Segundo" }),
    });
  });

  it("aborts in flight requests on unmount", async () => {
    let signal: AbortSignal | undefined;
    mockFetchChannelDetails.mockImplementationOnce((_id, requestSignal) => {
      signal = requestSignal;
      return new Promise<ChannelDetails>(() => {});
    });

    const { unmount } = renderHook(() => useChannelDetails("ch-1"));
    unmount();

    expect(signal?.aborted).toBe(true);
  });

  it("treats an aborted request as neither ready nor error", async () => {
    const abortError = new Error("aborted");
    abortError.name = "AbortError";
    mockFetchChannelDetails.mockRejectedValueOnce(abortError);

    const { result } = renderHook(() => useChannelDetails("ch-1"));
    await waitFor(() => expect(result.current.files.status).toBe("ready"));

    expect(result.current.details.status).toBe("loading");
  });
});
