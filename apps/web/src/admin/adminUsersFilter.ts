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
