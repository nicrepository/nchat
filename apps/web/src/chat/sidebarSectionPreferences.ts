/**
 * Persists only the sidebar's per-section presentation state (issue #779):
 * whether Canais / Mensagens diretas / Grupos is collapsed, and whether a
 * collapsed section shows its unread conversations. Never message content,
 * never a token, never anything server-authoritative — scoped by
 * (workspace, user) exactly like sidebarUnreadPersistence, so a different
 * account on the same browser never sees another one's layout choice.
 *
 * This is presentation only. The server's unread counts and channel/DM
 * membership remain the single source of truth; this module only remembers
 * how the viewer last chose to look at them.
 */

import { useMemo, useState } from "react";

export type SidebarSectionKind = "channels" | "directs" | "groups";

export interface SidebarSectionPref {
  collapsed: boolean;
  showUnreadOnly: boolean;
}

export type SidebarSectionPrefs = Record<SidebarSectionKind, SidebarSectionPref>;

const SECTION_KINDS: readonly SidebarSectionKind[] = ["channels", "directs", "groups"];

const DEFAULT_PREF: SidebarSectionPref = { collapsed: false, showUnreadOnly: false };

export const DEFAULT_SECTION_PREFS: SidebarSectionPrefs = {
  channels: { ...DEFAULT_PREF },
  directs: { ...DEFAULT_PREF },
  groups: { ...DEFAULT_PREF },
};

function storageKey(userId: string, workspaceId: string): string {
  return `nchat.sidebar.sections.v1:${encodeURIComponent(workspaceId)}:${encodeURIComponent(userId)}`;
}

function isValidPref(raw: unknown): raw is SidebarSectionPref {
  if (typeof raw !== "object" || raw === null) return false;
  const pref = raw as Record<string, unknown>;
  return typeof pref.collapsed === "boolean" && typeof pref.showUnreadOnly === "boolean";
}

/** Never throws. Returns safe defaults for missing, corrupt, or partial data. */
export function loadSectionPrefs(userId: string, workspaceId: string): SidebarSectionPrefs {
  try {
    const raw = localStorage.getItem(storageKey(userId, workspaceId));
    if (!raw) return DEFAULT_SECTION_PREFS;
    const parsed: unknown = JSON.parse(raw);
    if (typeof parsed !== "object" || parsed === null) return DEFAULT_SECTION_PREFS;
    const source = parsed as Record<string, unknown>;
    const result = { ...DEFAULT_SECTION_PREFS };
    for (const kind of SECTION_KINDS) {
      const candidate = source[kind];
      if (isValidPref(candidate)) {
        result[kind] = { collapsed: candidate.collapsed, showUnreadOnly: candidate.showUnreadOnly };
      }
    }
    return result;
  } catch {
    return DEFAULT_SECTION_PREFS;
  }
}

/** Never throws; a failed write is a no-op preference miss, not an app-breaking error. */
export function saveSectionPrefs(
  userId: string,
  workspaceId: string,
  prefs: SidebarSectionPrefs,
): void {
  try {
    localStorage.setItem(storageKey(userId, workspaceId), JSON.stringify(prefs));
  } catch {
    // Best-effort preference; a failed write must never block the UI update.
  }
}

export interface UseSidebarSectionPreferencesResult {
  prefs: SidebarSectionPrefs;
  toggleCollapsed: (kind: SidebarSectionKind) => void;
  toggleShowUnreadOnly: (kind: SidebarSectionKind) => void;
}

/**
 * The sidebar's per-section prefs, scoped to (user, workspace) and never
 * synchronized via a render-body `setState` call (code review, issue #779).
 *
 * `loaded` is a synchronous, pure derivation of the current scope — computed
 * during render like any other memoized value, not a side effect — so the
 * very first render for a given (user, workspace) already carries the right
 * value and there is nothing to flash from a wrong default.
 *
 * `override` holds only in-session edits, tagged with the scope they were
 * made in. Reading it is a plain comparison (`override.scopeKey === scopeKey`),
 * so a scope change — a different user or workspace — makes a stale override
 * stop matching automatically, and `loaded` (freshly computed for the new
 * scope) takes back over. `setOverride` only ever runs from `toggleCollapsed`/
 * `toggleShowUnreadOnly`, i.e. in response to a click — never during render.
 *
 * `userId`/`workspaceId` are `undefined` before the sidebar is ready; the
 * hook then always returns `DEFAULT_SECTION_PREFS` and every toggle is a
 * no-op, since there is nothing to scope a preference to yet.
 */
export function useSidebarSectionPreferences(
  userId: string | undefined,
  workspaceId: string | undefined,
): UseSidebarSectionPreferencesResult {
  const scopeKey = userId && workspaceId ? `${workspaceId}:${userId}` : null;

  const loaded = useMemo(
    () => (userId && workspaceId ? loadSectionPrefs(userId, workspaceId) : DEFAULT_SECTION_PREFS),
    [userId, workspaceId],
  );

  const [override, setOverride] = useState<{ scopeKey: string; prefs: SidebarSectionPrefs } | null>(
    null,
  );
  const prefs = override && override.scopeKey === scopeKey ? override.prefs : loaded;

  function updatePref(kind: SidebarSectionKind, change: Partial<SidebarSectionPref>) {
    if (!scopeKey || !userId || !workspaceId) return;
    const next: SidebarSectionPrefs = { ...prefs, [kind]: { ...prefs[kind], ...change } };
    saveSectionPrefs(userId, workspaceId, next);
    setOverride({ scopeKey, prefs: next });
  }

  return {
    prefs,
    toggleCollapsed: (kind) => updatePref(kind, { collapsed: !prefs[kind].collapsed }),
    toggleShowUnreadOnly: (kind) =>
      updatePref(kind, { showUnreadOnly: !prefs[kind].showUnreadOnly }),
  };
}
