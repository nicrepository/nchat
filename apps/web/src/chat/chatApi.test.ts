import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

// ── Mock authenticatedFetch ───────────────────────────────────────────────────

const { mockAuthFetch } = vi.hoisted(() => ({
  mockAuthFetch: vi.fn(),
}));

vi.mock("../lib/authClient", () => ({
  authenticatedFetch: (...args: unknown[]) => mockAuthFetch(...args),
}));

import { fetchChannels, fetchDMs } from "./chatApi";

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
