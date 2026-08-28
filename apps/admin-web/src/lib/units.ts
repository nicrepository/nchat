/**
 * Byte/MiB arithmetic for the upload policy screen.
 *
 * It is local to the console rather than shared with the chat app: issue #578
 * made this a separate bundle on a separate origin, and importing from
 * apps/web would put the two back in one dependency graph to save four small
 * functions. The *bounds* are not restated here — they arrive from the server
 * with every policy response.
 */

export const BYTES_PER_MIB = 1024 * 1024;

/** Exact whole MiB only; anything else is not a value this form can edit. */
export function isWholeMiB(bytes: number): boolean {
  return Number.isSafeInteger(bytes) && bytes % BYTES_PER_MIB === 0;
}

export function bytesToMiB(bytes: number): number {
  return Math.floor(bytes / BYTES_PER_MIB);
}

/**
 * Converts whole MiB to bytes, refusing anything that cannot be represented
 * exactly.
 *
 * Returns null rather than a wrong number: a value past Number.MAX_SAFE_INTEGER
 * would arrive at the server as a different limit than the one typed, and a
 * silent loss of precision on a size limit is the failure this function exists
 * to prevent. The server refuses the same values independently.
 */
export function mibToBytes(mib: number): number | null {
  if (!Number.isSafeInteger(mib) || mib < 0) return null;
  const bytes = mib * BYTES_PER_MIB;
  return Number.isSafeInteger(bytes) ? bytes : null;
}

/** Renders a byte count for a person, in the binary units the policy uses. */
export function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes)) return "—";
  if (bytes >= BYTES_PER_MIB * 1024) {
    return `${(bytes / (BYTES_PER_MIB * 1024)).toFixed(1)} GiB`;
  }
  if (bytes >= BYTES_PER_MIB) return `${bytesToMiB(bytes)} MiB`;
  return `${bytes} B`;
}

/** Renders an ISO timestamp, or an em dash when there is none. */
export function formatDateTime(value: string | null): string {
  if (!value) return "—";
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? "—" : date.toLocaleString("pt-BR");
}
