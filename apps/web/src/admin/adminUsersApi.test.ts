import { beforeEach, describe, expect, it, vi } from "vitest";

import { ApiRequestError } from "../lib/api";
import {
  ERR_INVALID_RESPONSE,
  classifyAdminError,
  createAdminInvite,
  listAdminUsers,
  updateUserStatus,
} from "./adminUsersApi";

/** A row that satisfies the DTO, used as the base for malformed variants. */
const VALID_ROW = {
  id: "u1",
  email: "alice@example.com",
  display_name: "Alice",
  full_name: "Alice Andrade",
  status: "active",
  auth_source: "manual",
  created_at: "2024-01-01T00:00:00Z",
};

const META = { next_cursor: null, has_more: false };

/** Wraps rows in the paginated envelope the service actually returns. */
function pageRaw(rows: unknown[], meta: Record<string, unknown> = META) {
  return { data: { data: rows, pagination: { limit: 50, ...meta } } };
}
const page = pageRaw;

// ── Mock authenticatedFetch ────────────────────────────────────────────────

const { mockAuthFetch } = vi.hoisted(() => ({
  mockAuthFetch: vi.fn(),
}));

vi.mock("../lib/authClient", () => ({
  authenticatedFetch: (...args: unknown[]) => mockAuthFetch(...args),
}));

// Calls accumulate across tests otherwise, so assertions reading calls[0]
// would inspect an earlier test's request and pass without checking anything.
beforeEach(() => {
  mockAuthFetch.mockReset();
});

// ── Tests ──────────────────────────────────────────────────────────────────

describe("listAdminUsers", () => {
  it("calls the canonical auth-service admin URL with GET", async () => {
    mockAuthFetch.mockResolvedValue(page([]));

    await listAdminUsers();

    expect(mockAuthFetch).toHaveBeenCalledWith(
      "/api/auth/admin/users?limit=50",
      expect.objectContaining({ method: "GET" }),
    );
  });

  it("does not target the admin-service prefix, which serves no user routes", async () => {
    mockAuthFetch.mockResolvedValue(page([]));

    await listAdminUsers();

    expect(mockAuthFetch.mock.calls[0][0] as string).not.toContain("/api/admin/users");
  });

  it("sends no workspace identifier — the server derives it from the session", async () => {
    mockAuthFetch.mockResolvedValue(page([]));

    await listAdminUsers();

    const [url, init] = mockAuthFetch.mock.calls[0] as [string, RequestInit];
    expect(url).not.toMatch(/workspace/i);
    expect(init.body).toBeUndefined();
  });

  it("maps snake_case response fields to camelCase", async () => {
    mockAuthFetch.mockResolvedValue(
      pageRaw([
        {
          id: "u1",
          email: "alice@example.com",
          display_name: "Alice",
          full_name: "Alice Andrade",
          status: "active",
          auth_source: "manual",
          created_at: "2024-01-01T00:00:00Z",
        },
      ]),
    );

    const { users } = await listAdminUsers();

    expect(users).toHaveLength(1);
    expect(users[0]).toEqual({
      id: "u1",
      email: "alice@example.com",
      displayName: "Alice",
      fullName: "Alice Andrade",
      status: "active",
      authSource: "manual",
      createdAt: "2024-01-01T00:00:00Z",
    });
  });

  it("maps optional full_name as undefined when absent", async () => {
    mockAuthFetch.mockResolvedValue(
      pageRaw([
        {
          id: "u2",
          email: "bob@example.com",
          display_name: "Bob",
          status: "active",
          auth_source: "manual",
          created_at: "2024-01-01T00:00:00Z",
        },
      ]),
    );

    const { users } = await listAdminUsers();
    expect(users[0].fullName).toBeUndefined();
  });

  it("treats an empty data array as the only valid empty result", async () => {
    mockAuthFetch.mockResolvedValue(page([]));
    await expect(listAdminUsers()).resolves.toEqual({
      users: [],
      nextCursor: null,
      hasMore: false,
    });
  });

  // A 200 whose body is not the agreed shape is a broken contract, not an
  // empty workspace. Returning [] here is the same defect as swallowing a 404,
  // one layer further in.
  it.each([
    ["null body", null],
    ["missing outer envelope", {}],
    ["data: null", { data: null }],
    ["page without users array", { data: { pagination: META } }],
    ["users as object", { data: { data: {}, pagination: META } }],
    ["top-level array", [{ id: "u1" }]],
    ["missing pagination", { data: { data: [] } }],
    ["pagination not an object", { data: { data: [], pagination: "nope" } }],
    [
      "has_more not boolean",
      { data: { data: [], pagination: { has_more: "yes", next_cursor: null } } },
    ],
    [
      "next_cursor wrong type",
      { data: { data: [], pagination: { has_more: false, next_cursor: 7 } } },
    ],
    [
      "has_more true without cursor",
      { data: { data: [], pagination: { has_more: true, next_cursor: null } } },
    ],
  ])("rejects a 200 with %s", async (_label, body) => {
    mockAuthFetch.mockResolvedValue(body);

    await expect(listAdminUsers()).rejects.toMatchObject({
      name: "ApiRequestError",
      code: ERR_INVALID_RESPONSE,
    });
  });

  it.each([
    ["a non-object row", pageRaw(["nope"])],
    ["a missing required field", pageRaw([{ id: "u1", email: "a@b.com" }])],
    ["a non-string id", pageRaw([VALID_ROW, { ...VALID_ROW, id: 7 }])],
    ["a non-string full_name", pageRaw([{ ...VALID_ROW, full_name: { a: 1 } }])],
  ])("rejects a row with %s", async (_label, body) => {
    mockAuthFetch.mockResolvedValue(body);

    await expect(listAdminUsers()).rejects.toMatchObject({ code: ERR_INVALID_RESPONSE });
  });

  it("accepts a row whose full_name is absent or null", async () => {
    const withoutFullName = { ...VALID_ROW };
    delete (withoutFullName as Partial<typeof VALID_ROW>).full_name;
    mockAuthFetch.mockResolvedValue(
      pageRaw([withoutFullName, { ...VALID_ROW, id: "u9", full_name: null }]),
    );

    const { users } = await listAdminUsers();
    expect(users.map((u) => u.fullName)).toEqual([undefined, undefined]);
  });

  it("classifies a contract violation as a recoverable error, not a permissions problem", async () => {
    mockAuthFetch.mockResolvedValue({ data: null });

    const err = await listAdminUsers().catch((e: unknown) => e);
    expect(classifyAdminError(err)).toBe("error");
  });

  // The regression this issue is about: a 404 used to be swallowed and rendered
  // as an empty workspace, hiding a broken deployment for as long as it did.
  it.each([401, 403, 404, 500])("propagates HTTP %i instead of returning []", async (status) => {
    const err = new ApiRequestError(status, "code", "message");
    mockAuthFetch.mockRejectedValue(err);

    await expect(listAdminUsers()).rejects.toThrow(err);
  });

  it("propagates network failures", async () => {
    const err = new ApiRequestError(0, "network_error", "Network error");
    mockAuthFetch.mockRejectedValue(err);

    await expect(listAdminUsers()).rejects.toThrow(err);
  });

  it("requests the next page with the cursor, URL-encoded", async () => {
    mockAuthFetch.mockResolvedValue(page([]));

    await listAdminUsers({ cursor: "eyJ2IjoxfQ+/=" });

    const url = mockAuthFetch.mock.calls[0][0] as string;
    expect(url).toContain("limit=50");
    // URLSearchParams escapes the opaque token rather than splicing it raw.
    expect(url).toContain("cursor=eyJ2IjoxfQ%2B%2F%3D");
  });

  it("omits the cursor parameter on the first page", async () => {
    mockAuthFetch.mockResolvedValue(page([]));

    await listAdminUsers();

    expect(mockAuthFetch.mock.calls[0][0] as string).not.toContain("cursor");
  });

  it("honours an explicit limit", async () => {
    mockAuthFetch.mockResolvedValue(page([]));

    await listAdminUsers({ limit: 10 });

    expect(mockAuthFetch.mock.calls[0][0] as string).toContain("limit=10");
  });

  it("surfaces nextCursor and hasMore from the pagination block", async () => {
    mockAuthFetch.mockResolvedValue(
      pageRaw([VALID_ROW], { next_cursor: "next-token", has_more: true }),
    );

    const result = await listAdminUsers();

    expect(result.nextCursor).toBe("next-token");
    expect(result.hasMore).toBe(true);
  });

  it("propagates JSON parsing failures", async () => {
    const err = new SyntaxError("Unexpected token < in JSON");
    mockAuthFetch.mockRejectedValue(err);

    await expect(listAdminUsers()).rejects.toThrow(err);
  });
});

describe("classifyAdminError", () => {
  it("maps 401 to unauthorized", () => {
    expect(classifyAdminError(new ApiRequestError(401, "unauthorized", "x"))).toBe("unauthorized");
  });

  it("maps 403 to forbidden", () => {
    expect(classifyAdminError(new ApiRequestError(403, "forbidden", "x"))).toBe("forbidden");
  });

  // A missing route is a deployment failure, not a permissions one, and must
  // never read as "no users".
  it("maps 429 to rate-limited", () => {
    expect(classifyAdminError(new ApiRequestError(429, "rate_limited", "x"))).toBe("rate-limited");
  });

  it.each([404, 500, 0])("maps HTTP %i to a generic recoverable error", (status) => {
    expect(classifyAdminError(new ApiRequestError(status, "code", "x"))).toBe("error");
  });

  it("maps non-API errors to a generic error", () => {
    expect(classifyAdminError(new Error("boom"))).toBe("error");
  });
});

describe("createAdminInvite", () => {
  it("POSTs to the canonical invite endpoint", async () => {
    mockAuthFetch.mockResolvedValue({ id: "inv-1" });

    await createAdminInvite({ email: "new@example.com", displayName: "New User" });

    expect(mockAuthFetch).toHaveBeenCalledWith(
      "/api/auth/admin/invites",
      expect.objectContaining({
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ email: "new@example.com", display_name: "New User" }),
      }),
    );
  });

  it("trims whitespace from the submitted values", async () => {
    mockAuthFetch.mockResolvedValue({});

    await createAdminInvite({ email: "  new@example.com  ", displayName: "  New  " });

    const body = JSON.parse((mockAuthFetch.mock.calls[0][1] as RequestInit).body as string);
    expect(body).toEqual({ email: "new@example.com", display_name: "New" });
  });

  it("sends no role or workspace field", async () => {
    mockAuthFetch.mockResolvedValue({});

    await createAdminInvite({ email: "new@example.com", displayName: "New" });

    const body = JSON.parse((mockAuthFetch.mock.calls[0][1] as RequestInit).body as string);
    expect(body).not.toHaveProperty("role");
    expect(body).not.toHaveProperty("workspace_id");
  });

  // authenticatedFetch only retries on 401, so a 429 must surface after exactly
  // one POST. An automatic resubmit would spend a budget already exhausted.
  it("propagates a 429 without resubmitting", async () => {
    mockAuthFetch.mockRejectedValue(
      new ApiRequestError(429, "rate_limited", "rate limit exceeded"),
    );

    await expect(
      createAdminInvite({ email: "new@example.com", displayName: "New" }),
    ).rejects.toMatchObject({ status: 429 });
    expect(mockAuthFetch).toHaveBeenCalledTimes(1);
  });

  it("propagates invite failures", async () => {
    const err = new ApiRequestError(409, "conflict", "invite conflict");
    mockAuthFetch.mockRejectedValue(err);

    await expect(
      createAdminInvite({ email: "dup@example.com", displayName: "Dup" }),
    ).rejects.toThrow(err);
  });

  it("does not include X-NChat-Admin-Token", async () => {
    mockAuthFetch.mockResolvedValue({});

    await createAdminInvite({ email: "new@example.com", displayName: "New" });

    const headers = (mockAuthFetch.mock.calls[0][1] as RequestInit).headers as
      | Record<string, string>
      | undefined;
    expect(headers?.["x-nchat-admin-token"] ?? headers?.["X-NChat-Admin-Token"]).toBeUndefined();
  });
});

describe("updateUserStatus", () => {
  it("PATCHes the auth-service admin path with the status body", async () => {
    mockAuthFetch.mockResolvedValue({
      data: {
        id: "u1",
        email: "alice@example.com",
        display_name: "Alice",
        status: "suspended",
        auth_source: "manual",
        created_at: "2024-01-01T00:00:00Z",
      },
    });

    await updateUserStatus("u1", "suspended");

    expect(mockAuthFetch).toHaveBeenCalledWith(
      "/api/auth/admin/users/u1/status",
      expect.objectContaining({
        method: "PATCH",
        body: JSON.stringify({ status: "suspended" }),
      }),
    );
  });

  it("maps snake_case response to AdminUser", async () => {
    mockAuthFetch.mockResolvedValue({
      data: {
        id: "u2",
        email: "bob@example.com",
        display_name: "Bob",
        status: "active",
        auth_source: "manual",
        created_at: "2024-03-01T00:00:00Z",
      },
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
      data: {
        id: "u1",
        email: "a@b.com",
        display_name: "A",
        status: "suspended",
        auth_source: "manual",
        created_at: "2024-01-01T00:00:00Z",
      },
    });

    await updateUserStatus("u1", "suspended");

    const headers = (mockAuthFetch.mock.calls[0][1] as RequestInit).headers as
      | Record<string, string>
      | undefined;
    expect(headers?.["x-nchat-admin-token"] ?? headers?.["X-NChat-Admin-Token"]).toBeUndefined();
  });
});
