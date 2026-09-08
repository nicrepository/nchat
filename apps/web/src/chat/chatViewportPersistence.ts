/**
 * Tab-local viewport anchor persistence (#492) — survives a same-tab refresh
 * only. Never message content, never tokens: only an id, a pixel offset and a
 * boolean. Mirrors sidebarUnreadPersistence.ts's key-scoping and
 * never-throw/validate-on-read discipline, on sessionStorage instead of
 * localStorage (this is navigation/device state, not a durable preference).
 */

export interface ViewportAnchor {
  atBottom: boolean;
  anchorMessageId: string | null;
  anchorOffsetPx: number;
  savedAt: number;
}

const maxIdLength = 256;

function storageKey(userId: string, kind: "channel" | "dm", targetId: string): string {
  return `nchat.chat.viewport.v1:${encodeURIComponent(userId)}:${kind}:${encodeURIComponent(targetId)}`;
}

function isValidAnchor(raw: unknown): raw is ViewportAnchor {
  if (typeof raw !== "object" || raw === null) return false;
  const anchor = raw as Record<string, unknown>;
  return (
    typeof anchor.atBottom === "boolean" &&
    (anchor.anchorMessageId === null ||
      (typeof anchor.anchorMessageId === "string" &&
        anchor.anchorMessageId.length > 0 &&
        anchor.anchorMessageId.length <= maxIdLength)) &&
    typeof anchor.anchorOffsetPx === "number" &&
    Number.isFinite(anchor.anchorOffsetPx) &&
    typeof anchor.savedAt === "number" &&
    Number.isFinite(anchor.savedAt)
  );
}

/** Never throws. Returns null for missing, corrupt, or invalid data. */
export function loadViewportAnchor(
  userId: string,
  kind: "channel" | "dm",
  targetId: string,
): ViewportAnchor | null {
  try {
    const raw = sessionStorage.getItem(storageKey(userId, kind, targetId));
    if (!raw) return null;
    const parsed: unknown = JSON.parse(raw);
    return isValidAnchor(parsed) ? parsed : null;
  } catch {
    return null;
  }
}

/** Never throws; a failed write is a no-op, never an app-breaking error. */
export function saveViewportAnchor(
  userId: string,
  kind: "channel" | "dm",
  targetId: string,
  anchor: ViewportAnchor,
): void {
  try {
    sessionStorage.setItem(storageKey(userId, kind, targetId), JSON.stringify(anchor));
  } catch {
    // Best-effort cache; a failed write must never block the UI.
  }
}
