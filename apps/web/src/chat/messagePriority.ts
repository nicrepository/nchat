/**
 * What the composer states about one message's priority, and the one place the
 * rules about it live (issue #822).
 *
 * Three fields travel together because the server reads them together: a
 * priority (#821), a request for confirmation (#824) and a persistent reminder
 * policy (#825). Splitting them across three pieces of composer state is what
 * lets them disagree — a reminder policy left behind by a priority that is no
 * longer urgent is the stale-flag bug this module exists to make unreachable.
 *
 * Nothing here is a security boundary. chat-service validates every one of
 * these fields on create and refuses the combinations it does not accept (see
 * domain.ValidatePersistentNotifications); this only decides what the composer
 * offers and what it states, so a browser that posts something else is refused
 * by the server exactly as it would be without this file.
 */

import type { MessagePriority } from "./chatTypes";

/**
 * One message's stated attention, as a single value.
 *
 * Always normalised before it is stored or sent — see normalizePriorityIntent,
 * which is the only way flags and priority are allowed to be combined.
 */
export interface MessagePriorityIntent {
  priority: MessagePriority;
  /** Ask the recipients to confirm receipt explicitly (issue #824). */
  acknowledgementRequired: boolean;
  /** Keep reminding recipients who neither confirmed nor answered (issue #825). */
  persistentNotifications: boolean;
}

/** What every composer starts at, and what removing a configuration returns to. */
export const standardPriorityIntent: MessagePriorityIntent = {
  priority: "standard",
  acknowledgementRequired: false,
  persistentNotifications: false,
};

export const priorityLabels: Record<MessagePriority, string> = {
  standard: "Padrão",
  important: "Importante",
  urgent: "Urgente",
};

/**
 * What a stated priority draws on the message itself (issue #823).
 *
 * `label` is visible text and `icon` is a decorative Material Symbols ligature,
 * so every state this map describes is perceivable without colour — #820 and
 * #823 both require that, and the tint in the stylesheet is a third signal
 * rather than the first one.
 */
export interface MessagePriorityBadge {
  /** Material Symbols ligature. Rendered aria-hidden: it never carries meaning alone. */
  icon: string;
  label: string;
}

/**
 * The priorities that draw a badge, and what each one draws.
 *
 * Deliberately partial: `standard` has no entry, so an ordinary message renders
 * exactly what it rendered before this issue — no element, no class, no
 * attribute. That is the whole of the "standard preserves the current
 * rendering" rule, expressed as an absent key rather than as a test somewhere.
 *
 * It is also where the fail-safe lands. normalizeMessagePriority resolves
 * anything this build does not recognise to `standard`, which lands here on a
 * missing key and draws nothing — a future fourth priority is read as an
 * ordinary message, never as an alarm nobody here has reasoned about.
 */
export const messagePriorityBadges: Partial<Record<MessagePriority, MessagePriorityBadge>> = {
  important: { icon: "label_important", label: priorityLabels.important },
  // The same ligature the composer's priority trigger uses, so the axis reads
  // as one thing from the moment it is stated to the moment it is delivered.
  urgent: { icon: "error", label: priorityLabels.urgent },
};

/**
 * Whether this priority may carry the confirmation and reminder options — the
 * whole of the first version's policy, in one place.
 *
 * Urgent only. `persistent_notifications` on anything else is refused by the
 * server outright, and #820 states the first UI may offer acknowledgement for
 * Urgent alone. Every surface asks this rather than testing the priority
 * itself, so widening the policy later is one edit and not a hunt.
 */
export function allowsAttentionOptions(priority: MessagePriority): boolean {
  return priority === "urgent";
}

/**
 * Drops the options the policy does not allow for the stated priority.
 *
 * Applied on every transition, so a draft that was urgent with both options on
 * and is then set back to Padrão carries neither of them afterwards. The UI
 * hiding a control is not what clears it: the value is.
 */
export function normalizePriorityIntent(intent: MessagePriorityIntent): MessagePriorityIntent {
  if (allowsAttentionOptions(intent.priority)) return intent;
  return { ...standardPriorityIntent, priority: intent.priority };
}

/** Whether this intent says anything at all beyond the default. */
export function isDefaultPriorityIntent(intent: MessagePriorityIntent): boolean {
  const normalized = normalizePriorityIntent(intent);
  return (
    normalized.priority === "standard" &&
    !normalized.acknowledgementRequired &&
    !normalized.persistentNotifications
  );
}

/**
 * The applied options in words, for the composer's summary and for the
 * trigger's accessible name.
 *
 * Text, never a colour: #820 and #822 both require the state to be perceivable
 * without one, and a screen reader is only ever handed this.
 */
export function priorityOptionLabels(intent: MessagePriorityIntent): string[] {
  const labels: string[] = [];
  if (intent.acknowledgementRequired) labels.push("Confirmação solicitada");
  if (intent.persistentNotifications) labels.push("Notificações persistentes");
  return labels;
}

/** The trigger's accessible name: the priority, then whatever else is applied. */
export function priorityTriggerLabel(intent: MessagePriorityIntent): string {
  const stated = `Prioridade da mensagem: ${priorityLabels[intent.priority]}`;
  const options = priorityOptionLabels(intent);
  return options.length ? `${stated}. ${options.join(". ")}` : stated;
}
