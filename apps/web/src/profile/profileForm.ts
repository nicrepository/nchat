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

// The bio is the sole multiline field in this form. CRLF/CR are normalized to
// LF so the browser validates exactly the same persisted value as the Go
// service; all remaining controls except LF are still removed.
// eslint-disable-next-line no-control-regex -- control characters are exactly what must be stripped.
const BIO_CONTROL_CHARACTERS_PATTERN = /[\u0000-\u0009\u000B-\u001F\u007F-\u009F]/g;

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
 * Mirrors auth-service's multiline bio normalization. It preserves internal
 * line breaks while removing all other control characters before counting.
 */
export function sanitizeBio(value: string): string {
  return value.replace(/\r\n?/g, "\n").trim().replace(BIO_CONTROL_CHARACTERS_PATTERN, "");
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

/**
 * Mirrors auth-service's selfShortFieldMaxLen (job_title, department,
 * location — same short-label category as display_name, so it reuses that
 * bound too).
 */
export const MAX_SHORT_FIELD_CODE_POINTS = MAX_DISPLAY_NAME_CODE_POINTS;

/**
 * Mirrors auth-service's selfBioMaxLen. Unlike the field above, there is no
 * sibling field to reuse a bound from — see the server-side comment for why
 * this specific number is a judgment call, not a requirement.
 */
export const MAX_BIO_CODE_POINTS = 500;

/**
 * Validates job_title, department or location. Unlike validateDisplayName,
 * "" is valid (these fields are optional) — only the length bound is
 * enforced, using the same sanitize-then-count treatment.
 */
export function validateShortProfileField(value: string, fieldLabel: string): string | null {
  const sanitized = sanitizeDisplayName(value);
  if (displayNameLength(sanitized) > MAX_SHORT_FIELD_CODE_POINTS) {
    return `${fieldLabel} deve ter no máximo ${MAX_SHORT_FIELD_CODE_POINTS} caracteres.`;
  }
  return null;
}

/** Validates bio. "" is valid (optional), only the length bound is enforced. */
export function validateBio(value: string): string | null {
  const sanitized = sanitizeBio(value);
  if (displayNameLength(sanitized) > MAX_BIO_CODE_POINTS) {
    return `A biografia deve ter no máximo ${MAX_BIO_CODE_POINTS} caracteres.`;
  }
  return null;
}

/**
 * The IANA time zone names this UI offers, read from the browser's own time
 * zone database (Intl.supportedValuesOf) rather than a hand-maintained list
 * that could drift out of sync with what the browser (and auth-service's
 * time.LoadLocation) actually recognize. Offering a closed set through a
 * <select> — instead of a free-text field — is what makes a client-side
 * "is this valid" check meaningful at all: a free-text timezone field could
 * always contain a string that looks plausible but resolves to nothing.
 *
 * "UTC" is added explicitly: Intl.supportedValuesOf("timeZone") does not
 * include it (it lists no "Etc/*" zone either), but auth-service's
 * time.LoadLocation("UTC") is a Go built-in that always succeeds regardless
 * of the tzdata embedded via time/tzdata — leaving it out here would hide a
 * choice the server already fully supports.
 *
 * Empty (besides UTC) when the runtime does not implement supportedValuesOf
 * (older engines): the select then has little to offer besides "not set" and
 * UTC, which is a degraded but still safe experience — the server remains
 * authoritative either way.
 */
export function supportedTimezones(): readonly string[] {
  if (typeof Intl.supportedValuesOf !== "function") return ["UTC"];
  return ["UTC", ...Intl.supportedValuesOf("timeZone")];
}

/**
 * Validates timezone. "" is valid (optional, clears the field). A non-empty
 * value must be one of supportedTimezones() — this only guards against a
 * value reaching the request some way other than picking a <select> option
 * (e.g. a future change to how the field is edited); the server validates
 * independently regardless.
 */
export function validateTimezone(value: string): string | null {
  if (value === "") return null;
  if (!supportedTimezones().includes(value)) {
    return "Selecione um fuso horário válido.";
  }
  return null;
}
