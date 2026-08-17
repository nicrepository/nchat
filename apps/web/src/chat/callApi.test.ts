import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

// ── Mock authenticatedFetch (not callApi itself) ────────────────────────────
// These tests exist to protect the actual HTTP contract the media-service
// token endpoint expects: exact URL, method, headers and body shape for both
// the RF-23 call_id mode and the RF-24 resource_kind/resource_id modes.

const { mockAuthFetch } = vi.hoisted(() => ({
  mockAuthFetch: vi.fn(),
}));

vi.mock("../lib/authClient", () => ({
  authenticatedFetch: (...args: unknown[]) => mockAuthFetch(...args),
}));

import { issueCallToken, issueResourceCallToken } from "./callApi";

beforeEach(() => {
  vi.clearAllMocks();
});
afterEach(() => vi.clearAllMocks());

const tokenEnvelope = {
  data: {
    token: "signed-livekit-token",
    expiresAt: "2026-08-15T12:05:00Z",
    serverUrl: "wss://livekit-dev.nic-labs.com",
  },
};

describe("issueCallToken (RF-23)", () => {
  it("POSTs call_id as the only body field to /api/media/media/livekit/token", async () => {
    mockAuthFetch.mockResolvedValueOnce(tokenEnvelope);
    const callId = "00000000-0000-4000-8000-000000000501";

    await issueCallToken(callId);

    expect(mockAuthFetch).toHaveBeenCalledExactlyOnceWith("/api/media/media/livekit/token", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ call_id: callId }),
    });
  });

  it("returns the token, expiry and server URL exactly as received", async () => {
    mockAuthFetch.mockResolvedValueOnce(tokenEnvelope);

    await expect(issueCallToken("00000000-0000-4000-8000-000000000501")).resolves.toEqual(
      tokenEnvelope.data,
    );
  });

  it("never sends a resource_kind or resource_id field", async () => {
    mockAuthFetch.mockResolvedValueOnce(tokenEnvelope);

    await issueCallToken("00000000-0000-4000-8000-000000000501");

    const [, init] = mockAuthFetch.mock.calls[0] as [string, { body: string }];
    const body = JSON.parse(init.body) as Record<string, unknown>;
    expect(body).not.toHaveProperty("resource_kind");
    expect(body).not.toHaveProperty("resource_id");
  });
});

describe("issueResourceCallToken (RF-24)", () => {
  it("POSTs resource_kind=channel and resource_id, never call_id", async () => {
    mockAuthFetch.mockResolvedValueOnce(tokenEnvelope);
    const channelId = "00000000-0000-4000-8000-000000000601";

    await issueResourceCallToken("channel", channelId);

    expect(mockAuthFetch).toHaveBeenCalledExactlyOnceWith("/api/media/media/livekit/token", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ resource_kind: "channel", resource_id: channelId }),
    });
  });

  it("POSTs resource_kind=dm and resource_id for a group room", async () => {
    mockAuthFetch.mockResolvedValueOnce(tokenEnvelope);
    const groupId = "00000000-0000-4000-8000-000000000602";

    await issueResourceCallToken("dm", groupId);

    expect(mockAuthFetch).toHaveBeenCalledExactlyOnceWith("/api/media/media/livekit/token", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ resource_kind: "dm", resource_id: groupId }),
    });
  });

  it("returns the token, expiry and server URL exactly as received", async () => {
    mockAuthFetch.mockResolvedValueOnce(tokenEnvelope);

    await expect(
      issueResourceCallToken("channel", "00000000-0000-4000-8000-000000000601"),
    ).resolves.toEqual(tokenEnvelope.data);
  });

  it("never sends a call_id, room, user_id or session_id field", async () => {
    mockAuthFetch.mockResolvedValueOnce(tokenEnvelope);

    await issueResourceCallToken("channel", "00000000-0000-4000-8000-000000000601");

    const [, init] = mockAuthFetch.mock.calls[0] as [string, { body: string }];
    const body = JSON.parse(init.body) as Record<string, unknown>;
    expect(Object.keys(body).sort()).toEqual(["resource_id", "resource_kind"]);
  });
});
