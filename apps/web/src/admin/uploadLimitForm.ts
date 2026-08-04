/**
 * RF-32 upload limit form validation (issue #458).
 *
 * Separate from the component for the same reason antiSpamForm.ts is: it keeps
 * the page a pure component module and makes the rules testable on their own.
 * The arithmetic itself lives in ../lib/uploadLimit, shared with the composer.
 *
 * This exists for feedback, not enforcement. The backend validates the same
 * rules and the database CHECK backs them up, so a request that skips these
 * functions is rejected all the same.
 */

import { bytesToMiB, isWholeMiB, mibToBytes } from "../lib/uploadLimit";

export { BYTES_PER_MIB, bytesToMiB, isWholeMiB, mibToBytes } from "../lib/uploadLimit";

/**
 * Returns an error message, or null when the value is acceptable.
 *
 * `minBytes` and `maxBytes` are the server's own bounds, echoed back by the
 * endpoint, so the message quotes the server's numbers rather than restating
 * limits the frontend decided. Both are whole MiB by the same rule the value
 * is, so the bounds shown are exact.
 */
export function validateUploadLimitMiB(
  raw: string,
  minBytes: number,
  maxBytes: number,
): string | null {
  const trimmed = raw.trim();
  if (trimmed === "") {
    return "Informe um valor.";
  }
  // Digits only: rejects decimals, signs, exponents and whitespace-separated
  // input that Number() would otherwise coerce into something plausible. A
  // decimal MiB is not a policy this API accepts, so it is refused here rather
  // than rounded into one.
  if (!/^\d+$/.test(trimmed)) {
    return "Use apenas números inteiros de MiB.";
  }
  const bytes = mibToBytes(Number(trimmed));
  if (!Number.isSafeInteger(bytes) || bytes < minBytes || bytes > maxBytes) {
    return `O limite deve ser um número inteiro entre ${bytesToMiB(minBytes)} e ${bytesToMiB(maxBytes)} MiB.`;
  }
  return null;
}

/**
 * Reports whether a policy the server returned is one this form can edit.
 *
 * A value that is not a whole number of MiB cannot be shown in the field
 * without changing it. Rather than round it — which would let an ordinary save
 * overwrite a limit the administrator never touched — the page refuses to edit
 * and says so.
 */
export function isEditablePolicy(bytes: number): boolean {
  return isWholeMiB(bytes);
}
