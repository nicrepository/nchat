/**
 * Presentation values shared by the details panel and the header control that
 * opens it (issue #435).
 *
 * They live outside ChannelDetailsPanel.tsx so that file exports components and
 * nothing else — the toggle needs the panel's id for aria-controls, and
 * importing a component module for a string would be the wrong dependency.
 */

/** aria-labelledby target: the panel's own heading. */
export const channelDetailsTitleId = "chat-channel-details-title";

/** aria-controls target: stable, so the toggle can name the panel it opens. */
export const channelDetailsPanelId = "chat-channel-details-panel";

/** aria-labelledby target for the group panel's own heading (issue #441). */
export const groupDetailsTitleId = "chat-group-details-title";

/** aria-controls target: stable, so the group toggle can name what it opens. */
export const groupDetailsPanelId = "chat-group-details-panel";

/**
 * Human-readable size, binary units, pt-BR decimal separator.
 *
 * The boundaries are where a naive implementation goes wrong: 0 must not read
 * "0,0 B", exactly 1024 must roll over to "1,0 KB" rather than "1024 B", and the
 * loop must stop at the largest unit instead of dividing past it. A negative or
 * non-finite value is not a size and yields "" rather than a nonsense figure.
 */
export function formatFileSize(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes < 0) return "";
  if (bytes < 1024) return `${Math.round(bytes)} B`;
  const units = ["KB", "MB", "GB", "TB"];
  let value = bytes / 1024;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit += 1;
  }
  // One decimal below 10 (2,4 MB), none above it (128 MB) — the prototype's
  // density, and it keeps the row from wrapping.
  const rounded = value < 10 ? value.toFixed(1) : Math.round(value).toString();
  return `${rounded.replace(".", ",")} ${units[unit]}`;
}
