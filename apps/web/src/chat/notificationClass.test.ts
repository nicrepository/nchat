import { describe, expect, it } from "vitest";

import type { MessagePriority } from "./chatTypes";
import {
  isInConversation,
  resolveNotificationClass,
  type AttentionContext,
  type NotificationClass,
  type NotificationClassInput,
} from "./notificationClass";

/** The reader sitting in front of the conversation the event belongs to. */
const attending: AttentionContext = {
  conversationOpen: true,
  documentVisible: true,
  windowFocused: true,
};

/** The reader somewhere else entirely. */
const away: AttentionContext = {
  conversationOpen: false,
  documentVisible: false,
  windowFocused: false,
};

function input(overrides: Partial<NotificationClassInput> = {}): NotificationClassInput {
  return { priority: "standard", namesRecipient: false, attention: away, ...overrides };
}

function attention(overrides: Partial<AttentionContext> = {}): AttentionContext {
  return { ...attending, ...overrides };
}

describe("resolveNotificationClass — precedence", () => {
  // Urgent is the strongest claim an author can make and nothing below it in
  // the ladder may take it away, whatever else is true of the event or of
  // where the reader is looking.
  it.each<[string, NotificationClassInput]>([
    ["on its own", input({ priority: "urgent" })],
    ["with a mention", input({ priority: "urgent", namesRecipient: true })],
    ["with the conversation attended", input({ priority: "urgent", attention: attending })],
    [
      "with a mention and the conversation attended",
      input({ priority: "urgent", namesRecipient: true, attention: attending }),
    ],
  ])("resolves urgent %s", (_case, given) => {
    expect(resolveNotificationClass(given)).toBe("urgent");
  });

  // A room the reader is watching is exactly when something addressed to them
  // personally must still stand out from the ambient traffic.
  it.each<[string, NotificationClassInput]>([
    ["outside the conversation", input({ namesRecipient: true })],
    [
      "with the conversation open in a hidden tab",
      input({
        namesRecipient: true,
        attention: attention({ documentVisible: false, windowFocused: false }),
      }),
    ],
    [
      "with the document visible but the window unfocused",
      input({ namesRecipient: true, attention: attention({ windowFocused: false }) }),
    ],
    [
      "with the window focused but the conversation closed",
      input({ namesRecipient: true, attention: attention({ conversationOpen: false }) }),
    ],
    ["with the whole attention context", input({ namesRecipient: true, attention: attending })],
  ])("resolves mention %s", (_case, given) => {
    expect(resolveNotificationClass(given)).toBe("mention");
  });

  it("lets a mention outrank an ordinary message and the attended conversation", () => {
    expect(resolveNotificationClass(input({ namesRecipient: true }))).toBe("mention");
    expect(resolveNotificationClass(input({ namesRecipient: false }))).toBe("message");
    expect(resolveNotificationClass(input({ attention: attending }))).toBe("in-conversation");
  });
});

describe("resolveNotificationClass — priority", () => {
  // #826 is explicit that `important` introduces no class of its own at this
  // stage: it keeps whatever treatment its context already gives it, which is
  // exactly what `standard` gets in the same context.
  it("gives important and standard the same class in every context", () => {
    const contexts: AttentionContext[] = [away, attending, attention({ windowFocused: false })];
    const classesFor = (priority: MessagePriority): NotificationClass[] =>
      contexts.flatMap((context) =>
        [false, true].map((namesRecipient) =>
          resolveNotificationClass(input({ priority, namesRecipient, attention: context })),
        ),
      );

    expect(classesFor("important")).toEqual(classesFor("standard"));
  });

  it("never produces a class outside the closed vocabulary", () => {
    const vocabulary: NotificationClass[] = ["urgent", "mention", "message", "in-conversation"];
    for (const priority of ["standard", "important", "urgent"] as const) {
      expect(vocabulary).toContain(resolveNotificationClass(input({ priority })));
    }
  });

  it("resolves important as an ordinary message away from the conversation", () => {
    expect(resolveNotificationClass(input({ priority: "important" }))).toBe("message");
  });

  it("resolves important as in-conversation while the reader is attending it", () => {
    expect(resolveNotificationClass(input({ priority: "important", attention: attending }))).toBe(
      "in-conversation",
    );
  });
});

describe("attention context", () => {
  it.each<[string, AttentionContext, boolean]>([
    ["all three facts hold", attending, true],
    ["the conversation is not open", attention({ conversationOpen: false }), false],
    ["the tab is in the background", attention({ documentVisible: false }), false],
    ["the window has lost focus", attention({ windowFocused: false }), false],
    ["nothing holds", away, false],
  ])("reads %s as in-conversation=%s", (_case, given, expected) => {
    expect(isInConversation(given)).toBe(expected);
  });

  // The defect #826 names: a conversation is not attended just because its id
  // is the selected one. Each of these is a reader who is somewhere else.
  it.each<[string, AttentionContext]>([
    ["selected in a hidden tab", attention({ documentVisible: false })],
    ["selected in an unfocused window", attention({ windowFocused: false })],
    ["visible and focused on another conversation", attention({ conversationOpen: false })],
  ])("does not downgrade a message to in-conversation when %s", (_case, given) => {
    expect(resolveNotificationClass(input({ attention: given }))).toBe("message");
  });

  it("downgrades to in-conversation only with conversation, visibility and focus together", () => {
    expect(resolveNotificationClass(input({ attention: attending }))).toBe("in-conversation");
  });
});

describe("resolveNotificationClass — determinism", () => {
  it("returns the same class for the same input, however often it is asked", () => {
    const given = input({ priority: "important", namesRecipient: true, attention: attending });
    const answers = Array.from({ length: 25 }, () => resolveNotificationClass(given));

    expect(new Set(answers).size).toBe(1);
    expect(answers[0]).toBe("mention");
  });

  it("resolves two separately built but equal inputs identically", () => {
    expect(resolveNotificationClass(input({ attention: attention() }))).toBe(
      resolveNotificationClass(input({ attention: { ...attending } })),
    );
  });

  it("does not mutate the input it was given", () => {
    const given = input({ priority: "urgent", namesRecipient: true, attention: attending });
    const before = structuredClone(given);

    resolveNotificationClass(given);

    expect(given).toEqual(before);
  });
});
