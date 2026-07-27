/**
 * RF-19 anti-spam form validation (issue #419).
 *
 * Separate from the component for the same reason channelForm.ts is: it keeps
 * the page a pure component module and makes the rule testable on its own.
 *
 * This exists for feedback, not enforcement. The backend validates the same
 * bounds and the database CHECK backs it up, so a request that skips this
 * function is rejected all the same.
 */

/** Returns an error message, or null when the value is acceptable. */
export function validateLimit(raw: string, min: number, max: number): string | null {
  const trimmed = raw.trim();
  if (trimmed === "") {
    return "Informe um valor.";
  }
  // Digits only: rejects decimals, signs, exponents and whitespace-separated
  // input that Number() would otherwise coerce into something plausible.
  if (!/^\d+$/.test(trimmed)) {
    return "Use apenas números inteiros.";
  }
  const value = Number(trimmed);
  if (value < min || value > max) {
    return `Informe um valor entre ${min} e ${max}.`;
  }
  return null;
}
