/**
 * Validation for the two policy forms.
 *
 * Pure functions, separate from the components, for the same reason apps/web
 * keeps antiSpamForm.ts and uploadLimitForm.ts apart from their pages: these
 * are the rules most likely to be argued about, and keeping them out of a
 * component means they can be tested by calling them.
 *
 * They exist for feedback, not enforcement. The backend validates the same
 * values against the same bounds and a database CHECK backs it up, so a request
 * that skipped these functions is refused all the same. The bounds are never
 * restated here — they arrive from the server with every policy response.
 */

import type { PolicyBounds } from "../api/managementApi";
import { bytesToMiB, mibToBytes } from "./units";

/** Returns an error message, or null when the value is acceptable. */
export function validateRateLimit(raw: string, bounds: PolicyBounds): string | null {
  const trimmed = raw.trim();
  if (trimmed === "") return "Informe um valor.";
  // Digits only: rejects decimals, signs, exponents and whitespace-separated
  // input that Number() would otherwise coerce into something plausible.
  if (!/^\d+$/.test(trimmed)) return "Use apenas números inteiros.";
  const value = Number(trimmed);
  if (value < bounds.min || value > bounds.max) {
    return `Informe um valor entre ${bounds.min} e ${bounds.max} mensagens por minuto.`;
  }
  return null;
}

/**
 * A limit far above normal human cadence protects nothing in practice.
 *
 * It is allowed — the server accepts it — and the console says so before it is
 * saved, because "within the permitted range" and "a good idea" are not the
 * same claim.
 */
export function rateLimitWarning(value: number, bounds: PolicyBounds): string | null {
  if (value >= bounds.max) {
    return "No teto permitido: na prática o anti-spam deixa de conter um remetente automatizado.";
  }
  if (value > bounds.default * 4) {
    return "Bem acima do padrão da plataforma. Um remetente automatizado passa a conseguir enviar muito mais antes de ser contido.";
  }
  return null;
}

/**
 * Validates a typed MiB value against the server's own bounds.
 *
 * The whole-MiB rule is not cosmetic: this field edits whole MiB, so a stored
 * limit that is not one could not be shown without being changed, and an
 * ordinary save would then write a limit nobody typed. mibToBytes refuses a
 * conversion that would lose precision rather than returning a different number.
 */
export function validateUploadMiB(raw: string, bounds: PolicyBounds): string | null {
  const trimmed = raw.trim();
  if (trimmed === "") return "Informe um valor.";
  if (!/^\d+$/.test(trimmed)) return "Use apenas números inteiros de MiB.";
  const bytes = mibToBytes(Number(trimmed));
  if (bytes === null) return "Valor grande demais para ser representado com exatidão.";
  if (bytes < bounds.min || bytes > bounds.max) {
    return `O limite deve estar entre ${bytesToMiB(bounds.min)} e ${bytesToMiB(bounds.max)} MiB.`;
  }
  return null;
}

/** A limit at the ceiling is allowed and worth saying out loud. */
export function uploadWarning(bytes: number, bounds: PolicyBounds): string | null {
  if (bytes >= bounds.max) {
    return "No teto permitido. Cada anexo pode ocupar uma conexão e armazenamento proporcionais por um tempo longo.";
  }
  return null;
}
