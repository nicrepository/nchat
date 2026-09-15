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
