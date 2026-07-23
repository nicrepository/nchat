import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ApiRequestError } from "../lib/api";

// ── Mock authenticatedFetch ───────────────────────────────────────────────────

const { mockAuthFetch } = vi.hoisted(() => ({
  mockAuthFetch: vi.fn(),
}));

vi.mock("../lib/authClient", () => ({
  authenticatedFetch: (...args: unknown[]) => mockAuthFetch(...args),
}));

import {
  deleteMessage,
  editMessage,
  favoriteMessage,
  forwardChannelMessage,
  fetchChannelMessage,
  fetchChannelMessages,
  fetchPins,
  fetchFavorites,
  fetchAllowedReactionEmojis,
  fetchChannels,
  fetchDMMessage,
  fetchDMMessages,
  fetchDMs,
  fetchMentionCandidates,
  getOrCreateDirectDM,
  getMessageHistory,
  MessageEditError,
  fetchSidebarData,
  messagesPath,
  pinMessage,
  postChannelMessage,
  postDMMessage,
  resetAllowedReactionEmojisCache,
  resolveChannelMessageReferences,
  resolveDMMessageReferences,
  searchDMCandidates,
  unfavoriteMessage,
  unpinMessage,
} from "./chatApi";

describe("message reference batch resolution", () => {
  it("posts destination IDs once and maps authorized and unavailable references", async () => {
    mockAuthFetch.mockResolvedValueOnce({
      data: {
        references: [
          {
            message_id: "destination-1",
            reference: {
              available: true,
              message_id: "source-1",
              target_type: "channel",
              target_id: "private-source",
              target_label: "Privado",
              author_display_name: "Ana",
              body: "segredo",
              body_format: "v3",
              created_at: "2026-07-21T12:00:00Z",
            },
          },
          { message_id: "destination-2", reference: { available: false } },
        ],
      },
    });
    const signal = new AbortController().signal;

    const references = await resolveChannelMessageReferences(
      "canal privado",
      ["destination-1", "destination-2"],
      signal,
    );

    expect(mockAuthFetch).toHaveBeenCalledWith(
      "/api/chat/channels/canal%20privado/message-references",
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({ message_ids: ["destination-1", "destination-2"] }),
        signal,
      }),
    );
    expect(references["destination-1"]).toMatchObject({
      available: true,
      messageId: "source-1",
      bodyText: "segredo",
    });
    expect(references["destination-2"]).toEqual({ available: false });
  });

  it("uses the DM batch endpoint", async () => {
    mockAuthFetch.mockResolvedValueOnce({ data: { references: [] } });
    await resolveDMMessageReferences("dm-1", ["destination-1"]);
    expect(mockAuthFetch).toHaveBeenCalledWith(
      "/api/chat/dm/dm-1/message-references",
      expect.objectContaining({ method: "POST" }),
    );
  });
});

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

beforeEach(() => {
  vi.clearAllMocks();
  resetAllowedReactionEmojisCache();
});
afterEach(() => vi.clearAllMocks());

describe("fetchAllowedReactionEmojis", () => {
  it("fetches the authenticated allowlist once and reuses the in-memory result", async () => {
    mockAuthFetch.mockResolvedValue({ data: { emojis: ["👍", "❤️", "🚀"] } });

    const [first, second] = await Promise.all([
      fetchAllowedReactionEmojis(),
      fetchAllowedReactionEmojis(),
    ]);

    expect(first).toEqual(["👍", "❤️", "🚀"]);
    expect(second).toEqual(first);
    expect(mockAuthFetch).toHaveBeenCalledTimes(1);
    expect(mockAuthFetch).toHaveBeenCalledWith(
      expect.stringContaining("/reactions/allowed-emojis"),
      { method: "GET" },
    );
  });

  it("fetches again after the cache is reset", async () => {
    mockAuthFetch.mockResolvedValueOnce({ data: { emojis: ["👍"] } });
    await fetchAllowedReactionEmojis();

    resetAllowedReactionEmojisCache();
    mockAuthFetch.mockResolvedValueOnce({ data: { emojis: ["🚀"] } });

    await expect(fetchAllowedReactionEmojis()).resolves.toEqual(["🚀"]);
    expect(mockAuthFetch).toHaveBeenCalledTimes(2);
  });

  it("drops non-string values from a malformed allowlist response", async () => {
    mockAuthFetch.mockResolvedValue({ data: { emojis: ["👍", 42, null, "🚀"] } });

    await expect(fetchAllowedReactionEmojis()).resolves.toEqual(["👍", "🚀"]);
  });

  it("fails closed when the allowlist is not an array", async () => {
    mockAuthFetch.mockResolvedValue({ data: { emojis: "👍" } });

    await expect(fetchAllowedReactionEmojis()).resolves.toEqual([]);
  });

  it("does not let a stale request failure clear the new session cache", async () => {
    let rejectStale!: (error: Error) => void;
    mockAuthFetch.mockImplementationOnce(
      () => new Promise((_resolve, reject) => (rejectStale = reject)),
    );
    const staleRequest = fetchAllowedReactionEmojis();

    resetAllowedReactionEmojisCache();
    mockAuthFetch.mockResolvedValueOnce({ data: { emojis: ["🚀"] } });
    const currentRequest = fetchAllowedReactionEmojis();
    await expect(currentRequest).resolves.toEqual(["🚀"]);

    rejectStale(new Error("stale session"));
    await expect(staleRequest).rejects.toThrow("stale session");
    await expect(fetchAllowedReactionEmojis()).resolves.toEqual(["🚀"]);
    expect(mockAuthFetch).toHaveBeenCalledTimes(2);
  });
});

// ── fetchChannels ─────────────────────────────────────────────────────────────

describe("fetchChannels", () => {
  it("returns channels mapped from sidebar response", async () => {
    mockAuthFetch.mockResolvedValue(
      sidebarResponse({
        channels: [
          {
            id: "ch-1",
            slug: "geral",
            display_name: "geral",
            type: "public",
            is_general: true,
            can_write: true,
          },
          {
            id: "ch-2",
            slug: "eng",
            display_name: "eng",
            type: "private",
            is_general: false,
            can_write: false,
          },
        ],
      }),
    );

    const channels = await fetchChannels();
    expect(channels).toHaveLength(2);
    expect(channels[0]).toEqual({ id: "ch-1", name: "geral", type: "public", canWrite: true });
    expect(channels[1]).toEqual({ id: "ch-2", name: "eng", type: "private", canWrite: false });
  });

  it("fails closed when can_write is missing or unexpected", async () => {
    mockAuthFetch.mockResolvedValue(
      sidebarResponse({
        channels: [
          { id: "missing", slug: "missing", display_name: "Missing", type: "public" },
          {
            id: "unexpected",
            slug: "unexpected",
            display_name: "Unexpected",
            type: "public",
            can_write: "true",
          },
        ],
      }),
    );

    const channels = await fetchChannels();
    expect(channels.map(({ id, canWrite }) => ({ id, canWrite }))).toEqual([
      { id: "missing", canWrite: false },
      { id: "unexpected", canWrite: false },
    ]);
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
          {
            id: "ch-1",
            slug: "geral",
            display_name: "geral",
            type: "public",
            is_general: true,
            can_write: true,
          },
        ],
        dms: [{ id: "dm-1", type: "direct", name: "Juliane" }],
      }),
    );

    const { channels, dms } = await fetchSidebarData();
    expect(channels).toHaveLength(1);
    expect(channels[0]).toEqual({ id: "ch-1", name: "geral", type: "public", canWrite: true });
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

  it("keeps distinct names for distinct 1:1 conversations", async () => {
    // The backend resolves `name` per viewer; the client must not collapse or
    // reorder them.
    mockAuthFetch.mockResolvedValue(
      sidebarResponse({
        dms: [
          { id: "dm-1", type: "direct", name: "Juliane Lino" },
          { id: "dm-2", type: "direct", name: "Caio Almeida" },
        ],
      }),
    );
    const { dms } = await fetchSidebarData();
    expect(dms.map((dm) => [dm.id, dm.name])).toEqual([
      ["dm-1", "Juliane Lino"],
      ["dm-2", "Caio Almeida"],
    ]);
  });

  it("keeps the conversation id untouched for navigation", async () => {
    mockAuthFetch.mockResolvedValue(
      sidebarResponse({
        dms: [{ id: "9f1c0d2e-0000-4000-8000-000000000001", type: "direct", name: "Ana" }],
      }),
    );
    const { dms } = await fetchSidebarData();
    expect(dms[0].id).toBe("9f1c0d2e-0000-4000-8000-000000000001");
  });

  it("passes the generic backend fallback through unchanged", async () => {
    mockAuthFetch.mockResolvedValue(
      sidebarResponse({ dms: [{ id: "dm-1", type: "direct", name: "Mensagem Direta" }] }),
    );
    const { dms } = await fetchSidebarData();
    expect(dms[0].name).toBe("Mensagem Direta");
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

describe("partial sidebar compatibility", () => {
  it("normalizes missing destination arrays to empty lists", async () => {
    mockAuthFetch.mockResolvedValue({
      data: {
        current_user_id: "user-1",
        workspace: { id: "ws-1", name: "NIC Labs", slug: "default" },
      },
    });

    await expect(fetchChannels()).resolves.toEqual([]);
    await expect(fetchDMs()).resolves.toEqual([]);
    await expect(fetchSidebarData()).resolves.toEqual({
      currentUserId: "user-1",
      channels: [],
      dms: [],
    });
  });
});

describe("direct DM contracts", () => {
  it("searches candidates with the authenticated endpoint and maps the response", async () => {
    mockAuthFetch.mockResolvedValue({
      data: {
        candidates: [{ user_id: "user-2", display_name: "Joana Silva" }],
      },
    });
    const controller = new AbortController();

    await expect(searchDMCandidates("  Joana  ", controller.signal)).resolves.toEqual([
      { userId: "user-2", displayName: "Joana Silva" },
    ]);
    expect(mockAuthFetch).toHaveBeenCalledWith("/api/chat/dm-candidates?query=Joana&limit=20", {
      method: "GET",
      signal: controller.signal,
    });
  });

  it("posts only the selected user ID and returns the canonical conversation", async () => {
    mockAuthFetch.mockResolvedValue({
      data: { conversation_id: "dm-canonical", created: false },
    });
    const controller = new AbortController();

    await expect(getOrCreateDirectDM("user-2", controller.signal)).resolves.toEqual({
      conversationId: "dm-canonical",
      created: false,
    });
    expect(mockAuthFetch).toHaveBeenCalledWith("/api/chat/dms", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ other_user_id: "user-2" }),
      signal: controller.signal,
    });
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

  it("maps the server-derived forwarding marker", async () => {
    mockAuthFetch.mockResolvedValue(msgListEnvelope([msgRaw({ is_forwarded: true })]));
    const page = await fetchChannelMessages("geral");
    expect(page.messages[0].isForwarded).toBe(true);
  });

  it.each([
    ["false", false],
    ["missing", undefined],
    ["unexpected", "true"],
  ])("normalizes %s is_forwarded to false", async (_case, isForwarded) => {
    const legacyFields = _case === "missing" ? { body_text: undefined } : {};
    mockAuthFetch.mockResolvedValue(
      msgListEnvelope([msgRaw({ is_forwarded: isForwarded, ...legacyFields })]),
    );
    const page = await fetchChannelMessages("geral");
    expect(page.messages[0].isForwarded).toBe(false);
    if (_case === "missing") expect(page.messages[0].bodyText).toBe("");
  });

  it("maps reaction aggregates", async () => {
    mockAuthFetch.mockResolvedValue(
      msgListEnvelope([msgRaw({ reactions: [{ emoji: "👍", count: 2, reacted_by_me: true }] })]),
    );
    const page = await fetchChannelMessages("geral");
    expect(page.messages[0].reactions).toEqual([{ emoji: "👍", count: 2, reactedByMe: true }]);
  });

  it("maps inline quoted message previews", async () => {
    mockAuthFetch.mockResolvedValue(
      msgListEnvelope([
        msgRaw({
          quoted: {
            id: "parent-1",
            author_id: "user-parent",
            body: "texto citado",
            body_format: "v3",
            created_at: "2024-01-15T09:00:00Z",
          },
        }),
      ]),
    );

    const page = await fetchChannelMessages("geral");

    expect(page.messages[0].quoted).toEqual({
      id: "parent-1",
      authorId: "user-parent",
      bodyText: "texto citado",
      bodyFormat: "v3",
      isRemoved: false,
      deletedAt: null,
      createdAt: "2024-01-15T09:00:00Z",
    });
  });

  it("maps removed quoted previews without body", async () => {
    mockAuthFetch.mockResolvedValue(
      msgListEnvelope([
        msgRaw({
          quoted: {
            id: "parent-1",
            author_id: "user-parent",
            body_format: "v2",
            is_removed: true,
            deleted_at: "2024-01-15T09:30:00Z",
            created_at: "2024-01-15T09:00:00Z",
          },
        }),
      ]),
    );

    const page = await fetchChannelMessages("geral");

    expect(page.messages[0].quoted).toMatchObject({
      id: "parent-1",
      bodyText: "",
      bodyFormat: "v2",
      isRemoved: true,
      deletedAt: "2024-01-15T09:30:00Z",
    });
  });

  it("maps authorized and unavailable cross-channel references", async () => {
    mockAuthFetch.mockResolvedValue(
      msgListEnvelope([
        msgRaw({
          reference: {
            available: true,
            message_id: "source-1",
            target_type: "channel",
            target_id: "private-1",
            target_label: "privado",
            author_display_name: "Ana",
            body: "<img src=x onerror=alert(1)>",
            body_format: "v3",
            created_at: "2024-01-15T09:00:00Z",
          },
        }),
        msgRaw({ id: "msg-2", reference: { available: false } }),
      ]),
    );

    const page = await fetchChannelMessages("geral");

    expect(page.messages[0].reference).toEqual({
      available: true,
      messageId: "source-1",
      targetType: "channel",
      targetId: "private-1",
      targetLabel: "privado",
      authorDisplayName: "Ana",
      bodyText: "<img src=x onerror=alert(1)>",
      bodyFormat: "v3",
      createdAt: "2024-01-15T09:00:00Z",
    });
    expect(page.messages[1].reference).toEqual({ available: false });
  });

  it("fails closed when an available reference is missing navigation fields", async () => {
    mockAuthFetch.mockResolvedValue(
      msgListEnvelope([msgRaw({ reference: { available: true, body: "must not render" } })]),
    );
    const page = await fetchChannelMessages("geral");
    expect(page.messages[0].reference).toEqual({ available: false });
  });

  it("fails closed for each malformed navigation field and defaults optional preview text", async () => {
    mockAuthFetch.mockResolvedValue(
      msgListEnvelope([
        msgRaw({
          id: "bad-type",
          reference: {
            available: true,
            message_id: "source-1",
            target_type: "workspace",
            target_id: "target-1",
            created_at: "2024-01-15T09:00:00Z",
          },
        }),
        msgRaw({
          id: "missing-target",
          reference: {
            available: true,
            message_id: "source-2",
            target_type: "channel",
            created_at: "2024-01-15T09:00:00Z",
          },
        }),
        msgRaw({
          id: "missing-created-at",
          reference: {
            available: true,
            message_id: "source-3",
            target_type: "dm",
            target_id: "dm-1",
          },
        }),
        msgRaw({
          id: "minimal-valid",
          reference: {
            available: true,
            message_id: "source-4",
            target_type: "dm",
            target_id: "dm-1",
            created_at: "2024-01-15T09:00:00Z",
          },
        }),
      ]),
    );

    const page = await fetchChannelMessages("geral");

    expect(page.messages.slice(0, 3).map((message) => message.reference)).toEqual([
      { available: false },
      { available: false },
      { available: false },
    ]);
    expect(page.messages[3].reference).toEqual({
      available: true,
      messageId: "source-4",
      targetType: "dm",
      targetId: "dm-1",
      targetLabel: "",
      authorDisplayName: "",
      bodyText: "",
      bodyFormat: "v1",
      createdAt: "2024-01-15T09:00:00Z",
    });
  });

  it("maps an explicit v2 body format", async () => {
    mockAuthFetch.mockResolvedValue(msgListEnvelope([msgRaw({ body_format: "v2" })]));
    const page = await fetchChannelMessages("geral");
    expect(page.messages[0].bodyFormat).toBe("v2");
  });

  it("maps an explicit v3 body format", async () => {
    mockAuthFetch.mockResolvedValue(msgListEnvelope([msgRaw({ body_format: "v3" })]));
    const page = await fetchChannelMessages("geral");
    expect(page.messages[0].bodyFormat).toBe("v3");
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
    expect(page.messages[0].bodyText).toBe("");
  });

  it("fails closed when deleted_at is present despite an active status", async () => {
    mockAuthFetch.mockResolvedValue(
      msgListEnvelope([
        msgRaw({
          status: "active",
          body_text: "não expor",
          deleted_at: "2026-07-14T12:00:00Z",
        }),
      ]),
    );

    const page = await fetchChannelMessages("geral");

    expect(page.messages[0]).toMatchObject({
      bodyText: "",
      status: "deleted",
      isRemoved: true,
      deletedAt: "2026-07-14T12:00:00Z",
    });
  });

  it("handles absent messages field as empty array", async () => {
    // Covers `res.data.messages ?? []` null-coalescing branch when field is absent.
    mockAuthFetch.mockResolvedValue({ data: {} });
    const page = await fetchChannelMessages("geral");
    expect(page.messages).toEqual([]);
    expect(page.nextCursor).toBe("");
  });
});

describe("message editing", () => {
  it("sends only the editable fields and maps the authoritative response", async () => {
    mockAuthFetch.mockResolvedValue(
      msgEnvelope(
        msgRaw({
          body_text: "Texto editado",
          body_format: "v3",
          edited_at: "2026-07-13T12:00:00Z",
          edit_count: 2,
          is_edited: true,
        }),
      ),
    );

    const message = await editMessage("msg/1", "Texto editado", 3);

    expect(mockAuthFetch).toHaveBeenCalledWith("/api/chat/messages/msg%2F1", {
      method: "PATCH",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ body: "Texto editado", body_format: "v3" }),
    });
    expect(message).toMatchObject({
      bodyText: "Texto editado",
      bodyFormat: "v3",
      isEdited: true,
      editCount: 2,
      editedAt: "2026-07-13T12:00:00Z",
    });
  });

  it.each([
    [403, "forbidden"],
    [404, "not_found"],
    [409, "window_expired"],
    [429, "rate_limited"],
  ] as const)("maps HTTP %s to a typed %s error", async (status, reason) => {
    mockAuthFetch.mockRejectedValue(new ApiRequestError(status, "request_failed", "failed"));

    await expect(editMessage("msg-1", "body", 2)).rejects.toMatchObject({
      status,
      reason,
      name: "MessageEditError",
    } satisfies Partial<MessageEditError>);
  });

  it.each([
    ["transport error", new Error("network unavailable")],
    ["unmapped HTTP error", new ApiRequestError(500, "internal_error", "failed")],
  ])("preserves an unexpected %s", async (_name, error) => {
    mockAuthFetch.mockRejectedValue(error);

    await expect(editMessage("msg-1", "body", 2)).rejects.toBe(error);
  });

  it("maps history, pagination and body formats", async () => {
    mockAuthFetch.mockResolvedValue({
      data: {
        history: [
          { body: "mais recente", body_format: "v3", versioned_at: "2026-07-13T12:00:00Z" },
          { body: "anterior", body_format: "v2", versioned_at: "2026-07-13T11:00:00Z" },
          { body: "legado", body_format: "v1", versioned_at: "2026-07-13T10:00:00Z" },
        ],
        offset: 2,
      },
    });

    const page = await getMessageHistory("msg-1", { cursor: "2", limit: 3 });

    expect(mockAuthFetch).toHaveBeenCalledWith(
      "/api/chat/messages/msg-1/history?limit=3&offset=2",
      {
        method: "GET",
      },
    );
    expect(page).toEqual({
      entries: [
        { body: "mais recente", bodyFormat: 3, versionedAt: "2026-07-13T12:00:00Z" },
        { body: "anterior", bodyFormat: 2, versionedAt: "2026-07-13T11:00:00Z" },
        { body: "legado", bodyFormat: 1, versionedAt: "2026-07-13T10:00:00Z" },
      ],
      nextCursor: "5",
    });
  });
});

describe("message deletion", () => {
  it("DELETEs the encoded message path and maps a sanitized placeholder", async () => {
    mockAuthFetch.mockResolvedValue(
      msgEnvelope(
        msgRaw({
          body_text: "conteúdo que não deve reaparecer",
          quoted: { id: "parent-1", body: "citação antiga" },
          status: "deleted",
          deleted_at: "2026-07-14T12:00:00Z",
        }),
      ),
    );

    const message = await deleteMessage("msg/1");

    expect(mockAuthFetch).toHaveBeenCalledWith("/api/chat/messages/msg%2F1", {
      method: "DELETE",
    });
    expect(message).toMatchObject({
      id: "msg-1",
      bodyText: "",
      status: "deleted",
      isRemoved: true,
      deletedAt: "2026-07-14T12:00:00Z",
    });
    expect(message.quoted).toBeUndefined();
  });

  it("propagates a rejected deletion without changing its error details", async () => {
    const error = new ApiRequestError(403, "forbidden", "request failed");
    mockAuthFetch.mockRejectedValue(error);

    await expect(deleteMessage("msg-1")).rejects.toBe(error);
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

describe("fetchChannelMessage", () => {
  it("fetches one channel message with the encoded message id", async () => {
    mockAuthFetch.mockResolvedValue(msgEnvelope(msgRaw({ id: "msg/1" })));
    const ctrl = new AbortController();

    const msg = await fetchChannelMessage("geral", "msg/1", ctrl.signal);

    expect(mockAuthFetch).toHaveBeenCalledWith(
      expect.stringContaining("/channels/geral/messages/msg%2F1"),
      expect.objectContaining({ method: "GET", signal: ctrl.signal }),
    );
    expect(msg.id).toBe("msg/1");
  });
});

describe("fetchDMMessage", () => {
  it("fetches one DM message with the encoded message id", async () => {
    mockAuthFetch.mockResolvedValue(msgEnvelope(msgRaw({ id: "msg dm" })));
    const ctrl = new AbortController();

    const msg = await fetchDMMessage("dm-juliane", "msg dm", ctrl.signal);

    expect(mockAuthFetch).toHaveBeenCalledWith(
      expect.stringContaining("/dm/dm-juliane/messages/msg%20dm"),
      expect.objectContaining({ method: "GET", signal: ctrl.signal }),
    );
    expect(msg.id).toBe("msg dm");
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

  it("sends body_text as format v3 in JSON payload", async () => {
    mockAuthFetch.mockResolvedValue(msgEnvelope(msgRaw()));
    await postChannelMessage("geral", "Hello world");
    const [, options] = mockAuthFetch.mock.calls[0] as [string, RequestInit];
    const body = JSON.parse(options.body as string) as Record<string, unknown>;
    expect(body).toEqual({ body_text: "Hello world", body_format: "v3" });
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
    await postChannelMessage("geral", "Hello", undefined, undefined, ctrl.signal);
    expect(mockAuthFetch).toHaveBeenCalledWith(
      expect.anything(),
      expect.objectContaining({ signal: ctrl.signal }),
    );
  });

  it("sends parent_message_id when replying in a channel", async () => {
    mockAuthFetch.mockResolvedValue(msgEnvelope(msgRaw()));
    await postChannelMessage("geral", "Hello world", "parent-1");
    const [, options] = mockAuthFetch.mock.calls[0] as [string, RequestInit];
    const body = JSON.parse(options.body as string) as Record<string, unknown>;
    expect(body).toEqual({
      body_text: "Hello world",
      body_format: "v3",
      parent_message_id: "parent-1",
    });
  });

  it("sends referenced_message_id for RF-09", async () => {
    mockAuthFetch.mockResolvedValue(msgEnvelope(msgRaw()));
    await postChannelMessage("geral", "Veja", undefined, "source-1");
    const [, options] = mockAuthFetch.mock.calls[0] as [string, RequestInit];
    expect(JSON.parse(options.body as string)).toEqual({
      body_text: "Veja",
      body_format: "v3",
      referenced_message_id: "source-1",
    });
  });

  it("preserves reply and reference with an abort signal", async () => {
    mockAuthFetch.mockResolvedValue(msgEnvelope(msgRaw()));
    const ctrl = new AbortController();
    await postChannelMessage("geral", "Veja", "parent-1", "source-1", ctrl.signal);
    const [, options] = mockAuthFetch.mock.calls[0] as [string, RequestInit];
    expect(JSON.parse(options.body as string)).toEqual({
      body_text: "Veja",
      body_format: "v3",
      parent_message_id: "parent-1",
      referenced_message_id: "source-1",
    });
    expect(options.signal).toBe(ctrl.signal);
  });

  it("returns mapped Message from response", async () => {
    mockAuthFetch.mockResolvedValue(msgEnvelope(msgRaw({ body_text: "Hello" })));
    const msg = await postChannelMessage("geral", "Hello");
    expect(msg.bodyText).toBe("Hello");
    expect(msg.senderId).toBe("user-abc");
  });
});

describe("forwardChannelMessage", () => {
  it("posts only source_message_id to the encoded dedicated destination route", async () => {
    mockAuthFetch.mockResolvedValue(
      msgEnvelope(msgRaw({ body_text: "snapshot", body_format: "v3", is_forwarded: true })),
    );
    const controller = new AbortController();

    const message = await forwardChannelMessage(
      "canal privado",
      "source-1",
      "forward-action-1",
      controller.signal,
    );

    expect(mockAuthFetch).toHaveBeenCalledWith(
      "/api/chat/channels/canal%20privado/messages/forward",
      expect.objectContaining({
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          "Idempotency-Key": "forward-action-1",
        },
        body: JSON.stringify({ source_message_id: "source-1" }),
        signal: controller.signal,
      }),
    );
    const [, options] = mockAuthFetch.mock.calls[0] as [string, RequestInit];
    const payload = JSON.parse(options.body as string) as Record<string, unknown>;
    expect(payload).not.toHaveProperty("body_text");
    expect(payload).not.toHaveProperty("forwarded_from_message_id");
    expect(message).toMatchObject({ bodyText: "snapshot", bodyFormat: "v3", isForwarded: true });
  });
});

describe("fetchMentionCandidates", () => {
  it("scopes the query to the current channel and maps both candidate types", async () => {
    mockAuthFetch.mockResolvedValue({
      data: {
        users: [{ type: "user", id: "user-1", label: "Ana" }],
        channels: [{ type: "channel", id: "channel-2", label: "anuncios" }],
      },
    });

    const candidates = await fetchMentionCandidates("channel-1", "an");

    expect(mockAuthFetch).toHaveBeenCalledWith(
      expect.stringContaining("/channels/channel-1/mentions?q=an"),
      expect.objectContaining({ method: "GET" }),
    );
    expect(candidates).toEqual([
      { mentionType: "user", id: "user-1", label: "Ana" },
      { mentionType: "channel", id: "channel-2", label: "anuncios" },
    ]);
  });

  it("fails safe to an empty list when the response shape is malformed", async () => {
    mockAuthFetch.mockResolvedValue({ data: { users: [{ id: "user-1" }], channels: null } });

    const candidates = await fetchMentionCandidates("channel-1", "an");

    expect(candidates).toEqual([]);
  });

  it("fails safe to an empty list when a candidate has an out-of-enum type", async () => {
    mockAuthFetch.mockResolvedValue({
      data: { users: [{ type: "admin", id: "user-1", label: "Ana" }], channels: [] },
    });

    const candidates = await fetchMentionCandidates("channel-1", "an");

    expect(candidates).toEqual([]);
  });

  it("fails safe to an empty list when data is missing entirely", async () => {
    mockAuthFetch.mockResolvedValue({});

    const candidates = await fetchMentionCandidates("channel-1", "an");

    expect(candidates).toEqual([]);
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
    await postDMMessage("dm-juliane", "Hi", undefined, undefined, ctrl.signal);
    expect(mockAuthFetch).toHaveBeenCalledWith(
      expect.anything(),
      expect.objectContaining({ signal: ctrl.signal }),
    );
  });

  it("sends parent_message_id when replying in a DM", async () => {
    mockAuthFetch.mockResolvedValue(msgEnvelope(msgRaw()));
    await postDMMessage("dm-juliane", "Mensagem direta", "parent-dm-1");
    const [, options] = mockAuthFetch.mock.calls[0] as [string, RequestInit];
    const body = JSON.parse(options.body as string) as Record<string, unknown>;
    expect(body).toEqual({
      body_text: "Mensagem direta",
      body_format: "v2",
      parent_message_id: "parent-dm-1",
    });
  });

  it("sends referenced_message_id when citing into a DM", async () => {
    mockAuthFetch.mockResolvedValue(msgEnvelope(msgRaw()));
    await postDMMessage("dm-juliane", "Veja", undefined, "source-1");
    const [, options] = mockAuthFetch.mock.calls[0] as [string, RequestInit];
    expect(JSON.parse(options.body as string)).toEqual({
      body_text: "Veja",
      body_format: "v2",
      referenced_message_id: "source-1",
    });
  });

  it("preserves reply and reference with an abort signal", async () => {
    mockAuthFetch.mockResolvedValue(msgEnvelope(msgRaw()));
    const ctrl = new AbortController();
    await postDMMessage("dm-juliane", "Veja", "parent-1", "source-1", ctrl.signal);
    const [, options] = mockAuthFetch.mock.calls[0] as [string, RequestInit];
    expect(JSON.parse(options.body as string)).toEqual({
      body_text: "Veja",
      body_format: "v2",
      parent_message_id: "parent-1",
      referenced_message_id: "source-1",
    });
    expect(options.signal).toBe(ctrl.signal);
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

// ── Favorites (RF-06) ─────────────────────────────────────────────────────────

describe("favoriteMessage / unfavoriteMessage", () => {
  it("POSTs to the favorite path with the encoded message ID", async () => {
    mockAuthFetch.mockResolvedValue(undefined);
    await favoriteMessage("msg/1");
    expect(mockAuthFetch).toHaveBeenCalledWith("/api/chat/messages/msg%2F1/favorite", {
      method: "POST",
      signal: undefined,
    });
  });

  it("DELETEs the favorite path", async () => {
    mockAuthFetch.mockResolvedValue(undefined);
    await unfavoriteMessage("msg-1");
    expect(mockAuthFetch).toHaveBeenCalledWith("/api/chat/messages/msg-1/favorite", {
      method: "DELETE",
      signal: undefined,
    });
  });

  it("propagates errors from authenticatedFetch", async () => {
    mockAuthFetch.mockRejectedValue(new Error("boom"));
    await expect(favoriteMessage("msg-1")).rejects.toThrow("boom");
    await expect(unfavoriteMessage("msg-1")).rejects.toThrow("boom");
  });
});

describe("fetchFavorites", () => {
  const favoriteResponse = {
    data: {
      favorites: [
        {
          message: {
            id: "msg-1",
            sender_id: "user-1",
            sender_display_name: "Ana",
            kind: "user",
            body_text: "olá",
            body_format: "v2",
            status: "active",
            created_at: "2025-01-15T10:00:00Z",
            updated_at: "2025-01-15T10:00:00Z",
            is_favorited: true,
          },
          channel_id: "ch-1",
          favorited_at: "2025-02-01T12:00:00Z",
        },
        {
          message: {
            id: "msg-2",
            sender_id: "user-2",
            kind: "user",
            is_removed: true,
            status: "deleted",
            created_at: "2025-01-10T10:00:00Z",
            updated_at: "2025-01-10T10:00:00Z",
          },
          dm_conversation_id: "dm-1",
          favorited_at: "2025-01-20T12:00:00Z",
        },
      ],
      next_cursor: "cursor-abc",
    },
  };

  it("maps favorites, source IDs, and next cursor", async () => {
    mockAuthFetch.mockResolvedValue(favoriteResponse);
    const page = await fetchFavorites();
    expect(mockAuthFetch).toHaveBeenCalledWith("/api/chat/favorites", {
      method: "GET",
      signal: undefined,
    });
    expect(page.nextCursor).toBe("cursor-abc");
    expect(page.favorites).toHaveLength(2);
    expect(page.favorites[0]).toMatchObject({
      channelId: "ch-1",
      dmConversationId: "",
      favoritedAt: "2025-02-01T12:00:00Z",
    });
    expect(page.favorites[0].message).toMatchObject({ id: "msg-1", isFavorited: true });
    expect(page.favorites[1]).toMatchObject({ channelId: "", dmConversationId: "dm-1" });
    expect(page.favorites[1].message).toMatchObject({ isRemoved: true, status: "deleted" });
  });

  it("appends the before cursor as a query param", async () => {
    mockAuthFetch.mockResolvedValue({ data: { favorites: [] } });
    const page = await fetchFavorites("cur|sor");
    expect(mockAuthFetch).toHaveBeenCalledWith("/api/chat/favorites?before=cur%7Csor", {
      method: "GET",
      signal: undefined,
    });
    expect(page).toEqual({ favorites: [], nextCursor: "" });
  });

  it("propagates errors from authenticatedFetch", async () => {
    mockAuthFetch.mockRejectedValue(new Error("boom"));
    await expect(fetchFavorites()).rejects.toThrow("boom");
  });
});

describe("pinMessage / unpinMessage (RF-05)", () => {
  it("POSTs to the channel-scoped pin path with encoded IDs", async () => {
    mockAuthFetch.mockResolvedValue(undefined);
    await pinMessage({ kind: "channel", id: "ch/1" }, "msg/1");
    expect(mockAuthFetch).toHaveBeenCalledWith("/api/chat/channels/ch%2F1/messages/msg%2F1/pin", {
      method: "POST",
      signal: undefined,
    });
  });

  it("DELETEs the channel-scoped pin path", async () => {
    mockAuthFetch.mockResolvedValue(undefined);
    await unpinMessage({ kind: "channel", id: "ch-1" }, "msg-1");
    expect(mockAuthFetch).toHaveBeenCalledWith("/api/chat/channels/ch-1/messages/msg-1/pin", {
      method: "DELETE",
      signal: undefined,
    });
  });

  it("propagates errors from authenticatedFetch", async () => {
    mockAuthFetch.mockRejectedValue(new Error("forbidden"));
    await expect(pinMessage({ kind: "channel", id: "ch-1" }, "msg-1")).rejects.toThrow("forbidden");
    await expect(unpinMessage({ kind: "channel", id: "ch-1" }, "msg-1")).rejects.toThrow(
      "forbidden",
    );
  });

  it("uses the DM pin path for DM targets", async () => {
    mockAuthFetch.mockResolvedValue(undefined);
    await pinMessage({ kind: "dm", id: "dm-1" }, "msg-1");
    expect(mockAuthFetch).toHaveBeenCalledWith("/api/chat/dm/dm-1/messages/msg-1/pin", {
      method: "POST",
      signal: undefined,
    });
  });
});

describe("fetchPins (RF-05)", () => {
  const pinsResponse = {
    data: {
      pins: [
        {
          message: {
            id: "msg-1",
            sender_id: "user-1",
            sender_display_name: "Ana",
            kind: "user",
            body_text: "importante",
            body_format: "v3",
            status: "active",
            created_at: "2025-01-15T10:00:00Z",
            updated_at: "2025-01-15T10:00:00Z",
          },
          pinned_by_user_id: "mod-1",
          pinned_at: "2025-02-01T12:00:00Z",
        },
        {
          message: {
            id: "msg-2",
            sender_id: "user-2",
            kind: "user",
            is_removed: true,
            status: "deleted",
            created_at: "2025-01-10T10:00:00Z",
            updated_at: "2025-01-10T10:00:00Z",
          },
          pinned_by_user_id: "mod-1",
          pinned_at: "2025-01-20T12:00:00Z",
        },
      ],
    },
  };

  it("maps pins with pinnedBy and preserves deleted-message placeholder", async () => {
    mockAuthFetch.mockResolvedValue(pinsResponse);
    const pins = await fetchPins({ kind: "channel", id: "ch-1" });
    expect(mockAuthFetch).toHaveBeenCalledWith("/api/chat/channels/ch-1/pins", {
      method: "GET",
      signal: undefined,
    });
    expect(pins).toHaveLength(2);
    expect(pins[0]).toMatchObject({ pinnedByUserId: "mod-1", pinnedAt: "2025-02-01T12:00:00Z" });
    expect(pins[0].message).toMatchObject({ id: "msg-1", bodyText: "importante" });
    expect(pins[1].message).toMatchObject({ isRemoved: true, status: "deleted" });
  });

  it("returns an empty list when the payload has no pins", async () => {
    mockAuthFetch.mockResolvedValue({ data: {} });
    await expect(fetchPins({ kind: "channel", id: "ch-1" })).resolves.toEqual([]);
  });

  it("uses the DM pins path for DM targets", async () => {
    mockAuthFetch.mockResolvedValue({ data: { pins: [] } });
    await expect(fetchPins({ kind: "dm", id: "dm-1" })).resolves.toEqual([]);
    expect(mockAuthFetch).toHaveBeenCalledWith("/api/chat/dm/dm-1/pins", {
      method: "GET",
      signal: undefined,
    });
  });

  it("propagates errors from authenticatedFetch", async () => {
    mockAuthFetch.mockRejectedValue(new Error("boom"));
    await expect(fetchPins({ kind: "channel", id: "ch-1" })).rejects.toThrow("boom");
  });
});
