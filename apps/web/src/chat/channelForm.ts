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
  if (displayName.trim() === "") return "Informe um nome para o canal.";
  const normalized = slug.trim().toLowerCase();
  if (normalized === "") return "Informe um identificador para o canal.";
  if (normalized === RESERVED_CHANNEL_SLUG) return "O identificador “geral” é reservado.";
  if (!slugPattern.test(normalized)) {
    return "Use apenas letras minúsculas, números e hifens internos (até 63 caracteres).";
  }
  return null;
}
