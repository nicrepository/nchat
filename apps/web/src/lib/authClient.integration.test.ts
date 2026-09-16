import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { getOrCreateDirectDM } from "../chat/chatApi";
import { listSessions } from "../profile/sessionsApi";
import { _resetState, authenticatedFetch } from "./authClient";
import { getAccessToken, setTokens } from "./authSession";

/**
 * Whole-stack tests for the request the browser actually sends: chatApi builds
 * the RequestInit, authClient injects Authorization and drives the refresh
 * retry, apiFetch merges the headers and only then fetch is called. Every layer
 * here is the real one — the unit suites mock the layer below and therefore
 * cannot see the seam where a duplicated Content-Type used to be produced.
 */

const mockFetch = vi.fn<typeof fetch>();
vi.stubGlobal("fetch", mockFetch);

const DM_URL = "/api/chat/dms";
const OTHER_USER_ID = "11111111-1111-1111-1111-111111111111";
const CONVERSATION_ID = "22222222-2222-2222-2222-222222222222";

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function unauthorized(): Response {
  return jsonResponse({ error: { code: "token_expired", message: "Token expired" } }, 401);
}

function dmCreated(): Response {
  return jsonResponse({ data: { conversation_id: CONVERSATION_ID, created: true } });
}

function refreshed(accessToken: string): Response {
  return jsonResponse({ access_token: accessToken, token_type: "Bearer", expires_in: 900 });
}

function sessionsListed(): Response {
  return jsonResponse({
    data: {
      data: [
        {
          id: "session-current",
          device_id: null,
          created_at: "2026-08-31T11:53:47Z",
          last_seen_at: "2026-08-31T17:08:49Z",
          idle_expires_at: "2026-09-01T05:08:49Z",
          absolute_expires_at: "2026-09-30T11:53:47Z",
          revoked_at: null,
          user_agent: "test-agent",
          current: true,
        },
      ],
      pagination: { limit: 50, next_cursor: null },
    },
  });
}

/** The request as the network sees it: url, method, headers and body. */
function sentRequest(call: number) {
  const [url, init] = mockFetch.mock.calls[call] as [string, RequestInit];
  return { url, method: init.method, headers: new Headers(init.headers), body: init.body };
}

beforeEach(() => {
  sessionStorage.clear();
  vi.clearAllMocks();
  _resetState();
});

afterEach(() => {
  sessionStorage.clear();
});

describe("DM creation over the real request stack", () => {
  it("posts JSON with a single application/json content type", async () => {
    setTokens("at");
    mockFetch.mockResolvedValue(dmCreated());

    const result = await getOrCreateDirectDM(OTHER_USER_ID);

    const request = sentRequest(0);
    expect(request.url).toBe(DM_URL);
    expect(request.method).toBe("POST");
    expect(request.headers.get("content-type")).toBe("application/json");
    expect(request.headers.get("authorization")).toBe("Bearer at");
    expect(request.body).toBe(JSON.stringify({ other_user_id: OTHER_USER_ID }));
    expect(result).toEqual({ conversationId: CONVERSATION_ID, created: true });
  });

  it("preserves url, method, content type and body on the retry after a refresh", async () => {
    setTokens("expired_at");
    mockFetch
      .mockResolvedValueOnce(unauthorized())
      .mockResolvedValueOnce(refreshed("new_at"))
      .mockResolvedValueOnce(dmCreated());

    const result = await getOrCreateDirectDM(OTHER_USER_ID);

    expect(mockFetch).toHaveBeenCalledTimes(3);
    expect(sentRequest(1).url).toBe("/api/auth/refresh");

    const retry = sentRequest(2);
    const original = sentRequest(0);
    expect(retry.url).toBe(original.url);
    expect(retry.method).toBe(original.method);
    expect(retry.body).toBe(original.body);
    expect(retry.headers.get("content-type")).toBe("application/json");
    expect(retry.headers.get("authorization")).toBe("Bearer new_at");
    expect(result).toEqual({ conversationId: CONVERSATION_ID, created: true });
  });

  it("fails with the original 401 and stops when the refresh is rejected", async () => {
    setTokens("expired_at");
    mockFetch.mockResolvedValueOnce(unauthorized()).mockResolvedValueOnce(unauthorized());

    await expect(getOrCreateDirectDM(OTHER_USER_ID)).rejects.toMatchObject({ status: 401 });

    expect(mockFetch).toHaveBeenCalledTimes(2);
    expect(sentRequest(1).url).toBe("/api/auth/refresh");
    expect(getAccessToken()).toBeNull();
  });

  it("retries at most once when the refreshed token is still rejected", async () => {
    setTokens("expired_at");
    mockFetch
      .mockResolvedValueOnce(unauthorized())
      .mockResolvedValueOnce(refreshed("new_at"))
      .mockResolvedValueOnce(unauthorized());

    await expect(getOrCreateDirectDM(OTHER_USER_ID)).rejects.toMatchObject({ status: 401 });

    expect(mockFetch).toHaveBeenCalledTimes(3);
  });

  it("keeps a bodyless request bodyless across the refresh retry", async () => {
    setTokens("expired_at");
    mockFetch
      .mockResolvedValueOnce(unauthorized())
      .mockResolvedValueOnce(refreshed("new_at"))
      .mockResolvedValueOnce(jsonResponse({ data: {} }));

    await authenticatedFetch("/api/chat/sidebar", { method: "GET" });

    const retry = sentRequest(2);
    expect(retry.method).toBe("GET");
    expect(retry.body).toBeUndefined();
    expect(retry.headers.get("authorization")).toBe("Bearer new_at");
  });

  it("does not mutate the caller's headers while retrying", async () => {
    setTokens("expired_at");
    const callerHeaders = new Headers({ "content-type": "application/json" });
    mockFetch
      .mockResolvedValueOnce(unauthorized())
      .mockResolvedValueOnce(refreshed("new_at"))
      .mockResolvedValueOnce(jsonResponse({ data: {} }));

    await authenticatedFetch("/api/chat/dms", {
      method: "POST",
      headers: callerHeaders,
      body: JSON.stringify({ other_user_id: OTHER_USER_ID }),
    });

    expect(callerHeaders.has("authorization")).toBe(false);
    expect(callerHeaders.get("content-type")).toBe("application/json");
  });
});

describe("session listing over the real request stack", () => {
  it("returns sessions after an expired access token is refreshed and retried", async () => {
    setTokens("expired_at");
    mockFetch
      .mockResolvedValueOnce(unauthorized())
      .mockResolvedValueOnce(refreshed("new_at"))
      .mockResolvedValueOnce(sessionsListed());

    await expect(listSessions()).resolves.toEqual([
      expect.objectContaining({
        id: "session-current",
        userAgent: "test-agent",
        current: true,
      }),
    ]);

    expect(mockFetch).toHaveBeenCalledTimes(3);
    expect(sentRequest(0).url).toBe("/api/auth/me/sessions");
    expect(sentRequest(1).url).toBe("/api/auth/refresh");
    expect(sentRequest(2).url).toBe("/api/auth/me/sessions");
    expect(sentRequest(2).headers.get("authorization")).toBe("Bearer new_at");
  });
});
