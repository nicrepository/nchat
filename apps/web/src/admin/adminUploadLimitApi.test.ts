import { beforeEach, describe, expect, it, vi } from "vitest";

import { fetchUploadLimitPolicy, updateUploadLimitPolicy } from "./adminUploadLimitApi";

const { mockAuthFetch } = vi.hoisted(() => ({
  mockAuthFetch: vi.fn(),
}));

vi.mock("../lib/authClient", () => ({
  authenticatedFetch: (...args: unknown[]) => mockAuthFetch(...args),
}));

beforeEach(() => {
  mockAuthFetch.mockReset();
});

const policyPayload = {
  workspace_id: "ws-1",
  max_upload_bytes: 262144000,
  min: 1048576,
  max: 536870912,
};

describe("fetchUploadLimitPolicy", () => {
  it("maps the policy and the server-supplied bounds", async () => {
    mockAuthFetch.mockResolvedValue({ data: policyPayload });

    const policy = await fetchUploadLimitPolicy("ws-1");

    expect(mockAuthFetch).toHaveBeenCalledWith(
      "/api/chat/workspaces/ws-1/upload-limit",
      expect.objectContaining({ method: "GET" }),
    );
    expect(policy).toEqual({
      workspaceId: "ws-1",
      maxUploadBytes: 262144000,
      min: 1048576,
      max: 536870912,
    });
  });

  it("encodes the workspace ID into the path", async () => {
    mockAuthFetch.mockResolvedValue({ data: { ...policyPayload, workspace_id: "a/b" } });

    await fetchUploadLimitPolicy("a/b");

    expect(mockAuthFetch).toHaveBeenCalledWith(
      "/api/chat/workspaces/a%2Fb/upload-limit",
      expect.objectContaining({ method: "GET" }),
    );
  });
});

describe("updateUploadLimitPolicy", () => {
  it("sends only the limit, in bytes", async () => {
    mockAuthFetch.mockResolvedValue({ data: { ...policyPayload, max_upload_bytes: 104857600 } });

    const policy = await updateUploadLimitPolicy("ws-1", 104857600);

    const [url, init] = mockAuthFetch.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/chat/workspaces/ws-1/upload-limit");
    expect(init.method).toBe("PATCH");
    expect(JSON.parse(init.body as string)).toEqual({ max_upload_bytes: 104857600 });
    expect(policy.maxUploadBytes).toBe(104857600);
  });
});
