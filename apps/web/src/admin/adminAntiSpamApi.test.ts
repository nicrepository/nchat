import { beforeEach, describe, expect, it, vi } from "vitest";

import {
  fetchAntiSpamPolicy,
  fetchCurrentWorkspaceId,
  updateAntiSpamPolicy,
} from "./adminAntiSpamApi";

// ── Mock authenticatedFetch ────────────────────────────────────────────────

const { mockAuthFetch } = vi.hoisted(() => ({
  mockAuthFetch: vi.fn(),
}));

vi.mock("../lib/authClient", () => ({
  authenticatedFetch: (...args: unknown[]) => mockAuthFetch(...args),
}));

beforeEach(() => {
  mockAuthFetch.mockReset();
});

// ── Tests ──────────────────────────────────────────────────────────────────

describe("fetchCurrentWorkspaceId", () => {
  it("reads the workspace from the caller's own sidebar", async () => {
    mockAuthFetch.mockResolvedValue({ data: { workspace: { id: "ws-1" } } });

    await expect(fetchCurrentWorkspaceId()).resolves.toBe("ws-1");
    expect(mockAuthFetch).toHaveBeenCalledWith(
      expect.stringContaining("/api/chat/sidebar"),
      expect.objectContaining({ method: "GET" }),
    );
  });

  it("rejects when the response carries no workspace", async () => {
    mockAuthFetch.mockResolvedValue({ data: {} });

    await expect(fetchCurrentWorkspaceId()).rejects.toThrow();
  });
});

describe("fetchAntiSpamPolicy", () => {
  it("maps the policy and the server-supplied bounds", async () => {
    mockAuthFetch.mockResolvedValue({
      data: { workspace_id: "ws-1", message_rate_limit_per_minute: 45, min: 1, max: 600 },
    });

    const policy = await fetchAntiSpamPolicy("ws-1");

    expect(mockAuthFetch).toHaveBeenCalledWith(
      "/api/chat/workspaces/ws-1/anti-spam",
      expect.objectContaining({ method: "GET" }),
    );
    expect(policy).toEqual({ workspaceId: "ws-1", messagesPerMinute: 45, min: 1, max: 600 });
  });
});

describe("updateAntiSpamPolicy", () => {
  it("PATCHes only the rate limit field", async () => {
    mockAuthFetch.mockResolvedValue({
      data: { workspace_id: "ws-1", message_rate_limit_per_minute: 30, min: 1, max: 600 },
    });

    const policy = await updateAntiSpamPolicy("ws-1", 30);

    const [, init] = mockAuthFetch.mock.calls[0] as [string, RequestInit];
    expect(init.method).toBe("PATCH");
    // The endpoint rejects unknown fields, so the body must carry nothing else.
    expect(JSON.parse(init.body as string)).toEqual({ message_rate_limit_per_minute: 30 });
    expect(policy.messagesPerMinute).toBe(30);
  });

  it("escapes the workspace ID in the path", async () => {
    mockAuthFetch.mockResolvedValue({
      data: { workspace_id: "a/b", message_rate_limit_per_minute: 30, min: 1, max: 600 },
    });

    await updateAntiSpamPolicy("a/b", 30);

    expect(mockAuthFetch).toHaveBeenCalledWith(
      "/api/chat/workspaces/a%2Fb/anti-spam",
      expect.anything(),
    );
  });

  it("propagates request failures instead of swallowing them", async () => {
    mockAuthFetch.mockRejectedValue(new Error("boom"));

    await expect(updateAntiSpamPolicy("ws-1", 30)).rejects.toThrow("boom");
  });
});
