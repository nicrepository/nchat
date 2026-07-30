import { describe, expect, it } from "vitest";

import type { AdminUser } from "./adminUsersApi";
import { FILTER_CHIPS, filterAdminUsers, sortAdminUsersForDisplay } from "./adminUsersFilter";

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

// ── Display ordering ───────────────────────────────────────────────────────
//
// The server pages by user id, which is meaningless to read, so the table
// orders what it has here. These pin the order itself; AdminUsersPage.test.tsx
// pins that the table actually applies it across a loadMore.

function row(over: Partial<AdminUser> & { id: string }): AdminUser {
  return {
    email: `${over.id}@example.com`,
    displayName: over.id,
    status: "active",
    authSource: "local",
    createdAt: "2024-01-01T00:00:00Z",
    ...over,
  };
}

describe("sortAdminUsersForDisplay", () => {
  it("orders by visible name, not by id", () => {
    const sorted = sortAdminUsersForDisplay([
      row({ id: "z1", displayName: "Ana" }),
      row({ id: "a1", displayName: "Zoe" }),
      row({ id: "m1", displayName: "Bruno" }),
    ]);
    expect(sorted.map((u) => u.displayName)).toEqual(["Ana", "Bruno", "Zoe"]);
  });

  it("falls back to the e-mail when there is no display name", () => {
    const sorted = sortAdminUsersForDisplay([
      row({ id: "1", displayName: "Zoe", email: "zoe@example.com" }),
      row({ id: "2", displayName: "   ", email: "ana@example.com" }),
    ]);
    // The blank name is not treated as a name that sorts first: what the table
    // shows for that row is the address, so that is what it sorts on.
    expect(sorted.map((u) => u.id)).toEqual(["2", "1"]);
  });

  it("breaks a tie on name with the e-mail, then with the id", () => {
    const sorted = sortAdminUsersForDisplay([
      row({ id: "c", displayName: "Ana", email: "ana-b@example.com" }),
      row({ id: "a", displayName: "Ana", email: "ana-a@example.com" }),
      row({ id: "b", displayName: "Ana", email: "ana-b@example.com" }),
    ]);
    expect(sorted.map((u) => u.id)).toEqual(["a", "b", "c"]);
  });

  it("is case- and accent-insensitive, so names read alphabetically", () => {
    const sorted = sortAdminUsersForDisplay([
      row({ id: "1", displayName: "bruno" }),
      row({ id: "2", displayName: "Álvaro" }),
      row({ id: "3", displayName: "Ana" }),
    ]);
    expect(sorted.map((u) => u.displayName)).toEqual(["Álvaro", "Ana", "bruno"]);
  });

  it("is total, so the same input always yields the same order", () => {
    const users = [
      row({ id: "c", displayName: "Ana", email: "same@example.com" }),
      row({ id: "a", displayName: "Ana", email: "same@example.com" }),
      row({ id: "b", displayName: "Ana", email: "same@example.com" }),
    ];
    const first = sortAdminUsersForDisplay(users).map((u) => u.id);
    const again = sortAdminUsersForDisplay([...users].reverse()).map((u) => u.id);
    expect(first).toEqual(again);
  });

  it("does not mutate or reorder the caller's array", () => {
    const users = [row({ id: "z", displayName: "Zoe" }), row({ id: "a", displayName: "Ana" })];
    const snapshot = users.map((u) => u.id);

    const sorted = sortAdminUsersForDisplay(users);

    expect(users.map((u) => u.id)).toEqual(snapshot);
    expect(sorted).not.toBe(users);
  });
});
