/**
 * Pure rules for the channel creation form (RF-01).
 *
 * They mirror chat-service's ChannelService exactly and exist apart from the
 * dialog because agreement with the server is much easier to prove on a function
 * than on a rendered component. None of them is an authorisation check: the
 * server re-validates every one, and these only keep the UI from offering a
 * request that would be refused.
 */

export type ChannelFormType = "public" | "private";

/** chat-service: slugRE caps a slug at 63 characters. */
export const MAX_CHANNEL_SLUG_LENGTH = 63;

/**
 * chat-service: domain.MaxChannelDisplayNameCodePoints.
 *
 * Security resource cap. Counted in Unicode code points to match Go
 * utf8.RuneCountInString and PostgreSQL char_length.
 */
export const MAX_CHANNEL_DISPLAY_NAME_CODE_POINTS = 100;

/**
 * Counts a string the way the server does.
 *
 * `String.length` counts UTF-16 units, so an emoji outside the BMP would score
 * two and half a name of them would be refused that the server accepts. Spread
 * iteration walks code points, which is what utf8.RuneCountInString and
 * char_length both measure.
 */
export function channelDisplayNameLength(value: string): number {
  return Array.from(value).length;
}

/** chat-service reserves this slug for the workspace's #geral channel. */
export const RESERVED_CHANNEL_SLUG = "geral";

/**
 * The server's slugRE, character for character:
 * `^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$` — lowercase alphanumeric with
 * internal hyphens, no leading or trailing hyphen. A single character is valid.
 */
const slugPattern = /^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$/;

/**
 * Derives a slug suggestion from a display name.
 *
 * Accents are stripped through NFD normalisation rather than a character map, so
 * "Operações" becomes "operacoes" instead of being rejected. Everything the
 * pattern does not accept collapses into single hyphens, and the leading and
 * trailing ones are dropped because the server refuses them. The result can
 * still be empty (a name of only emoji, say), in which case the user types their
 * own slug and validation explains why.
 */
export function slugifyChannelName(displayName: string): string {
  return displayName
    .normalize("NFD")
    .replace(/[\u0300-\u036f]/g, "")
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, MAX_CHANNEL_SLUG_LENGTH)
    .replace(/-+$/, "");
}

/**
 * The channel-name rule on its own, mirroring domain.NormalizeChannelDisplayName.
 *
 * Separate from validateChannelForm so the form can show it while the user
 * types, without also complaining about a slug they have not reached yet.
 * Trimming happens before counting, exactly as the server does — surrounding
 * whitespace must never be what pushes an acceptable name over.
 */
export function validateChannelDisplayName(displayName: string): string | null {
  const trimmed = displayName.trim();
  if (trimmed === "") return "Informe um nome para o canal.";
  if (channelDisplayNameLength(trimmed) > MAX_CHANNEL_DISPLAY_NAME_CODE_POINTS) {
    return `O nome do canal deve ter no máximo ${MAX_CHANNEL_DISPLAY_NAME_CODE_POINTS} caracteres.`;
  }
  return null;
}

export interface ChannelFormValues {
  displayName: string;
  slug: string;
}

/**
 * Returns the message to show, or null when the form may be submitted.
 *
 * The order matters: the reserved-slug case is reported specifically because
 * "geral" satisfies the pattern and a generic format message would leave the
 * user retyping a valid-looking slug forever.
 */
export function validateChannelForm({ displayName, slug }: ChannelFormValues): string | null {
  const nameMessage = validateChannelDisplayName(displayName);
  if (nameMessage) return nameMessage;
  const normalized = slug.trim().toLowerCase();
  if (normalized === "") return "Informe um identificador para o canal.";
  if (normalized === RESERVED_CHANNEL_SLUG) return "O identificador “geral” é reservado.";
  if (!slugPattern.test(normalized)) {
    return "Use apenas letras minúsculas, números e hifens internos (até 63 caracteres).";
  }
  return null;
}
