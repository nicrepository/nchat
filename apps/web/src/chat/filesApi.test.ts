import { afterEach, describe, expect, it, vi } from "vitest";

import { ApiRequestError } from "../lib/api";

const { mockAuthFetch } = vi.hoisted(() => ({ mockAuthFetch: vi.fn() }));

vi.mock("../lib/authClient", () => ({
  authenticatedFetch: (...args: unknown[]) => mockAuthFetch(...args),
}));

import { fetchConversationAttachments } from "./filesApi";

afterEach(() => {
  vi.clearAllMocks();
});

describe("fetchChannelAttachments", () => {
  it("targets the files gateway prefix with an encoded channel and the requested limit", async () => {
    mockAuthFetch.mockResolvedValueOnce({ data: { attachments: [] } });

    await fetchConversationAttachments({ kind: "channel", id: "ch 1" }, 5);

    expect(mockAuthFetch).toHaveBeenCalledWith("/api/files/channels/ch%201/attachments?limit=5", {
      method: "GET",
      signal: undefined,
    });
  });

  it("maps the payload and preserves the server's order", async () => {
    mockAuthFetch.mockResolvedValueOnce({
      data: {
        attachments: [
          {
            id: "a-1",
            filename: "recente.pdf",
            contentType: "application/pdf",
            size: 2048,
            status: "clean",
            createdAt: "2026-07-15T12:00:00Z",
          },
          {
            id: "a-2",
            filename: "antigo.png",
            contentType: "image/png",
            size: 10,
            status: "rejected",
            createdAt: "2026-07-14T12:00:00Z",
          },
        ],
      },
    });

    const attachments = await fetchConversationAttachments({ kind: "channel", id: "ch-1" }, 5);

    expect(attachments.map((item) => item.id)).toEqual(["a-1", "a-2"]);
    expect(attachments[0]).toEqual({
      id: "a-1",
      filename: "recente.pdf",
      contentType: "application/pdf",
      size: 2048,
      status: "clean",
      createdAt: "2026-07-15T12:00:00Z",
    });
    expect(attachments[1].status).toBe("rejected");
  });

  it("never promotes an unrecognised status to clean", async () => {
    mockAuthFetch.mockResolvedValueOnce({
      data: {
        attachments: [
          { id: "a-1", status: "pending_upload" },
          { id: "a-2" },
          { id: "a-3", status: 42 },
        ],
      },
    });

    const attachments = await fetchConversationAttachments({ kind: "channel", id: "ch-1" }, 5);

    expect(attachments.every((item) => item.status === "pending_scan")).toBe(true);
  });

  it("drops entries that are not usable attachments", async () => {
    mockAuthFetch.mockResolvedValueOnce({
      data: { attachments: [null, "nope", { filename: "sem id" }, { id: "" }, { id: "a-1" }] },
    });

    const attachments = await fetchConversationAttachments({ kind: "channel", id: "ch-1" }, 5);

    expect(attachments.map((item) => item.id)).toEqual(["a-1"]);
    expect(attachments[0].size).toBe(0);
    expect(attachments[0].filename).toBe("");
  });

  it("treats a payload without an attachments array as empty", async () => {
    mockAuthFetch.mockResolvedValueOnce({ data: {} });

    await expect(fetchConversationAttachments({ kind: "channel", id: "ch-1" }, 5)).resolves.toEqual(
      [],
    );
  });

  it("propagates a refusal rather than showing an empty file list", async () => {
    mockAuthFetch.mockRejectedValueOnce(new ApiRequestError(404, "not_found", "not found"));

    await expect(
      fetchConversationAttachments({ kind: "channel", id: "ch-1" }, 5),
    ).rejects.toBeInstanceOf(ApiRequestError);
  });
});

describe("fetchConversationAttachments — destino de grupo (issue #441)", () => {
  it("targets the dm collection, never the channel one", async () => {
    mockAuthFetch.mockResolvedValueOnce({ data: { attachments: [] } });

    await fetchConversationAttachments({ kind: "dm", id: "conv 1" }, 5);

    // A group's files live under the conversation resource; asking the channel
    // route for them would be a different destination space entirely.
    expect(mockAuthFetch).toHaveBeenCalledWith("/api/files/dm/conv%201/attachments?limit=5", {
      method: "GET",
      signal: undefined,
    });
  });
});
