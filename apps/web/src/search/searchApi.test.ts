import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { ApiRequestError } from "../lib/api";

const { mockAuthFetch } = vi.hoisted(() => ({
  mockAuthFetch: vi.fn(),
}));

vi.mock("../lib/authClient", () => ({
  authenticatedFetch: (...args: unknown[]) => mockAuthFetch(...args),
}));

import { classifySearchError, searchCategory } from "./searchApi";

/**
 * Every nchat service wraps its body in a shared {"data": ...} envelope
 * (libs/go/platform/httputil.WriteJSON), so the wire response search-service
 * actually returns is this doubly-nested shape — not the inner page alone.
 */
function envelope<T>(items: T[], nextCursor: string | null = null, hasMore = false) {
  return {
    data: { data: items, pagination: { limit: 20, next_cursor: nextCursor, has_more: hasMore } },
  };
}

function requestedUrl(): URL {
  return new URL(mockAuthFetch.mock.calls[0][0] as string, "http://localhost");
}

beforeEach(() => {
  mockAuthFetch.mockReset();
});

afterEach(() => {
  vi.clearAllMocks();
});

describe("searchCategory", () => {
  it("sends only q, limit and cursor to the category's endpoint", async () => {
    mockAuthFetch.mockResolvedValue(envelope([], "next", true));
    const signal = new AbortController().signal;

    const page = await searchCategory("groups", "backup", { limit: 5, cursor: "prev", signal });

    const url = requestedUrl();
    expect(url.pathname).toBe("/api/search/groups");
    expect([...url.searchParams.keys()].sort()).toEqual(["cursor", "limit", "q"]);
    expect(url.searchParams.get("q")).toBe("backup");
    expect(mockAuthFetch.mock.calls[0][1]).toEqual({ method: "GET", signal });
    expect(page).toEqual({ items: [], nextCursor: "next", hasMore: true });
  });

  it("omits limit and cursor when not given", async () => {
    mockAuthFetch.mockResolvedValue(envelope([]));
    await searchCategory("users", "ana");
    expect([...requestedUrl().searchParams.keys()]).toEqual(["q"]);
  });

  it("maps a message to its conversation, whatever its kind", async () => {
    mockAuthFetch.mockResolvedValue(
      envelope([
        {
          id: "m1",
          conversation_kind: "dm",
          conversation_id: "d1",
          conversation_type: "group",
          conversation_name: "Projeto",
          sender_id: "u1",
          sender_display_name: "Ana",
          sender_avatar_url: "/avatars/a.png",
          body_text: "backup",
          created_at: "2026-01-01T00:00:00Z",
          score: 0.5,
        },
      ]),
    );
    const { items } = await searchCategory("messages", "backup");
    expect(items[0]).toEqual({
      id: "m1",
      conversation: { kind: "dm", id: "d1", type: "group", name: "Projeto" },
      senderId: "u1",
      senderDisplayName: "Ana",
      senderAvatarUrl: "/avatars/a.png",
      bodyText: "backup",
      createdAt: "2026-01-01T00:00:00Z",
      score: 0.5,
    });
  });

  it("degrades an unknown conversation kind or type to the most restrictive reading", async () => {
    mockAuthFetch.mockResolvedValue(
      envelope([
        {
          id: "m1",
          conversation_kind: "weird",
          conversation_id: "x",
          conversation_type: "weird",
          conversation_name: "",
          sender_id: "u1",
          sender_display_name: "Ana",
          body_text: "b",
          created_at: "",
          score: 0,
        },
        {
          id: "m2",
          conversation_kind: "channel",
          conversation_id: "c",
          conversation_type: "weird",
          conversation_name: "c",
          sender_id: "u1",
          sender_display_name: "Ana",
          body_text: "b",
          created_at: "",
          score: 0,
        },
      ]),
    );
    const { items } = await searchCategory("messages", "b");
    expect(items[0].conversation).toMatchObject({ kind: "dm", type: "direct" });
    expect(items[0].senderAvatarUrl).toBeNull();
    expect(items[1].conversation).toMatchObject({ kind: "channel", type: "private" });
  });

  it("maps people, channels and groups without inventing metadata", async () => {
    mockAuthFetch.mockResolvedValueOnce(
      envelope([
        { id: "u1", display_name: "Ana" },
        { id: "u2", display_name: "Bia", avatar_url: "javascript:alert(1)" },
        { id: "u3", display_name: "Cid", avatar_url: "https://tracker.example/p.png" },
      ]),
    );
    expect((await searchCategory("users", "a")).items.map((u) => u.avatarUrl)).toEqual([
      null,
      null,
      null,
    ]);

    mockAuthFetch.mockResolvedValueOnce(
      envelope([
        {
          id: "c1",
          slug: "infra",
          display_name: "Infra",
          type: "private",
          description: "  ",
          member_count: 3,
          is_general: false,
        },
        {
          id: "c2",
          slug: "geral",
          display_name: "Geral",
          type: "public",
          member_count: 9,
          is_general: true,
        },
      ]),
    );
    const channels = (await searchCategory("channels", "i")).items;
    expect(channels[0]).toMatchObject({ isPrivate: true, description: null, memberCount: 3 });
    expect(channels[1]).toMatchObject({ isPrivate: false, isGeneral: true });

    mockAuthFetch.mockResolvedValueOnce(
      envelope([{ id: "g1", title: "Projeto", participant_count: 4 }]),
    );
    expect((await searchCategory("groups", "p")).items).toEqual([
      { id: "g1", title: "Projeto", participantCount: 4, lastMessageAt: null },
    ]);
  });

  it("maps a file with the attachment parsers' conservative defaults", async () => {
    mockAuthFetch.mockResolvedValue(
      envelope([
        {
          id: "f1",
          filename: "a.pdf",
          content_type: "application/pdf",
          size: 10,
          status: "mystery",
          preview_status: "ready",
          message_id: "m1",
          conversation_kind: "channel",
          conversation_id: "c1",
          conversation_type: "public",
          conversation_name: "geral",
          created_at: "2026-01-01T00:00:00Z",
        },
      ]),
    );
    const [file] = (await searchCategory("files", "a")).items;
    expect(file.status).toBe("pending_scan");
    expect(file.previewStatus).toBe("available");
    expect(file.conversation).toEqual({ kind: "channel", id: "c1", type: "public", name: "geral" });
    expect(file.messageId).toBe("m1");
  });
});

describe("classifySearchError", () => {
  it("maps statuses to UI error kinds", () => {
    expect(classifySearchError(new ApiRequestError(400, "bad_request", "x"))).toBe("bad_request");
    expect(classifySearchError(new ApiRequestError(403, "forbidden", "x"))).toBe("forbidden");
    expect(classifySearchError(new ApiRequestError(503, "internal", "x"))).toBe("server_error");
    expect(classifySearchError(new ApiRequestError(404, "not_found", "x"))).toBe("unknown");
    expect(classifySearchError(new Error("network"))).toBe("unknown");
  });
});

// ── Messages across a rollout (#900): V2 first, legacy only when V2 is absent ──

const v2Row = {
  id: "m1",
  conversation_kind: "dm",
  conversation_id: "d1",
  conversation_type: "group",
  conversation_name: "Projeto",
  sender_id: "u1",
  sender_display_name: "Ana",
  body_text: "backup",
  created_at: "2026-01-01T00:00:00Z",
  score: 1,
};
const legacyRow = {
  id: "m2",
  channel_id: "c1",
  channel_name: "geral",
  sender_id: "u1",
  sender_display_name: "Ana",
  body_text: "backup",
  created_at: "2026-01-01T00:00:00Z",
  score: 1,
};
const paths = () =>
  mockAuthFetch.mock.calls.map(([url]) => new URL(url as string, "http://localhost").pathname);
const notFound = () => new ApiRequestError(404, "not_found", "not found");

describe("searchCategory('messages') across versions", () => {
  it("A. uses V2 and never the legacy endpoint when V2 answers", async () => {
    mockAuthFetch.mockResolvedValue(envelope([v2Row]));
    const page = await searchCategory("messages", "backup", { limit: 5 });
    expect(paths()).toEqual(["/api/search/v2/messages"]);
    expect(page.items[0].conversation).toEqual({
      kind: "dm",
      id: "d1",
      type: "group",
      name: "Projeto",
    });
  });

  it("B. falls back exactly once to the legacy endpoint when V2 does not exist, and normalizes it", async () => {
    mockAuthFetch
      .mockRejectedValueOnce(notFound())
      .mockResolvedValueOnce(envelope([legacyRow], "raw-legacy", true));
    const signal = new AbortController().signal;
    const page = await searchCategory("messages", "backup", { limit: 5, signal });

    expect(paths()).toEqual(["/api/search/v2/messages", "/api/search/messages"]);
    const legacyUrl = new URL(mockAuthFetch.mock.calls[1][0] as string, "http://localhost");
    expect(legacyUrl.searchParams.get("q")).toBe("backup");
    expect(legacyUrl.searchParams.get("limit")).toBe("5");
    expect(mockAuthFetch.mock.calls[1][1]).toEqual({ method: "GET", signal });
    expect(page.items).toEqual([
      {
        id: "m2",
        conversation: { kind: "channel", id: "c1", type: "public", name: "geral" },
        senderId: "u1",
        senderDisplayName: "Ana",
        senderAvatarUrl: null,
        bodyText: "backup",
        createdAt: "2026-01-01T00:00:00Z",
        score: 1,
      },
    ]);
    expect(page.hasMore).toBe(true);
    expect(page.nextCursor).not.toBe("raw-legacy");
  });

  it.each([
    ["C. 401", new ApiRequestError(401, "unauthorized", "x")],
    ["D. 403", new ApiRequestError(403, "forbidden", "x")],
    ["E. 500", new ApiRequestError(500, "internal", "x")],
    ["E. 503", new ApiRequestError(503, "unavailable", "x")],
    ["F. network failure", new TypeError("Failed to fetch")],
    ["F. abort", new DOMException("aborted", "AbortError")],
  ])("%s is a real failure, never a fallback", async (_name, error) => {
    mockAuthFetch.mockRejectedValue(error);
    await expect(searchCategory("messages", "backup")).rejects.toBe(error);
    expect(paths()).toEqual(["/api/search/v2/messages"]);
  });

  it("does not loop when the legacy endpoint is missing too", async () => {
    mockAuthFetch.mockRejectedValue(notFound());
    await expect(searchCategory("messages", "backup")).rejects.toBeInstanceOf(ApiRequestError);
    expect(paths()).toEqual(["/api/search/v2/messages", "/api/search/messages"]);
  });

  it("G. sends a legacy next page back to the legacy endpoint with its own cursor", async () => {
    mockAuthFetch
      .mockRejectedValueOnce(notFound())
      .mockResolvedValueOnce(envelope([legacyRow], "raw-legacy", true));
    const first = await searchCategory("messages", "backup");
    mockAuthFetch.mockClear().mockResolvedValue(envelope([{ ...legacyRow, id: "m3" }]));

    const second = await searchCategory("messages", "backup", { cursor: first.nextCursor! });

    expect(paths()).toEqual(["/api/search/messages"]);
    expect(
      new URL(mockAuthFetch.mock.calls[0][0] as string, "http://localhost").searchParams.get(
        "cursor",
      ),
    ).toBe("raw-legacy");
    expect(second.items[0].conversation).toEqual({
      kind: "channel",
      id: "c1",
      type: "public",
      name: "geral",
    });
    expect(second.nextCursor).toBeNull();
  });

  it("never replays a V2 cursor against the legacy endpoint", async () => {
    mockAuthFetch.mockRejectedValue(notFound());
    await expect(
      searchCategory("messages", "backup", { cursor: "v2-cursor" }),
    ).rejects.toBeInstanceOf(ApiRequestError);
    expect(paths()).toEqual(["/api/search/v2/messages"]);
  });

  it("G. drops a legacy row with no channel instead of routing it to undefined", async () => {
    const withoutChannel: Partial<typeof legacyRow> = { ...legacyRow };
    delete withoutChannel.channel_id;
    mockAuthFetch
      .mockRejectedValueOnce(notFound())
      .mockResolvedValueOnce(envelope([withoutChannel, legacyRow]));
    const page = await searchCategory("messages", "backup");
    expect(page.items.map((item) => item.id)).toEqual(["m2"]);
    expect(page.items.every((item) => typeof item.conversation.id === "string")).toBe(true);
  });
});
