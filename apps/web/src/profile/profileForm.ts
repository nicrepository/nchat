/**
 * Pure rule for the profile display-name field (cronograma, ID 7).
 *
 * There is no product requirement specifying a length for this field, so the
 * bound below is not invented: it reuses the value auth-service's
 * UserService.UpdateDisplayName already enforces for a user's own
 * display_name (which itself reuses the bound already established for the
 * same kind of field — a short, human-chosen label — by
 * UpdateDeviceDisplayName). Counted in Unicode code points to match the
 * server's `len([]rune(...))`.
 */
export const MAX_DISPLAY_NAME_CODE_POINTS = 80;

/**
 * Counts a string the way the server does.
 *
 * `String.length` counts UTF-16 units, so an emoji outside the BMP would
 * score two and half a name of them would be refused that the server
 * accepts. Spread iteration walks code points, which is what
 * `len([]rune(...))` on the server also measures.
 */
export function displayNameLength(value: string): number {
  return Array.from(value).length;
}

/**
 * The exact character class auth-service's sanitizeDisplayName removes via
 * Go's `unicode.IsControl`: the C0 controls (U+0000–U+001F), DEL
 * (U+007F) and the C1 block (U+0080–U+009F). IsControl never reaches
 * past Latin-1, so this range is the whole rule, not an approximation of it.
 */
// eslint-disable-next-line no-control-regex -- control characters are exactly what must be stripped.
const CONTROL_CHARACTERS_PATTERN = /[\u0000-\u001F\u007F-\u009F]/g;

/**
 * Mirrors auth-service's sanitizeDisplayName: trim, then drop the same
 * control characters (see CONTROL_CHARACTERS_PATTERN above).
 *
 * Counting has to happen on this sanitized value, not the raw input: the
 * server strips these characters before it measures length, so a raw count
 * here could reject a value the server would accept (control characters
 * pushing the raw count over 80 while the sanitized count stays under it).
 */
export function sanitizeDisplayName(value: string): string {
  return value.trim().replace(CONTROL_CHARACTERS_PATTERN, "");
}

/**
 * Returns the message to show, or null when the value may be submitted.
 * Sanitizing before counting, exactly as the server does — surrounding
 * whitespace and control characters must never be what pushes an acceptable
 * name over the limit, or hide one that is genuinely too long.
 */
export function validateDisplayName(value: string): string | null {
  const sanitized = sanitizeDisplayName(value);
  if (sanitized === "") return "Informe um nome de exibição.";
  if (displayNameLength(sanitized) > MAX_DISPLAY_NAME_CODE_POINTS) {
    return `O nome deve ter no máximo ${MAX_DISPLAY_NAME_CODE_POINTS} caracteres.`;
  }
  return null;
}
