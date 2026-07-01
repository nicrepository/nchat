import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

// ── Mock authenticatedFetch ───────────────────────────────────────────────────

const { mockAuthFetch } = vi.hoisted(() => ({
  mockAuthFetch: vi.fn(),
}));

vi.mock("../lib/authClient", () => ({
  authenticatedFetch: (...args: unknown[]) => mockAuthFetch(...args),
}));

import {
  fetchChannelMessages,
  fetchChannels,
  fetchDMMessages,
  fetchDMs,
  fetchSidebarData,
  messagesPath,
  postChannelMessage,
  postDMMessage,
} from "./chatApi";

// ── Helpers ───────────────────────────────────────────────────────────────────

function sidebarResponse(
  overrides: {
    channels?: object[];
    dms?: object[];
  } = {},
) {
  return {
    data: {
      workspace: { id: "ws-1", name: "NIC Labs", slug: "default" },
      channels: overrides.channels ?? [],
      dm_conversations: overrides.dms ?? [],
    },
  };
}

beforeEach(() => vi.clearAllMocks());
afterEach(() => vi.clearAllMocks());

// ── fetchChannels ─────────────────────────────────────────────────────────────

describe("fetchChannels", () => {
  it("returns channels mapped from sidebar response", async () => {
    mockAuthFetch.mockResolvedValue(
      sidebarResponse({
        channels: [
          { id: "ch-1", slug: "geral", display_name: "geral", type: "public", is_general: true },
          { id: "ch-2", slug: "eng", display_name: "eng", type: "private", is_general: false },
        ],
      }),
    );

    const channels = await fetchChannels();
    expect(channels).toHaveLength(2);
    expect(channels[0]).toEqual({ id: "ch-1", name: "geral", type: "public" });
    expect(channels[1]).toEqual({ id: "ch-2", name: "eng", type: "private" });
  });

  it("falls back to slug when display_name is empty", async () => {
    mockAuthFetch.mockResolvedValue(
      sidebarResponse({
        channels: [
          { id: "ch-1", slug: "geral", display_name: "", type: "public", is_general: false },
        ],
      }),
    );

    const channels = await fetchChannels();
    expect(channels[0].name).toBe("geral");
  });

  it("returns empty array when channels list is empty", async () => {
    mockAuthFetch.mockResolvedValue(sidebarResponse({ channels: [] }));
    const channels = await fetchChannels();
    expect(channels).toEqual([]);
  });

  it("calls /api/chat/sidebar with GET method", async () => {
    mockAuthFetch.mockResolvedValue(sidebarResponse());
    await fetchChannels();
    expect(mockAuthFetch).toHaveBeenCalledWith(
      expect.stringContaining("/sidebar"),
      expect.objectContaining({ method: "GET" }),
    );
  });

  it("propagates errors from authenticatedFetch", async () => {
    mockAuthFetch.mockRejectedValue(new Error("network error"));
    await expect(fetchChannels()).rejects.toThrow("network error");
  });

  it("makes an independent request per call (no cross-call caching)", async () => {
    mockAuthFetch
      .mockResolvedValueOnce(
        sidebarResponse({
          channels: [
            { id: "ch-a", slug: "a", display_name: "a", type: "public", is_general: false },
          ],
        }),
      )
      .mockResolvedValueOnce(
        sidebarResponse({
          channels: [
            { id: "ch-b", slug: "b", display_name: "b", type: "public", is_general: false },
          ],
        }),
      );

    const first = await fetchChannels();
    const second = await fetchChannels();

    expect(first[0].id).toBe("ch-a");
    expect(second[0].id).toBe("ch-b");
    expect(mockAuthFetch).toHaveBeenCalledTimes(2);
  });
});

// ── fetchDMs ──────────────────────────────────────────────────────────────────

describe("fetchDMs", () => {
  it("returns direct DMs mapped from sidebar response", async () => {
    mockAuthFetch.mockResolvedValue(
      sidebarResponse({
        dms: [{ id: "dm-1", type: "direct", name: "Juliane Lino" }],
      }),
    );

    const dms = await fetchDMs();
    expect(dms).toHaveLength(1);
    expect(dms[0]).toEqual({ id: "dm-1", type: "1:1", name: "Juliane Lino", participants: [] });
  });

  it("maps group DM type correctly", async () => {
    mockAuthFetch.mockResolvedValue(
      sidebarResponse({
        dms: [{ id: "dm-grp", type: "group", name: "Equipe Infra" }],
      }),
    );

    const dms = await fetchDMs();
    expect(dms[0].type).toBe("group");
  });

  it("returns empty array when dm_conversations list is empty", async () => {
    mockAuthFetch.mockResolvedValue(sidebarResponse({ dms: [] }));
    const dms = await fetchDMs();
    expect(dms).toEqual([]);
  });

  it("propagates errors from authenticatedFetch", async () => {
    mockAuthFetch.mockRejectedValue(new Error("auth error"));
    await expect(fetchDMs()).rejects.toThrow("auth error");
  });

  it("makes an independent request per call — different users see different data", async () => {
    // Regression: no _inflight singleton means session changes are safe.
    // User A starts a request; before it resolves, User B starts their own.
    // B must receive their own response, not A's.
    let resolveUserA!: (v: ReturnType<typeof sidebarResponse>) => void;
    const userAPromise = new Promise<ReturnType<typeof sidebarResponse>>((r) => {
      resolveUserA = r;
    });

    const userBResponse = sidebarResponse({
      dms: [{ id: "dm-b", type: "direct", name: "User B DM" }],
    });

    mockAuthFetch.mockReturnValueOnce(userAPromise).mockResolvedValueOnce(userBResponse);

    // User A starts a request (in-flight, not yet resolved).
    const userADMs = fetchDMs();

    // User B starts their own independent request while A is still in flight.
    const userBDMs = fetchDMs();

    // Resolve user A's request — must not affect B's independent promise.
    resolveUserA(sidebarResponse({ dms: [{ id: "dm-a", type: "direct", name: "User A DM" }] }));

    const [resultA, resultB] = await Promise.all([userADMs, userBDMs]);

    // A gets A's data.
    expect(resultA[0].id).toBe("dm-a");
    // B gets B's own data, not A's.
    expect(resultB[0].id).toBe("dm-b");
    expect(resultB[0].name).toBe("User B DM");
  });
});

// ── fetchSidebarData ──────────────────────────────────────────────────────────

describe("fetchSidebarData", () => {
  it("returns both channels and DMs in one request", async () => {
    mockAuthFetch.mockResolvedValue(
      sidebarResponse({
        channels: [
          { id: "ch-1", slug: "geral", display_name: "geral", type: "public", is_general: true },
        ],
        dms: [{ id: "dm-1", type: "direct", name: "Juliane" }],
      }),
    );

    const { channels, dms } = await fetchSidebarData();
    expect(channels).toHaveLength(1);
    expect(channels[0]).toEqual({ id: "ch-1", name: "geral", type: "public" });
    expect(dms).toHaveLength(1);
    expect(dms[0]).toEqual({ id: "dm-1", type: "1:1", name: "Juliane", participants: [] });
    expect(mockAuthFetch).toHaveBeenCalledTimes(1);
  });

  it("maps group DM type correctly", async () => {
    mockAuthFetch.mockResolvedValue(
      sidebarResponse({ dms: [{ id: "dm-g", type: "group", name: "Equipe" }] }),
    );
    const { dms } = await fetchSidebarData();
    expect(dms[0].type).toBe("group");
  });

  it("returns empty arrays when sidebar lists are empty", async () => {
    mockAuthFetch.mockResolvedValue(sidebarResponse());
    const { channels, dms } = await fetchSidebarData();
    expect(channels).toEqual([]);
    expect(dms).toEqual([]);
  });

  it("falls back to slug when display_name is empty", async () => {
    mockAuthFetch.mockResolvedValue(
      sidebarResponse({
        channels: [{ id: "ch-1", slug: "geral", display_name: "", type: "public" }],
      }),
    );
    const { channels } = await fetchSidebarData();
    expect(channels[0].name).toBe("geral");
  });

  it("propagates errors from authenticatedFetch", async () => {
    mockAuthFetch.mockRejectedValue(new Error("network error"));
    await expect(fetchSidebarData()).rejects.toThrow("network error");
  });

  it("handles missing dm_conversations field with empty array fallback", async () => {
    // Covers the `?? []` null-coalescing branch for dm_conversations.
    mockAuthFetch.mockResolvedValue({
      data: {
        workspace: { id: "ws-1", name: "NIC Labs", slug: "default" },
        channels: [],
        // dm_conversations intentionally omitted
      },
    });
    const { dms } = await fetchSidebarData();
    expect(dms).toEqual([]);
  });
});

// ── Message API helpers ───────────────────────────────────────────────────────

function msgRaw(overrides: Record<string, unknown> = {}) {
  return {
    id: "msg-1",
    sender_id: "user-abc",
    kind: "user",
    body_text: "Olá",
    is_removed: false,
    status: "active",
    created_at: "2024-01-15T10:00:00Z",
    updated_at: "2024-01-15T10:00:00Z",
    ...overrides,
  };
}

function msgListEnvelope(messages: object[] = [], nextCursor?: string) {
  return { data: { messages, next_cursor: nextCursor } };
}

function msgEnvelope(msg: object) {
  return { data: msg };
}

// ── fetchChannelMessages ──────────────────────────────────────────────────────

describe("fetchChannelMessages", () => {
  it("calls the correct URL for a channel", async () => {
    mockAuthFetch.mockResolvedValue(msgListEnvelope());
    await fetchChannelMessages("geral");
    const [url] = mockAuthFetch.mock.calls[0] as [string];
    expect(url).toContain("/channels/geral/messages");
  });

  it("percent-encodes channel ID with special characters", async () => {
    mockAuthFetch.mockResolvedValue(msgListEnvelope());
    await fetchChannelMessages("equipe infra");
    const [url] = mockAuthFetch.mock.calls[0] as [string];
    expect(url).toContain("/channels/equipe%20infra/messages");
  });

  it("appends before cursor as query param when provided", async () => {
    mockAuthFetch.mockResolvedValue(msgListEnvelope());
    await fetchChannelMessages("geral", "cursor==abc");
    const [url] = mockAuthFetch.mock.calls[0] as [string];
    expect(url).toContain("?before=cursor%3D%3Dabc");
  });

  it("does not append before param when cursor is absent", async () => {
    mockAuthFetch.mockResolvedValue(msgListEnvelope());
    await fetchChannelMessages("geral");
    const [url] = mockAuthFetch.mock.calls[0] as [string];
    expect(url).not.toContain("?before=");
  });

  it("passes abort signal to authenticatedFetch", async () => {
    mockAuthFetch.mockResolvedValue(msgListEnvelope());
    const ctrl = new AbortController();
    await fetchChannelMessages("geral", undefined, ctrl.signal);
    expect(mockAuthFetch).toHaveBeenCalledWith(
      expect.anything(),
      expect.objectContaining({ signal: ctrl.signal }),
    );
  });

  it("maps snake_case response fields to camelCase Message", async () => {
    mockAuthFetch.mockResolvedValue(msgListEnvelope([msgRaw()]));
    const page = await fetchChannelMessages("geral");
    expect(page.messages).toHaveLength(1);
    expect(page.messages[0]).toMatchObject({
      id: "msg-1",
      senderId: "user-abc",
      kind: "user",
      bodyText: "Olá",
      bodyFormat: "v1",
      isRemoved: false,
      status: "active",
      createdAt: "2024-01-15T10:00:00Z",
      updatedAt: "2024-01-15T10:00:00Z",
    });
  });

  it("maps an explicit v2 body format", async () => {
    mockAuthFetch.mockResolvedValue(msgListEnvelope([msgRaw({ body_format: "v2" })]));
    const page = await fetchChannelMessages("geral");
    expect(page.messages[0].bodyFormat).toBe("v2");
  });

  it("returns nextCursor from response", async () => {
    mockAuthFetch.mockResolvedValue(msgListEnvelope([msgRaw()], "next-page-cursor"));
    const page = await fetchChannelMessages("geral");
    expect(page.nextCursor).toBe("next-page-cursor");
  });

  it("returns empty nextCursor when not in response", async () => {
    mockAuthFetch.mockResolvedValue(msgListEnvelope([msgRaw()]));
    const page = await fetchChannelMessages("geral");
    expect(page.nextCursor).toBe("");
  });

  it("sets isRemoved true for removed message", async () => {
    mockAuthFetch.mockResolvedValue(
      msgListEnvelope([msgRaw({ is_removed: true, body_text: undefined })]),
    );
    const page = await fetchChannelMessages("geral");
    expect(page.messages[0].isRemoved).toBe(true);
    expect(page.messages[0].bodyText).toBe("");
  });

  it("handles absent is_removed field as false", async () => {
    // Covers `r.is_removed ?? false` null-coalescing branch when field is absent.
    // eslint-disable-next-line @typescript-eslint/no-unused-vars
    const { is_removed: _, ...withoutRemoved } = msgRaw();
    mockAuthFetch.mockResolvedValue(msgListEnvelope([withoutRemoved]));
    const page = await fetchChannelMessages("geral");
    expect(page.messages[0].isRemoved).toBe(false);
  });

  it("maps status deleted correctly", async () => {
    mockAuthFetch.mockResolvedValue(msgListEnvelope([msgRaw({ status: "deleted" })]));
    const page = await fetchChannelMessages("geral");
    expect(page.messages[0].status).toBe("deleted");
  });

  it("handles absent messages field as empty array", async () => {
    // Covers `res.data.messages ?? []` null-coalescing branch when field is absent.
    mockAuthFetch.mockResolvedValue({ data: {} });
    const page = await fetchChannelMessages("geral");
    expect(page.messages).toEqual([]);
    expect(page.nextCursor).toBe("");
  });
});

// ── fetchDMMessages ───────────────────────────────────────────────────────────

describe("fetchDMMessages", () => {
  it("calls the correct URL for a DM conversation", async () => {
    mockAuthFetch.mockResolvedValue(msgListEnvelope());
    await fetchDMMessages("dm-juliane");
    const [url] = mockAuthFetch.mock.calls[0] as [string];
    expect(url).toContain("/dm/dm-juliane/messages");
  });

  it("percent-encodes DM ID with special characters", async () => {
    mockAuthFetch.mockResolvedValue(msgListEnvelope());
    await fetchDMMessages("dm user/special");
    const [url] = mockAuthFetch.mock.calls[0] as [string];
    expect(url).toContain("/dm/dm%20user%2Fspecial/messages");
  });

  it("appends before cursor when provided", async () => {
    mockAuthFetch.mockResolvedValue(msgListEnvelope());
    await fetchDMMessages("dm-juliane", "abc123");
    const [url] = mockAuthFetch.mock.calls[0] as [string];
    expect(url).toContain("?before=abc123");
  });

  it("passes abort signal to authenticatedFetch", async () => {
    mockAuthFetch.mockResolvedValue(msgListEnvelope());
    const ctrl = new AbortController();
    await fetchDMMessages("dm-juliane", undefined, ctrl.signal);
    expect(mockAuthFetch).toHaveBeenCalledWith(
      expect.anything(),
      expect.objectContaining({ signal: ctrl.signal }),
    );
  });

  it("maps response fields correctly", async () => {
    mockAuthFetch.mockResolvedValue(msgListEnvelope([msgRaw({ kind: "system" })]));
    const page = await fetchDMMessages("dm-juliane");
    expect(page.messages[0].kind).toBe("system");
  });

  it("handles absent messages field as empty array", async () => {
    // Covers `res.data.messages ?? []` null-coalescing branch in fetchDMMessages.
    mockAuthFetch.mockResolvedValue({ data: {} });
    const page = await fetchDMMessages("dm-juliane");
    expect(page.messages).toEqual([]);
    expect(page.nextCursor).toBe("");
  });
});

// ── postChannelMessage ────────────────────────────────────────────────────────

describe("postChannelMessage", () => {
  it("calls the correct URL for a channel", async () => {
    mockAuthFetch.mockResolvedValue(msgEnvelope(msgRaw()));
    await postChannelMessage("geral", "Hello");
    const [url] = mockAuthFetch.mock.calls[0] as [string];
    expect(url).toContain("/channels/geral/messages");
  });

  it("percent-encodes channel ID", async () => {
    mockAuthFetch.mockResolvedValue(msgEnvelope(msgRaw()));
    await postChannelMessage("equipe infra", "Hello");
    const [url] = mockAuthFetch.mock.calls[0] as [string];
    expect(url).toContain("/channels/equipe%20infra/messages");
  });

  it("uses POST method", async () => {
    mockAuthFetch.mockResolvedValue(msgEnvelope(msgRaw()));
    await postChannelMessage("geral", "Hello");
    expect(mockAuthFetch).toHaveBeenCalledWith(
      expect.anything(),
      expect.objectContaining({ method: "POST" }),
    );
  });

  it("sends body_text as format v2 in JSON payload", async () => {
    mockAuthFetch.mockResolvedValue(msgEnvelope(msgRaw()));
    await postChannelMessage("geral", "Hello world");
    const [, options] = mockAuthFetch.mock.calls[0] as [string, RequestInit];
    const body = JSON.parse(options.body as string) as Record<string, unknown>;
    expect(body).toEqual({ body_text: "Hello world", body_format: "v2" });
  });

  it("does not include author_id in payload", async () => {
    mockAuthFetch.mockResolvedValue(msgEnvelope(msgRaw()));
    await postChannelMessage("geral", "Hello");
    const [, options] = mockAuthFetch.mock.calls[0] as [string, RequestInit];
    const body = JSON.parse(options.body as string) as Record<string, unknown>;
    expect(body).not.toHaveProperty("author_id");
  });

  it("passes abort signal to authenticatedFetch", async () => {
    mockAuthFetch.mockResolvedValue(msgEnvelope(msgRaw()));
    const ctrl = new AbortController();
    await postChannelMessage("geral", "Hello", ctrl.signal);
    expect(mockAuthFetch).toHaveBeenCalledWith(
      expect.anything(),
      expect.objectContaining({ signal: ctrl.signal }),
    );
  });

  it("returns mapped Message from response", async () => {
    mockAuthFetch.mockResolvedValue(msgEnvelope(msgRaw({ body_text: "Hello" })));
    const msg = await postChannelMessage("geral", "Hello");
    expect(msg.bodyText).toBe("Hello");
    expect(msg.senderId).toBe("user-abc");
  });
});

// ── postDMMessage ─────────────────────────────────────────────────────────────

describe("postDMMessage", () => {
  it("calls the correct URL for a DM conversation", async () => {
    mockAuthFetch.mockResolvedValue(msgEnvelope(msgRaw()));
    await postDMMessage("dm-juliane", "Oi!");
    const [url] = mockAuthFetch.mock.calls[0] as [string];
    expect(url).toContain("/dm/dm-juliane/messages");
  });

  it("percent-encodes DM ID", async () => {
    mockAuthFetch.mockResolvedValue(msgEnvelope(msgRaw()));
    await postDMMessage("dm user/1", "Oi!");
    const [url] = mockAuthFetch.mock.calls[0] as [string];
    expect(url).toContain("/dm/dm%20user%2F1/messages");
  });

  it("sends body_text as format v2 in JSON payload", async () => {
    mockAuthFetch.mockResolvedValue(msgEnvelope(msgRaw()));
    await postDMMessage("dm-juliane", "Mensagem direta");
    const [, options] = mockAuthFetch.mock.calls[0] as [string, RequestInit];
    const body = JSON.parse(options.body as string) as Record<string, unknown>;
    expect(body).toEqual({ body_text: "Mensagem direta", body_format: "v2" });
  });

  it("does not include author_id in payload", async () => {
    mockAuthFetch.mockResolvedValue(msgEnvelope(msgRaw()));
    await postDMMessage("dm-juliane", "Hi");
    const [, options] = mockAuthFetch.mock.calls[0] as [string, RequestInit];
    const body = JSON.parse(options.body as string) as Record<string, unknown>;
    expect(body).not.toHaveProperty("author_id");
  });

  it("passes abort signal to authenticatedFetch", async () => {
    mockAuthFetch.mockResolvedValue(msgEnvelope(msgRaw()));
    const ctrl = new AbortController();
    await postDMMessage("dm-juliane", "Hi", ctrl.signal);
    expect(mockAuthFetch).toHaveBeenCalledWith(
      expect.anything(),
      expect.objectContaining({ signal: ctrl.signal }),
    );
  });

  it("returns mapped Message from response", async () => {
    mockAuthFetch.mockResolvedValue(msgEnvelope(msgRaw({ body_text: "Oi!" })));
    const msg = await postDMMessage("dm-juliane", "Oi!");
    expect(msg.bodyText).toBe("Oi!");
    expect(msg.senderId).toBe("user-abc");
  });
});

// ── messagesPath ──────────────────────────────────────────────────────────────

describe("messagesPath", () => {
  it("returns channel messages path", () => {
    expect(messagesPath("channel", "geral")).toMatch(/\/channels\/geral\/messages$/);
  });

  it("returns DM messages path", () => {
    expect(messagesPath("dm", "dm-juliane")).toMatch(/\/dm\/dm-juliane\/messages$/);
  });

  it("percent-encodes channel ID", () => {
    expect(messagesPath("channel", "equipe infra")).toContain("/channels/equipe%20infra/messages");
  });

  it("percent-encodes DM ID", () => {
    expect(messagesPath("dm", "dm user/1")).toContain("/dm/dm%20user%2F1/messages");
  });

  it("channel and dm produce distinct paths for the same ID", () => {
    const ch = messagesPath("channel", "abc");
    const dm = messagesPath("dm", "abc");
    expect(ch).not.toBe(dm);
    expect(ch).toContain("/channels/");
    expect(dm).toContain("/dm/");
  });
});
