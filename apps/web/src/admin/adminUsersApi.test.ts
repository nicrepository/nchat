import { describe, expect, it, vi } from "vitest";

import { ApiRequestError } from "../lib/api";
import { listAdminUsers, updateUserStatus } from "./adminUsersApi";

// ── Mock authenticatedFetch ────────────────────────────────────────────────

const { mockAuthFetch } = vi.hoisted(() => ({
  mockAuthFetch: vi.fn(),
}));

vi.mock("../lib/authClient", () => ({
  authenticatedFetch: (...args: unknown[]) => mockAuthFetch(...args),
}));

// ── Tests ──────────────────────────────────────────────────────────────────

describe("listAdminUsers", () => {
  it("maps snake_case response fields to camelCase", async () => {
    mockAuthFetch.mockResolvedValue([
      {
        id: "u1",
        email: "alice@example.com",
        display_name: "Alice",
        full_name: "Alice Andrade",
        status: "active",
        auth_source: "local",
        created_at: "2024-01-01T00:00:00Z",
      },
    ]);

    const users = await listAdminUsers();

    expect(mockAuthFetch).toHaveBeenCalledWith(
      expect.stringContaining("/api/admin/users"),
      expect.objectContaining({ method: "GET" }),
    );
    expect(users).toHaveLength(1);
    expect(users[0]).toEqual({
      id: "u1",
      email: "alice@example.com",
      displayName: "Alice",
      fullName: "Alice Andrade",
      status: "active",
      authSource: "local",
      createdAt: "2024-01-01T00:00:00Z",
    });
  });

  it("maps optional full_name as undefined when absent", async () => {
    mockAuthFetch.mockResolvedValue([
      {
        id: "u2",
        email: "bob@example.com",
        display_name: "Bob",
        status: "active",
        auth_source: "local",
        created_at: "2024-01-01T00:00:00Z",
      },
    ]);

    const users = await listAdminUsers();
    expect(users[0].fullName).toBeUndefined();
  });

  it("returns empty array when API returns null", async () => {
    mockAuthFetch.mockResolvedValue(null);
    const users = await listAdminUsers();
    expect(users).toEqual([]);
  });

  it("returns empty array on 404 (endpoint not yet deployed)", async () => {
    mockAuthFetch.mockRejectedValue(new ApiRequestError(404, "not_found", "Not Found"));
    const users = await listAdminUsers();
    expect(users).toEqual([]);
  });

  it("rethrows non-404 API errors", async () => {
    const err = new ApiRequestError(500, "internal_error", "Server Error");
    mockAuthFetch.mockRejectedValue(err);
    await expect(listAdminUsers()).rejects.toThrow(err);
  });

  it("rethrows non-ApiRequestError errors", async () => {
    const err = new Error("network failure");
    mockAuthFetch.mockRejectedValue(err);
    await expect(listAdminUsers()).rejects.toThrow(err);
  });
});

describe("updateUserStatus", () => {
  it("PATCHes the correct URL with the status body", async () => {
    mockAuthFetch.mockResolvedValue({
      id: "u1",
      email: "alice@example.com",
      display_name: "Alice",
      status: "suspended",
      auth_source: "manual",
      created_at: "2024-01-01T00:00:00Z",
    });

    await updateUserStatus("u1", "suspended");

    expect(mockAuthFetch).toHaveBeenCalledWith(
      expect.stringContaining("/api/admin/users/u1/status"),
      expect.objectContaining({
        method: "PATCH",
        body: JSON.stringify({ status: "suspended" }),
      }),
    );
  });

  it("maps snake_case response to AdminUser", async () => {
    mockAuthFetch.mockResolvedValue({
      id: "u2",
      email: "bob@example.com",
      display_name: "Bob",
      status: "active",
      auth_source: "manual",
      created_at: "2024-03-01T00:00:00Z",
    });

    const user = await updateUserStatus("u2", "active");

    expect(user).toEqual({
      id: "u2",
      email: "bob@example.com",
      displayName: "Bob",
      fullName: undefined,
      status: "active",
      authSource: "manual",
      createdAt: "2024-03-01T00:00:00Z",
    });
  });

  it("propagates non-2xx errors", async () => {
    const err = new ApiRequestError(422, "invalid_transition", "status transition not allowed");
    mockAuthFetch.mockRejectedValue(err);

    await expect(updateUserStatus("u1", "active")).rejects.toThrow(err);
  });

  it("propagates network errors", async () => {
    mockAuthFetch.mockRejectedValue(new Error("network failure"));
    await expect(updateUserStatus("u1", "suspended")).rejects.toThrow("network failure");
  });

  it("does not include X-NChat-Admin-Token", async () => {
    mockAuthFetch.mockResolvedValue({
      id: "u1",
      email: "a@b.com",
      display_name: "A",
      status: "suspended",
      auth_source: "manual",
      created_at: "2024-01-01T00:00:00Z",
    });

    await updateUserStatus("u1", "suspended");

    const callArgs = mockAuthFetch.mock.calls[0];
    const headers = (callArgs[1] as RequestInit)?.headers as Record<string, string> | undefined;
    expect(headers?.["x-nchat-admin-token"] ?? headers?.["X-NChat-Admin-Token"]).toBeUndefined();
  });
});
