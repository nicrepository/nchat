import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { ApiRequestError } from "../lib/api";

const { mockAuthFetch } = vi.hoisted(() => ({
  mockAuthFetch: vi.fn(),
}));

vi.mock("../lib/authClient", () => ({
  authenticatedFetch: (...args: unknown[]) => mockAuthFetch(...args),
}));

import { classifySearchError, searchCategory } from "./searchApi";
import { SEARCH_CATEGORIES } from "./searchTypes";

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
  it("POSTs only q, limit and cursor, in the body, to the category's endpoint", async () => {
    mockAuthFetch.mockResolvedValue(envelope([], "next", true));
    const signal = new AbortController().signal;

    const page = await searchCategory("groups", "backup", { limit: 5, cursor: "prev", signal });

    const url = requestedUrl();
    expect(url.pathname).toBe("/api/search/groups");
    expect(url.search).toBe("");
    const init = mockAuthFetch.mock.calls[0][1] as RequestInit;
    expect(init).toEqual({ method: "POST", body: expect.any(String), signal });
    expect(JSON.parse(init.body as string)).toEqual({ q: "backup", limit: 5, cursor: "prev" });
    expect(page).toEqual({ items: [], nextCursor: "next", hasMore: true });
  });

  it("omits limit and cursor when not given", async () => {
    mockAuthFetch.mockResolvedValue(envelope([]));
    await searchCategory("users", "ana");
    const init = mockAuthFetch.mock.calls[0][1] as RequestInit;
    expect(JSON.parse(init.body as string)).toEqual({ q: "ana" });
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

describe("searchCategory('links')", () => {
  const row = {
    message_id: "m7",
    target_key: "abababababababababababababababab",
    url: "https://docs.example.com/runbook?ref=fixture",
    hostname: "docs.example.com",
    conversation_kind: "dm",
    conversation_id: "g1",
    conversation_type: "group",
    conversation_name: "Projeto",
    sender_id: "u1",
    sender_display_name: "Ana",
    created_at: "2026-09-01T09:41:00Z",
  };

  it("POSTs the query in the body and never in the URL", async () => {
    mockAuthFetch.mockResolvedValue(envelope([row], "next", true));
    const signal = new AbortController().signal;
    const query = "https://docs.example.com/runbook?ref=fixture";

    const page = await searchCategory("links", query, { limit: 5, cursor: "prev", signal });

    const [url, init] = mockAuthFetch.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/search/links");
    expect(url).not.toContain("ref=fixture");
    expect(init).toEqual({ method: "POST", body: expect.any(String), signal });
    expect(JSON.parse(init.body as string)).toEqual({ q: query, limit: 5, cursor: "prev" });
    expect(page).toEqual({
      items: [
        {
          id: "m7:abababababababababababababababab",
          messageId: "m7",
          url: row.url,
          hostname: "docs.example.com",
          conversation: { kind: "dm", id: "g1", type: "group", name: "Projeto" },
          senderId: "u1",
          senderDisplayName: "Ana",
          createdAt: "2026-09-01T09:41:00Z",
        },
      ],
      nextCursor: "next",
      hasMore: true,
    });
  });

  it("sends only the query when there is no limit or cursor", async () => {
    mockAuthFetch.mockResolvedValue(envelope([]));
    await searchCategory("links", "docs");
    const init = mockAuthFetch.mock.calls[0][1] as RequestInit;
    expect(JSON.parse(init.body as string)).toEqual({ q: "docs" });
  });

  it("surfaces a missing route as an error, never as an empty page or a fallback", async () => {
    mockAuthFetch.mockRejectedValue(new ApiRequestError(404, "not_found", "not found"));
    const failure = searchCategory("links", "docs").catch((error: unknown) => error);
    expect(classifySearchError(await failure)).toBe("unavailable");
    expect(mockAuthFetch).toHaveBeenCalledTimes(1);
  });
});

describe("classifySearchError", () => {
  it("maps statuses to UI error kinds", () => {
    expect(classifySearchError(new ApiRequestError(400, "bad_request", "x"))).toBe("bad_request");
    expect(classifySearchError(new ApiRequestError(403, "forbidden", "x"))).toBe("forbidden");
    expect(classifySearchError(new ApiRequestError(503, "internal", "x"))).toBe("server_error");
    // A route the deployed search-service lacks (rollout): never "no results".
    expect(classifySearchError(new ApiRequestError(404, "not_found", "x"))).toBe("unavailable");
    expect(classifySearchError(new ApiRequestError(409, "conflict", "x"))).toBe("unknown");
    expect(classifySearchError(new Error("network"))).toBe("unknown");
  });
});

// ── Transport (#1081): a POST body, always, and nothing after it ─────────────
//
// Any string may be a secret, so no query is ever "safe for a URL": a signed
// URL and a short opaque token are proven the same way. Whatever the
// search-service answers — including an older release without the POST (404,
// 405) — the client sends exactly one POST and never a GET, a V2 GET or the
// legacy /messages.

// Built from fragments so the source carries no secret-like literal for the
// scanners; at runtime they are exactly a signed URL and a short opaque token.
const SIGNED_URL_QUERY = ["https://example.test/reset/token?signature=", "SECRET", "123"].join("");
const SHORT_OPAQUE_QUERY = ["ABCDEF", "1234567890"].join("");
const QUERIES = [SIGNED_URL_QUERY, SHORT_OPAQUE_QUERY];

const ENDPOINTS = {
  messages: "/api/search/v2/messages",
  users: "/api/search/users",
  channels: "/api/search/channels",
  groups: "/api/search/groups",
  files: "/api/search/files",
  links: "/api/search/links",
} as const;

const FAILURES: Array<[string, unknown]> = [
  ["404", new ApiRequestError(404, "not_found", "not found")],
  ["405", new ApiRequestError(405, "bad_request", "method not allowed")],
  ["401", new ApiRequestError(401, "unauthorized", "x")],
  ["403", new ApiRequestError(403, "forbidden", "x")],
  ["500", new ApiRequestError(500, "internal", "x")],
  ["503", new ApiRequestError(503, "unavailable", "x")],
  ["network failure", new TypeError("Failed to fetch")],
  ["abort", new DOMException("aborted", "AbortError")],
];

/** The one request sent: a POST to the category's endpoint, q in the body only. */
function expectOnePostWithTheQueryInItsBody(category: keyof typeof ENDPOINTS, query: string) {
  expect(mockAuthFetch).toHaveBeenCalledTimes(1);
  const [url, init] = mockAuthFetch.mock.calls[0] as [string, RequestInit];
  const parsed = new URL(url, "http://localhost");
  expect(parsed.pathname).toBe(ENDPOINTS[category]);
  expect(parsed.search).toBe("");
  expect(url).not.toContain(query);
  expect(init.method).toBe("POST");
  return JSON.parse(init.body as string) as Record<string, unknown>;
}

describe("the query only ever travels in a POST body (#1081)", () => {
  it.each(SEARCH_CATEGORIES.flatMap((c) => QUERIES.map((q) => [c, q] as const)))(
    "%s, %j: one POST, cursor and limit in the body too",
    async (category, query) => {
      mockAuthFetch.mockResolvedValue(envelope([], "next", true));
      const page = await searchCategory(category, query, { limit: 20, cursor: "page-2" });
      const body = expectOnePostWithTheQueryInItsBody(category, query);
      expect(body).toEqual({ q: query, limit: 20, cursor: "page-2" });
      expect(page.nextCursor).toBe("next");
    },
  );

  it.each(
    SEARCH_CATEGORIES.flatMap((c) =>
      QUERIES.flatMap((q) => FAILURES.map(([name, error]) => [c, q, name, error] as const)),
    ),
  )(
    "%s, %j, %s: the failure surfaces and no GET follows",
    async (category, query, _name, error) => {
      mockAuthFetch.mockRejectedValue(error);
      await expect(searchCategory(category, query)).rejects.toBe(error);
      expectOnePostWithTheQueryInItsBody(category, query);
    },
  );

  it("an older search-service (404/405) is unavailable, never an empty result", async () => {
    for (const [, error] of FAILURES.slice(0, 2)) {
      mockAuthFetch.mockReset().mockRejectedValue(error);
      const failure = await searchCategory("users", SHORT_OPAQUE_QUERY).catch((e: unknown) => e);
      expect(classifySearchError(failure)).toBe("unavailable");
    }
  });
});
