/**
 * The composer's priority rules, as rules (issue #822).
 *
 * Tested here rather than only through the popover because they are what the
 * popover, the summary and the send all consult: a normalisation that is only
 * exercised by clicking is a normalisation the request builder can quietly stop
 * agreeing with.
 */

import { describe, expect, it } from "vitest";

import {
  allowsAttentionOptions,
  isDefaultPriorityIntent,
  messagePriorityBadges,
  normalizePriorityIntent,
  priorityOptionLabels,
  priorityTriggerLabel,
  standardPriorityIntent,
  type MessagePriorityIntent,
} from "./messagePriority";

const urgentWithBoth: MessagePriorityIntent = {
  priority: "urgent",
  acknowledgementRequired: true,
  persistentNotifications: true,
};

describe("priority policy", () => {
  it("offers the extra options for urgent alone", () => {
    expect(allowsAttentionOptions("urgent")).toBe(true);
    expect(allowsAttentionOptions("important")).toBe(false);
    expect(allowsAttentionOptions("standard")).toBe(false);
  });
});

describe("normalizePriorityIntent", () => {
  it("keeps everything an urgent message stated", () => {
    expect(normalizePriorityIntent(urgentWithBoth)).toEqual(urgentWithBoth);
  });

  // The downgrade this exists for: the UI stops drawing the two options, and
  // the value must stop carrying them too — a hidden true is still sent.
  it.each(["important", "standard"] as const)("drops urgent-only options on %s", (priority) => {
    expect(normalizePriorityIntent({ ...urgentWithBoth, priority })).toEqual({
      priority,
      acknowledgementRequired: false,
      persistentNotifications: false,
    });
  });
});

describe("isDefaultPriorityIntent", () => {
  it("is true for the value every composer starts at", () => {
    expect(isDefaultPriorityIntent(standardPriorityIntent)).toBe(true);
  });

  it("is false for anything the reader stated", () => {
    expect(isDefaultPriorityIntent({ ...standardPriorityIntent, priority: "important" })).toBe(
      false,
    );
    expect(isDefaultPriorityIntent(urgentWithBoth)).toBe(false);
  });

  // A standard intent carrying a stale flag is still the default: the flag is
  // not part of what would be sent.
  it("ignores options the policy would drop anyway", () => {
    expect(isDefaultPriorityIntent({ ...urgentWithBoth, priority: "standard" })).toBe(true);
  });
});

describe("labels", () => {
  it("states the applied options in words", () => {
    expect(priorityOptionLabels(urgentWithBoth)).toEqual([
      "Confirmação solicitada",
      "Notificações persistentes",
    ]);
    expect(priorityOptionLabels(standardPriorityIntent)).toEqual([]);
  });

  it("names the trigger by the whole applied state, never by its tint", () => {
    expect(priorityTriggerLabel(standardPriorityIntent)).toBe("Prioridade da mensagem: Padrão");
    expect(priorityTriggerLabel(urgentWithBoth)).toBe(
      "Prioridade da mensagem: Urgente. Confirmação solicitada. Notificações persistentes",
    );
  });
});

/**
 * What a delivered message draws for its stated priority (issue #823).
 *
 * The map is asserted directly because its shape *is* the rule: a priority with
 * no entry draws nothing, which is how `standard` keeps the rendering it had
 * before this issue and how a priority this build does not recognise is read as
 * an ordinary message rather than as an alarm.
 */
describe("message priority badges", () => {
  it("draws nothing at all for a standard message", () => {
    expect(messagePriorityBadges.standard).toBeUndefined();
  });

  it("gives important and urgent a word of their own, never a colour alone", () => {
    expect(messagePriorityBadges.important?.label).toBe("Importante");
    expect(messagePriorityBadges.urgent?.label).toBe("Urgente");
    // Distinct on both axes a reader without colour has: the words differ, and
    // so do the icons beside them.
    expect(messagePriorityBadges.important?.icon).not.toBe(messagePriorityBadges.urgent?.icon);
  });

  it("keeps the labels the composer already states", () => {
    expect(priorityTriggerLabel({ ...standardPriorityIntent, priority: "urgent" })).toContain(
      messagePriorityBadges.urgent!.label,
    );
  });
});
