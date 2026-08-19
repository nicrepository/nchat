/**
 * `crypto.randomUUID()` only exists in secure contexts (HTTPS, or the literal
 * hostname `localhost`). Local dev is routed through `http://nchat.local:8080`,
 * which is neither, so the browser leaves it undefined — calling it directly
 * throws and, wherever that happens inside a component with no error boundary,
 * blanks the whole page. `crypto.getRandomValues` has no such restriction, so
 * it is always safe to fall back to it.
 */
export function randomId(): string {
  if (typeof crypto.randomUUID === "function") return crypto.randomUUID();
  const bytes = crypto.getRandomValues(new Uint8Array(16));
  bytes[6] = (bytes[6] & 0x0f) | 0x40;
  bytes[8] = (bytes[8] & 0x3f) | 0x80;
  const hex = Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join("");
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`;
}
