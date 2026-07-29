import { describe, expect, it } from "vitest";

import type { AdminUser } from "./adminUsersApi";
import { FILTER_CHIPS, filterAdminUsers } from "./adminUsersFilter";

function user(overrides: Partial<AdminUser> = {}): AdminUser {
  return {
    id: "u1",
    email: "alice@example.com",
    displayName: "Alice Andrade",
    status: "active",
    authSource: "manual",
    createdAt: "2024-01-15T10:00:00Z",
    ...overrides,
  };
}

const ALICE = user();
const BOB = user({
  id: "u2",
  email: "bob@example.com",
  displayName: "Bob Bastos",
  status: "suspended",
});
const USERS = [ALICE, BOB];

describe("filterAdminUsers", () => {
  it("returns everyone for the 'all' chip with no search", () => {
    expect(filterAdminUsers(USERS, "all", "")).toEqual(USERS);
  });

  it.each([
    ["active", ALICE],
    ["suspended", BOB],
  ] as const)("filters to %s users", (chip, expected) => {
    expect(filterAdminUsers(USERS, chip, "")).toEqual([expected]);
  });

  it("matches status case-insensitively", () => {
    const shouting = user({ id: "u3", status: "ACTIVE" });
    expect(filterAdminUsers([shouting], "active", "")).toEqual([shouting]);
  });

  // A status the UI has no chip for must not be swept into another bucket.
  it("excludes an unknown status from both status chips", () => {
    const invited = user({ id: "u4", status: "invited" });
    expect(filterAdminUsers([invited], "active", "")).toEqual([]);
    expect(filterAdminUsers([invited], "suspended", "")).toEqual([]);
    expect(filterAdminUsers([invited], "all", "")).toEqual([invited]);
  });

  // These carry no data yet. Showing everyone under "Admins" would assert
  // something false about who administers the workspace.
  it.each(["admins", "invites"] as const)("returns nothing for the %s chip", (chip) => {
    expect(filterAdminUsers(USERS, chip, "")).toEqual([]);
  });

  it("searches display name and e-mail, case-insensitively", () => {
    expect(filterAdminUsers(USERS, "all", "BOB")).toEqual([BOB]);
    expect(filterAdminUsers(USERS, "all", "alice@")).toEqual([ALICE]);
  });

  it("ignores surrounding whitespace in the query", () => {
    expect(filterAdminUsers(USERS, "all", "   ")).toEqual(USERS);
    expect(filterAdminUsers(USERS, "all", "  bob  ")).toEqual([BOB]);
  });

  it("combines chip and search", () => {
    expect(filterAdminUsers(USERS, "active", "bob")).toEqual([]);
    expect(filterAdminUsers(USERS, "active", "alice")).toEqual([ALICE]);
  });

  it("returns an empty list when nothing matches", () => {
    expect(filterAdminUsers(USERS, "all", "nobody")).toEqual([]);
  });

  it("does not mutate the input", () => {
    const input = [...USERS];
    filterAdminUsers(input, "active", "alice");
    expect(input).toEqual(USERS);
  });

  it("exposes every chip the page renders", () => {
    expect(FILTER_CHIPS.map((c) => c.id)).toEqual([
      "all",
      "active",
      "suspended",
      "admins",
      "invites",
    ]);
  });
});
