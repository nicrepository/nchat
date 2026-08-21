/**
 * Persists only the sidebar's visual unread/mention state — never message
 * content, tokens, or anything else — scoped by (workspace, user) so a
 * different account on the same browser never sees another one's badges.
 * This is only a fast display cache. Server-provided unread counts always
 * replace it; mention highlighting remains local-only in this delivery.
 */

export interface PersistedUnreadEntry {
  id: string;
  type: "channel" | "dm";
  unreadCount: number;
  hasMentionUnread: boolean;
}

/** Guards against a hostile/corrupt payload turning an entry into a memory hog. */
const maxIdLength = 256;
const maxUnreadCount = 9999;
const maxEntries = 2000;

function storageKey(userId: string, workspaceId: string): string {
  return `nchat.sidebar.unread.v1:${encodeURIComponent(workspaceId)}:${encodeURIComponent(userId)}`;
}

function isValidEntry(raw: unknown): raw is PersistedUnreadEntry {
  if (typeof raw !== "object" || raw === null) return false;
  const entry = raw as Record<string, unknown>;
  return (
    typeof entry.id === "string" &&
    entry.id.length > 0 &&
    entry.id.length <= maxIdLength &&
    (entry.type === "channel" || entry.type === "dm") &&
    typeof entry.unreadCount === "number" &&
    Number.isInteger(entry.unreadCount) &&
    entry.unreadCount >= 0 &&
    entry.unreadCount <= maxUnreadCount &&
    typeof entry.hasMentionUnread === "boolean"
  );
}

/** Never throws. Returns [] for missing, corrupt, or invalid data. */
export function loadPersistedUnread(userId: string, workspaceId: string): PersistedUnreadEntry[] {
  try {
    const raw = localStorage.getItem(storageKey(userId, workspaceId));
    if (!raw) return [];
    const parsed: unknown = JSON.parse(raw);
    if (!Array.isArray(parsed)) return [];
    // Drops only the individual malformed entries, matching mapSidebarDM's
    // "drop the row, not the whole list" convention in chatApi.ts.
    return parsed.filter(isValidEntry).slice(0, maxEntries);
  } catch {
    return [];
  }
}

/**
 * Always writes — including an empty array — so a conversation that becomes
 * read is actually cleared from disk rather than left to resurrect on the
 * next restore. Never throws; a failed write is a no-op preference miss, not
 * an app-breaking error.
 */
export function savePersistedUnread(
  userId: string,
  workspaceId: string,
  entries: PersistedUnreadEntry[],
): void {
  try {
    const worthKeeping = entries.filter((entry) => entry.unreadCount > 0 || entry.hasMentionUnread);
    localStorage.setItem(storageKey(userId, workspaceId), JSON.stringify(worthKeeping));
  } catch {
    // Best-effort cache; a failed write must never block the UI update.
  }
}
