/**
 * Returns true only for safe internal app paths.
 *
 * A safe path must be:
 * - a non-empty string
 * - starting with /
 * - not starting with // (protocol-relative URL)
 * - not starting with /\ (Windows-style protocol-relative variant)
 */
export function isInternalPath(path: unknown): path is string {
  if (typeof path !== "string" || path.length === 0) return false;
  if (!path.startsWith("/")) return false;
  if (path.startsWith("//")) return false;
  if (path.startsWith("/\\")) return false;
  return true;
}

/**
 * Returns `from` when it is a safe internal path; otherwise returns "/".
 * Never returns an external URL.
 */
export function safeFrom(from: unknown): string {
  return isInternalPath(from) ? from : "/";
}
