import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { ApiRequestError } from "../lib/api";

const { mockAuthFetch } = vi.hoisted(() => ({
  mockAuthFetch: vi.fn(),
}));

vi.mock("../lib/authClient", () => ({
  authenticatedFetch: (...args: unknown[]) => mockAuthFetch(...args),
}));

import { classifySearchError, searchChannels, searchMessages, searchUsers } from "./searchApi";

/**
 * Every nchat service wraps its body in a shared {"data": ...} envelope
 * (libs/go/platform/httputil.WriteJSON), so the wire response search-service
 * actually returns is this doubly-nested shape — not the inner page alone.
 */
function envelope<T>(items: T[], nextCursor: string | null, hasMore: boolean) {
  return {
    data: { data: items, pagination: { limit: 20, next_cursor: nextCursor, has_more: hasMore } },
  };
}

beforeEach(() => {
  mockAuthFetch.mockReset();
});

afterEach(() => {
  vi.clearAllMocks();
});

describe("searchMessages", () => {
  it("builds the URL with q, limit and cursor, and maps the response", async () => {
    mockAuthFetch.mockResolvedValue(
      envelope(
        [
          {
            id: "m1",
            channel_id: "c1",
            channel_name: "geral",
            sender_id: "u1",
            sender_display_name: "Alice",
            body_text: "hello orion",
            created_at: "2026-01-01T00:00:00Z",
            score: 1.5,
          },
        ],
        "abc",
        true,
      ),
    );

    const page = await searchMessages("orion", { limit: 20, cursor: "prev" });

    expect(mockAuthFetch).toHaveBeenCalledTimes(1);
    const [url, init] = mockAuthFetch.mock.calls[0];
    expect(url).toContain("/api/search/messages?");
    const params = new URLSearchParams(url.split("?")[1]);
    expect(params.get("q")).toBe("orion");
    expect(params.get("limit")).toBe("20");
    expect(params.get("cursor")).toBe("prev");
    expect(init).toMatchObject({ method: "GET" });

    expect(page).toEqual({
      items: [
        {
          id: "m1",
          channelId: "c1",
          channelName: "geral",
          senderId: "u1",
          senderDisplayName: "Alice",
          bodyText: "hello orion",
          createdAt: "2026-01-01T00:00:00Z",
          score: 1.5,
        },
      ],
      nextCursor: "abc",
      hasMore: true,
    });
  });

  it("omits cursor and limit params when not provided", async () => {
    mockAuthFetch.mockResolvedValue(envelope([], null, false));

    await searchMessages("orion");

    const [url] = mockAuthFetch.mock.calls[0];
    const params = new URLSearchParams(url.split("?")[1]);
    expect(params.has("limit")).toBe(false);
    expect(params.has("cursor")).toBe(false);
  });

  it("forwards the abort signal", async () => {
    mockAuthFetch.mockResolvedValue(envelope([], null, false));
    const controller = new AbortController();

    await searchMessages("orion", { signal: controller.signal });

    const [, init] = mockAuthFetch.mock.calls[0];
    expect(init.signal).toBe(controller.signal);
  });
});

describe("searchUsers", () => {
  it("maps the user result fields", async () => {
    mockAuthFetch.mockResolvedValue(
      envelope([{ id: "u1", display_name: "Alice", avatar_url: null }], null, false),
    );

    const page = await searchUsers("ali");

    expect(mockAuthFetch.mock.calls[0][0]).toContain("/api/search/users?");
    expect(page.items).toEqual([{ id: "u1", displayName: "Alice", avatarUrl: null }]);
  });
});

describe("searchChannels", () => {
  it("maps the channel result fields", async () => {
    mockAuthFetch.mockResolvedValue(
      envelope(
        [{ id: "ch1", slug: "geral", display_name: "Geral", is_general: true }],
        null,
        false,
      ),
    );

    const page = await searchChannels("geral");

    expect(mockAuthFetch.mock.calls[0][0]).toContain("/api/search/channels?");
    expect(page.items).toEqual([
      { id: "ch1", slug: "geral", displayName: "Geral", isGeneral: true },
    ]);
  });
});

describe("classifySearchError", () => {
  it("classifies 400 as bad_request", () => {
    expect(classifySearchError(new ApiRequestError(400, "invalid_query", "bad"))).toBe(
      "bad_request",
    );
  });

  it("classifies 403 as forbidden", () => {
    expect(classifySearchError(new ApiRequestError(403, "forbidden", "no"))).toBe("forbidden");
  });

  it("classifies 5xx as server_error", () => {
    expect(classifySearchError(new ApiRequestError(500, "internal", "oops"))).toBe("server_error");
    expect(classifySearchError(new ApiRequestError(503, "unavailable", "oops"))).toBe(
      "server_error",
    );
  });

  it("classifies other statuses and non-ApiRequestError values as unknown", () => {
    expect(classifySearchError(new ApiRequestError(404, "not_found", "no"))).toBe("unknown");
    expect(classifySearchError(new Error("boom"))).toBe("unknown");
  });
});
