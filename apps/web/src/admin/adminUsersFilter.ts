/**
 * Filtering for the admin users table (issue #425).
 *
 * A pure function rather than a `useMemo` body: the rules below are the part
 * most likely to be argued about and adjusted, and keeping them out of the
 * component means they can be tested by calling them, with no render.
 *
 * Sibling of `antiSpamForm.ts`, which plays the same role for the anti-spam
 * page.
 */

import type { AdminUser } from "./adminUsersApi";

export type FilterChip = "all" | "active" | "suspended" | "admins" | "invites";

export const FILTER_CHIPS: { id: FilterChip; label: string }[] = [
  { id: "all", label: "Todos" },
  { id: "active", label: "Ativos" },
  { id: "suspended", label: "Suspensos" },
  { id: "admins", label: "Admins" },
  { id: "invites", label: "Convites pendentes" },
];

/**
 * Chips with no data behind them yet.
 *
 * The listing carries no role and no invite state, so these two can only ever
 * be empty. They resolve to an empty list rather than to every user, because
 * showing everyone under "Admins" would assert something false about who
 * administers the workspace.
 */
const UNSUPPORTED_CHIPS: ReadonlySet<FilterChip> = new Set<FilterChip>(["admins", "invites"]);

function matchesChip(user: AdminUser, chip: FilterChip): boolean {
  if (chip === "all") return true;
  return user.status.toLowerCase() === chip;
}

/** Case-insensitive match on display name or e-mail. */
function matchesSearch(user: AdminUser, normalizedQuery: string): boolean {
  if (!normalizedQuery) return true;
  return (
    user.displayName.toLowerCase().includes(normalizedQuery) ||
    user.email.toLowerCase().includes(normalizedQuery)
  );
}

/**
 * Applies the active chip and the search box to `users`.
 *
 * Deterministic and allocation-light: one pass, no sorting, input never
 * mutated. `"active"` and `"suspended"` match the status values the API
 * returns, so a status the UI does not know about simply matches neither.
 */
export function filterAdminUsers(
  users: readonly AdminUser[],
  chip: FilterChip,
  search: string,
): AdminUser[] {
  if (UNSUPPORTED_CHIPS.has(chip)) return [];
  const query = search.trim().toLowerCase();
  return users.filter((user) => matchesChip(user, chip) && matchesSearch(user, query));
}

/** The label the table shows for a user, which is what it should sort on. */
function visibleName(user: AdminUser): string {
  return user.displayName.trim() || user.email;
}

/**
 * Orders users the way the table presents them: by name, then e-mail, then id.
 *
 * The server pages by user id, because that is the column its index covers, and
 * id order is meaningless to a person reading the table. Sorting is therefore a
 * presentation concern and lives here — applied to the rows already loaded, on
 * the way to the screen.
 *
 * That means the visible order is only total across what has been fetched: a
 * later page can bring a name that belongs earlier in the alphabet, and it will
 * appear above rows already shown. The alternative is fetching every page
 * before rendering anything, which is exactly the unbounded read the pagination
 * exists to prevent.
 *
 * Returns a new array — the caller's is not reordered, so this can never
 * disturb the sequence the cursor was derived from.
 */
export function sortAdminUsersForDisplay(users: readonly AdminUser[]): AdminUser[] {
  return [...users].sort(
    (a, b) =>
      visibleName(a).localeCompare(visibleName(b), "pt-BR", { sensitivity: "base" }) ||
      a.email.localeCompare(b.email, "pt-BR", { sensitivity: "base" }) ||
      // Ids are opaque, so compare them as plain strings: the tiebreak only has
      // to be stable and total, not meaningful.
      (a.id < b.id ? -1 : a.id > b.id ? 1 : 0),
  );
}
