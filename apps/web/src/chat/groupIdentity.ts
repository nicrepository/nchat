/**
 * A group's identity draft (issue #1026): Automático or one emoji.
 *
 * Pure, so the rules the dialog and the API client must agree on are proved on
 * a function. None of them is a security check: chat-service validates the
 * emoji against its catalog on every write.
 */

export type GroupIdentityMode = "auto" | "emoji";

export interface GroupIdentity {
  mode: GroupIdentityMode;
  /** The chosen sequence. Only meaningful in emoji mode. */
  emoji?: string;
}

export const AUTOMATIC_IDENTITY: GroupIdentity = { mode: "auto" };

/** The identity a group already has, as the sidebar reported it. */
export function identityOf(avatarEmoji: string | undefined): GroupIdentity {
  return avatarEmoji ? { mode: "emoji", emoji: avatarEmoji } : AUTOMATIC_IDENTITY;
}

/**
 * Switches mode. Leaving emoji mode drops the emoji: Automático is the absence
 * of a personalisation, so nothing of the previous choice may survive into the
 * request.
 */
export function withIdentityMode(identity: GroupIdentity, mode: GroupIdentityMode): GroupIdentity {
  if (mode === "auto") return AUTOMATIC_IDENTITY;
  return identity.mode === "emoji" ? identity : { mode: "emoji" };
}

/** What is persisted: the emoji in emoji mode, nothing otherwise. */
export function persistedEmoji(identity: GroupIdentity): string | undefined {
  return identity.mode === "emoji" ? identity.emoji : undefined;
}

/** Emoji mode with nothing picked yet cannot be confirmed. */
export function identityIncomplete(identity: GroupIdentity): boolean {
  return identity.mode === "emoji" && !identity.emoji;
}
