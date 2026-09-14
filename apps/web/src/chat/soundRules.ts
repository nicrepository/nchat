import type { SoundNotificationMode } from "./soundPreference";
import type { WSNotificationPolicy } from "./useChatWebSocket";

/**
 * Local execution gates, one per alert surface (issue #744).
 *
 * The authority for *whether an event may alert* is the central policy engine,
 * `libs/go/platform/notificationpolicy`. chat-service runs it when it publishes
 * the event and puts the answer on the wire as `notification_policy`, with a
 * separate decision per channel. Nothing here decides policy: this file answers
 * the second question only — may this browser, right now, execute something the
 * policy has already permitted?
 *
 * One gate per surface, and each reads its own channel:
 * `shouldExecuteInAppNotification` reads `in_app`, `shouldExecuteSound` reads
 * `sound`, `shouldExecuteNativeNotification` reads `web_push`. None ever
 * consults another's channel. A single boolean opening several surfaces is how
 * "may this chime?" quietly becomes "may this raise an OS notification?" —
 * different questions with different answers.
 *
 * Every rule below is therefore **monotonic**: it can only turn an allow into
 * silence. There is no path from a central `deny` to execution.
 *
 * A payload carrying *no* decision is a different state and not a denial: it
 * comes from a chat-service that predates issue #744 and could not answer. The
 * gates then apply their local conditions alone, with an unknown class. Absence
 * means "nobody told us", never "we were told no".
 *
 * What is deliberately *not* here, because it belongs to the engine: working
 * hours, conversation mute, historical and imported origins, reaction silence,
 * priority, Web Push, the policy version, and the classification of an event as
 * a DM, a mention or a reply. The class this file uses arrives already decided.
 *
 * Two things are still local, and only these two:
 *
 *   - whether this window is focused and whether this tab is showing the
 *     conversation the event belongs to, neither of which any server observes
 *     (see PresenceConnected in the engine);
 *   - the chime preference, which has no server-side source of truth yet
 *     (#136/#729) and lives in localStorage.
 *
 * Neither is policy and neither may grant anything.
 */

/**
 * The class of an event, as the server classified it for this recipient.
 *
 * "unknown" is the rollout state: a chat-service that predates issue #744 sends
 * no decision and therefore no class. It is not a fourth kind of event — it is
 * the absence of an answer, and the rules below refuse to restrict on a class
 * they were never told.
 */
export type SoundClass = "general" | "direct" | "mention" | "unknown";

/**
 * Whether the message names this recipient, according to the server's own
 * mention codec. The browser no longer reads the body to find out: a client
 * grammar that drifts from the server's is a client that disagrees about what a
 * mention is.
 */
export function isNamedRecipient(
  policy: WSNotificationPolicy | undefined,
  currentUserId: string,
): boolean {
  if (!policy) return false;
  return policy.names_everyone === true || (policy.named_user_ids ?? []).includes(currentUserId);
}

/** The authoritative class, with the recipient's own naming applied. */
export function soundClassFor(
  policy: WSNotificationPolicy | undefined,
  currentUserId: string,
): SoundClass {
  if (!policy) return "unknown";
  if (isNamedRecipient(policy, currentUserId)) return "mention";
  return policy.sound_class === "direct" ? "direct" : "general";
}

/**
 * What both gates need. It is deliberately one shape: the local conditions —
 * who sent it, whether it is a repeat, whether the reader silenced the
 * conversation, where they are looking — are the same for every surface. Only
 * the channel each gate reads differs.
 */
export interface AlertExecutionInput {
  /**
   * The central decision. Absent means the server that sent this event predates
   * issue #744 — see shouldExecuteSound. Every message payload this backend
   * publishes carries one, including for events it considers not notifiable,
   * which it says as an explicit deny.
   */
  policy: WSNotificationPolicy | undefined;
  currentUserId: string;
  /**
   * The chime preference. It is the one input with no server-side source of
   * truth yet (it lives in localStorage), so it is applied here — and, like
   * every other rule in this file, it may only take a permitted sound away.
   */
  localMode: SoundNotificationMode;
  isOwnMessage: boolean;
  isDuplicate: boolean;
  /** The recipient's own mute preference for this conversation. */
  isMutedConversation: boolean;
  /** This tab is showing the conversation the event belongs to. */
  isActiveConversation: boolean;
  isWindowFocused: boolean;
}

/**
 * Whether this browser should play a sound for an event the policy allowed.
 *
 * The first line is the contract: an explicit central `deny` ends here, and
 * nothing below can undo it.
 *
 * A *missing* decision is a different state and is deliberately not a denial.
 * It means the chat-service that sent the event predates issue #744, so it
 * could not answer — and during a rolling deploy a browser served ahead of its
 * backend would otherwise go completely silent, which is the worst failure
 * direction available: silent, and indistinguishable from working. The event
 * then passes through the local gates alone, with an unknown class, and nothing
 * about the product's rules is reconstructed here to compensate.
 */
export function shouldExecuteSound(input: AlertExecutionInput): boolean {
  if (input.policy && input.policy.sound !== "allow") return false;
  if (input.isDuplicate || input.isOwnMessage) return false;
  if (localMuteApplies(input)) return false;

  const soundClass = soundClassFor(input.policy, input.currentUserId);
  if (!localPreferenceAllows(input.localMode, soundClass)) return false;
  return !alreadyInFrontOfTheReader(input, soundClass);
}

/**
 * Whether this browser should raise the interruptive in-app surface — the toast
 * — for an event the policy allowed on that channel.
 *
 * It reads `in_app` and never `sound` or `web_push`. The three are decided
 * separately by the engine, and each one authorises exactly one surface: a
 * reader who permitted a chime has said nothing about whether the app may put a
 * panel in front of them.
 *
 * Where the reader is looking is already in the central decision for this
 * channel — the engine denies the in-app surface for a conversation that is
 * open and visible, and for a recipient who is not in the foreground at all —
 * so nothing here re-derives it. A purely local condition may still remove the
 * surface; none may add it.
 */
export function shouldExecuteInAppNotification(input: AlertExecutionInput): boolean {
  if (input.policy && input.policy.in_app !== "allow") return false;
  if (input.isDuplicate || input.isOwnMessage) return false;
  if (localMuteApplies(input)) return false;
  // An in-app surface is a thing drawn in this window, so a window nobody is
  // looking at cannot execute one. It is not deferred either: a toast that
  // waited for the reader to come back would announce a message that is by then
  // old, on top of whatever they returned to. The OS-level surface is the one
  // that exists for an unfocused window, and it is decided on its own channel.
  if (!input.isWindowFocused) return false;
  // The other local gate: this tab is showing the conversation, so the message
  // is already on screen and a panel over it would announce what the reader is
  // looking at. The server cannot know either of these — see PresenceConnected —
  // and both only ever remove a surface.
  return !input.isActiveConversation;
}

/**
 * Whether this browser should raise an OS-level notification for an event the
 * policy allowed on that channel.
 *
 * It reads `web_push` and never `sound`: a chime the reader permitted says
 * nothing about whether the operating system may interrupt them. The local
 * chime preference is likewise not consulted — it is a preference about sound.
 *
 * Today the realtime decision denies this channel on every event, because the
 * evaluation runs on the foreground surface and no push capability is declared.
 * That is the honest state of a channel with no provider behind it: the gate is
 * in place and closed, rather than open on a neighbouring channel's authority.
 */
export function shouldExecuteNativeNotification(input: AlertExecutionInput): boolean {
  if (input.policy && input.policy.web_push !== "allow") return false;
  if (input.isDuplicate || input.isOwnMessage) return false;
  return !localMuteApplies(input);
}

/**
 * Whether this browser still has to apply mute itself.
 *
 * It does not, for any payload this backend produces. Mute is resolved
 * server-side per recipient (chat.conversation_notification_prefs, read by the
 * realtime fan-out and by the notification worker), so a decision that arrived
 * has already accounted for it and re-applying it here would be the browser
 * deciding policy a second time.
 *
 * It remains for the legacy path only, where no decision arrived at all: there
 * the client has nothing but its own copy of the server's mute list, and using
 * it is the compatibility behaviour that was already agreed.
 */
function localMuteApplies(input: AlertExecutionInput): boolean {
  return !input.policy && input.isMutedConversation;
}

/**
 * The local chime preference, which only ever removes a permitted sound.
 *
 * The two mention modes need the class, so against an older server that sent
 * none they do not restrict: silencing on a classification nobody supplied
 * would be guessing, and guessing quiet is the failure nobody notices. "off"
 * still means off — that one needs no class at all.
 */
function localPreferenceAllows(mode: SoundNotificationMode, soundClass: SoundClass): boolean {
  switch (mode) {
    case "off":
      return false;
    case "mentions":
      return soundClass === "mention" || soundClass === "unknown";
    case "mentions_and_dms":
      return soundClass !== "general";
    default:
      return true;
  }
}

/**
 * The purely local gate: this tab is already showing the conversation.
 *
 * Focused, the message is in front of the reader and a chime adds nothing.
 * Unfocused, they are looking elsewhere, so something addressed to them
 * personally is still worth hearing while ambient room activity is not.
 */
function alreadyInFrontOfTheReader(input: AlertExecutionInput, soundClass: SoundClass): boolean {
  if (!input.isActiveConversation) return false;
  // Focused, the reader is looking at it whatever the class is. Unfocused, the
  // class decides — and an unknown one does not silence, for the same reason
  // the preference above does not.
  return input.isWindowFocused || soundClass === "general";
}
