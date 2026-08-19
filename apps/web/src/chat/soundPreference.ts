/**
 * How the user wants new-message chimes to behave. Local-only (no backend
 * preferences endpoint exists yet) and read fresh on every call rather than
 * cached, so a change in one tab/component is visible to the next check
 * immediately without a pub/sub layer.
 */

export type SoundNotificationMode = "off" | "all" | "mentions" | "mentions_and_dms";

const MODE_KEY = "nchat.notifications.sound.mode";
// Pre-dates the 3-mode preference (was a plain on/off toggle). Kept readable,
// never written to or deleted, so an existing choice survives the upgrade.
const LEGACY_ENABLED_KEY = "nchat.notifications.sound.enabled";
// Pre-dates "mentions_and_dms" being its own mode (was "mentions" plus this
// separate flag). Read for migration, then cleared the moment the user makes
// any explicit choice via setSoundNotificationMode — unlike LEGACY_ENABLED_KEY,
// leaving it in place would silently re-upgrade a deliberate later choice of
// plain "mentions" back to "mentions_and_dms" forever.
const LEGACY_DM_WITHOUT_MENTION_KEY = "nchat.notifications.sound.dmWithoutMention";
const DEFAULT_MODE: SoundNotificationMode = "all";
const VALID_MODES: readonly SoundNotificationMode[] = [
  "off",
  "all",
  "mentions",
  "mentions_and_dms",
];

function legacyDmWithoutMentionFlagIsOn(): boolean {
  try {
    return localStorage.getItem(LEGACY_DM_WITHOUT_MENTION_KEY) === "true";
  } catch {
    return false;
  }
}

function migrateLegacyBooleanMode(): SoundNotificationMode {
  try {
    const legacy = localStorage.getItem(LEGACY_ENABLED_KEY);
    if (legacy === "false") return "off";
    return DEFAULT_MODE;
  } catch {
    return DEFAULT_MODE;
  }
}

export function getSoundNotificationMode(): SoundNotificationMode {
  try {
    const stored = localStorage.getItem(MODE_KEY);
    if (stored && (VALID_MODES as string[]).includes(stored)) {
      // Upgrades the older "mentions" + separate DM flag combination into
      // the single "mentions_and_dms" mode it now maps to.
      if (stored === "mentions" && legacyDmWithoutMentionFlagIsOn()) {
        return "mentions_and_dms";
      }
      return stored as SoundNotificationMode;
    }
    return migrateLegacyBooleanMode();
  } catch {
    return DEFAULT_MODE;
  }
}

export function setSoundNotificationMode(mode: SoundNotificationMode): void {
  try {
    localStorage.setItem(MODE_KEY, mode);
    // See LEGACY_DM_WITHOUT_MENTION_KEY: an explicit choice always wins from
    // here on, so the legacy flag has nothing left to contribute.
    localStorage.removeItem(LEGACY_DM_WITHOUT_MENTION_KEY);
  } catch {
    // Best-effort preference; a failed write must never block the toggle action.
  }
}
