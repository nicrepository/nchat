/** Guards against a hostile payload turning an image attribute into a memory hog. */
const MAX_AVATAR_URL_LENGTH = 512;

function currentOrigin(): string {
  if (typeof window === "undefined") return "";
  const origin = window.location?.origin;
  return typeof origin === "string" && origin !== "" && origin !== "null" ? origin : "";
}

/**
 * Returns an avatar URL only when the browser can prove it is a same-origin
 * HTTP(S) resource. The original spelling is preserved for the image element.
 */
export function safeAvatarUrl(raw: unknown): string | undefined {
  if (typeof raw !== "string") return undefined;
  // eslint-disable-next-line no-control-regex -- Reject controls before trimming whitespace.
  if (/[\u0000-\u001f\u007f]/.test(raw)) return undefined;
  const value = raw.trim();
  if (value === "" || value.length > MAX_AVATAR_URL_LENGTH) return undefined;
  // eslint-disable-next-line no-control-regex -- URL controls are intentionally rejected.
  if (/[\u0000-\u0020\u007f]/.test(value) || value.includes("\\")) return undefined;

  const origin = currentOrigin();
  if (!origin) return undefined;
  try {
    const parsed = new URL(value, origin);
    if (parsed.protocol !== "http:" && parsed.protocol !== "https:") return undefined;
    if (parsed.origin !== origin) return undefined;
    if (parsed.username !== "" || parsed.password !== "") return undefined;
    return value;
  } catch {
    return undefined;
  }
}
