/**
 * Attachment size limit arithmetic (RF-32, issue #458).
 *
 * The administrative policy is a whole number of MiB. It is stored and
 * transmitted in bytes, but every value it can take is an exact multiple of
 * 1 MiB — the backend rejects anything else rather than adjusting it — so the
 * conversion between the two is exact in both directions and never rounds.
 *
 * This module is the single place either side of the app does that arithmetic:
 * the admin form that edits the policy and the composer that reports it to a
 * user. A second copy of the constant is exactly the drift the requirement
 * forbids.
 */

/** One MiB. The unit the policy is expressed in. */
export const BYTES_PER_MIB = 1024 * 1024;

/**
 * True when `bytes` is a limit the policy can actually hold: a positive whole
 * number of MiB.
 *
 * Anything else is a value this UI cannot represent without changing it, which
 * is why it is surfaced as an error rather than rounded.
 */
export function isWholeMiB(bytes: number): boolean {
  return Number.isSafeInteger(bytes) && bytes > 0 && bytes % BYTES_PER_MIB === 0;
}

/**
 * Bytes as whole MiB. Exact division — callers must have checked
 * `isWholeMiB` first, and passing anything else is a programming error rather
 * than something this function silently absorbs.
 */
export function bytesToMiB(bytes: number): number {
  return bytes / BYTES_PER_MIB;
}

/** Whole MiB back to bytes. Integer arithmetic throughout. */
export function mibToBytes(mib: number): number {
  return mib * BYTES_PER_MIB;
}

/**
 * The limit as a person reads it, e.g. "250 MiB".
 *
 * "MiB" and not "MB": the value is binary, and labelling 262144000 bytes as
 * "250 MB" is the ambiguity this issue exists to remove. A value that is not a
 * whole number of MiB cannot come from the policy, so it is reported in bytes
 * rather than rounded into a unit it does not fit.
 */
export function formatUploadLimit(bytes: number): string {
  if (!isWholeMiB(bytes)) return `${bytes} bytes`;
  return `${bytesToMiB(bytes)} MiB`;
}
