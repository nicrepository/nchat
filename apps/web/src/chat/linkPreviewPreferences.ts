/**
 * "Ocultar visualização" (issue #807 §28), remembered per reader in this
 * browser.
 *
 * Hiding a card is a presentation choice: it keeps the link, changes nothing
 * about its safety, and needs no confirmation. There is no server-side record
 * of it — a preference nobody else can see has no reason to leave the device —
 * so it lives in localStorage, keyed by message and target, and a browser
 * without storage simply shows the card again next time.
 */

const storageKey = "nchat:link-preview-hidden";
const maxRemembered = 500;

function read(): string[] {
  try {
    const raw = localStorage.getItem(storageKey);
    const parsed: unknown = raw ? JSON.parse(raw) : [];
    return Array.isArray(parsed) ? parsed.filter((item) => typeof item === "string") : [];
  } catch {
    return [];
  }
}

function write(keys: string[]): void {
  try {
    localStorage.setItem(storageKey, JSON.stringify(keys.slice(-maxRemembered)));
  } catch {
    // Storage unavailable or full: the card comes back next time, which is the
    // harmless direction.
  }
}

function keyFor(messageId: string, url: string): string {
  return `${messageId}|${url}`;
}

export function isLinkPreviewHidden(messageId: string, url: string): boolean {
  return read().includes(keyFor(messageId, url));
}

export function hideLinkPreview(messageId: string, url: string): void {
  const key = keyFor(messageId, url);
  const keys = read();
  if (!keys.includes(key)) write([...keys, key]);
}
